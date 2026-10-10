package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/notify"
)

func (e *env) sweep() {
	e.t.Helper()
	if err := e.hub.Sweep(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) agentInfo(name string) api.Agent {
	e.t.Helper()
	var as []api.Agent
	e.ok(e.afif(), "GET", "/v1/agents", nil, &as)
	for _, a := range as {
		if a.Name == name {
			return a
		}
	}
	e.t.Fatalf("no agent %s", name)
	return api.Agent{}
}

func (e *env) heartbeat(c caller) {
	e.t.Helper()
	e.ok(c, "POST", "/v1/agents/"+c.agent+"/heartbeat", nil, nil)
}

func TestAgentLeaseExpiresIntoOffline(t *testing.T) {
	e := newEnv(t)
	// Agents that never had a terminal vouched for are never expired.
	e.sweep()
	if st := e.agentInfo("worker").State; st != "unknown" {
		t.Fatalf("an agent with no lease changed state: %s", st)
	}

	e.heartbeat(e.worker())
	a := e.agentInfo("worker")
	if a.LeaseUntil == nil || !a.LeaseUntil.After(e.clock.now()) {
		t.Fatalf("no lease after a heartbeat: %+v", a)
	}
	e.ok(e.worker(), "POST", "/v1/agents/worker/state", api.StateReq{State: "working"}, nil)
	e.clock.advance(60 * time.Second)
	e.sweep()
	if st := e.agentInfo("worker").State; st != "working" {
		t.Fatalf("expired too early: %s", st)
	}
	e.heartbeat(e.worker()) // renewed
	e.clock.advance(60 * time.Second)
	e.sweep()
	if st := e.agentInfo("worker").State; st != "working" {
		t.Fatalf("the renewal did not count: %s", st)
	}
	e.clock.advance(31 * time.Second)
	e.sweep()
	a = e.agentInfo("worker")
	if a.State != "offline" || a.LeaseUntil != nil {
		t.Fatalf("after the lease ran out: %+v", a)
	}
	e.sweep() // once only
	n := 0
	for _, r := range e.audit() {
		if r.Action == "agent.lease_expired" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d lease_expired rows, want 1", n)
	}
	// The terminal comes back: unknown until its hooks say more.
	e.heartbeat(e.worker())
	if st := e.agentInfo("worker").State; st != "unknown" {
		t.Fatalf("after the terminal came back: %s", st)
	}
	// Only the agent's own device may vouch.
	e.fail(403, e.afif(), "POST", "/v1/agents/worker/heartbeat", nil)
	e.fail(403, e.worker2(), "POST", "/v1/agents/worker/heartbeat", nil)
}

func TestLeadLostIsToldToTheHuman(t *testing.T) {
	e := newEnv(t)
	f := newFakeNotifier()
	e.hub.opt.Notifier = f
	lead := e.agent("lead1")
	e.agent("w1")
	j := e.newJob("A job", "lead1")
	e.inbox(lead)
	var task api.Task
	e.ok(lead, "POST", "/v1/tasks", api.TaskCreateReq{Title: "work", AssignedTo: "w1"}, &task)

	e.heartbeat(lead)
	e.clock.advance(2 * time.Minute)
	e.sweep()
	select {
	case <-f.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no push when the lead was lost")
	}
	f.mu.Lock()
	n := f.got[0]
	f.mu.Unlock()
	if n.Kind != notify.KindLeadLost || strings.Contains(n.Text, "work") {
		t.Fatalf("notification: %+v", n)
	}
	if !strings.Contains(n.Text, fmt.Sprintf("job #%d", j.ID)) {
		t.Fatalf("the push does not say which job: %q", n.Text)
	}
	if !e.audited("job.lead_lost", "agent:lead1") {
		t.Fatal("lead loss is not audited")
	}
	// The inbox says what to do.
	var d api.Digest
	e.ok(e.afif(), "GET", "/v1/digest", nil, &d)
	found := false
	for _, it := range d.NeedsYou {
		found = found || (it.Kind == "lead-silent" && it.ID == j.ID && strings.Contains(it.Detail, "job resume"))
	}
	if !found {
		t.Fatalf("digest: %+v", d.NeedsYou)
	}
}

func TestLeadWithNoWorkLeftIsNotAnAlarm(t *testing.T) {
	e := newEnv(t)
	f := newFakeNotifier()
	e.hub.opt.Notifier = f
	lead := e.agent("lead1")
	e.newJob("A job", "lead1")
	e.heartbeat(lead)
	e.clock.advance(2 * time.Minute)
	e.sweep()
	select {
	case <-f.ch:
		t.Fatal("pushed for a lead with nothing unfinished")
	case <-time.After(300 * time.Millisecond):
	}
	if e.audited("job.lead_lost", "agent:lead1") {
		t.Fatal("lead_lost without work")
	}
	var d api.Digest
	e.ok(e.afif(), "GET", "/v1/digest", nil, &d)
	for _, it := range d.NeedsYou {
		if it.Kind == "lead-silent" {
			t.Fatalf("digest alarms for an idle job: %+v", it)
		}
	}
}

func TestLeadEndingItsSessionWithWorkLeftIsTold(t *testing.T) {
	e := newEnv(t)
	f := newFakeNotifier()
	e.hub.opt.Notifier = f
	lead := e.agent("lead1")
	e.newJob("A job", "lead1")
	e.ok(lead, "POST", "/v1/tasks", api.TaskCreateReq{Title: "work"}, nil)
	e.ok(lead, "POST", "/v1/agents/lead1/state", api.StateReq{State: "offline"}, nil) // what the SessionEnd hook does
	select {
	case <-f.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no push when the lead's session ended with work left")
	}
}

func TestResumeAJobWithANewLead(t *testing.T) {
	e := newEnv(t)
	old := e.agent("lead1")
	newLead := e.agent("lead2")
	w := e.agent("w1")
	j := e.newJob("A job", "lead1")
	e.inbox(old)
	var t1, t2 api.Task
	e.ok(old, "POST", "/v1/tasks", api.TaskCreateReq{Title: "done already", AssignedTo: "w1"}, &t1)
	e.ok(old, "POST", "/v1/tasks", api.TaskCreateReq{Title: "still to do", DependsOn: []int64{t1.ID}}, &t2)
	e.ok(w, "POST", taskPath(t1.ID, "claim"), nil, nil)
	e.ok(w, "POST", taskPath(t1.ID, "submit"), api.SubmitReq{Evidence: []string{"file:a"}, Note: "done"}, nil) // mail for lead1, unread
	e.ok(w, "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "please look at the evidence"}, nil)

	// Not for agents, not for a bad target.
	e.fail(403, newLead, "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "lead2"})
	e.fail(404, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "nobody"})
	e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "lead1"}) // already the lead
	e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "lead"})  // leads something else (the project)

	var got api.Job
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "lead2"}, &got)
	if got.Lead != "lead2" {
		t.Fatalf("job after resume: %+v", got)
	}
	if a := e.agentInfo("lead1"); a.Role != "worker" || a.Job != nil {
		t.Fatalf("the old lead: %+v", a)
	}
	msgs := e.inboxBodies(newLead)
	for _, want := range []string{"resuming job", "1 open", "1 submitted", "[forwarded from lead1", "please look at the evidence", "submitted by w1"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("new lead's mail lacks %q:\n%s", want, msgs)
		}
	}
	if left := e.inbox(old); len(left) != 0 {
		t.Fatalf("the old lead still has unread mail: %+v", left)
	}
	// The new lead can pick up the board and judge the submitted work.
	var tasks []api.Task
	e.ok(newLead, "GET", fmt.Sprintf("/v1/tasks?job=%d", j.ID), nil, &tasks)
	if len(tasks) != 2 {
		t.Fatalf("tasks: %+v", tasks)
	}
	e.ok(newLead, "POST", taskPath(t1.ID, "accept"), nil, nil)
	if !e.audited("job.resume", fmt.Sprintf("job:%d", j.ID)) {
		t.Fatal("resume is not audited")
	}
	// New work from the worker now reaches the new lead only.
	e.ok(w, "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "next question"}, nil)
	if !strings.Contains(e.inboxBodies(newLead), "next question") || len(e.inbox(old)) != 0 {
		t.Fatal("role:lead did not follow the job to its new lead")
	}
	// A closed job cannot be resumed.
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), api.JobCloseReq{Cancel: true}, nil)
	e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "lead1"})
}
