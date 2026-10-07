package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/drivers"
	"handloom/internal/hub"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// fakeDriver records nudges instead of typing them.
type fakeDriver struct {
	mu     sync.Mutex
	nudges []string // "target|line"
	err    error
	dead   bool // the terminal is gone: Alive says no
}

func (f *fakeDriver) setDead(v bool) {
	f.mu.Lock()
	f.dead = v
	f.mu.Unlock()
}

func (f *fakeDriver) Name() string { return "tmux" }

func (f *fakeDriver) Alive(_ context.Context, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead
}

func (f *fakeDriver) Nudge(_ context.Context, target, line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.nudges = append(f.nudges, target+"|"+line)
	return nil
}

func (f *fakeDriver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.nudges)
}

type fixture struct {
	t      *testing.T
	link   *Link
	driver *fakeDriver
	now    time.Time
	mu     sync.Mutex
	admin  *client.Client
	lead   *client.Client // through the link socket
	worker *client.Client
	human  *client.Client
	url    string // the hub
	cred   string // this device's credential
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// newFixture starts a hub and one link (not its loops) with a lead and a
// worker on the same device. The worker has a wake target; tests set states.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	// The link writes scope and verification files under its home: never the real one.
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	f := &fixture{t: t, driver: &fakeDriver{}, now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	adminTok, err := store.Init(db, f.clock())
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(db, hub.Options{Now: f.clock, MsgRate: 1000})
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)

	f.admin = client.Direct(srv.URL, adminTok)
	var tok api.TokenResp
	f.must(f.admin.Post("/v1/admin/devices", api.NameReq{Name: "dev"}, &tok))
	var join api.JoinResp
	f.must(client.Direct(srv.URL, "").Post("/v1/devices/join", api.JoinReq{JoinToken: tok.Token}, &join))
	f.must(f.admin.Post("/v1/admin/humans", api.NameReq{Name: "afif"}, &tok))
	f.human = client.Direct(srv.URL, tok.Token)
	f.url, f.cred = srv.URL, join.Credential

	sock := filepath.Join(t.TempDir(), "link.sock")
	f.link = New(Options{
		Hub: srv.URL, Device: "dev", Credential: join.Credential, Socket: sock,
		Now: f.clock, Log: log.New(io.Discard, "", 0),
		Drivers: func(target string) (drivers.Driver, string, error) {
			if !strings.HasPrefix(target, "tmux:") {
				return nil, "", errors.New("no driver")
			}
			return f.driver, strings.TrimPrefix(target, "tmux:"), nil
		},
	})
	ln, err := listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	sockSrv := httptest.NewUnstartedServer(f.link.handler())
	sockSrv.Listener = ln
	sockSrv.Start()
	t.Cleanup(sockSrv.Close)

	f.lead = client.Socket(sock, "lead")
	f.worker = client.Socket(sock, "worker")
	f.must(f.lead.Post("/v1/agents", api.RegisterReq{Name: "lead", Kind: "claude"}, nil))
	f.must(f.worker.Post("/v1/agents", api.RegisterReq{Name: "worker", Kind: "claude", WakeTarget: "tmux::%1"}, nil))
	f.must(f.admin.Post("/v1/agents/lead/role", api.RoleReq{Role: "lead"}, nil))
	return f
}

func (f *fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) state(c *client.Client, state string) {
	f.t.Helper()
	f.must(c.Post("/v1/agents/"+c.Agent+"/state", api.StateReq{State: state}, nil))
}

func (f *fixture) mail(to string) {
	f.t.Helper()
	f.must(f.human.Post("/v1/messages", api.SendReq{To: to, Body: "hello"}, nil))
}

func (f *fixture) ladder() { f.link.runLadder(context.Background()) }

func (f *fixture) inbox(c *client.Client) []api.Message {
	f.t.Helper()
	var m []api.Message
	f.must(c.Get("/v1/inbox", &m))
	return m
}

func (f *fixture) audit(action string) []api.AuditRow {
	f.t.Helper()
	var rows, out []api.AuditRow
	f.must(f.human.Get("/v1/audit", &rows))
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func (f *fixture) agentState(name string) string {
	f.t.Helper()
	var agents []api.Agent
	f.must(f.human.Get("/v1/agents", &agents))
	for _, a := range agents {
		if a.Name == name {
			return a.State
		}
	}
	return ""
}

// The socket adds the device credential; the caller only names its agent.
func TestSocketForwardsAsAgent(t *testing.T) {
	f := newFixture(t)
	var w api.WhoAmI
	f.must(f.worker.Get("/v1/whoami", &w))
	if w.Agent == nil || w.Agent.Name != "worker" || w.Device != "dev" {
		t.Fatalf("whoami: %+v", w)
	}
	// Scopes still apply behind the socket.
	err := f.worker.Post("/v1/tasks", api.TaskCreateReq{Title: "t"}, nil)
	var ae *client.Error
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("worker created a task through the socket: %v", err)
	}
	// A caller cannot swap in its own token.
	f.worker.Token = "hva_stolen"
	f.must(f.worker.Get("/v1/whoami", &w))
	if w.Kind != "device" {
		t.Fatalf("caller-supplied token was used: %+v", w)
	}
}

func TestLadderIdleAgentIsNudged(t *testing.T) {
	f := newFixture(t)
	f.state(f.worker, "idle")
	f.ladder()
	if f.driver.count() != 0 {
		t.Fatal("nudged with no mail")
	}
	f.mail("worker")
	f.mail("worker")
	f.ladder()
	if f.driver.count() != 1 {
		t.Fatalf("nudges: %v", f.driver.nudges)
	}
	// One nudge for both messages, fixed text, typed at the wake target.
	if got := f.driver.nudges[0]; got != ":%1|You have 2 new handloom messages. Run: handloom inbox" {
		t.Fatalf("nudge: %q", got)
	}
	wakes := f.audit("wake")
	if len(wakes) != 1 || wakes[0].Target != "agent:worker" || !strings.Contains(string(wakes[0].Payload), `"method":"tmux"`) {
		t.Fatalf("audit: %+v", wakes)
	}
	// Delivered now, so no second nudge for the same mail.
	f.ladder()
	f.advance(2 * time.Minute)
	f.ladder()
	if f.driver.count() != 1 {
		t.Fatalf("nudged again for delivered mail: %v", f.driver.nudges)
	}
}

func TestLadderNeverTypesIntoWorkingOrBlocked(t *testing.T) {
	f := newFixture(t)
	f.state(f.lead, "working") // the lead's own mail waits for its end-of-turn hook
	f.state(f.worker, "working")
	f.mail("worker")
	f.ladder()
	if f.driver.count() != 0 || len(f.audit("wake.failed")) != 0 {
		t.Fatal("a working agent must be left to its end-of-turn hook")
	}

	f.state(f.worker, "blocked")
	f.ladder()
	if len(f.audit("wake.failed")) != 0 {
		t.Fatal("a prompt that may be answered in a moment was reported at once")
	}
	f.advance(31 * time.Second)
	f.ladder()
	f.ladder()
	if f.driver.count() != 0 {
		t.Fatal("typed into a blocked agent")
	}
	// Reported to the lead, once for this batch.
	fails := f.audit("wake.failed")
	if len(fails) != 1 || !strings.Contains(string(fails[0].Payload), "state_blocked") {
		t.Fatalf("audit: %+v", fails)
	}
	if m := f.inbox(f.lead); len(m) != 1 || !strings.Contains(m[0].Body, "worker cannot be woken (state_blocked)") {
		t.Fatalf("lead inbox: %+v", m)
	}
	// More mail while still blocked: the lead already knows.
	f.mail("worker")
	f.ladder()
	if len(f.audit("wake.failed")) != 1 {
		t.Fatal("the same failure was reported twice")
	}
	// Unblocked and blocked again is a new episode.
	f.state(f.worker, "working")
	f.ladder()
	f.state(f.worker, "blocked")
	f.advance(31 * time.Second)
	f.ladder()
	if len(f.audit("wake.failed")) != 2 || f.driver.count() != 0 {
		t.Fatal("a new blocked episode was not reported")
	}
}

