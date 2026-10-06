package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestRepoJobValidation(t *testing.T) {
	e := newEnv(t)
	for name, req := range map[string]api.JobNewReq{
		"relative repo":    {Title: "x", Repo: "project", Device: "d1"},
		"dotdot":           {Title: "x", Repo: "/a/../b", Device: "d1"},
		"trailing slash":   {Title: "x", Repo: "/a/b/", Device: "d1"},
		"newline in repo":  {Title: "x", Repo: "/a\nb", Device: "d1"},
		"verify no repo":   {Title: "x", Verify: "make test"},
		"verify multiline": {Title: "x", Repo: "/a", Verify: "make test\nrm -rf /", Device: "d1"},
		"two device names": {Title: "x", Repo: "/a"}, // two joined devices: name one
		"unknown device":   {Title: "x", Repo: "/a", Device: "nowhere"},
		"lead and profile": {Title: "x", Lead: "lead", LeadProfile: "p", Device: "d1"},
	} {
		if code, _ := e.do(e.afif(), "POST", "/v1/jobs", req); code < 400 {
			t.Errorf("%s: accepted (status %d)", name, code)
		}
	}
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Fix the bug", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)
	if j.Repo != "/srv/app" || j.Verify != "./check.sh" || j.Device != "d1" {
		t.Fatalf("job: %+v", j)
	}
	var got api.Job
	e.ok(e.worker(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	if got.Repo != "/srv/app" || got.Device != "d1" {
		t.Fatalf("job as read by an agent: %+v", got)
	}
}

func TestSpawnIntoARepoJob(t *testing.T) {
	e := newEnv(t)
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)

	// The job's device is where its agents start, without being told.
	s := e.spawn(e.afif(), api.SpawnReq{Name: "w1", Kind: "claude", Job: j.ID})
	if s.Device != "d1" || s.Repo != "/srv/app" || s.Role != "worker" {
		t.Fatalf("spawn: %+v", s)
	}
	// Another device is refused: the repository is not there.
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "w2", Kind: "claude", Job: j.ID, Device: "d2"})
}

func TestTheJobStartsItsOwnLead(t *testing.T) {
	e := newEnv(t)
	own := caller{e.admin, ""}
	e.newProfile(own, "boss", researcher())
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Repo: "/srv/app", Device: "d1", LeadProfile: "boss"}, &j)

	var spawns []api.Spawn
	e.ok(e.afif(), "GET", "/v1/spawns", nil, &spawns)
	if len(spawns) != 1 || spawns[0].Role != "lead" || spawns[0].Name != fmt.Sprintf("lead-%d", j.ID) || spawns[0].Profile != "boss@1" || spawns[0].Repo != "/srv/app" {
		t.Fatalf("spawns: %+v", spawns)
	}
	if got := e.jobLead(j.ID); got != "" {
		t.Fatalf("a lead before it exists: %q", got)
	}
	// The link does its part: the agent registers, then the spawn is reported started.
	s := spawns[0]
	e.report(e.d1, s.ID, api.SpawnReport{Status: "launching"})
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: s.Name, Kind: "claude"}, nil)
	if code, _ := e.report(e.d1, s.ID, api.SpawnReport{Status: "started", Pane: "tmux:/x:%1"}); code != 200 {
		t.Fatalf("started: %d", code)
	}
	if got := e.jobLead(j.ID); got != s.Name {
		t.Fatalf("the job's lead is %q, want %s", got, s.Name)
	}
	a := e.agentInfo(s.Name)
	if a.Role != "lead" || a.Job == nil || *a.Job != j.ID {
		t.Fatalf("agent: %+v", a)
	}
	if !strings.Contains(e.inboxBodies(caller{e.d1, s.Name}), "lead of job #") {
		t.Fatal("the new lead was not told what it leads")
	}
	// The lead starts workers on its own device, in its own job, and they get the repo.
	w := e.spawn(caller{e.d1, s.Name}, api.SpawnReq{Name: "w1", Kind: "claude"})
	if w.Repo != "/srv/app" || w.Role != "worker" || w.Device != "d1" {
		t.Fatalf("a lead's worker: %+v", w)
	}
	// A lead cannot start another lead, and a second lead for the job is refused.
	e.fail(403, caller{e.d1, s.Name}, "POST", "/v1/spawns", api.SpawnReq{Name: "boss2", Kind: "claude", Role: "lead"})
	e.fail(409, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "boss2", Kind: "claude", Role: "lead", Job: j.ID})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "boss3", Kind: "claude", Role: "lead", Device: "d1"}) // no job
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "boss3", Kind: "claude", Role: "captain", Device: "d1"})
}

func (e *env) jobLead(id int64) string {
	var j api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", id), nil, &j)
	return j.Lead
}

func TestConfidentialJobsStartTheirLeadFromALocalProfile(t *testing.T) {
	e := newEnv(t)
	e.hub.opt.AllowConfidential = true
	own := caller{e.admin, ""}
	e.newProfile(own, "cloudy", researcher())
	local := researcher()
	local.Runtime = "local"
	e.newProfile(own, "private", local)
	e.fail(400, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "x", Confidential: true, Device: "d1", LeadProfile: "cloudy"})
	// The refused job left nothing behind: no job, no spawn.
	var js []api.Job
	e.ok(e.afif(), "GET", "/v1/jobs", nil, &js)
	var sp []api.Spawn
	e.ok(e.afif(), "GET", "/v1/spawns", nil, &sp)
	if len(js) != 0 || len(sp) != 0 {
		t.Fatalf("a refused job left %d jobs and %d spawns", len(js), len(sp))
	}
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "x", Confidential: true, Device: "d1", LeadProfile: "private"}, nil)
}

func TestOnlyTheDeviceReportsARefusedSubmit(t *testing.T) {
	e := newEnv(t)
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead"}, &j)
	var task api.Task
	e.ok(e.lead(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "t", AssignedTo: "worker"}, &task)
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)
	e.inbox(e.lead())
	req := api.ScopeRefusal{Agent: "worker", Task: task.ID, Paths: []string{"docs/x.md", "main.go"}}
	e.fail(403, e.afif(), "POST", "/v1/device/scope-refused", req)
	e.fail(403, caller{e.d2, ""}, "POST", "/v1/device/scope-refused", req) // not this agent's device
	e.fail(404, caller{e.d1, ""}, "POST", "/v1/device/scope-refused", api.ScopeRefusal{Agent: "worker", Task: 999})
	e.ok(caller{e.d1, ""}, "POST", "/v1/device/scope-refused", req, nil)
	if !e.audited("task.scope_refused", fmt.Sprintf("task:%d", task.ID)) {
		t.Fatal("not audited")
	}
	got := e.inboxBodies(e.lead())
	if !strings.Contains(got, "worker tried to submit") || !strings.Contains(got, "docs/x.md, main.go") {
		t.Fatalf("the lead's mail: %q", got)
	}
}
