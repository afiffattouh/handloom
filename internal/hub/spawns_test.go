package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

func (e *env) spawn(c caller, req api.SpawnReq) api.Spawn {
	e.t.Helper()
	var s api.Spawn
	e.ok(c, "POST", "/v1/spawns", req, &s)
	return s
}

func (e *env) report(device string, id int64, r api.SpawnReport) (int, []byte) {
	return e.do(caller{device, ""}, "POST", fmt.Sprintf("/v1/spawns/%d/report", id), r)
}

func TestSpawnLifecycle(t *testing.T) {
	e := newEnv(t)
	j := e.newJob("A job", "")
	s := e.spawn(e.afif(), api.SpawnReq{Name: "researcher", Kind: "claude", Device: "d1", Job: j.ID, Model: "sonnet"})
	if s.Status != "pending" || s.Device != "d1" || s.Job == nil || *s.Job != j.ID {
		t.Fatalf("spawn: %+v", s)
	}

	// The device's link finds it, and nobody else's does.
	var mine, theirs []api.Spawn
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/spawns", nil, &mine)
	e.ok(caller{e.d2, ""}, "GET", "/v1/device/spawns", nil, &theirs)
	if len(mine) != 1 || len(theirs) != 0 {
		t.Fatalf("device spawns: %+v / %+v", mine, theirs)
	}
	// The request wakes the device's long-poll, and only that device's.
	var ev struct {
		Events []api.Event `json:"events"`
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/events?after=0&wait=0", nil, &ev)
	found := false
	for _, x := range ev.Events {
		found = found || x.Type == "spawn.requested"
	}
	if !found {
		t.Fatalf("no spawn.requested event for d1: %+v", ev.Events)
	}
	e.ok(caller{e.d2, ""}, "GET", "/v1/events?after=0&wait=0", nil, &ev)
	for _, x := range ev.Events {
		if x.Type == "spawn.requested" {
			t.Fatal("another device was woken for the spawn")
		}
	}

	// Only the owning device reports, and only forward.
	if code, _ := e.report(e.d2, s.ID, api.SpawnReport{Status: "launching"}); code != 403 {
		t.Fatalf("another device reporting: %d", code)
	}
	if code, _ := e.do(e.afif(), "POST", fmt.Sprintf("/v1/spawns/%d/report", s.ID), api.SpawnReport{Status: "started"}); code != 403 {
		t.Fatalf("a human reporting: %d", code)
	}
	if code, _ := e.report(e.d1, s.ID, api.SpawnReport{Status: "nonsense"}); code != 400 {
		t.Fatalf("bad status: %d", code)
	}
	if code, _ := e.report(e.d1, s.ID, api.SpawnReport{Status: "launching"}); code != 200 {
		t.Fatalf("launching: %d", code)
	}
	if code, _ := e.report(e.d1, s.ID, api.SpawnReport{Status: "launching"}); code != 409 {
		t.Fatalf("launching twice (a second link run must not start it again): %d", code)
	}
	// The spawned agent registers (adapter install does that), then the link reports started.
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "researcher", Kind: "claude"}, nil)
	if code, _ := e.report(e.d1, s.ID, api.SpawnReport{Status: "started", Pane: "tmux:/tmp/x:%3"}); code != 200 {
		t.Fatalf("started: %d", code)
	}
	if a := e.agentInfo("researcher"); a.Job == nil || *a.Job != j.ID || a.Role != "worker" {
		t.Fatalf("the spawned agent did not join the job: %+v", a)
	}
	if code, _ := e.report(e.d1, s.ID, api.SpawnReport{Status: "launching"}); code != 409 {
		t.Fatalf("going back: %d", code)
	}
	for _, a := range []string{"spawn.request", "spawn.launching", "spawn.started"} {
		if !e.audited(a, fmt.Sprintf("spawn:%d", s.ID)) {
			t.Errorf("audit lacks %s", a)
		}
	}
}

func TestSpawnRules(t *testing.T) {
	e := newEnv(t)
	lead1 := e.agent("lead1")
	e.agent("taken")
	e.newJob("A job", "lead1")
	// Validation.
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "bad name!", Kind: "claude", Device: "d1"})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "x", Kind: "emacs", Device: "d1"})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "x", Kind: "claude", Device: "d1", Model: "x; rm -rf /"})
	e.fail(404, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "x", Kind: "claude", Device: "nowhere"})
	e.fail(404, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "x", Kind: "claude", Device: "d1", Job: 999})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "x", Kind: "claude"}) // two devices: name one
	e.fail(409, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "taken", Kind: "claude", Device: "d1"})
	// Names are unique among requests in flight.
	e.spawn(e.afif(), api.SpawnReq{Name: "once", Kind: "claude", Device: "d1"})
	e.fail(409, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "once", Kind: "claude", Device: "d1"})

	// A lead does not choose where or into what.
	e.fail(403, lead1, "POST", "/v1/spawns", api.SpawnReq{Name: "w", Kind: "claude", Device: "d2"})
	e.fail(403, lead1, "POST", "/v1/spawns", api.SpawnReq{Name: "w", Kind: "claude", Job: 1})
	s := e.spawn(lead1, api.SpawnReq{Name: "w", Kind: "claude"})
	if s.Device != "d1" || s.Job == nil {
		t.Fatalf("a lead's spawn lands on its own device and in its own job: %+v", s)
	}
	// Others cannot.
	e.fail(403, e.worker(), "POST", "/v1/spawns", api.SpawnReq{Name: "y", Kind: "claude"})
	e.fail(403, e.observer(), "POST", "/v1/spawns", api.SpawnReq{Name: "y", Kind: "claude"})
	// A viewer reads only.
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'afif'`)
	e.fail(403, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "y", Kind: "claude", Device: "d1"})
	var list []api.Spawn
	e.ok(e.afif(), "GET", "/v1/spawns", nil, &list)
	if len(list) != 2 {
		t.Fatalf("spawn list: %+v", list)
	}
}

func TestSpawnCapPerJob(t *testing.T) {
	e := newEnv(t)
	e.hub.opt.MaxSpawns = 2
	lead := e.agent("lead1")
	j := e.newJob("A job", "lead1")
	e.spawn(lead, api.SpawnReq{Name: "a", Kind: "claude"})
	e.spawn(lead, api.SpawnReq{Name: "b", Kind: "claude"})
	e.fail(409, lead, "POST", "/v1/spawns", api.SpawnReq{Name: "c", Kind: "claude"})
	// Another job is not affected.
	j2 := e.newJob("Another", "")
	e.spawn(e.afif(), api.SpawnReq{Name: "d", Kind: "claude", Device: "d1", Job: j2.ID})
	_ = j
}

func TestSpawnFailsWhenNobodyStartsIt(t *testing.T) {
	e := newEnv(t)
	s := e.spawn(e.afif(), api.SpawnReq{Name: "ghost", Kind: "claude", Device: "d1"})
	e.clock.advance(60 * time.Second)
	e.sweep()
	var got api.Spawn
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/spawns/%d", s.ID), nil, &got)
	if got.Status != "pending" {
		t.Fatalf("failed too early: %+v", got)
	}
	e.clock.advance(60 * time.Second)
	e.sweep()
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/spawns/%d", s.ID), nil, &got)
	if got.Status != "failed" || !strings.Contains(got.Error, "link") {
		t.Fatalf("after the timeout: %+v", got)
	}
	// The name is free again.
	e.spawn(e.afif(), api.SpawnReq{Name: "ghost", Kind: "claude", Device: "d1"})
}