func TestLadderRateLimitAndMerge(t *testing.T) {
	f := newFixture(t)
	f.state(f.worker, "idle")
	f.mail("worker")
	f.ladder()
	// More mail within the minute: no second nudge yet.
	f.advance(20 * time.Second)
	f.mail("worker")
	f.mail("worker")
	f.ladder()
	if f.driver.count() != 1 {
		t.Fatalf("second nudge within 60s: %v", f.driver.nudges)
	}
	// After the minute, one nudge covers everything pending.
	f.advance(41 * time.Second)
	f.ladder()
	if f.driver.count() != 2 || !strings.Contains(f.driver.nudges[1], "3 new handloom messages") {
		t.Fatalf("nudges: %v", f.driver.nudges)
	}
}

func TestLadderMarksUnknownAfterTwoUnansweredNudges(t *testing.T) {
	f := newFixture(t)
	f.state(f.lead, "working")
	f.state(f.worker, "idle")
	f.mail("worker")
	f.ladder() // nudge 1
	f.advance(5 * time.Minute)
	f.ladder() // nudge 2: mail still unread
	if f.driver.count() != 2 {
		t.Fatalf("nudges: %v", f.driver.nudges)
	}
	f.advance(4 * time.Minute)
	f.ladder()
	if f.agentState("worker") != "idle" || f.driver.count() != 2 {
		t.Fatal("gave up before 10 minutes, or nudged a third time")
	}
	f.advance(2 * time.Minute)
	f.ladder()
	f.ladder()
	if f.agentState("worker") != api.StateUnknown {
		t.Fatalf("state %s, want unknown", f.agentState("worker"))
	}
	m := f.inbox(f.lead)
	if len(m) != 1 || !strings.Contains(m[0].Body, "no_inbox_after_nudges") {
		t.Fatalf("lead inbox: %+v", m)
	}
}

func TestInboxCallResetsNudgeCount(t *testing.T) {
	f := newFixture(t)
	f.state(f.worker, "idle")
	f.mail("worker")
	f.ladder()
	if len(f.inbox(f.worker)) != 1 { // read through the socket: the link sees it
		t.Fatal("inbox")
	}
	for i := 0; i < 3; i++ {
		f.advance(5 * time.Minute)
		f.mail("worker")
		f.ladder()
		f.inbox(f.worker)
	}
	if f.driver.count() != 4 || f.agentState("worker") != "idle" {
		t.Fatalf("nudges %d, state %s", f.driver.count(), f.agentState("worker"))
	}
}

func TestLadderReportsWhenNoWayToWake(t *testing.T) {
	f := newFixture(t)
	// The lead has no wake target.
	f.state(f.lead, "idle")
	f.state(f.worker, "idle")
	f.must(f.worker.Post("/v1/messages", api.SendReq{To: "lead", Body: "hi"}, nil))
	f.ladder()
	fails := f.audit("wake.failed")
	if len(fails) != 1 || fails[0].Target != "agent:lead" || !strings.Contains(string(fails[0].Payload), "no_wake_target") {
		t.Fatalf("audit: %+v", fails)
	}
	// A driver error is reported too.
	f.driver.err = errors.New("pane is gone")
	f.mail("worker")
	f.ladder()
	fails = f.audit("wake.failed")
	if len(fails) != 2 || !strings.Contains(string(fails[1].Payload), "driver_error: pane is gone") {
		t.Fatalf("audit: %+v", fails)
	}
}

// The end-of-turn hook path, through the socket.
func TestTurnEndThroughSocket(t *testing.T) {
	f := newFixture(t)
	f.state(f.worker, "working")
	f.mail("worker")
	f.ladder()
	var r api.TurnEndResp
	f.must(f.worker.Post("/v1/agents/worker/turn-end", nil, &r))
	if !r.Block || r.Unread != 1 {
		t.Fatalf("turn-end: %+v", r)
	}
	f.inbox(f.worker)
	f.must(f.worker.Post("/v1/agents/worker/turn-end", nil, &r))
	if r.Block || f.agentState("worker") != "idle" {
		t.Fatalf("turn-end after reading: %+v", r)
	}
	if f.driver.count() != 0 {
		t.Fatal("typed a nudge although the hook delivered")
	}
}

// Activity from the adapter extends the leases of the agent's claimed tasks
// and ends a blocked state.
func TestActivityHeartbeat(t *testing.T) {
	f := newFixture(t)
	var task api.Task
	f.must(f.lead.Post("/v1/tasks", api.TaskCreateReq{Title: "t"}, &task))
	f.must(f.worker.Post("/v1/tasks/1/claim", nil, &task))
	first := *task.LeaseExpiresAt

	f.state(f.worker, "blocked")
	f.ladder()
	f.advance(5 * time.Minute)
	f.link.activity("worker")
	f.must(f.worker.Get("/v1/tasks/1", &task))
	if !task.LeaseExpiresAt.After(first) {
		t.Fatalf("lease not extended: %v", task.LeaseExpiresAt)
	}
	if f.agentState("worker") != "working" {
		t.Fatalf("state %s, want working", f.agentState("worker"))
	}
	// Throttled: a second report within the interval does nothing.
	f.advance(30 * time.Second)
	f.link.activity("worker")
	if n := len(f.audit("task.heartbeat")); n != 1 {
		t.Fatalf("%d heartbeats, want 1", n)
	}
}

func TestSocketIsOwnerOnlyAndExclusive(t *testing.T) {
	f := newFixture(t)
	st, err := os.Stat(f.link.opt.Socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, want 0600 (%v)", st.Mode().Perm(), err)
	}
	if _, err := listen(f.link.opt.Socket); err == nil {
		t.Fatal("a second link started on the same socket")
	}
}

// ---- headless resume (wake ladder step 3) ----

type headlessCall struct {
	agent, dir string
	argv       []string
}

// headlessFixture makes "worker" a headless agent and records the turns the
// link starts instead of running them.
func headlessFixture(t *testing.T) (*fixture, *[]headlessCall, chan error) {
	f := newFixture(t)
	var calls []headlessCall
	done := make(chan error, 4)
	f.link.opt.HeadlessRun = func(_ context.Context, agent, dir string, argv []string) (func() error, error) {
		calls = append(calls, headlessCall{agent, dir, argv})
		return func() error { return <-done }, nil
	}
	f.must(f.worker.Post("/v1/agents", api.RegisterReq{Name: "worker", Kind: "claude",
		WakeTarget: HeadlessTarget, SessionID: "sess-9", Dir: "/work/project"}, nil))
	f.state(f.lead, "working")
	return f, &calls, done
}

func TestLadderHeadlessResume(t *testing.T) {
	f, calls, done := headlessFixture(t)
	f.state(f.worker, "offline") // a headless agent is offline between turns
	f.mail("worker")
	f.ladder()
	if len(*calls) != 1 || f.driver.count() != 0 {
		t.Fatalf("calls %v, typed nudges %d", *calls, f.driver.count())
	}
	c := (*calls)[0]
	want := "claude -p --resume sess-9 You have 1 new handloom message. Run: handloom inbox"
	if c.agent != "worker" || c.dir != "/work/project" || strings.Join(c.argv, " ") != want {
		t.Fatalf("headless call: %+v", c)
	}
	wakes := f.audit("wake")
	if len(wakes) != 1 || !strings.Contains(string(wakes[0].Payload), `"method":"headless"`) {
		t.Fatalf("audit: %+v", wakes)
	}
	// While the turn runs, more mail starts no second turn: its Stop hook delivers.
	f.advance(2 * time.Minute)
	f.mail("worker")
	f.ladder()
	if len(*calls) != 1 {
		t.Fatalf("second turn started while the first runs: %v", *calls)
	}
	// The turn ends; the unread mail gets a new turn.
	done <- nil
	waitFor(t, func() bool { f.ladder(); return len(*calls) == 2 })
	done <- nil
}

