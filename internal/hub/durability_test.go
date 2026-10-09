package hub

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Failure cases of the task layer. The hub's promise is that the task graph, the
// evidence and the audit log are the durable state, so each test makes something go wrong
// (an owner that is too late, a request that arrives twice, a hub that restarts or is
// restored from an older copy) and checks that nothing is lost, nothing happens twice, and
// the refusal is a plain 4xx in words, never a 5xx.

func (e *env) claimed(c caller, title string) api.Task {
	e.t.Helper()
	t := e.create(e.lead(), api.TaskCreateReq{Title: title, AssignedTo: c.agent})
	return e.act(c, t.ID, "claim", nil)
}

func TestALateSubmitAfterTheLeaseExpiredIsRefusedAndTheTaskStaysOpen(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "late")
	e.clock.advance(20 * time.Minute) // the lease is 15
	e.sweep()
	if got := e.task(task.ID); got.Status != "open" {
		t.Fatalf("after the lease expired: %+v", got)
	}
	msg := e.fail(403, e.worker(), "POST", taskPath(task.ID, "submit"), evidence)
	if !strings.Contains(msg, "not yours") {
		t.Fatalf("the refusal should say why: %s", msg)
	}
	if got := e.task(task.ID); got.Status != "open" || len(got.Evidence) != 0 {
		t.Fatalf("a refused submit changed the task: %+v", got)
	}
	// The old owner can claim it again like anybody else, and then submit.
	e.act(e.worker(), task.ID, "claim", nil)
	e.act(e.worker(), task.ID, "submit", evidence)
}

func TestAReassignedTaskCannotBeSubmittedByItsOldOwner(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "moved")
	e.clock.advance(20 * time.Minute)
	e.sweep()
	e.act(e.lead(), task.ID, "assign", api.AssignReq{Agent: "worker2"})
	e.act(e.worker2(), task.ID, "claim", nil)
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "submit"), evidence)
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "heartbeat"), nil)
	e.act(e.worker2(), task.ID, "submit", evidence)
	if got := e.task(task.ID); got.Status != "submitted" || got.Owner != "worker2" {
		t.Fatalf("task: %+v", got)
	}
}

func TestAcceptingTwiceIsRefusedAndRecordedOnce(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "accepted once")
	e.act(e.worker(), task.ID, "submit", evidence)
	e.act(e.afif(), task.ID, "accept", nil)
	e.fail(409, e.afif(), "POST", taskPath(task.ID, "accept"), nil)
	e.fail(409, e.lead(), "POST", taskPath(task.ID, "accept"), nil)
	n := 0
	for _, r := range e.audit() {
		if r.Action == "task.accept" && r.Target == fmt.Sprintf("task:%d", task.ID) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("task.accept recorded %d times", n)
	}
}

func TestTwoAcceptsAtTheSameTimeHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "race")
	e.act(e.worker(), task.ID, "submit", evidence)
	codes := raced(e, 8, func() int {
		status, _ := e.do(e.afif(), "POST", taskPath(task.ID, "accept"), nil)
		return status
	})
	if codes[200] != 1 || codes[409] != 7 {
		t.Fatalf("eight simultaneous accepts: %v (want one 200 and seven 409)", codes)
	}
}

func TestTwoClaimsAtTheSameTimeHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "up for grabs"})
	callers := []caller{e.worker(), e.worker2(), e.observer(), e.lead()}
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for _, c := range callers {
		wg.Add(1)
		go func(c caller) {
			defer wg.Done()
			status, _ := e.do(c, "POST", taskPath(task.ID, "claim"), nil)
			mu.Lock()
			codes[status]++
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	if codes[200] != 1 {
		t.Fatalf("simultaneous claims: %v (want exactly one 200)", codes)
	}
	for status := range codes {
		if status >= 500 {
			t.Fatalf("a claim race produced a %d", status)
		}
	}
}

func TestTwoAnswersAtTheSameTimeHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	var esc api.Escalation
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "Ship it?", Options: []string{"yes", "no"}}, &esc)
	codes := raced(e, 6, func() int {
		status, _ := e.do(e.afif(), "POST", fmt.Sprintf("/v1/escalations/%d/answer", esc.ID), api.AnswerReq{Answer: "yes"})
		return status
	})
	if codes[200] != 1 || codes[409] != 5 {
		t.Fatalf("simultaneous answers: %v", codes)
	}
	answers := 0
	for _, m := range e.inbox(e.lead()) {
		if strings.Contains(m.Body, "Answer to your question") {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("the lead was told %d times", answers)
	}
}

