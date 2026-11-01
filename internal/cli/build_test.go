package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/client"
)

func TestBuildDryRunNeedsNoConnection(t *testing.T) {
	var out, errs bytes.Buffer
	code := Main([]string{"build", "A booking MVP", "--dry-run"}, &out, &errs)
	if code != 0 || !strings.Contains(out.String(), "10 stages") || !strings.Contains(out.String(), "not deployment") {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
}
func TestBuildUsesOneAtomicRequest(t *testing.T) {
	var got api.ProductBuildReq
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/products" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(api.Job{ID: 42, Title: "Booking", Tasks: api.JobCounts{Open: 10}})
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	code := MainWith(client.Direct(srv.URL, "test"), []string{"build", "A booking MVP", "--repo", "/repo", "--device", "desk", "--verify", "./check", "--request-id", "retry-me"}, &out, &errs)
	if code != 0 || calls != 1 || got.RequestID != "retry-me" || got.Brief != "A booking MVP" || got.Playbook != "mvp-v1" {
		t.Fatalf("%d %d %+v %s", code, calls, got, errs.String())
	}
	if !strings.Contains(out.String(), "handloom job plan 42") {
		t.Fatal("missing progress command")
	}
}