func TestLadderHeadlessFailures(t *testing.T) {
	f, calls, done := headlessFixture(t)
	f.state(f.worker, "offline")
	// A turn that ends with an error is reported to the lead.
	f.mail("worker")
	f.ladder()
	done <- errors.New("exit status 1")
	waitFor(t, func() bool { return len(f.audit("wake.failed")) == 1 })
	if p := string(f.audit("wake.failed")[0].Payload); !strings.Contains(p, "headless turn failed: exit status 1") {
		t.Fatalf("audit: %s", p)
	}

	// Overrides replace the command; a kind with no command cannot be woken.
	f.link.opt.Headless = map[string][]string{"claude": {"/opt/claude", "--resume={session_id}", "-p", "{prompt}"}}
	argv, err := f.link.headlessArgv(api.DeviceAgent{Agent: api.Agent{Kind: "claude", SessionID: "s", Dir: "/d"}}, "hi")
	if err != nil || strings.Join(argv, " ") != "/opt/claude --resume=s -p hi" {
		t.Fatalf("override: %v %v", argv, err)
	}
	for _, a := range []api.Agent{{Kind: "shell", SessionID: "s", Dir: "/d"}, {Kind: "claude", Dir: "/d"}, {Kind: "claude", SessionID: "s"}} {
		if _, err := f.link.headlessArgv(api.DeviceAgent{Agent: a}, "hi"); err == nil {
			t.Errorf("headless command built for %+v", a)
		}
	}
	_ = calls
}

// An offline agent with a terminal target is not resumed headless: its
// human closed it.
func TestOfflineTerminalAgentIsNotResumed(t *testing.T) {
	f := newFixture(t)
	f.state(f.lead, "working")
	called := false
	f.link.opt.HeadlessRun = func(context.Context, string, string, []string) (func() error, error) {
		called = true
		return func() error { return nil }, nil
	}
	f.state(f.worker, "offline")
	f.mail("worker")
	f.ladder()
	if called || f.driver.count() != 0 {
		t.Fatal("woke an offline terminal agent")
	}
	if fails := f.audit("wake.failed"); len(fails) != 1 || !strings.Contains(string(fails[0].Payload), "state_offline") {
		t.Fatalf("audit: %+v", fails)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func (f *fixture) lease(agent string) *time.Time {
	f.t.Helper()
	var as []api.Agent
	f.must(f.human.Get("/v1/agents", &as))
	for _, a := range as {
		if a.Name == agent {
			return a.LeaseUntil
		}
	}
	f.t.Fatalf("no agent %s", agent)
	return nil
}

// The link vouches for an agent's terminal while it exists, and only then;
// agents without a terminal are not asked to.
func TestLadderVouchesForLiveTerminals(t *testing.T) {
	f := newFixture(t)
	f.state(f.worker, api.StateWorking)
	f.ladder()
	first := f.lease("worker")
	if first == nil {
		t.Fatal("no lease after the ladder ran with a live terminal")
	}
	if f.lease("lead") != nil {
		t.Fatal("an agent with no terminal got a lease")
	}

	// Not renewed more often than LiveEvery.
	f.advance(5 * time.Second)
	f.ladder()
	if got := f.lease("worker"); got == nil || !got.Equal(*first) {
		t.Fatalf("renewed too soon: %v then %v", first, got)
	}
	f.advance(30 * time.Second)
	f.ladder()
	second := f.lease("worker")
	if second == nil || !second.After(*first) {
		t.Fatalf("not renewed after LiveEvery: %v then %v", first, second)
	}

	// The terminal disappears: the lease is left to run out.
	f.driver.setDead(true)
	f.advance(30 * time.Second)
	f.ladder()
	if got := f.lease("worker"); got == nil || !got.Equal(*second) {
		t.Fatalf("a dead terminal was vouched for: %v then %v", second, got)
	}
}

// ---- spawn ----

type tmuxLog struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error // by first tmux argument after -L <sock>
	up    bool             // the server has a "handloom" session
}

func (t *tmuxLog) run(_ context.Context, name string, args ...string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, name+" "+strings.Join(args, " "))
	sub := args[2]
	if err := t.fail[sub]; err != nil {
		return "", err
	}
	switch sub {
	case "has-session":
		if !t.up {
			return "", errors.New("no server")
		}
	case "new-session":
		t.up = true
	}
	return "", nil
}

func (t *tmuxLog) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.calls...)
}

func (f *fixture) spawnLink(tl *tmuxLog) {
	f.t.Helper()
	f.link.opt.Tmux = tl.run
	f.link.opt.TmuxSocket = "hltest-spawn"
	f.link.opt.WorkRoot = filepath.Join(f.t.TempDir(), "work")
	f.link.opt.Binary = "/opt/handloom/bin/handloom"
}

func (f *fixture) spawnStatus(id int64) api.Spawn {
	f.t.Helper()
	var s api.Spawn
	f.must(f.human.Get(fmt.Sprintf("/v1/spawns/%d", id), &s))
	return s
}

func (f *fixture) requestSpawn(name string, job int64) api.Spawn {
	f.t.Helper()
	var s api.Spawn
	f.must(f.human.Post("/v1/spawns", api.SpawnReq{Name: name, Kind: "claude", Device: "dev", Job: job}, &s))
	return s
}

func TestLinkStartsASpawnInItsOwnTmux(t *testing.T) {
	f := newFixture(t)
	tl := &tmuxLog{}
	f.spawnLink(tl)
	s1 := f.requestSpawn("alpha", 0)
	f.ladder()
	calls := tl.all()
	if len(calls) != 3 || !strings.HasPrefix(calls[0], "tmux -L hltest-spawn has-session") || !strings.Contains(calls[2], "remain-on-exit failed") {
		t.Fatalf("tmux calls: %q", calls)
	}
	first := calls[1]
	for _, want := range []string{"tmux -L hltest-spawn new-session -d -s handloom -n alpha -c " + f.link.opt.WorkRoot,
		"spawn-exec " + fmt.Sprint(s1.ID), "'/opt/handloom/bin/handloom'", "HANDLOOM_HOME="} {
		if !strings.Contains(first, want) {
			t.Errorf("new-session command lacks %q: %s", want, first)
		}
	}
	if got := f.spawnStatus(s1.ID).Status; got != "launching" {
		t.Fatalf("status after the link took it: %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.link.opt.WorkRoot, "none", "alpha")); err != nil {
		t.Fatalf("work directory: %v", err)
	}

	// Another tick does not start it twice; a second agent gets a window in the same server.
	f.ladder()
	s2 := f.requestSpawn("beta", 0)
	f.ladder()
	n := 0
	for _, c := range tl.all() {
		if strings.Contains(c, "spawn-exec "+fmt.Sprint(s1.ID)) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("spawn %d was launched %d times", s1.ID, n)
	}
	last := tl.all()[len(tl.all())-1]
	if !strings.Contains(last, "new-window -d -t handloom: -n beta") || !strings.Contains(last, "spawn-exec "+fmt.Sprint(s2.ID)) {
		t.Fatalf("second spawn: %s", last)
	}
}

func TestLinkReportsAFailedStart(t *testing.T) {
	f := newFixture(t)
	tl := &tmuxLog{fail: map[string]error{"new-session": errors.New("tmux: no space for a new terminal")}}
	f.spawnLink(tl)
	s := f.requestSpawn("alpha", 0)
	f.ladder()
	got := f.spawnStatus(s.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "no space") {
		t.Fatalf("after a failed start: %+v", got)
	}
}

func TestLinkRefusesAHostileName(t *testing.T) {
	f := newFixture(t)
	tl := &tmuxLog{}
	f.spawnLink(tl)
	err := f.link.launch(context.Background(), api.Spawn{ID: 1, Name: "x; rm -rf ~", Kind: "claude"})
	if err == nil || len(tl.all()) != 0 {
		t.Fatalf("a name with shell characters was launched: %v %q", err, tl.all())
	}
}

// ---- repo jobs ----

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) repoJob(repo string) api.Job { return f.repoJobVerify(repo, "") }