func TestWorkSubmittedWhileTheLeadIsLostIsReviewedByTheNewLead(t *testing.T) {
	e := newEnv(t)
	var job api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "resilient", Lead: "lead"}, &job)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "part", AssignedTo: "worker"})
	e.act(e.worker(), task.ID, "claim", nil)
	// the lead's terminal goes away
	e.ok(e.lead(), "POST", "/v1/agents/lead/heartbeat", nil, nil)
	e.clock.advance(5 * time.Minute)
	e.sweep()
	if a := e.agentInfo("lead"); a.State != "offline" {
		t.Fatalf("lead: %+v", a)
	}
	// the worker finishes anyway; nothing is lost
	e.act(e.worker(), task.ID, "submit", evidence)
	if got := e.task(task.ID); got.Status != "submitted" {
		t.Fatalf("task: %+v", got)
	}
	// a person gives the job a new lead; the submitted work is theirs to review
	e.ok(e.observer(), "POST", "/v1/agents/observer/heartbeat", nil, nil)
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/resume", job.ID), api.JobResumeReq{Lead: "observer"}, nil)
	e.act(e.observer(), task.ID, "accept", nil)
	if got := e.task(task.ID); got.Status != "done" {
		t.Fatalf("task after the new lead accepted: %+v", got)
	}
}

func TestAStuckAgentThatNeverClaimedAnythingIsReported(t *testing.T) {
	e := newEnv(t)
	e.ok(e.worker(), "POST", "/v1/agents/worker/state", api.StateReq{State: "working"}, nil)
	e.clock.advance(20 * time.Minute)
	e.sweep()
	if e.audited("agent.stuck", "agent:worker") {
		t.Fatal("reported too early")
	}
	e.clock.advance(60 * time.Minute)
	e.sweep()
	if !e.audited("agent.stuck", "agent:worker") {
		t.Fatal("an agent that has said it is working for over an hour, holding no task, was not reported")
	}
	told := 0
	for _, m := range e.inbox(e.lead()) {
		if strings.Contains(m.Body, "worker") && strings.Contains(m.Body, "no task") {
			told++
		}
	}
	if told != 1 {
		t.Fatalf("the lead was told %d times", told)
	}
	// reported once per stretch of working, not every sweep
	e.clock.advance(10 * time.Minute)
	e.sweep()
	n := 0
	for _, r := range e.audit() {
		if r.Action == "agent.stuck" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("agent.stuck recorded %d times", n)
	}
	// an agent that is working on a claimed task is not stuck in this sense: the task lease covers it
	e.ok(e.worker2(), "POST", "/v1/agents/worker2/state", api.StateReq{State: "working"}, nil)
	busy := e.claimed(e.worker2(), "busy")
	for i := 0; i < 10; i++ { // ninety minutes of work, the lease renewed as the agent acts
		e.clock.advance(9 * time.Minute)
		e.ok(e.worker2(), "POST", taskPath(busy.ID, "heartbeat"), nil, nil)
		e.sweep()
	}
	if e.audited("agent.stuck", "agent:worker2") {
		t.Fatal("an agent holding a task was reported as stuck")
	}
}

// raced runs fn n times at once and counts the status codes.
func raced(e *env, n int, fn func() int) map[int]int {
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := fn()
			mu.Lock()
			codes[s]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	for s := range codes {
		if s >= 500 {
			e.t.Fatalf("a race produced a %d", s)
		}
	}
	return codes
}

// ---- the hub itself going away ----

// fileEnv is a hub on a database file, to be stopped and started again.
type fileEnv struct {
	t     *testing.T
	path  string
	clock *clock
	admin string
	srv   *httptest.Server
	hub   *Hub
	db    *sql.DB
}

