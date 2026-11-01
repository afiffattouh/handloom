package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/client"
	"handloom/internal/plan"
)

func TestJobPlanCommand(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"steps":[{"key":"build","title":"Build","profile":"coder"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/jobs/7/plan" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Method == "POST" {
			var p plan.Plan
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.MaxAttempts != 2 {
				t.Errorf("request: %+v %v", p, err)
			}
		}
		json.NewEncoder(w).Encode(plan.Status{Job: 7, Version: 1, MaxAttempts: 2, Steps: []plan.StepStatus{{Key: "build", Task: 9, Profile: "coder@1", Status: "open"}}})
	}))
	defer srv.Close()
	for _, args := range [][]string{{"job", "plan", "7", "--file", file}, {"job", "plan", "7"}} {
		var out, errs bytes.Buffer
		if code := MainWith(client.Direct(srv.URL, "test"), args, &out, &errs); code != 0 {
			t.Fatalf("%d: %s", code, errs.String())
		}
		if !strings.Contains(out.String(), "coder@1") {
			t.Fatalf("output: %s", out.String())
		}
	}
	if calls != 2 {
		t.Fatalf("calls: %d", calls)
	}
}
