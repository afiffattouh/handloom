package hub

import (
	"fmt"
	"handloom/internal/store"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/plan"
)

func planPath(id int64) string { return fmt.Sprintf("/v1/jobs/%d/plan", id) }
func (e *env) planStatus(id int64) plan.Status {
	e.t.Helper()
	var s plan.Status
	e.ok(e.afif(), "GET", planPath(id), nil, &s)
	return s
}
func (e *env) planSweep() {
	e.t.Helper()
	if err := e.hub.Sweep(); err != nil {
		e.t.Fatal(err)
	}
}
func examplePlan() plan.Plan {
	return plan.Plan{Version: 1, Device: "d1", MaxAttempts: 2, Steps: []plan.Step{
		{Key: "build", Title: "Build the product", Profile: "coder"},
		{Key: "qa", Title: "Test the product", Profile: "coder", DependsOn: []string{"build"}},
	}}
}
func (e *env) preparePlan() (api.Job, plan.Plan) {
	e.t.Helper()
	e.newProfile(caller{e.admin, ""}, "coder", researcher())
	return e.newJob("Managed product", ""), examplePlan()
}
func (e *env) startPlanWorker(s plan.StepStatus) caller {
	e.t.Helper()
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: s.Worker, Kind: "claude"}, nil)
	var spawnID int64
	if err := e.hub.db.QueryRow(`SELECT spawn_id FROM plan_step WHERE task_id = ?`, s.Task).Scan(&spawnID); err != nil {
		e.t.Fatal(err)
	}
	if code, b := e.report(e.d1, spawnID, api.SpawnReport{Status: "started"}); code != 200 {
		e.t.Fatalf("report: %d %s", code, b)
	}
	return caller{e.d1, s.Worker}
}

func TestPlanDispatchesDependenciesAndSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	e.planSweep()
	s := e.planStatus(j.ID)
	if len(s.Steps) != 2 || s.Steps[0].Attempts != 1 || s.Steps[1].Attempts != 0 {
		t.Fatalf("first dispatch: %+v", s)
	}
	// Retry the submission after a lost response: no duplicate task graph.
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	e.srv.Close()
	snapshot := filepath.Join(t.TempDir(), "hub.db")
	if err := store.Backup(e.hub.db, snapshot); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.hub = New(db, Options{Now: e.clock.now, MsgRate: 1000})
	e.srv = httptest.NewServer(e.hub.Handler())
	t.Cleanup(e.srv.Close)
	e.planSweep()
	if got := e.planStatus(j.ID); got.Steps[0].Attempts != 1 || got.Steps[0].Worker != s.Steps[0].Worker {
		t.Fatalf("restart duplicated launch: %+v", got)
	}
	w := e.startPlanWorker(s.Steps[0])
	e.ok(w, "POST", taskPath(s.Steps[0].Task, "claim"), nil, nil)
	e.ok(w, "POST", taskPath(s.Steps[0].Task, "submit"), api.SubmitReq{Evidence: []string{"test passed"}, Note: "done"}, nil)
	e.planSweep()
	if e.planStatus(j.ID).Steps[1].Attempts != 0 {
		t.Fatal("submitted but unaccepted dependency started QA")
	}
	e.ok(e.afif(), "POST", taskPath(s.Steps[0].Task, "accept"), nil, nil)
	e.planSweep()
	got := e.planStatus(j.ID)
	if got.Steps[1].Attempts != 1 {
		t.Fatalf("accepted dependency did not start QA: %+v", got)
	}
	var stops []string
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/plan-stops", nil, &stops)
	if len(stops) != 1 || stops[0] != s.Steps[0].Worker {
		t.Fatalf("completed worker not retired: %v", stops)
	}
	e.ok(caller{e.d2, ""}, "GET", "/v1/device/plan-stops", nil, &stops)
	if len(stops) != 0 {
		t.Fatal("another device was told to stop a worker")
	}
}

func TestPlanRetriesAreBoundedAndFenceOldWorkers(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	p.Steps = p.Steps[:1]
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	e.planSweep()
	first := e.planStatus(j.ID).Steps[0]
	// No link picks up the spawn; timeout and retry survive normal sweeps.
	e.clock.advance(2 * time.Minute)
	e.planSweep()
	second := e.planStatus(j.ID).Steps[0]
	if second.Attempts != 2 || second.Worker == first.Worker {
		t.Fatalf("retry: %+v", second)
	}
	// A delayed old launch cannot claim while the replacement is registering.
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: first.Worker, Kind: "claude"}, nil)
	e.fail(403, caller{e.d1, first.Worker}, "POST", taskPath(first.Task, "claim"), nil)
	e.clock.advance(2 * time.Minute)
	e.planSweep()
	e.planSweep()
	final := e.planStatus(j.ID).Steps[0]
	if final.Attempts != 2 || final.Detail == "" {
		t.Fatalf("retry limit: %+v", final)
	}
}