func newFileEnv(t *testing.T, path string, cl *clock, admin string) *fileEnv {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if admin == "" {
		if admin, err = store.Init(db, cl.now()); err != nil {
			t.Fatal(err)
		}
	}
	h := New(db, Options{Lease: 15 * time.Minute, Now: cl.now, MsgRate: 1000})
	f := &fileEnv{t: t, path: path, clock: cl, admin: admin, hub: h, srv: httptest.NewServer(h.Handler()), db: db}
	return f
}

func (f *fileEnv) stop() { f.srv.Close(); f.db.Close() }

func (f *fileEnv) as() *env { // an env-like view for the same hub, for the request helpers
	return &env{t: f.t, hub: f.hub, srv: f.srv, clock: f.clock, admin: f.admin}
}

func TestARestartedHubKeepsTheTaskGraphTheLeasesAndTheAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	cl := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	f := newFileEnv(t, path, cl, "")
	e := f.as()
	d1 := e.device("d1")
	e.ok(caller{d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "lead", Kind: "shell"}, nil)
	e.ok(caller{d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "worker", Kind: "shell"}, nil)
	e.ok(caller{e.admin, ""}, "POST", "/v1/agents/lead/role", api.RoleReq{Role: "lead"}, nil)
	var tok api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "afif"}, &tok)
	lead, worker, human := caller{d1, "lead"}, caller{d1, "worker"}, caller{tok.Token, ""}
	a := e.create(lead, api.TaskCreateReq{Title: "claimed before", AssignedTo: "worker"})
	e.act(worker, a.ID, "claim", nil)
	b := e.create(lead, api.TaskCreateReq{Title: "submitted before", AssignedTo: "worker"})
	e.act(worker, b.ID, "claim", nil)
	e.act(worker, b.ID, "submit", evidence)
	var esc api.Escalation
	e.ok(lead, "POST", "/v1/escalations", api.AskReq{Question: "Which one?", Options: []string{"x", "y"}}, &esc)
	auditBefore := len(e.audit2(human))

	f.stop()
	cl.advance(3 * time.Minute) // the hub was down for a while
	f = newFileEnv(t, path, cl, f.admin)
	defer f.stop()
	e = f.as()

	// the same credentials still work, and everything is where it was
	if got := e.task2(human, a.ID); got.Status != "claimed" || got.Owner != "worker" {
		t.Fatalf("claimed task after restart: %+v", got)
	}
	if got := e.task2(human, b.ID); got.Status != "submitted" || len(got.Evidence) == 0 {
		t.Fatalf("submitted task after restart: %+v", got)
	}
	var open []api.Escalation
	e.ok(human, "GET", "/v1/escalations", nil, &open)
	if len(open) != 1 || open[0].Question != "Which one?" {
		t.Fatalf("open question after restart: %+v", open)
	}
	if n := len(e.audit2(human)); n < auditBefore {
		t.Fatalf("the audit log shrank: %d before, %d after", auditBefore, n)
	}
	// the lease still runs from where it was: heartbeat extends it, silence expires it
	e.ok(worker, "POST", taskPath(a.ID, "heartbeat"), nil, nil)
	cl.advance(16 * time.Minute)
	if err := f.hub.Sweep(); err != nil {
		t.Fatal(err)
	}
	if got := e.task2(human, a.ID); got.Status != "open" {
		t.Fatalf("the lease did not expire after the restart: %+v", got)
	}
	// and work carries on
	e.ok(human, "POST", taskPath(b.ID, "accept"), nil, nil)
	e.ok(human, "POST", fmt.Sprintf("/v1/escalations/%d/answer", esc.ID), api.AnswerReq{Answer: "x"}, nil)
}