func (f *fixture) repoJobVerify(repo, verify string) api.Job {
	f.t.Helper()
	var j api.Job
	f.must(f.human.Post("/v1/jobs", api.JobNewReq{Title: "Fix it", Repo: repo, Verify: verify, Device: "dev"}, &j))
	return j
}

func TestEachAgentOfARepoJobGetsItsOwnWorktree(t *testing.T) {
	f := newFixture(t)
	tl := &tmuxLog{}
	f.spawnLink(tl)
	repo := gitRepo(t)
	head := gitOut(t, repo, "rev-parse", "HEAD")
	// A change nobody committed must not stop the start.
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main // edited\n"), 0o644)
	j := f.repoJob(repo)
	a, b := f.requestSpawn("alpha", j.ID), f.requestSpawn("beta", j.ID)
	f.ladder()

	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), name)
		if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != fmt.Sprintf("job/%d/%s", j.ID, name) {
			t.Errorf("%s is on branch %q", name, got)
		}
		if got := gitOut(t, dir, "rev-parse", "HEAD"); got != head {
			t.Errorf("%s was cut from %s, not the repository's HEAD %s", name, got, head)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "main.go")); err != nil || string(b) != "package main\n" {
			t.Errorf("%s: main.go is %q (%v): a worktree is the last commit, not the working copy", name, b, err)
		}
	}
	// The two are separate directories on separate branches.
	os.WriteFile(filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha", "new.txt"), []byte("x"), 0o644)
	if _, err := os.Stat(filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "beta", "new.txt")); err == nil {
		t.Fatal("beta sees alpha's file")
	}
	if got := f.spawnStatus(a.ID).Status; got != "launching" {
		t.Fatalf("alpha: %s", got)
	}
	_ = b
	// The window for each opens in its own worktree.
	if n := strings.Count(strings.Join(tl.all(), "\n"), "-c "+filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID))); n != 2 {
		t.Fatalf("windows opened in the worktrees: %d\n%s", n, strings.Join(tl.all(), "\n"))
	}
}

func TestARepoJobWithoutARepoFailsTheSpawn(t *testing.T) {
	f := newFixture(t)
	f.spawnLink(&tmuxLog{})
	j := f.repoJob(filepath.Join(t.TempDir(), "not-a-repo"))
	s := f.requestSpawn("alpha", j.ID)
	f.ladder()
	got := f.spawnStatus(s.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "not a git repository") {
		t.Fatalf("spawn: %+v", got)
	}
}

func TestAStartedAgainAgentReusesItsBranch(t *testing.T) {
	f := newFixture(t)
	f.spawnLink(&tmuxLog{})
	repo := gitRepo(t)
	j := f.repoJob(repo)
	s := f.requestSpawn("alpha", j.ID)
	f.ladder()
	// It fails (nobody started it), the worktree is removed, and the name is asked for again.
	dir := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha")
	exec.Command("git", "-C", repo, "worktree", "remove", "--force", dir).Run()
	s2 := f.requestSpawnAfterFail("alpha", j.ID, s.ID)
	f.ladder()
	if got := f.spawnStatus(s2.ID).Status; got != "launching" {
		t.Fatalf("second start: %+v", f.spawnStatus(s2.ID))
	}
}

// requestSpawnAfterFail fails an earlier request by hand (through the device's own report) and asks again.
func (f *fixture) requestSpawnAfterFail(name string, job, old int64) api.Spawn {
	f.t.Helper()
	dev := client.Direct(f.url, f.cred)
	f.must(dev.Post(fmt.Sprintf("/v1/spawns/%d/report", old), api.SpawnReport{Status: "failed", Error: "test"}, nil))
	return f.requestSpawn(name, job)
}

// ---- the scope check ----

// scopedAgent starts an agent of a repo job under a profile that may only
// change src/**, with a task assigned to it that it has claimed. It returns
// the agent's client (through the link socket), its worktree and the task.
func (f *fixture) scopedAgent(write []string) (*client.Client, string, api.Task) {
	return f.scopedAgentVerify(write, "")
}

func (f *fixture) scopedAgentVerify(write []string, verify string) (*client.Client, string, api.Task) {
	f.t.Helper()
	tl := &tmuxLog{}
	f.spawnLink(tl)
	repo := gitRepo(f.t)
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	os.WriteFile(filepath.Join(repo, "src", "a.go"), []byte("package src\n"), 0o644)
	gitOut(f.t, repo, "add", ".")
	gitOut(f.t, repo, "commit", "-q", "-m", "src")
	f.must(f.admin.Post("/v1/profiles", api.ProfileReq{Name: "coder", Spec: profile.Spec{Kind: "claude", Tools: profile.Tools{Allow: []string{"read", "edit"}}, Write: write}}, nil))
	j := f.repoJobVerify(repo, verify)
	var s api.Spawn
	f.must(f.human.Post("/v1/spawns", api.SpawnReq{Name: "alpha", Profile: "coder", Job: j.ID, Device: "dev"}, &s))
	f.ladder()
	if got := f.spawnStatus(s.ID).Status; got != "launching" {
		f.t.Fatalf("spawn: %+v", f.spawnStatus(s.ID))
	}
	ac := client.Socket(f.link.opt.Socket, "alpha")
	f.must(ac.Post("/v1/agents", api.RegisterReq{Name: "alpha", Kind: "claude"}, nil))
	dev := client.Direct(f.url, f.cred)
	f.must(dev.Post(fmt.Sprintf("/v1/spawns/%d/report", s.ID), api.SpawnReport{Status: "started"}, nil))
	var task api.Task
	f.must(f.human.Post("/v1/tasks", api.TaskCreateReq{Title: "change src", Job: j.ID, AssignedTo: "alpha"}, &task))
	f.must(ac.Post(fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil))
	return ac, filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha"), task
}

func (f *fixture) submit(ac *client.Client, task api.Task) error {
	return ac.Post(fmt.Sprintf("/v1/tasks/%d/submit", task.ID), api.SubmitReq{Evidence: []string{"file:x"}}, nil)
}