func TestPlanValidationIsAtomicAndScoped(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	bad := examplePlan()
	bad.Steps[1].Profile = "missing"
	e.fail(404, e.afif(), "POST", planPath(j.ID), bad)
	var n int
	if err := e.hub.db.QueryRow(`SELECT count(*) FROM task WHERE job_id = ?`, j.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial plan: %d %v", n, err)
	}
	e.fail(403, e.worker(), "POST", planPath(j.ID), p)
	e.fail(403, e.lead(), "POST", planPath(j.ID), p)
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	p.Steps[0].Title = "Changed"
	e.fail(409, e.afif(), "POST", planPath(j.ID), p)
	e.fail(403, e.worker(), "POST", taskPath(e.planStatus(j.ID).Steps[0].Task, "claim"), nil)
}

func TestPlanPinsProfilesAndHonorsCapacityAndCancellation(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	spec := researcher()
	spec.Prompt = "new prompt"
	e.newProfile(caller{e.admin, ""}, "coder", spec)
	e.hub.opt.MaxSpawns = 1
	e.planSweep()
	s := e.planStatus(j.ID)
	if s.Steps[0].Profile != "coder@1" {
		t.Fatalf("profile not pinned: %+v", s)
	}
	e.startPlanWorker(s.Steps[0])
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), api.JobCloseReq{Cancel: true}, nil)
	e.clock.advance(2 * time.Minute)
	e.planSweep()
	if got := e.planStatus(j.ID); got.Steps[1].Attempts != 0 {
		t.Fatal("cancelled job dispatched work")
	}
}

func TestPlanWaitsForIntegrationAndStartsFromItsBranch(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	// Model device-reported integration states without executing Git here.
	if _, err := e.hub.db.Exec(`UPDATE task SET repo = '/repo', device_id = (SELECT id FROM device WHERE name = 'd1') WHERE id = ?`, j.ID); err != nil {
		t.Fatal(err)
	}
	s := e.planStatus(j.ID)
	if _, err := e.hub.db.Exec(`UPDATE task SET status = 'done' WHERE id = ?`, s.Steps[0].Task); err != nil {
		t.Fatal(err)
	}
	e.planSweep()
	if e.planStatus(j.ID).Steps[1].Attempts != 0 {
		t.Fatal("dependency with no integration started")
	}
	if _, err := e.hub.db.Exec(`INSERT INTO merge(task_id,job_id,device_id,agent,status,created_at) VALUES (?,?,(SELECT id FROM device WHERE name = 'd1'),'worker','failed',0)`, s.Steps[0].Task, j.ID); err != nil {
		t.Fatal(err)
	}
	e.planSweep()
	if e.planStatus(j.ID).Steps[1].Attempts != 0 {
		t.Fatal("failed integration started downstream work")
	}
	if _, err := e.hub.db.Exec(`UPDATE merge SET status = 'merged' WHERE job_id = ?`, j.ID); err != nil {
		t.Fatal(err)
	}
	e.planSweep()
	var base string
	if err := e.hub.db.QueryRow(`SELECT sp.base FROM spawn sp JOIN plan_step ps ON ps.spawn_id = sp.id WHERE ps.task_id = ?`, s.Steps[1].Task).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if base != fmt.Sprintf("job/%d/integration", j.ID) {
		t.Fatalf("wrong base: %s", base)
	}
}

func TestPlanCapacityAndJobStartCap(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	p.Steps[1].DependsOn = nil
	e.hub.opt.MaxSpawns = 1
	e.hub.opt.MaxJobStarts = 2
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	e.planSweep()
	s := e.planStatus(j.ID)
	if s.Steps[0].Attempts != 1 || s.Steps[1].Attempts != 0 {
		t.Fatalf("capacity ignored: %+v", s)
	}
	e.clock.advance(2 * time.Minute)
	e.planSweep()
	e.clock.advance(2 * time.Minute)
	e.planSweep()
	s = e.planStatus(j.ID)
	if s.Steps[0].Attempts != 2 || s.Steps[1].Attempts != 0 || s.Steps[1].Detail != "Job worker-start limit reached." {
		t.Fatalf("start cap ignored: %+v", s)
	}
}

func TestCancelledPlanCannotLaunchQueuedWorker(t *testing.T) {
	e := newEnv(t)
	j, p := e.preparePlan()
	e.ok(e.afif(), "POST", planPath(j.ID), p, nil)
	e.planSweep()
	var queued []api.Spawn
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/spawns", nil, &queued)
	if len(queued) != 1 {
		t.Fatalf("queued: %+v", queued)
	}
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), api.JobCloseReq{Cancel: true}, nil)
	var after []api.Spawn
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/spawns", nil, &after)
	if len(after) != 0 {
		t.Fatal("cancelled work is still offered to the device")
	}
	if code, _ := e.report(e.d1, queued[0].ID, api.SpawnReport{Status: "launching"}); code != 409 {
		t.Fatalf("stale launch claim: %d", code)
	}
}
