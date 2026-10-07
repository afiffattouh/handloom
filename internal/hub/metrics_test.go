package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

func TestMetricsComeFromTheRecordAndSayHowManyRecordsBackThem(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)
	var t1, t2 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "one", AssignedTo: "worker"}, &t1)
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "two", AssignedTo: "worker"}, &t2)
	step := func(id int64, exit int, accept bool) {
		ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", id), nil, nil)
		ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", id), api.SubmitReq{Evidence: []string{"commit:abc"}}, nil)
		e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", id), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: exit}, nil)
		if accept {
			ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", id), nil, nil)
		} else {
			ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/reject", id), api.ReasonReq{Reason: "fix"}, nil)
		}
	}
	step(t1.ID, 0, true)  // accepted first time, check passed
	step(t2.ID, 1, false) // check failed, sent back
	e.clock.advance(5 * time.Minute)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t2.ID), api.SubmitReq{Evidence: []string{"commit:def"}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t2.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t2.ID), nil, nil)

	var m Metrics
	e.apiOK(e.admin, "", "GET", "/v1/metrics?range=7d", nil, &m)
	if m.Checks.N != 3 || int(m.Checks.Pct) != 66 {
		t.Fatalf("checks: %+v (2 of 3 passed)", m.Checks)
	}
	if m.Checks.PriorN != 0 || m.Checks.PriorPct != -1 {
		t.Fatalf("nothing before, so no prior rate: %+v", m.Checks)
	}
	var acc, rej int
	for _, b := range m.Buckets {
		acc += b.Accepted
		rej += b.Rejected
	}
	if acc != 2 || rej != 1 || len(m.Buckets) != 7 {
		t.Fatalf("buckets: accepted %d rejected %d of %d", acc, rej, len(m.Buckets))
	}
	if m.Agents.Total != 2 {
		t.Fatalf("agents: %+v", m.Agents)
	}
	if m.TaskTime.N != 2 || m.TaskTime.Value <= 0 {
		t.Fatalf("task time: %+v", m.TaskTime)
	}
	if len(m.Fleet) != 2 || m.Fleet[0].Device != "d1" {
		t.Fatalf("fleet: %+v", m.Fleet)
	}
	// 24h uses hourly buckets.
	var h Metrics
	e.apiOK(e.admin, "", "GET", "/v1/metrics?range=24h", nil, &h)
	if len(h.Buckets) != 24 {
		t.Fatalf("24h buckets: %d", len(h.Buckets))
	}
}

func TestInsightsStateOnlyWhatTheRecordShows(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "one", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 1, Tail: "FAIL"}, nil)
	ag.worker("POST", "/v1/agents/worker/state", api.StateReq{State: "blocked"}, nil)
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "Which one?"}, nil)

	titles := func() string {
		var m Metrics
		e.apiOK(e.admin, "", "GET", "/v1/metrics", nil, &m)
		var b []string
		for _, i := range m.Insights {
			b = append(b, i.Title)
			if i.Basis == "" {
				t.Fatalf("an insight without a basis: %+v", i)
			}
		}
		return strings.Join(b, " | ")
	}
	if got := titles(); got != "" {
		t.Fatalf("nothing is old enough to point out yet, got: %s", got)
	}
	e.clock.advance(45 * time.Minute)
	got := titles()
	for _, want := range []string{"A question from lead has waited 45 min", "worker has been blocked for 45 min", fmt.Sprintf("Task #%d failed its check 45 min ago and is still waiting", t1.ID)} {
		if !strings.Contains(got, want) {
			t.Errorf("insights lack %q: %s", want, got)
		}
	}
	for _, banned := range []string{"faster", "likely", "would", "because"} {
		if strings.Contains(got, banned) {
			t.Errorf("an insight claims more than the record shows (%q): %s", banned, got)
		}
	}
}

func TestUtilisationAddsUpAndIgnoresNothing(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	ag.worker("POST", "/v1/agents/worker/state", api.StateReq{State: "working"}, nil)
	e.clock.advance(30 * time.Minute)
	ag.worker("POST", "/v1/agents/worker/state", api.StateReq{State: "idle"}, nil)
	e.clock.advance(30 * time.Minute)
	var m Metrics
	e.apiOK(e.admin, "", "GET", "/v1/metrics?range=24h", nil, &m)
	for _, u := range m.Util {
		if u.Agent == "worker" {
			if u.Working != 50 || u.Idle != 50 || u.Blocked != 0 || u.Off != 0 {
				t.Fatalf("worked 30 min, idled 30: %+v", u)
			}
			return
		}
	}
	t.Fatalf("no row for the worker: %+v", m.Util)
}