func TestSubmitIsRefusedForFilesOutsideTheProfilesScope(t *testing.T) {
	f := newFixture(t)
	ac, dir, task := f.scopedAgent([]string{"src/**"})
	cleanAdapterFiles := func() {
		// What the adapter and the agent CLI write is not the agent's work.
		os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("rules"), 0o644)
		os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
		os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte("{}"), 0o644)
		os.MkdirAll(filepath.Join(dir, ".handloom"), 0o755)
		os.WriteFile(filepath.Join(dir, ".handloom", "scope.json"), []byte(`{"write":["**"]}`), 0o600) // an agent cannot widen its own scope
	}
	cleanAdapterFiles()

	// Outside the scope: an untracked file.
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("x"), 0o644)
	err := f.submit(ac, task)
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != 409 || !strings.Contains(ce.Msg, "docs/x.md") || !strings.Contains(ce.Msg, "src/**") {
		t.Fatalf("a new file outside the scope: %v", err)
	}
	if got := f.task(task.ID).Status; got != "claimed" {
		t.Fatalf("the refused submit reached the hub: task is %s", got)
	}
	os.Remove(filepath.Join(dir, "docs", "x.md"))

	// A tracked file outside the scope, edited and then committed: still refused.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // changed\n"), 0o644)
	if err := f.submit(ac, task); err == nil || !strings.Contains(err.Error(), "main.go") {
		t.Fatalf("an edited file outside the scope: %v", err)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-m", "oops")
	if err := f.submit(ac, task); err == nil || !strings.Contains(err.Error(), "main.go") {
		t.Fatalf("a committed change outside the scope: %v", err)
	}
	gitOut(t, dir, "checkout", "-q", "HEAD~1", "--", "main.go")
	gitOut(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-am", "revert")

	// A rename out of the scope counts for both names.
	gitOut(t, dir, "mv", "src/a.go", "docs-a.go")
	if err := f.submit(ac, task); err == nil || !strings.Contains(err.Error(), "docs-a.go") {
		t.Fatalf("a rename out of the scope: %v", err)
	}
	gitOut(t, dir, "mv", "docs-a.go", "src/a.go")

	// The refusals are on record and the lead was told.
	if rows := f.audit("task.scope_refused"); len(rows) < 3 {
		t.Fatalf("%d refusals audited", len(rows))
	}
	found := false
	for _, m := range f.inbox(f.lead) {
		found = found || (strings.Contains(m.Body, "alpha tried to submit") && strings.Contains(m.Body, "main.go"))
	}
	if !found {
		t.Fatal("the lead was not told")
	}

	// Now only src/ and adapter files differ from where it started: the submit goes through.
	os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("package src\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // better\n"), 0o644)
	if err := f.submit(ac, task); err != nil {
		t.Fatalf("a submit inside the scope: %v", err)
	}
	if got := f.task(task.ID).Status; got != "submitted" {
		t.Fatalf("task is %s", got)
	}
}

func TestTheScopeCheckFailsClosedAndSparesOthers(t *testing.T) {
	f := newFixture(t)
	ac, dir, task := f.scopedAgent([]string{"src/**"})
	// A worktree that cannot be inspected is not waved through.
	os.RemoveAll(dir)
	var ce *client.Error
	if err := f.submit(ac, task); !errors.As(err, &ce) || ce.Status != 500 || !strings.Contains(ce.Msg, "could not check") {
		t.Fatalf("an unreadable worktree: %v", err)
	}
	// Agents the link did not start, and profiles without write, are not checked.
	var plain api.Task
	f.must(f.human.Post("/v1/tasks", api.TaskCreateReq{Title: "plain", AssignedTo: "worker"}, &plain))
	f.must(f.worker.Post(fmt.Sprintf("/v1/tasks/%d/claim", plain.ID), nil, nil))
	if err := f.worker.Post(fmt.Sprintf("/v1/tasks/%d/submit", plain.ID), api.SubmitReq{Evidence: []string{"file:x"}}, nil); err != nil {
		t.Fatalf("an agent without a scope: %v", err)
	}
}

func TestAnAgentCannotFileARefusalThroughTheSocket(t *testing.T) {
	f := newFixture(t)
	err := f.worker.Post("/v1/device/scope-refused", api.ScopeRefusal{Agent: "lead", Task: 1, Paths: []string{"x"}}, nil)
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("an agent filing a scope refusal: %v", err)
	}
	if rows := f.audit("task.scope_refused"); len(rows) != 0 {
		t.Fatal("a refusal was recorded")
	}
}

func TestAProfileWithoutWriteIsNotChecked(t *testing.T) {
	f := newFixture(t)
	ac, dir, task := f.scopedAgent(nil)
	os.WriteFile(filepath.Join(dir, "anything.txt"), []byte("x"), 0o644)
	if err := f.submit(ac, task); err != nil {
		t.Fatalf("no write list, no check: %v", err)
	}
}

func (f *fixture) task(id int64) api.Task {
	f.t.Helper()
	var t api.Task
	f.must(f.human.Get(fmt.Sprintf("/v1/tasks/%d", id), &t))
	return t
}

// ---- verification ----

func (f *fixture) waitCheck(id int64) *api.TaskCheck {
	f.t.Helper()
	for i := 0; i < 300; i++ {
		if ck := f.task(id).Check; ck != nil {
			return ck
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("task %d was never verified", id)
	return nil
}

func TestTheDeviceVerifiesWhatWasSubmitted(t *testing.T) {
	t.Setenv("HANDLOOM_TOKEN", "must-not-reach-the-command")
	f := newFixture(t)
	ac, dir, task := f.scopedAgentVerify(nil, `echo checking; test -f src/a.go && test -z "$HANDLOOM_TOKEN"`)
	os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("package src\n"), 0o644)
	if err := f.submit(ac, task); err != nil {
		t.Fatal(err)
	}
	ck := f.waitCheck(task.ID)
	if ck.ExitCode != 0 || ck.TimedOut || ck.Agent != "alpha" || ck.Device != "dev" || !strings.Contains(ck.Tail, "checking") || len(ck.SHA256) != 64 {
		t.Fatalf("check: %+v", ck)
	}
	// The lead, an agent, may accept work the device passed.
	f.must(f.lead.Post(fmt.Sprintf("/v1/tasks/%d/accept", task.ID), nil, nil))
	if got := f.task(task.ID).Status; got != "done" {
		t.Fatalf("task is %s", got)
	}
	// The full log stays on the device.
	logs, _ := filepath.Glob(filepath.Join(client.Home(), "verify", "alpha-*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs: %v", logs)
	}
}

func TestAFailedVerificationBlocksTheLeadButNotAHuman(t *testing.T) {
	f := newFixture(t)
	ac, _, task := f.scopedAgentVerify(nil, `echo "FAIL: 2 tests broken"; exit 3`)
	if err := f.submit(ac, task); err != nil {
		t.Fatal(err)
	}
	ck := f.waitCheck(task.ID)
	if ck.ExitCode != 3 || !strings.Contains(ck.Tail, "2 tests broken") {
		t.Fatalf("check: %+v", ck)
	}
	var ce *client.Error
	if err := f.lead.Post(fmt.Sprintf("/v1/tasks/%d/accept", task.ID), nil, nil); !errors.As(err, &ce) || ce.Status != 409 || !strings.Contains(ce.Msg, "failed verification") {
		t.Fatalf("the lead accepted work that failed: %v", err)
	}
	// The lead is told, with the end of the output.
	found := false
	for _, m := range f.inbox(f.lead) {
		found = found || (strings.Contains(m.Body, "failed, exit 3") && strings.Contains(m.Body, "2 tests broken"))
	}
	if !found {
		t.Fatal("the lead was not told the check failed")
	}
	// Rejecting clears the check of the rejected work; a human may still decide otherwise.
	f.must(f.lead.Post(fmt.Sprintf("/v1/tasks/%d/reject", task.ID), api.ReasonReq{Reason: "tests broken"}, nil))
	if f.task(task.ID).Check != nil {
		t.Fatal("the check of rejected work is still there")
	}
	f.must(ac.Post(fmt.Sprintf("/v1/tasks/%d/submit", task.ID), api.SubmitReq{Evidence: []string{"file:x"}}, nil))
	f.waitCheck(task.ID)
	f.must(f.human.Post(fmt.Sprintf("/v1/tasks/%d/accept", task.ID), nil, nil))
}

func TestVerifyTimesOut(t *testing.T) {
	f := newFixture(t)
	f.link.opt.VerifyTimeout = 300 * time.Millisecond
	ac, _, task := f.scopedAgentVerify(nil, `sleep 20`)
	start := time.Now()
	f.must(f.submit(ac, task))
	ck := f.waitCheck(task.ID)
	if !ck.TimedOut || time.Since(start) > 5*time.Second {
		t.Fatalf("check: %+v after %s", ck, time.Since(start))
	}
}

func TestAnAgentCannotForgeAVerification(t *testing.T) {
	f := newFixture(t)
	ac, _, task := f.scopedAgentVerify(nil, `exit 1`)
	f.must(f.submit(ac, task))
	f.waitCheck(task.ID)
	var ce *client.Error
	err := ac.Post(fmt.Sprintf("/v1/tasks/%d/verify", task.ID), api.TaskCheckReq{Agent: "alpha", Command: "exit 1", ExitCode: 0}, nil)
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("an agent filing its own verification: %v", err)
	}
	if got := f.task(task.ID).Check; got == nil || got.ExitCode != 1 {
		t.Fatalf("the check was overwritten: %+v", got)
	}
}

