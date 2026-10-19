package hub

import (
	"fmt"
	"testing"
	"time"

	"handloom/internal/api"
)

func TestWhereAJobStands(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	long := now.Add(-10 * time.Minute)
	base := func(f jobFacts) jobFacts {
		f.status, f.now = api.StatusOpen, now
		switch {
		case f.leadState == "none": // no lead at all
			f.leadState = ""
		case f.lead == "" && f.starting == 0:
			f.lead, f.leadState, f.leadSince = "lead-4", api.StateWorking, now
		}
		return f
	}
	cases := []struct {
		name string
		f    jobFacts
		want string
		in   string // a phrase the reason must hold
	}{
		{"closed", jobFacts{status: api.StatusDone, now: now}, api.JobDone, "finished"},
		{"cancelled", jobFacts{status: api.StatusCancelled, now: now}, api.JobDone, "cancelled"},
		{"a question beats a stuck job", base(jobFacts{questions: 1, leadState: api.StateOffline, lead: "l"}), api.JobNeedsYou, "question"},
		{"the lead is starting", base(jobFacts{starting: 1, lead: ""}), api.JobPlanning, "Starting"},
		{"the lead plans", base(jobFacts{}), api.JobPlanning, "planning"},
		{"no lead at all", base(jobFacts{leadState: "none"}), api.JobStuck, "No lead"},
		{"lead stopped", base(jobFacts{lead: "l", leadState: api.StateOffline}), api.JobStuck, "stopped"},
		{"the lead does not answer while work waits", base(jobFacts{counts: api.JobCounts{Submitted: 1}, lead: "l", leadState: api.StateUnknown, leadSince: long}), api.JobStuck, "not answering"},
		{"the lead has only just become unknown", base(jobFacts{counts: api.JobCounts{Submitted: 1}, lead: "l", leadState: api.StateUnknown, leadSince: now}), api.JobWorking, "waiting to be checked"},
		{"a worker could not be started", base(jobFacts{counts: api.JobCounts{Open: 1}, failedStart: "Starting c failed: no such name", leadState: api.StateWorking, lead: "l"}), api.JobStuck, "failed"},
		{"job #4: the step waits for an agent of another job", base(jobFacts{counts: api.JobCounts{Open: 1}, waiting: []waitingTask{{id: 5, assignee: "coder-1", state: api.StateOffline, elsewhere: 1}}}), api.JobStuck, "job 1"},
		{"the step waits for an offline worker", base(jobFacts{counts: api.JobCounts{Open: 1}, waiting: []waitingTask{{id: 5, assignee: "c", state: api.StateOffline}}}), api.JobStuck, "offline"},
		{"steps wait, nobody works, the lead is idle", base(jobFacts{counts: api.JobCounts{Open: 2}, lead: "l", leadState: api.StateIdle, leadSince: long}), api.JobStuck, "2 steps"},
		{"the lead has only just gone idle", base(jobFacts{counts: api.JobCounts{Open: 2}, lead: "l", leadState: api.StateIdle, leadSince: now}), api.JobWorking, "0 of 2"},
		{"working", base(jobFacts{counts: api.JobCounts{Claimed: 1, Done: 1}, liveWorkers: 1}), api.JobWorking, "1 of 2"},
		{"solo: the helper is starting", base(jobFacts{leadState: "none", solo: true, starting: 1, counts: api.JobCounts{Open: 1}}), api.JobWorking, "Starting the helper"},
		{"solo: the helper works", base(jobFacts{leadState: "none", solo: true, liveWorkers: 1, counts: api.JobCounts{Claimed: 1}}), api.JobWorking, "working on it"},
		{"solo: the work waits for the person", base(jobFacts{leadState: "none", solo: true, liveWorkers: 1, counts: api.JobCounts{Submitted: 1}}), api.JobNeedsYou, "waiting for you"},
		{"solo: the helper is gone", base(jobFacts{leadState: "none", solo: true, counts: api.JobCounts{Open: 1}}), api.JobStuck, "not running"},
		{"solo: done", base(jobFacts{leadState: "none", solo: true, counts: api.JobCounts{Done: 1}}), api.JobReady, "step is done"},
		{"ready", base(jobFacts{counts: api.JobCounts{Done: 3}}), api.JobReady, "3 steps"},
	}
	for _, c := range cases {
		got := jobProgress(c.f)
		if got.State != c.want || !contains(got.Reason, c.in) {
			t.Errorf("%s: got %+v, want %s containing %q", c.name, got, c.want, c.in)
		}
		if got.State == api.JobStuck && got.Action == "" {
			t.Errorf("%s: a stuck job must say what to do", c.name)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestAJobShowsWhereItStandsThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	e.agent("lead1")
	j := e.newJob("A job", "lead1")
	var got api.Job
	e.ok(e.afif(), "GET", "/v1/jobs/1", nil, &got)
	if got.Progress.State == "" || got.Progress.Reason == "" {
		t.Fatalf("a job says where it stands: %+v", got.Progress)
	}
	// The lead goes away: the job says so and what to do.
	e.hub.db.Exec(`UPDATE agent SET state = 'offline' WHERE name = 'lead1'`)
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	if got.Progress.State != api.JobStuck || got.Progress.Action == "" {
		t.Fatalf("lead gone: %+v", got.Progress)
	}
	// A closed job is done.
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), api.JobCloseReq{}, nil)
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	if got.Progress.State != api.JobDone {
		t.Fatalf("closed: %+v", got.Progress)
	}
}
