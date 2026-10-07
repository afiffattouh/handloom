package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestTheCommandCenterShowsTheRecordInWordsAndCharts(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	// A new hub: it says how to begin and invents nothing.
	body := e.req("GET", "/command", nil, c, nil).body
	for _, want := range []string{"Command center", "Needs you", "no device checks in this range", "no accepted tasks in this range", "Nothing to point out.", "Fleet"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty command center lacks %q", want)
		}
	}
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "first <b>task</b>", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t1.ID), nil, nil)

	for _, path := range []string{"/command", "/command?range=24h", "/command?range=30d", "/command?machine=d1", "/command/fragment?range=7d"} {
		r := e.req("GET", path, nil, c, nil)
		if r.status != 200 {
			t.Fatalf("%s: %d", path, r.status)
		}
		for _, want := range []string{"1 of 1 checks", "100", "worker", "lead", `<svg class="chart"`, "Profiles compared"} {
			if !strings.Contains(r.body, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if strings.Contains(r.body, "<b>task</b>") || strings.Contains(r.body, "style=") {
			t.Errorf("%s: unescaped text or an inline style", path)
		}
	}
	// A machine nobody has joined shows an empty fleet, not an error.
	if r := e.req("GET", "/command?machine=nowhere", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "No agents on this machine") {
		t.Fatalf("unknown machine: %d", r.status)
	}
	// Viewers read it too.
	vc, _ := e.member("vera", "viewer")
	if r := e.req("GET", "/command", nil, vc, nil); r.status != 200 || strings.Contains(r.body, "New job") {
		t.Fatalf("viewer: %d", r.status)
	}
	// The same numbers are an API.
	var m Metrics
	e.apiOK(e.admin, "", "GET", "/v1/metrics", nil, &m)
	if m.Range != "7d" || m.Checks.N != 1 {
		t.Fatalf("metrics: %+v", m.Checks)
	}
}