func TestTheLinkCommitsForAgentsWhoseCLICannot(t *testing.T) {
	f := newFixture(t)
	f.spawnLink(&tmuxLog{})
	repo := gitRepo(t)
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	os.WriteFile(filepath.Join(repo, "src", ".keep"), nil, 0o644)
	gitOut(t, repo, "add", ".")
	gitOut(t, repo, "commit", "-q", "-m", "src")
	f.must(f.admin.Post("/v1/profiles", api.ProfileReq{Name: "coder", Spec: profile.Spec{Kind: "codex", Tools: profile.Tools{Allow: []string{"read", "edit", "shell"}}, Write: []string{"src/**"}}}, nil))
	j := f.repoJobVerify(repo, "test -f src/new.txt")
	var s api.Spawn
	f.must(f.human.Post("/v1/spawns", api.SpawnReq{Name: "alpha", Profile: "coder", Job: j.ID, Device: "dev"}, &s))
	f.ladder()
	ac := client.Socket(f.link.opt.Socket, "alpha")
	f.must(ac.Post("/v1/agents", api.RegisterReq{Name: "alpha", Kind: "codex"}, nil))
	f.must(client.Direct(f.url, f.cred).Post(fmt.Sprintf("/v1/spawns/%d/report", s.ID), api.SpawnReport{Status: "started"}, nil))
	var task api.Task
	f.must(f.human.Post("/v1/tasks", api.TaskCreateReq{Title: "add a file", Job: j.ID, AssignedTo: "alpha"}, &task))
	f.must(ac.Post(fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil))
	dir := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha")
	base := gitOut(t, dir, "rev-parse", "HEAD")

	// What the CLI wrote itself is not committed; the agent's work is, in its name.
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("rules"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".codex"), 0o755)
	os.WriteFile(filepath.Join(dir, ".codex", "hooks.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "new.txt"), []byte("hi"), 0o644)
	// A hook in the repository must not run in the link's name.
	os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\ntouch /tmp/handloom-hook-ran-"+filepath.Base(dir)+"\nexit 1\n"), 0o755)
	if err := f.submit(ac, task); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got == base {
		t.Fatal("nothing was committed")
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%an <%ae>"); got != "alpha <alpha@handloom.local>" {
		t.Fatalf("commit author: %q", got)
	}
	if files := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(files) != "src/new.txt" {
		t.Fatalf("committed files: %q", files)
	}
	if _, err := os.Stat("/tmp/handloom-hook-ran-" + filepath.Base(dir)); err == nil {
		t.Fatal("the repository's pre-commit hook ran")
	}
	if ck := f.waitCheck(task.ID); ck.ExitCode != 0 {
		t.Fatalf("the check should pass on the committed work: %+v", ck)
	}
}

// ---- merges ----

func (f *fixture) commitIn(dir, file, content, msg string) {
	f.t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	gitOut(f.t, dir, "add", file)
	gitOut(f.t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", msg)
}

func (f *fixture) acceptAndMerge(id int64) api.TaskMerge {
	f.t.Helper()
	f.must(f.human.Post(fmt.Sprintf("/v1/tasks/%d/accept", id), nil, nil))
	if f.task(id).Merge == nil || f.task(id).Merge.Status != "pending" {
		f.t.Fatalf("an accepted repo task should have a pending merge: %+v", f.task(id).Merge)
	}
	f.ladder()
	m := f.task(id).Merge
	if m == nil || m.Status == "pending" {
		f.t.Fatalf("the merge was not done: %+v", m)
	}
	return *m
}

func TestAcceptedWorkIsMergedIntoTheIntegrationBranch(t *testing.T) {
	f := newFixture(t)
	ac, dir, t1 := f.scopedAgentVerify(nil, "test ! -f src/bad.txt")
	// Our own hooks must not run for the merge.
	integ := filepath.Join(filepath.Dir(dir), "_integration")

	f.commitIn(dir, "src/one.txt", "one\n", "one")
	f.must(f.submit(ac, t1))
	f.waitCheck(t1.ID)
	if m := f.acceptAndMerge(t1.ID); m.Status != "merged" || m.Head == "" {
		t.Fatalf("first merge: %+v", m)
	}
	if got := gitOut(t, integ, "show", "HEAD:src/one.txt"); got != "one" {
		t.Fatalf("the integration branch lacks the work: %q", got)
	}
	if got := gitOut(t, integ, "branch", "--show-current"); !strings.HasSuffix(got, "/integration") {
		t.Fatalf("integration worktree on %q", got)
	}

	// Somebody else changed src/a.go in the integration branch; alpha changes it differently.
	f.commitIn(integ, "src/a.go", "package src // theirs\n", "theirs")
	before := gitOut(t, integ, "rev-parse", "HEAD")
	var t2 api.Task
	f.must(f.human.Post("/v1/tasks", api.TaskCreateReq{Title: "edit a.go", Job: *t1Job(f, t1), AssignedTo: "alpha"}, &t2))
	f.must(ac.Post(fmt.Sprintf("/v1/tasks/%d/claim", t2.ID), nil, nil))
	f.commitIn(dir, "src/a.go", "package src // mine\n", "mine")
	f.must(f.submit(ac, t2))
	f.waitCheck(t2.ID)
	m := f.acceptAndMerge(t2.ID)
	if m.Status != "conflict" || !strings.Contains(m.Detail, "src/a.go") {
		t.Fatalf("a conflicting merge: %+v", m)
	}
	if gitOut(t, integ, "rev-parse", "HEAD") != before || gitOut(t, integ, "status", "--porcelain") != "" {
		t.Fatal("a failed merge left the integration branch changed")
	}
	var d api.Digest
	f.must(f.human.Get("/v1/digest", &d))
	found := false
	for _, it := range d.NeedsYou {
		found = found || it.Kind == "merge-conflict"
	}
	if !found {
		t.Fatalf("a conflict is not in the digest: %+v", d.NeedsYou)
	}

	// Work that merges cleanly but breaks the check does not stay in the branch.
	var t3 api.Task
	f.must(f.human.Post("/v1/tasks", api.TaskCreateReq{Title: "break it", Job: *t1Job(f, t1), AssignedTo: "alpha"}, &t3))
	f.must(ac.Post(fmt.Sprintf("/v1/tasks/%d/claim", t3.ID), nil, nil))
	gitOut(t, dir, "reset", "-q", "--hard", "HEAD~1") // drop the conflicting commit
	f.commitIn(dir, "src/bad.txt", "bad\n", "bad")
	f.must(f.submit(ac, t3))
	f.waitCheck(t3.ID)
	m = f.acceptAndMerge(t3.ID)
	if m.Status != "failed" || !strings.Contains(m.Detail, "test ! -f src/bad.txt") {
		t.Fatalf("a merge that breaks the check: %+v", m)
	}
	if gitOut(t, integ, "rev-parse", "HEAD") != before {
		t.Fatal("work that failed the check stayed in the integration branch")
	}
}

func t1Job(f *fixture, t api.Task) *int64 { return f.task(t.ID).Job }

func TestAnAgentCannotReachTheMergeEndpoints(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/device/merges"}, {"POST", "/v1/merges/1/report"}} {
		var err error
		if c.method == "GET" {
			err = f.worker.Get(c.path, nil)
		} else {
			err = f.worker.Post(c.path, api.MergeReport{Status: "merged"}, nil)
		}
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Status != 403 {
			t.Fatalf("%s %s from an agent: %v", c.method, c.path, err)
		}
	}
}

// ---- knowledge and base ----

// knowledgeRepo is a toy knowledge repository: a client note and a confidential folder.
func knowledgeRepo(t *testing.T) string {
	t.Helper()
	k := gitRepo(t)
	os.WriteFile(filepath.Join(k, "acme.md"), []byte("# Acme\ninvoice prefix is ACM\n"), 0o644)
	os.MkdirAll(filepath.Join(k, "confidential"), 0o755)
	os.WriteFile(filepath.Join(k, "confidential", "contract.md"), []byte("secret terms\n"), 0o644)
	gitOut(t, k, "add", ".")
	gitOut(t, k, "commit", "-q", "-m", "acme")
	return k
}

func (f *fixture) knowledgeJob(repo, knowledge, base, runtime string) (api.Job, api.Spawn, *client.Client) {
	f.t.Helper()
	f.spawnLink(&tmuxLog{})
	f.must(f.admin.Post("/v1/profiles", api.ProfileReq{Name: "coder", Spec: profile.Spec{Kind: "claude", Runtime: runtime, Tools: profile.Tools{Allow: []string{"read", "edit"}}}}, nil))
	var j api.Job
	f.must(f.human.Post("/v1/jobs", api.JobNewReq{Title: "Client work", Repo: repo, Knowledge: knowledge, Base: base, Device: "dev"}, &j))
	var s api.Spawn
	f.must(f.human.Post("/v1/spawns", api.SpawnReq{Name: "alpha", Profile: "coder", Job: j.ID, Device: "dev"}, &s))
	f.ladder()
	ac := client.Socket(f.link.opt.Socket, "alpha")
	if f.spawnStatus(s.ID).Status == "launching" {
		f.must(ac.Post("/v1/agents", api.RegisterReq{Name: "alpha", Kind: "claude"}, nil))
		f.must(client.Direct(f.url, f.cred).Post(fmt.Sprintf("/v1/spawns/%d/report", s.ID), api.SpawnReport{Status: "started"}, nil))
	}
	return j, s, ac
}

func (f *fixture) collectNow(job int64) {
	f.t.Helper()
	f.must(f.human.Post(fmt.Sprintf("/v1/jobs/%d/close", job), api.JobCloseReq{Cancel: true}, nil))
	f.ladder()
}

func TestAgentsGetTheClientKnowledgeAndTheirNotesLandOnABranch(t *testing.T) {
	f := newFixture(t)
	repo, k := gitRepo(t), knowledgeRepo(t)
	mainBefore := gitOut(t, k, "rev-parse", "HEAD")
	j, s, _ := f.knowledgeJob(repo, k, "", "cloud")
	if got := f.spawnStatus(s.ID).Status; got == "failed" {
		t.Fatalf("spawn: %+v", f.spawnStatus(s.ID))
	}
	dir := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha")
	mount := filepath.Join(dir, ".handloom", "knowledge")
	if b, err := os.ReadFile(filepath.Join(mount, "acme.md")); err != nil || !strings.Contains(string(b), "ACM") {
		t.Fatalf("the agent cannot read the client note: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mount, "confidential")); err == nil {
		t.Fatal("a cloud agent got the confidential folder")
	}
	// The code pipeline never sees the knowledge checkout.
	if files, _ := f.link.changedFiles(context.Background(), f.link.loadScope("alpha")); len(files) != 0 {
		t.Fatalf("the knowledge checkout shows up as the agent's work: %v", files)
	}

	// The agent proposes a note by writing it in its checkout; the link gathers it on close.
	os.WriteFile(filepath.Join(mount, "decisions.md"), []byte("# Decision\nuse prefix ACM\n"), 0o644)
	f.collectNow(j.ID)
	if got := gitOut(t, k, "rev-parse", "HEAD"); got != mainBefore {
		t.Fatal("something was committed to the knowledge repository's own branch")
	}
	if got := gitOut(t, k, "show", fmt.Sprintf("job/%d:decisions.md", j.ID)); !strings.Contains(got, "prefix ACM") {
		t.Fatalf("the proposal is not on the job's branch: %q", got)
	}
	if got := gitOut(t, k, "log", "-1", "--format=%an", fmt.Sprintf("job/%d", j.ID)); got != "alpha" {
		t.Fatalf("proposal author: %q", got)
	}
	var job api.Job
	f.must(f.human.Get(fmt.Sprintf("/v1/jobs/%d", j.ID), &job))
	if job.Notes != 1 || job.Knowledge != k {
		t.Fatalf("the hub should know a count and a path, nothing else: %+v", job)
	}
}

func TestALocalAgentGetsTheConfidentialFolderToo(t *testing.T) {
	f := newFixture(t)
	repo, k := gitRepo(t), knowledgeRepo(t)
	j, _, _ := f.knowledgeJob(repo, k, "", "local")
	mount := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha", ".handloom", "knowledge")
	if _, err := os.Stat(filepath.Join(mount, "confidential", "contract.md")); err != nil {
		t.Fatalf("a local-model agent should see confidential notes: %v", err)
	}
}

func TestTheJobsWorktreesStartFromTheBaseBranch(t *testing.T) {
	f := newFixture(t)
	repo := gitRepo(t)
	gitOut(t, repo, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("f"), 0o644)
	gitOut(t, repo, "add", ".")
	gitOut(t, repo, "commit", "-q", "-m", "feature work")
	gitOut(t, repo, "checkout", "-q", "-")
	j, _, _ := f.knowledgeJob(repo, "", "feature", "cloud")
	dir := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha")
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); err != nil {
		t.Fatalf("the worktree did not start from the base branch: %v", err)
	}
	if rec := f.link.loadScope("alpha"); rec != nil && rec.Base != gitOut(t, repo, "rev-parse", "feature") {
		t.Fatalf("scope base %s", rec.Base)
	}
}

func TestABaseThatDoesNotExistFailsTheSpawnInWords(t *testing.T) {
	f := newFixture(t)
	repo := gitRepo(t)
	_, s, _ := f.knowledgeJob(repo, "", "nope", "cloud")
	got := f.spawnStatus(s.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "base branch") {
		t.Fatalf("%+v", got)
	}
}

func TestAKnowledgePathThatIsNotARepositoryFailsTheSpawnInWords(t *testing.T) {
	f := newFixture(t)
	_, s, _ := f.knowledgeJob(gitRepo(t), t.TempDir(), "", "cloud")
	got := f.spawnStatus(s.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "not a git repository") {
		t.Fatalf("%+v", got)
	}
}

func TestConflictingProposalsAreReportedNotLost(t *testing.T) {
	f := newFixture(t)
	repo, k := gitRepo(t), knowledgeRepo(t)
	j, _, _ := f.knowledgeJob(repo, k, "", "cloud")
	mount := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "alpha", ".handloom", "knowledge")
	// Somebody put a different version of the same note on the job's branch first.
	wt := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), "_knowledge")
	gitOut(t, k, "worktree", "add", "-b", fmt.Sprintf("job/%d", j.ID), wt, "HEAD")
	f.commitIn(wt, "acme.md", "# Acme\ninvoice prefix is XYZ\n", "other agent")
	os.WriteFile(filepath.Join(mount, "acme.md"), []byte("# Acme\ninvoice prefix is QRS\n"), 0o644)
	f.collectNow(j.ID)
	var d api.Digest
	f.must(f.human.Get("/v1/digest", &d))
	_ = d // the job is cancelled, so it no longer asks; the job record keeps the problem
	var job api.Job
	f.must(f.human.Get(fmt.Sprintf("/v1/jobs/%d", j.ID), &job))
	if !strings.Contains(job.NotesProblem, "acme.md") {
		t.Fatalf("the conflict was not reported: %+v", job)
	}
	if got := gitOut(t, wt, "status", "--porcelain"); got != "" {
		t.Fatalf("the job's branch was left half-merged: %q", got)
	}
}