func TestAHubRestoredFromAnOlderCopyRefusesStaleRequestsGently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	cl := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	f := newFileEnv(t, path, cl, "")
	e := f.as()
	d1 := e.device("d1")
	e.ok(caller{d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "lead", Kind: "shell"}, nil)
	e.ok(caller{d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "worker", Kind: "shell"}, nil)
	e.ok(caller{e.admin, ""}, "POST", "/v1/agents/lead/role", api.RoleReq{Role: "lead"}, nil)
	var tok api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "afif"}, &tok)
	lead, worker, human := caller{d1, "lead"}, caller{d1, "worker"}, caller{tok.Token, ""}
	task := e.create(lead, api.TaskCreateReq{Title: "after the backup", AssignedTo: "worker"})

	// a backup is taken here; afterwards the work moves on
	backup := filepath.Join(dir, "backup.db")
	if err := store.Backup(f.db, backup); err != nil {
		t.Fatal(err)
	}
	e.act(worker, task.ID, "claim", nil)
	e.act(worker, task.ID, "submit", evidence)
	late := e.create(lead, api.TaskCreateReq{Title: "created after the backup"})
	f.stop()

	// the hub is restored from the older copy
	restored := filepath.Join(dir, "restored.db")
	if err := store.Restore(backup, restored, false); err != nil {
		t.Fatal(err)
	}
	f = newFileEnv(t, restored, cl, f.admin)
	defer f.stop()
	e = f.as()

	if got := e.task2(human, task.ID); got.Status != "open" {
		t.Fatalf("restored task: %+v", got)
	}
	// the worker, who remembers a newer state, is refused in words, not with a crash
	msg := e.fail2(403, worker, "POST", taskPath(task.ID, "submit"), evidence)
	if !strings.Contains(msg, "not yours") {
		t.Fatalf("a stale submit should be explained: %s", msg)
	}
	// a task that did not exist at backup time is plainly not found
	e.fail2(404, human, "GET", fmt.Sprintf("/v1/tasks/%d", late.ID), nil)
	// and the worker can simply do it again
	e.ok(worker, "POST", taskPath(task.ID, "claim"), nil, nil)
	e.ok(worker, "POST", taskPath(task.ID, "submit"), evidence, nil)
	e.ok(human, "POST", taskPath(task.ID, "accept"), nil, nil)
}

func (e *env) audit2(c caller) []api.AuditRow {
	e.t.Helper()
	var rows []api.AuditRow
	e.ok(c, "GET", "/v1/audit", nil, &rows)
	return rows
}

func (e *env) task2(c caller, id int64) api.Task {
	e.t.Helper()
	var t api.Task
	e.ok(c, "GET", fmt.Sprintf("/v1/tasks/%d", id), nil, &t)
	return t
}

func (e *env) fail2(want int, c caller, method, path string, body any) string {
	e.t.Helper()
	return e.fail(want, c, method, path, body)
}

func TestATaskHeldLongerThanTheTimeLimitIsTakenBackEvenIfItsOwnerIsBusy(t *testing.T) {
	e := newEnv(t)
	e.hub.opt.MaxTaskTime = time.Hour
	task := e.claimed(e.worker(), "looping")
	for i := 0; i < 5; i++ { // busy: the lease is renewed every ten minutes
		e.clock.advance(10 * time.Minute)
		e.ok(e.worker(), "POST", taskPath(task.ID, "heartbeat"), nil, nil)
		e.sweep()
	}
	if got := e.task(task.ID); got.Status != "claimed" {
		t.Fatalf("taken back too early: %+v", got)
	}
	e.clock.advance(11 * time.Minute) // past an hour since the claim
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "heartbeat"), nil) // the owner's next sign of life is refused
	e.sweep()                                                            // (the timer stores the take-back; a refused request cannot)
	if got := e.task(task.ID); got.Status != "open" {
		t.Fatalf("a task over the time limit was not taken back: %+v", got)
	}
	if !e.audited("task.time_limit", fmt.Sprintf("task:%d", task.ID)) {
		t.Fatal("not recorded")
	}
	told := false
	for _, m := range e.inbox(e.lead()) {
		told = told || strings.Contains(m.Body, "time limit")
	}
	if !told {
		t.Fatal("the lead was not told")
	}
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "submit"), evidence)
}

func TestNoTimeLimitMeansNoTimeLimit(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "long and fine")
	for i := 0; i < 30; i++ {
		e.clock.advance(10 * time.Minute)
		e.ok(e.worker(), "POST", taskPath(task.ID, "heartbeat"), nil, nil)
		e.sweep()
	}
	if got := e.task(task.ID); got.Status != "claimed" {
		t.Fatalf("a task was taken back with no limit set: %+v", got)
	}
}
