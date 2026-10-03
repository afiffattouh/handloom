package link

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/drivers"
	"handloom/internal/hub"
	"handloom/internal/store"
)

// fakeDriver records nudges instead of typing them.
type fakeDriver struct {
	mu     sync.Mutex
	nudges []string // "target|line"
	err    error
}

func (f *fakeDriver) Name() string { return "tmux" }

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
	// New mail is a new batch: reported again.
	f.mail("worker")
	f.ladder()
	if len(f.audit("wake.failed")) != 2 {
		t.Fatal("new mail for a blocked agent was not reported")
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