func TestAnAgentCannotReachTheNoteCollectionEndpoints(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/device/kcollects"}, {"POST", "/v1/kcollects/1/report"}} {
		var err error
		if c.method == "GET" {
			err = f.worker.Get(c.path, nil)
		} else {
			err = f.worker.Post(c.path, api.CollectReport{Status: "done"}, nil)
		}
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Status != 403 {
			t.Fatalf("%s %s from an agent: %v", c.method, c.path, err)
		}
	}
}

// ---- the terminal view ----

func TestScreensAreScrubbedBeforeTheyLeaveTheMachine(t *testing.T) {
	cred := "hvd_" + strings.Repeat("a1", 16)
	text := "ok\nexport HANDLOOM_TOKEN=hva_abcdefghijklmnopqrstuvwx\nAuthorization: Bearer abcdefghijklmnopqrstuvwxyz123456\nkey sk-ant-abcdefghijklmnopqrstuvwxyz\nGH ghp_abcdefghijklmnopqrstuvwxyz0123456789\nrun token hvr_ABCDEFGH12345678 and " + cred + "\nnothing secret here"
	got := redactScreen(text, cred, "runtoken-exact-value")
	for _, leak := range []string{"hva_abcdefgh", "abcdefghijklmnopqrstuvwxyz123456", "sk-ant-abc", "ghp_abc", "hvr_ABCDEFGH", cred} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	if !strings.Contains(got, "nothing secret here") || !strings.Contains(got, "ok\n") {
		t.Fatalf("ordinary text was damaged: %s", got)
	}
	if got := redactScreen("short abc", "abc"); got != "short abc" {
		t.Fatalf("a very short secret must not shred ordinary text: %q", got)
	}
}

