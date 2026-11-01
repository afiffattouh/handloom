package link

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPlanWorkerStoppedBeforeReportingOffline(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "kill failed"}[fail], func(t *testing.T) {
			var events []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					json.NewEncoder(w).Encode([]string{"plan-1-task-2-try-1"})
					return
				}
				if r.URL.Path != "/v1/agents/plan-1-task-2-try-1/state" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				events = append(events, "offline")
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			l := New(Options{Hub: srv.URL, Credential: "test", TmuxSocket: "test-socket", Tmux: func(_ context.Context, name string, args ...string) (string, error) {
				if name != "tmux" || args[0] != "-L" || args[1] != "test-socket" {
					t.Fatalf("wrong process: %s %v", name, args)
				}
				if args[2] == "list-windows" {
					return "unrelated\nplan-1-task-2-try-1\n", nil
				}
				if !strings.HasSuffix(strings.Join(args, " "), "kill-window -t =handloom:=plan-1-task-2-try-1") {
					t.Fatalf("wrong target: %v", args)
				}
				events = append(events, "kill")
				if fail {
					return "", errors.New("cannot kill")
				}
				return "", nil
			}})
			l.stopPlanWorkers(context.Background())
			want := []string{"kill", "offline"}
			if fail {
				want = []string{"kill"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events %v, want %v", events, want)
			}
		})
	}
}