func TestAnAgentCannotReachTheScreenEndpoints(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/device/tails"}, {"POST", "/v1/device/tails/worker"}} {
		var err error
		if c.method == "GET" {
			err = f.worker.Get(c.path, nil)
		} else {
			err = f.worker.Post(c.path, api.TailReq{Text: "x"}, nil)
		}
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Status != 403 {
			t.Fatalf("%s %s from an agent: %v", c.method, c.path, err)
		}
	}
}

// ---- usage ----

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeUsageIsCountedOncePerRequest(t *testing.T) {
	f := newFixture(t)
	home, dir := t.TempDir(), "/work/job-1/coder.1"
	log := filepath.Join(home, ".claude", "projects", claudeSlug(dir), "s1.jsonl")
	writeLines(t, log,
		`{"type":"user","timestamp":"2026-10-07T10:00:00Z","message":{"content":"private text"}}`,
		// the same request written three times while it streams: only the last, largest one counts
		`{"timestamp":"2026-10-07T10:00:01Z","requestId":"r1","message":{"id":"m1","model":"claude-sonnet-x","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":1000}}}`,
		`{"timestamp":"2026-10-07T10:00:02Z","requestId":"r1","message":{"id":"m1","model":"claude-sonnet-x","usage":{"input_tokens":10,"output_tokens":40,"cache_read_input_tokens":100,"cache_creation_input_tokens":1000}}}`,
		`{"timestamp":"2026-10-07T10:00:03Z","requestId":"r2","message":{"id":"m2","model":"claude-haiku-y","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`,
		`{"timestamp":"2026-10-07T10:00:04Z","requestId":"r3","message":{"id":"m3","model":"<synthetic>","usage":{"input_tokens":99,"output_tokens":99}}}`)
	got := f.link.claudeUsage(home, dir, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	s, h := got["claude-sonnet-x"], got["claude-haiku-y"]
	if s.Input != 10 || s.Output != 40 || s.CacheRead != 100 || s.CacheWrite != 1000 || h.Output != 2 || len(got) != 2 {
		t.Fatalf("usage: %+v", got)
	}
	// A session from before the agent existed is not the agent's.
	if got := f.link.claudeUsage(home, dir, time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC)); len(got) != 0 {
		t.Fatalf("an older session was counted: %+v", got)
	}
	// Another directory has no sessions.
	if got := f.link.claudeUsage(home, "/work/other", time.Time{}); len(got) != 0 {
		t.Fatalf("another directory: %+v", got)
	}
}

func TestCodexUsageComesFromTheSessionOfItsDirectory(t *testing.T) {
	f := newFixture(t)
	home, dir := t.TempDir(), "/work/job-1/coder-a"
	meta := func(cwd string) string {
		return `{"timestamp":"2026-10-07T10:00:00Z","type":"session_meta","payload":{"cwd":"` + cwd + `","model_provider":"openai"}}`
	}
	count := func(in, cached, out int) string {
		return fmt.Sprintf(`{"timestamp":"2026-10-07T10:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d}}}}`, in, cached, out)
	}
	writeLines(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "07", "rollout-a.jsonl"),
		meta(dir), `{"timestamp":"2026-10-07T10:00:01Z","type":"turn_context","payload":{"model":"gpt-x"}}`, count(1000, 900, 50), count(2000, 1500, 120))
	writeLines(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "07", "rollout-b.jsonl"), meta("/work/somebody-else"), count(5000, 0, 500))
	got := f.link.codexUsage(home, dir, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	m := got["gpt-x"]
	if len(got) != 1 || m.Input != 500 || m.CacheRead != 1500 || m.Output != 120 {
		t.Fatalf("usage (the last running total, cached tokens split out): %+v", got)
	}
}

func TestAnAgentCannotReportUsage(t *testing.T) {
	f := newFixture(t)
	err := f.worker.Post("/v1/device/usage", api.UsageReq{Agent: "worker", Models: []api.UsageModel{{Model: "m", Input: 1}}}, nil)
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("usage from an agent: %v", err)
	}
}

func TestASpawnedAgentIsNotNudgedWhileItsTerminalStarts(t *testing.T) {
	f := newFixture(t)
	f.link.opt.SpawnSettle = 20 * time.Second
	f.link.mu.Lock()
	f.link.fresh = map[string]time.Time{"worker": f.clock()}
	f.link.mu.Unlock()
	f.state(f.worker, "idle")
	f.mail("worker")
	f.ladder()
	if f.driver.count() != 0 {
		t.Fatalf("typed into a terminal that is still starting: %v", f.driver.nudges)
	}
	f.advance(25 * time.Second)
	f.ladder()
	if f.driver.count() != 1 {
		t.Fatalf("after the terminal has had time to start the agent is woken: %d", f.driver.count())
	}
}
