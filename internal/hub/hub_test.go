package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/notify"
	"handloom/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is a hub with the standard cast: device d1 runs lead, worker and
// observer; device d2 runs worker2; "afif" is the human.
type env struct {
	t     *testing.T
	hub   *Hub
	srv   *httptest.Server
	clock *clock
	admin string
	human string
	d1    string
	d2    string
}

// caller is who makes a request.
type caller struct {
	token string
	agent string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cl := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	admin, err := store.Init(db, cl.now())
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, Options{Lease: 15 * time.Minute, Now: cl.now, MsgRate: 1000})
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	e := &env{t: t, hub: h, srv: srv, clock: cl, admin: admin}

	e.d1 = e.device("d1")
	e.d2 = e.device("d2")
	for _, name := range []string{"lead", "worker", "observer"} {
		e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: name, Kind: "shell"}, nil)
	}
	e.ok(caller{e.d2, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "worker2", Kind: "shell"}, nil)
	e.ok(caller{e.admin, ""}, "POST", "/v1/agents/lead/role", api.RoleReq{Role: "lead"}, nil)
	e.ok(caller{e.admin, ""}, "POST", "/v1/agents/observer/role", api.RoleReq{Role: "observer"}, nil)
	var tok api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "afif"}, &tok)
	e.human = tok.Token
	return e
}

func (e *env) device(name string) string {
	e.t.Helper()
	var tok api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/devices", api.NameReq{Name: name}, &tok)
	var join api.JoinResp
	e.ok(caller{}, "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok.Token}, &join)
	return join.Credential
}

func (e *env) lead() caller     { return caller{e.d1, "lead"} }
func (e *env) worker() caller   { return caller{e.d1, "worker"} }
func (e *env) observer() caller { return caller{e.d1, "observer"} }
func (e *env) worker2() caller  { return caller{e.d2, "worker2"} }
func (e *env) afif() caller     { return caller{e.human, ""} }

func (e *env) subject(name string) caller {
	switch name {
	case "lead":
		return e.lead()
	case "worker":
		return e.worker()
	case "observer":
		return e.observer()
	case "human":
		return e.afif()
	}
	e.t.Fatalf("unknown subject %s", name)
	return caller{}
}

// do sends a request and returns the status and the raw body.
func (e *env) do(c caller, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.agent != "" {
		req.Header.Set(api.AgentHeader, c.agent)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ok sends a request that must succeed and decodes the answer into out.
func (e *env) ok(c caller, method, path string, body, out any) {
	e.t.Helper()
	status, raw := e.do(c, method, path, body)
	if status != 200 {
		e.t.Fatalf("%s %s: status %d: %s", method, path, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
}

// fail sends a request that must be rejected with the given status.
func (e *env) fail(want int, c caller, method, path string, body any) string {
	e.t.Helper()
	status, raw := e.do(c, method, path, body)
	if status != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	return string(raw)
}

func (e *env) create(c caller, req api.TaskCreateReq) api.Task {
	e.t.Helper()
	var t api.Task
	e.ok(c, "POST", "/v1/tasks", req, &t)
	return t
}

// act performs a task verb that must succeed and returns the task after it.
func (e *env) act(c caller, id int64, verb string, body any) api.Task {
	e.t.Helper()
	var t api.Task
	e.ok(c, "POST", taskPath(id, verb), body, &t)
	return t
}

func (e *env) task(id int64) api.Task {
	e.t.Helper()
	var t api.Task
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/tasks/%d", id), nil, &t)
	return t
}

func (e *env) inbox(c caller) []api.Message {
	e.t.Helper()
	var m []api.Message
	e.ok(c, "GET", "/v1/inbox", nil, &m)
	return m
}

func (e *env) audit() []api.AuditRow {
	e.t.Helper()
	var rows []api.AuditRow
	e.ok(e.afif(), "GET", "/v1/audit", nil, &rows)
	return rows
}

func (e *env) audited(action, target string) bool {
	for _, r := range e.audit() {
		if r.Action == action && (target == "" || r.Target == target) {
			return true
		}
	}
	return false
}

func taskPath(id int64, verb string) string { return fmt.Sprintf("/v1/tasks/%d/%s", id, verb) }

var evidence = api.SubmitReq{Evidence: []string{"test:go test ./... -> ok"}, Note: "done"}

// ---- scope table: DESIGN.md section 6 ----

// scopeCase is one verb of the scope table. run performs it as the given
// subject, after setting up whatever a permitted caller needs to succeed.
type scopeCase struct {
	name    string
	allowed map[string]bool // lead, worker, observer, human
	okCode  int             // status a permitted caller gets
	run     func(e *env, subject string) (int, []byte)
}

// claimedFor returns a task claimed (and, if submit is set, submitted) by
// subject when subject can work on tasks, else by worker2.
func claimedFor(e *env, subject string, submit bool) (api.Task, caller) {
	owner := e.worker2()
	if subject == "lead" || subject == "worker" {
		owner = e.subject(subject)
	}
	t := e.create(e.afif(), api.TaskCreateReq{Title: "t"})
	e.ok(owner, "POST", taskPath(t.ID, "claim"), nil, nil)
	if submit {
		e.ok(owner, "POST", taskPath(t.ID, "submit"), evidence, nil)
	}
	return t, owner
}

var scopeCases = []scopeCase{
	{
		name:    "read board",
		allowed: map[string]bool{"lead": true, "worker": true, "observer": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			return e.do(e.subject(s), "GET", "/v1/tasks", nil)
		},
	},
	{
		name:    "read agents",
		allowed: map[string]bool{"lead": true, "worker": true, "observer": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			return e.do(e.subject(s), "GET", "/v1/agents", nil)
		},
	},
	{
		name:    "send message",
		allowed: map[string]bool{"lead": true, "worker": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			return e.do(e.subject(s), "POST", "/v1/messages", api.SendReq{To: "worker2", Body: "hello"})
		},
	},
	{
		name:    "create task",
		allowed: map[string]bool{"lead": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			return e.do(e.subject(s), "POST", "/v1/tasks", api.TaskCreateReq{Title: "t"})
		},
	},
	{
		name:    "assign task",
		allowed: map[string]bool{"lead": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			t := e.create(e.afif(), api.TaskCreateReq{Title: "t"})
			return e.do(e.subject(s), "POST", taskPath(t.ID, "assign"), api.AssignReq{Agent: "worker2"})
		},
	},
	{
		name:    "accept task",
		allowed: map[string]bool{"lead": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			t, _ := claimedFor(e, "", true)
			return e.do(e.subject(s), "POST", taskPath(t.ID, "accept"), nil)
		},
	},
	{
		name:    "reject task",
		allowed: map[string]bool{"lead": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			t, _ := claimedFor(e, "", true)
			return e.do(e.subject(s), "POST", taskPath(t.ID, "reject"), api.ReasonReq{Reason: "no"})
		},
	},
	{
		name:    "cancel task",
		allowed: map[string]bool{"lead": true, "human": true},
		run: func(e *env, s string) (int, []byte) {
			t := e.create(e.afif(), api.TaskCreateReq{Title: "t"})
			return e.do(e.subject(s), "POST", taskPath(t.ID, "cancel"), nil)
		},
	},
	{
		name:    "claim task",
		allowed: map[string]bool{"lead": true, "worker": true},
		run: func(e *env, s string) (int, []byte) {
			t := e.create(e.afif(), api.TaskCreateReq{Title: "t"})
			return e.do(e.subject(s), "POST", taskPath(t.ID, "claim"), nil)
		},
	},
	{
		name:    "heartbeat task",
		allowed: map[string]bool{"lead": true, "worker": true},
		run: func(e *env, s string) (int, []byte) {
			t, _ := claimedFor(e, s, false)
			return e.do(e.subject(s), "POST", taskPath(t.ID, "heartbeat"), nil)
		},
	},
	{
		name:    "submit task",
		allowed: map[string]bool{"lead": true, "worker": true},
		run: func(e *env, s string) (int, []byte) {
			t, _ := claimedFor(e, s, false)
			return e.do(e.subject(s), "POST", taskPath(t.ID, "submit"), evidence)
		},
	},
	{
		name:    "release task",
		allowed: map[string]bool{"lead": true, "worker": true},
		run: func(e *env, s string) (int, []byte) {
			t, _ := claimedFor(e, s, false)
			return e.do(e.subject(s), "POST", taskPath(t.ID, "release"), nil)
		},
	},
	{
		name:    "block task",
		allowed: map[string]bool{"lead": true, "worker": true},
		run: func(e *env, s string) (int, []byte) {
			t, _ := claimedFor(e, s, false)
			return e.do(e.subject(s), "POST", taskPath(t.ID, "block"), api.BlockReq{Reason: "stuck"})
		},
	},
	{
		name:    "open escalation",
		allowed: map[string]bool{"lead": true},
		run: func(e *env, s string) (int, []byte) {
			return e.do(e.subject(s), "POST", "/v1/escalations", api.AskReq{Question: "Delete the old migrations?", Options: []string{"yes", "no"}})
		},
	},
	{
		name:    "answer escalation",
		allowed: map[string]bool{"human": true},
		run: func(e *env, s string) (int, []byte) {
			var esc api.Escalation
			e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "Ship it?"}, &esc)
			return e.do(e.subject(s), "POST", fmt.Sprintf("/v1/escalations/%d/answer", esc.ID), api.AnswerReq{Answer: "yes"})
		},
	},
}

// TestScopeTable checks every cell of the scope table: a permitted caller
// succeeds, and a forbidden one gets 403 and leaves a "denied" audit row.
func TestScopeTable(t *testing.T) {
	for _, sc := range scopeCases {
		for _, subject := range []string{"lead", "worker", "observer", "human"} {
			verdict := "forbidden"
			if sc.allowed[subject] {
				verdict = "allowed"
			}
			t.Run(fmt.Sprintf("%s/%s/%s", sc.name, subject, verdict), func(t *testing.T) {
				e := newEnv(t)
				before := len(e.audit())
				status, raw := sc.run(e, subject)
				if sc.allowed[subject] {
					want := sc.okCode
					if want == 0 {
						want = 200
					}
					if status != want {
						t.Fatalf("status %d, want %d: %s", status, want, raw)
					}
					return
				}
				if status != 403 {
					t.Fatalf("status %d, want 403: %s", status, raw)
				}
				var ae api.Error
				json.Unmarshal(raw, &ae)
				if ae.Code != "forbidden" {
					t.Fatalf("code %q, want forbidden", ae.Code)
				}
				denied := false
				for _, r := range e.audit()[before:] {
					denied = denied || r.Action == "denied"
				}
				if !denied {
					t.Fatal("the rejected action is not in the audit log")
				}
			})
		}
	}
}

// TestScopeTableMatchesDesign pins the table itself, so a change to it
// cannot pass unnoticed.
func TestScopeTableMatchesDesign(t *testing.T) {
	want := map[Action]string{ // subjects in the order lead, worker, observer, human
		ActRead:             "yes yes yes yes",
		ActSend:             "yes yes no yes",
		ActTaskManage:       "yes no no yes",
		ActTaskWork:         "yes yes no no",
		ActEscalationOpen:   "yes no no no",
		ActEscalationAnswer: "no no no yes",
	}
	if len(scopeTable) != len(want) {
		t.Fatalf("scope table has %d actions, want %d", len(scopeTable), len(want))
	}
	for action, row := range want {
		var got []string
		for _, s := range []string{"lead", "worker", "observer", "human"} {
			if Allowed(action, s) {
				got = append(got, "yes")
			} else {
				got = append(got, "no")
			}
		}
		if strings.Join(got, " ") != row {
			t.Errorf("%s: got %q, want %q", action, strings.Join(got, " "), row)
		}
	}
}

// Only the owner may heartbeat, submit, release or block a task, even when
// the caller's role allows task work.
func TestOnlyOwnerWorksOnTask(t *testing.T) {
	e := newEnv(t)
	task, _ := claimedFor(e, "", false) // owned by worker2
	for verb, body := range map[string]any{"heartbeat": nil, "release": nil, "submit": evidence, "block": api.BlockReq{Reason: "x"}} {
		for _, c := range []caller{e.worker(), e.lead()} {
			e.fail(403, c, "POST", taskPath(task.ID, verb), body)
		}
	}
	if got := e.task(task.ID); got.Status != api.StatusClaimed || got.Owner != "worker2" {
		t.Fatalf("task changed: %+v", got)
	}
}

func TestAdminTokenIsForAdministrationOnly(t *testing.T) {
	e := newEnv(t)
	admin := caller{e.admin, ""}
	e.fail(403, admin, "POST", "/v1/tasks", api.TaskCreateReq{Title: "t"})
	e.fail(403, admin, "POST", "/v1/messages", api.SendReq{To: "worker", Body: "hi"})
	e.ok(admin, "GET", "/v1/tasks", nil, nil)
	// And admin endpoints reject everyone else.
	for _, c := range []caller{e.lead(), e.worker(), e.afif()} {
		e.fail(403, c, "POST", "/v1/admin/devices", api.NameReq{Name: "x"})
		e.fail(403, c, "POST", "/v1/admin/humans", api.NameReq{Name: "x"})
	}
	// The audit log is for humans and the admin.
	e.fail(403, e.lead(), "GET", "/v1/audit", nil)
}

// ---- identity ----

func TestRolesAreNotSetByAgents(t *testing.T) {
	e := newEnv(t)
	for _, c := range []caller{e.worker(), e.lead(), {e.d1, ""}} {
		e.fail(403, c, "POST", "/v1/agents/worker/role", api.RoleReq{Role: "lead"})
	}
	// A role in the register request is refused, not ignored.
	e.fail(400, caller{e.d1, ""}, "POST", "/v1/agents", `{"name":"sneaky","kind":"shell","role":"lead"}`)
	// New agents are workers.
	var a api.Agent
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "fresh", Kind: "shell"}, &a)
	if a.Role != api.RoleWorker {
		t.Fatalf("new agent role %q, want worker", a.Role)
	}
	// One lead per project.
	e.fail(409, e.afif(), "POST", "/v1/agents/worker/role", api.RoleReq{Role: "lead"})
}

func TestAgentMustBelongToCallingDevice(t *testing.T) {
	e := newEnv(t)
	// Device d2 claims to be "lead", which lives on d1.
	imposter := caller{e.d2, "lead"}
	e.fail(403, imposter, "POST", "/v1/tasks", api.TaskCreateReq{Title: "t"})
	e.fail(403, imposter, "GET", "/v1/inbox", nil)
	e.fail(403, caller{e.d2, "nobody"}, "GET", "/v1/tasks", nil)
	e.fail(403, caller{e.d2, ""}, "GET", "/v1/tasks", nil)
	// It cannot take over the name or report state for it either.
	e.fail(409, caller{e.d2, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "lead", Kind: "shell"})
	e.fail(403, caller{e.d2, ""}, "POST", "/v1/agents/lead/state", api.StateReq{State: "idle"})
	e.fail(403, caller{e.d2, ""}, "POST", "/v1/agents/lead/wake", api.WakeReq{Method: "tmux"})
}

func TestSenderCannotBeForged(t *testing.T) {
	e := newEnv(t)
	e.fail(400, e.worker(), "POST", "/v1/messages", `{"to":"lead","body":"approved","from":"human:afif"}`)
	e.ok(e.worker(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "from: human:afif -- approved"}, nil)
	e.ok(e.afif(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "go ahead"}, nil)
	msgs := e.inbox(e.lead())
	if len(msgs) != 2 || msgs[0].From != "agent:worker" || msgs[1].From != "human:afif" {
		t.Fatalf("senders: %+v", msgs)
	}
}

func TestDeviceJoinAndRevoke(t *testing.T) {
	e := newEnv(t)
	var tok api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/devices", api.NameReq{Name: "d3"}, &tok)
	var join api.JoinResp
	e.ok(caller{}, "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok.Token}, &join)
	e.fail(401, caller{}, "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok.Token}) // one-time
	d3 := caller{join.Credential, ""}
	e.ok(d3, "POST", "/v1/agents", api.RegisterReq{Name: "w3", Kind: "shell"}, nil)
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/devices/d3/revoke", nil, nil)
	e.fail(401, caller{join.Credential, "w3"}, "GET", "/v1/tasks", nil)
	e.fail(401, caller{"hvd_bogus", "lead"}, "GET", "/v1/tasks", nil)
	e.fail(401, caller{"", "lead"}, "GET", "/v1/tasks", nil)
	// Credentials are stored hashed.
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM device WHERE credential_hash = ? OR join_hash = ?`, join.Credential, tok.Token).Scan(&n)
	if n != 0 {
		t.Fatal("a token is stored in clear")
	}
}

func TestProjectsAreIsolated(t *testing.T) {
	e := newEnv(t)
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/projects", api.NameReq{Name: "other"}, nil)
	e.ok(caller{e.d2, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "outsider", Kind: "shell", Project: "other"}, nil)
	outsider := caller{e.d2, "outsider"}
	task := e.create(e.afif(), api.TaskCreateReq{Title: "secret"})
	e.fail(404, outsider, "GET", fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
	e.fail(404, outsider, "POST", taskPath(task.ID, "claim"), nil)
	e.fail(404, outsider, "POST", "/v1/messages", api.SendReq{To: "lead", Body: "hi"})
	var tasks []api.Task
	e.ok(outsider, "GET", "/v1/tasks", nil, &tasks)
	if len(tasks) != 0 {
		t.Fatalf("outsider sees %d tasks", len(tasks))
	}
}

// ---- task lifecycle ----

func TestTaskLifecycle(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "build it", Body: "details"})
	if task.Status != api.StatusOpen || task.CreatedBy != "agent:lead" {
		t.Fatalf("created: %+v", task)
	}
	got := e.act(e.worker(), task.ID, "claim", nil)
	if got.Status != api.StatusClaimed || got.Owner != "worker" || got.LeaseExpiresAt == nil {
		t.Fatalf("claimed: %+v", got)
	}
	if want := e.clock.now().Add(15 * time.Minute); !got.LeaseExpiresAt.Equal(want) {
		t.Fatalf("lease %v, want %v", got.LeaseExpiresAt, want)
	}
	e.fail(409, e.worker2(), "POST", taskPath(task.ID, "claim"), nil) // already claimed
	e.fail(409, e.lead(), "POST", taskPath(task.ID, "accept"), nil)   // not submitted

	// Evidence is required.
	e.fail(400, e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Note: "trust me"})
	e.fail(400, e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"  "}})
	got = e.act(e.worker(), task.ID, "submit", evidence)
	if got.Status != api.StatusSubmitted || len(got.Evidence) != 1 || got.LeaseExpiresAt != nil {
		t.Fatalf("submitted: %+v", got)
	}
	if m := e.inbox(e.lead()); len(m) != 1 || m[0].From != "hub" || !strings.Contains(m[0].Body, "submitted by worker") {
		t.Fatalf("lead inbox after submit: %+v", m)
	}

	// Reject needs a reason and returns the task to its owner.
	e.fail(400, e.lead(), "POST", taskPath(task.ID, "reject"), api.ReasonReq{})
	got = e.act(e.lead(), task.ID, "reject", api.ReasonReq{Reason: "tests missing"})
	if got.Status != api.StatusClaimed || got.Owner != "worker" || got.LeaseExpiresAt == nil {
		t.Fatalf("rejected: %+v", got)
	}
	if m := e.inbox(e.worker()); len(m) != 1 || !strings.Contains(m[0].Body, "rejected: tests missing") {
		t.Fatalf("worker inbox after reject: %+v", m)
	}

	e.ok(e.worker(), "POST", taskPath(task.ID, "submit"), evidence, nil)
	got = e.act(e.lead(), task.ID, "accept", nil)
	if got.Status != api.StatusDone {
		t.Fatalf("accepted: %+v", got)
	}
	if m := e.inbox(e.worker()); len(m) != 1 || !strings.Contains(m[0].Body, "accepted") {
		t.Fatalf("worker inbox after accept: %+v", m)
	}
	e.fail(409, e.lead(), "POST", taskPath(task.ID, "cancel"), nil) // done is final

	target := fmt.Sprintf("task:%d", task.ID)
	for _, action := range []string{"task.create", "task.claim", "task.submit", "task.reject", "task.accept"} {
		if !e.audited(action, target) {
			t.Errorf("audit log has no %s for %s", action, target)
		}
	}
}

func TestLeaseExpiryReturnsTaskToOpen(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "t"})
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)

	// A heartbeat extends the lease.
	e.clock.advance(10 * time.Minute)
	e.ok(e.worker(), "POST", taskPath(task.ID, "heartbeat"), nil, nil)
	e.clock.advance(10 * time.Minute)
	if err := e.hub.Sweep(); err != nil {
		t.Fatal(err)
	}
	if got := e.task(task.ID); got.Status != api.StatusClaimed {
		t.Fatalf("lease expired despite heartbeat: %+v", got)
	}

	// No heartbeat: the lease runs out.
	e.clock.advance(16 * time.Minute)
	if err := e.hub.Sweep(); err != nil {
		t.Fatal(err)
	}
	got := e.task(task.ID)
	if got.Status != api.StatusOpen || got.Owner != "" || got.LeaseExpiresAt != nil {
		t.Fatalf("after expiry: %+v", got)
	}
	if m := e.inbox(e.lead()); len(m) != 1 || !strings.Contains(m[0].Body, "lease held by worker expired") {
		t.Fatalf("lead inbox: %+v", m)
	}
	if !e.audited("task.lease_expired", fmt.Sprintf("task:%d", task.ID)) {
		t.Fatal("no task.lease_expired in audit log")
	}
	// The old owner's next calls are rejected.
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "heartbeat"), nil)
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "submit"), evidence)
	// Another worker can take it.
	e.ok(e.worker2(), "POST", taskPath(task.ID, "claim"), nil, nil)
}

// Without a sweep, the owner's own late call still finds the lease expired.
func TestLeaseExpiryIsCheckedOnCall(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "t"})
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)
	e.clock.advance(16 * time.Minute)
	e.fail(403, e.worker(), "POST", taskPath(task.ID, "submit"), evidence)
	e.ok(e.worker2(), "POST", taskPath(task.ID, "claim"), nil, nil)
}

func TestDependenciesAndAssignment(t *testing.T) {
	e := newEnv(t)
	t1 := e.create(e.lead(), api.TaskCreateReq{Title: "one", AssignedTo: "worker"})
	t3 := e.create(e.lead(), api.TaskCreateReq{Title: "three", AssignedTo: "worker", DependsOn: []int64{t1.ID}})
	e.fail(404, e.lead(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "bad", DependsOn: []int64{999}})
	e.fail(409, e.lead(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "bad", AssignedTo: "observer"})

	m := e.inbox(e.worker())
	if len(m) != 2 || !strings.Contains(m[0].Body, "claim it") || !strings.Contains(m[1].Body, "depends on #1") {
		t.Fatalf("assignment notices: %+v", m)
	}
	// Only the assignee can claim.
	e.fail(403, e.worker2(), "POST", taskPath(t1.ID, "claim"), nil)
	// Unfinished dependency.
	e.fail(409, e.worker(), "POST", taskPath(t3.ID, "claim"), nil)

	e.ok(e.worker(), "POST", taskPath(t1.ID, "claim"), nil, nil)
	e.ok(e.worker(), "POST", taskPath(t1.ID, "submit"), evidence, nil)
	e.fail(409, e.worker(), "POST", taskPath(t3.ID, "claim"), nil) // submitted is not done
	e.ok(e.lead(), "POST", taskPath(t1.ID, "accept"), nil, nil)

	m = e.inbox(e.worker())
	if len(m) != 2 || !strings.Contains(m[1].Body, "Task #2 (three) can be claimed now") {
		t.Fatalf("after accept: %+v", m)
	}
	e.ok(e.worker(), "POST", taskPath(t3.ID, "claim"), nil, nil)

	// Reassigning an open task.
	t4 := e.create(e.afif(), api.TaskCreateReq{Title: "four", AssignedTo: "worker"})
	e.ok(e.afif(), "POST", taskPath(t4.ID, "assign"), api.AssignReq{Agent: "worker2"}, nil)
	e.fail(403, e.worker(), "POST", taskPath(t4.ID, "claim"), nil)
	e.ok(e.worker2(), "POST", taskPath(t4.ID, "claim"), nil, nil)
}

func TestReleaseBlockCancel(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "t"})
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)
	got := e.act(e.worker(), task.ID, "block", api.BlockReq{Reason: "need access"})
	if got.Status != api.StatusClaimed || got.BlockedReason != "need access" {
		t.Fatalf("blocked is a flag on a claimed task: %+v", got)
	}
	got = e.act(e.worker(), task.ID, "release", nil)
	if got.Status != api.StatusOpen || got.Owner != "" || got.BlockedReason != "" {
		t.Fatalf("released: %+v", got)
	}
	if m := e.inbox(e.lead()); len(m) != 2 {
		t.Fatalf("lead should hear about block and release: %+v", m)
	}
	e.ok(e.worker2(), "POST", taskPath(task.ID, "claim"), nil, nil)
	got = e.act(e.afif(), task.ID, "cancel", nil)
	if got.Status != api.StatusCancelled {
		t.Fatalf("cancelled: %+v", got)
	}
	e.fail(409, e.worker2(), "POST", taskPath(task.ID, "submit"), evidence)
	if m := e.inbox(e.worker2()); len(m) != 1 || !strings.Contains(m[0].Body, "cancelled") {
		t.Fatalf("owner should hear about the cancel: %+v", m)
	}
}

// ---- messages ----

func TestMessageAddressing(t *testing.T) {
	e := newEnv(t)
	var resp api.SendResp
	e.ok(e.worker(), "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "stuck"}, &resp)
	if len(resp.Recipients) != 1 || resp.Recipients[0] != "lead" {
		t.Fatalf("role:lead -> %v", resp.Recipients)
	}
	task, _ := claimedFor(e, "", false) // owned by worker2
	e.ok(e.afif(), "POST", "/v1/messages", api.SendReq{To: fmt.Sprintf("task:%d", task.ID), Body: "status?"}, &resp)
	if strings.Join(resp.Recipients, ",") != "worker2,lead" {
		t.Fatalf("task: -> %v, want owner plus lead", resp.Recipients)
	}
	e.fail(404, e.worker(), "POST", "/v1/messages", api.SendReq{To: "nobody", Body: "x"})
	e.fail(400, e.worker(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "  "})

	// Inbox marks messages read; --all keeps history.
	if m := e.inbox(e.lead()); len(m) != 2 {
		t.Fatalf("lead unread: %+v", m)
	}
	if m := e.inbox(e.lead()); len(m) != 0 {
		t.Fatalf("lead unread after read: %+v", m)
	}
	var all []api.Message
	e.ok(e.lead(), "GET", "/v1/inbox?all=1", nil, &all)
	if len(all) != 2 || all[0].ReadAt == nil || all[0].DeliveredBy != "inbox" {
		t.Fatalf("history: %+v", all)
	}
}

func TestMessageRateLimit(t *testing.T) {
	e := newEnv(t)
	e.hub.opt.MsgRate = 3
	for i := 0; i < 3; i++ {
		e.ok(e.worker(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "x"}, nil)
	}
	e.fail(429, e.worker(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "x"})
	e.ok(e.worker2(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "x"}, nil) // per sender
	e.clock.advance(time.Minute)
	e.ok(e.worker(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "x"}, nil)
}

// ---- wake: hub side ----

func (e *env) turnEnd(agent string) api.TurnEndResp {
	e.t.Helper()
	var r api.TurnEndResp
	e.ok(caller{e.d1, agent}, "POST", "/v1/agents/"+agent+"/turn-end", nil, &r)
	return r
}

func (e *env) state(agent string) string {
	e.t.Helper()
	var agents []api.Agent
	e.ok(e.afif(), "GET", "/v1/agents", nil, &agents)
	for _, a := range agents {
		if a.Name == agent {
			return a.State
		}
	}
	return ""
}

// The end-of-turn hook stops the agent at most once per batch of mail.
func TestTurnEndBlocksOncePerBatch(t *testing.T) {
	e := newEnv(t)
	e.ok(e.worker(), "POST", "/v1/agents/worker/state", api.StateReq{State: "working"}, nil)

	if r := e.turnEnd("worker"); r.Block || e.state("worker") != "idle" {
		t.Fatalf("no mail: %+v, state %s", r, e.state("worker"))
	}
	e.ok(e.lead(), "POST", "/v1/messages", api.SendReq{To: "worker", Body: "one"}, nil)
	if r := e.turnEnd("worker"); !r.Block || r.Unread != 1 || e.state("worker") != "working" {
		t.Fatalf("new mail must block the stop: %+v, state %s", r, e.state("worker"))
	}
	// The agent ignores it and stops again: let it stop.
	if r := e.turnEnd("worker"); r.Block || e.state("worker") != "idle" {
		t.Fatalf("same batch must not block twice: %+v", r)
	}
	// Newer mail is a new batch.
	e.ok(e.lead(), "POST", "/v1/messages", api.SendReq{To: "worker", Body: "two"}, nil)
	if r := e.turnEnd("worker"); !r.Block || r.Unread != 2 {
		t.Fatalf("newer mail must block again: %+v", r)
	}
	if r := e.turnEnd("worker"); r.Block {
		t.Fatalf("blocked twice: %+v", r)
	}
	var all []api.Message
	e.ok(e.worker(), "GET", "/v1/inbox?all=1", nil, &all)
	if len(all) != 2 || all[0].DeliveredBy != api.WakeHook || all[1].DeliveredBy != api.WakeHook {
		t.Fatalf("hook delivery not recorded: %+v", all)
	}
	wakes := 0
	for _, r := range e.audit() {
		if r.Action == "wake" && r.Target == "agent:worker" && strings.Contains(string(r.Payload), `"method":"hook"`) {
			wakes++
		}
	}
	if wakes != 2 {
		t.Fatalf("audit log has %d hook wakes, want 2", wakes)
	}
}

func TestWakeReports(t *testing.T) {
	e := newEnv(t)
	link := caller{e.d1, ""}
	e.ok(e.lead(), "POST", "/v1/messages", api.SendReq{To: "worker", Body: "one"}, nil)

	var agents []api.DeviceAgent
	e.ok(link, "GET", "/v1/device/agents", nil, &agents)
	var w api.DeviceAgent
	for _, a := range agents {
		if a.Name == "worker2" {
			t.Fatal("link sees an agent of another device")
		}
		if a.Name == "worker" {
			w = a
		}
	}
	if w.Unread != 1 || w.Undelivered != 1 || w.LastUnreadID == 0 {
		t.Fatalf("counters: %+v", w)
	}

	// A typed nudge marks the mail delivered and is audited with its method.
	var resp api.WakeResp
	e.ok(link, "POST", "/v1/agents/worker/wake", api.WakeReq{Method: api.WakeTmux}, &resp)
	if len(resp.Messages) != 1 {
		t.Fatalf("wake: %+v", resp)
	}
	e.ok(link, "GET", "/v1/device/agents", nil, &agents)
	for _, a := range agents {
		if a.Name == "worker" && (a.Unread != 1 || a.Undelivered != 0) {
			t.Fatalf("after nudge: %+v", a)
		}
	}
	if !e.audited("wake", "agent:worker") {
		t.Fatal("no wake in audit log")
	}

	// A failed wake is reported to the lead.
	e.ok(link, "POST", "/v1/agents/worker/wake", api.WakeReq{Method: api.WakeNone, Reason: "blocked"}, nil)
	if m := e.inbox(e.lead()); len(m) != 1 || !strings.Contains(m[0].Body, "worker cannot be woken (blocked)") {
		t.Fatalf("lead inbox: %+v", m)
	}
	e.ok(link, "POST", "/v1/agents/worker/wake", api.WakeReq{Method: api.WakeNone, Reason: "no_inbox_after_nudges"}, nil)
	if e.state("worker") != api.StateUnknown {
		t.Fatalf("state %s, want unknown", e.state("worker"))
	}
	e.fail(400, link, "POST", "/v1/agents/worker/wake", api.WakeReq{Method: "carrier-pigeon"})
	e.fail(403, link, "POST", "/v1/agents/worker2/wake", api.WakeReq{Method: api.WakeTmux})
}

func TestEventLongPoll(t *testing.T) {
	e := newEnv(t)
	poll := func(c caller, after int64, wait int) (events []api.Event, cursor int64) {
		var r struct {
			Events []api.Event `json:"events"`
			Cursor int64       `json:"cursor"`
		}
		e.ok(c, "GET", fmt.Sprintf("/v1/events?after=%d&wait=%d", after, wait), nil, &r)
		return r.Events, r.Cursor
	}
	d1, d2 := caller{e.d1, ""}, caller{e.d2, ""}
	if ev, _ := poll(d1, 0, 0); len(ev) != 0 {
		t.Fatalf("unexpected events: %+v", ev)
	}
	e.fail(403, e.afif(), "GET", "/v1/events?after=0&wait=0", nil)

	// A waiting poll returns as soon as a message for a local agent arrives.
	done := make(chan []api.Event, 1)
	go func() {
		ev, _ := poll(d1, 0, 10)
		done <- ev
	}()
	time.Sleep(100 * time.Millisecond)
	e.ok(e.worker2(), "POST", "/v1/messages", api.SendReq{To: "worker", Body: "hi"}, nil)
	select {
	case ev := <-done:
		if len(ev) != 1 || ev[0].Type != "message.new" || ev[0].Agent != "worker" {
			t.Fatalf("events: %+v", ev)
		}
		if ev2, _ := poll(d1, ev[0].Seq, 0); len(ev2) != 0 {
			t.Fatalf("cursor did not advance: %+v", ev2)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long-poll did not return")
	}
	// The other device does not see it.
	if ev, _ := poll(d2, 0, 0); len(ev) != 0 {
		t.Fatalf("d2 got events for d1's agent: %+v", ev)
	}
}

// ---- audit ----

func TestAuditAndEventLogsAreAppendOnly(t *testing.T) {
	e := newEnv(t)
	e.create(e.lead(), api.TaskCreateReq{Title: "t"})
	for _, q := range []string{
		`UPDATE audit SET actor = 'x'`, `DELETE FROM audit`,
		`UPDATE event SET type = 'x'`, `DELETE FROM event`,
	} {
		if _, err := e.hub.db.Exec(q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want append-only", q, err)
		}
	}
}

func TestEveryActionIsAudited(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "t", AssignedTo: "worker"})
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)
	e.ok(e.worker(), "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "on it"}, nil)
	e.inbox(e.lead())
	e.ok(e.worker(), "POST", "/v1/agents/worker/state", api.StateReq{State: "working"}, nil)
	e.fail(403, e.observer(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "no"})

	want := map[string]string{
		"device.add": "", "device.join": "", "human.add": "", "agent.register": "agent:worker",
		"agent.role": "agent:lead", "task.create": "task:1", "task.claim": "task:1",
		"message.send": "", "inbox.read": "agent:lead", "agent.state": "agent:worker", "denied": "POST /v1/tasks",
	}
	for action, target := range want {
		if !e.audited(action, target) {
			t.Errorf("audit log has no %s %s", action, target)
		}
	}
	rows := e.audit()
	for i, r := range rows {
		if r.Seq != int64(i+1) || r.Actor == "" {
			t.Fatalf("audit row %d: %+v", i, r)
		}
	}
	var denied api.AuditRow
	for _, r := range rows {
		if r.Action == "denied" {
			denied = r
		}
	}
	if denied.Actor != "agent:observer" {
		t.Fatalf("denied row actor %q, want agent:observer", denied.Actor)
	}
}

// An assigned task that nobody claims is reported to the lead once. Leases
// cannot catch this: there is no owner yet.
func TestUnclaimedAssignedTaskIsReported(t *testing.T) {
	e := newEnv(t)
	t1 := e.create(e.lead(), api.TaskCreateReq{Title: "one", AssignedTo: "worker"})
	t2 := e.create(e.lead(), api.TaskCreateReq{Title: "two", AssignedTo: "worker2", DependsOn: []int64{t1.ID}})
	e.create(e.lead(), api.TaskCreateReq{Title: "free for all"}) // unassigned: never reported
	sweep := func() []api.Message {
		t.Helper()
		if err := e.hub.Sweep(); err != nil {
			t.Fatal(err)
		}
		return e.inbox(e.lead())
	}
	e.clock.advance(9 * time.Minute)
	if m := sweep(); len(m) != 0 {
		t.Fatalf("reported too early: %+v", m)
	}
	e.clock.advance(2 * time.Minute)
	m := sweep()
	if len(m) != 1 || !strings.Contains(m[0].Body, "Task #1 (one) is assigned to worker and has not been claimed for 10m0s") {
		t.Fatalf("lead inbox: %+v", m)
	}
	if !e.audited("task.unclaimed", "task:1") {
		t.Fatal("no task.unclaimed in audit log")
	}
	e.clock.advance(30 * time.Minute)
	if m := sweep(); len(m) != 0 {
		t.Fatalf("reported twice, or reported a task whose dependency is not done: %+v", m)
	}

	// Task 2's clock starts when its dependency is accepted.
	e.ok(e.worker(), "POST", taskPath(t1.ID, "claim"), nil, nil)
	e.ok(e.worker(), "POST", taskPath(t1.ID, "submit"), evidence, nil)
	e.inbox(e.lead())
	e.ok(e.lead(), "POST", taskPath(t1.ID, "accept"), nil, nil)
	e.clock.advance(9 * time.Minute)
	if m := sweep(); len(m) != 0 {
		t.Fatalf("task 2 reported too early: %+v", m)
	}
	e.clock.advance(2 * time.Minute)
	if m := sweep(); len(m) != 1 || !strings.Contains(m[0].Body, "Task #2 (two) is assigned to worker2") {
		t.Fatalf("lead inbox: %+v", m)
	}
	// A claimed task is the lease's business, not this check's.
	e.ok(e.worker2(), "POST", taskPath(t2.ID, "claim"), nil, nil)
	e.ok(e.worker2(), "POST", taskPath(t2.ID, "release"), nil, nil)
	e.inbox(e.lead())
	e.clock.advance(11 * time.Minute)
	if m := sweep(); len(m) != 1 || !strings.Contains(m[0].Body, "has not been claimed") {
		t.Fatalf("a released, assigned task is unclaimed again: %+v", m)
	}
}

// ---- escalations ----

func TestEscalationRoundTrip(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "migrate"})
	var esc api.Escalation
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "Delete the old migrations?", Options: []string{"yes", "no"}, TaskID: &task.ID}, &esc)
	if esc.ID != 1 || esc.From != "lead" || esc.Answer != nil || len(esc.Options) != 2 {
		t.Fatalf("escalation after open: %+v", esc)
	}

	var open []api.Escalation
	e.ok(e.afif(), "GET", "/v1/escalations", nil, &open)
	if len(open) != 1 {
		t.Fatalf("open escalations: %+v", open)
	}

	// The answer must be one of the options, and only once.
	e.fail(400, e.afif(), "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "maybe"})
	e.fail(400, e.afif(), "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: " "})
	e.ok(e.afif(), "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "yes"}, &esc)
	if esc.Answer == nil || *esc.Answer != "yes" || esc.AnsweredBy != "human:afif" || esc.AnsweredAt == nil {
		t.Fatalf("escalation after answer: %+v", esc)
	}
	e.fail(409, e.afif(), "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "no"})

	e.ok(e.afif(), "GET", "/v1/escalations", nil, &open)
	if len(open) != 0 {
		t.Fatalf("answered escalation still listed as open: %+v", open)
	}
	var got api.Escalation
	e.ok(e.worker(), "GET", "/v1/escalations/1", nil, &got) // agents may read, to poll
	if got.Answer == nil || *got.Answer != "yes" {
		t.Fatalf("polled escalation: %+v", got)
	}

	// The lead receives the answer from the human, tied to the task.
	var msg *api.Message
	for _, m := range e.inbox(e.lead()) {
		if strings.Contains(m.Body, "Answer to your question #1") {
			m := m
			msg = &m
		}
	}
	if msg == nil || msg.From != "human:afif" || msg.TaskID == nil || *msg.TaskID != task.ID {
		t.Fatalf("answer message: %+v", msg)
	}
	for _, action := range []string{"escalation.open", "escalation.answer"} {
		if !e.audited(action, "escalation:1") {
			t.Errorf("audit log has no %s", action)
		}
	}
}

// An agent cannot answer, whoever it claims to be, and its attempt is audited.
func TestAgentCannotAnswerEscalation(t *testing.T) {
	e := newEnv(t)
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "Ship it?"}, nil)
	for _, c := range []caller{e.lead(), e.worker(), e.observer()} {
		e.fail(403, c, "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "yes"})
	}
	// Spoofing the human in the body gets nowhere: the scope check runs first.
	e.fail(403, e.lead(), "POST", "/v1/escalations/1/answer", `{"answer":"yes","answered_by":"human:afif"}`)
	var esc api.Escalation
	e.ok(e.afif(), "GET", "/v1/escalations/1", nil, &esc)
	if esc.Answer != nil {
		t.Fatalf("an agent answered: %+v", esc)
	}
	if !e.audited("denied", "POST /v1/escalations/1/answer") {
		t.Error("the rejected answers are not in the audit log")
	}
	// The admin token may not answer either.
	e.fail(403, caller{e.admin, ""}, "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "yes"})
}

func TestEscalationValidation(t *testing.T) {
	e := newEnv(t)
	e.fail(400, e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "  "})
	e.fail(400, e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "q", Options: []string{"a", "a"}})
	e.fail(400, e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "q", Options: make([]string, 11)})
	big := int64(99999)
	e.fail(404, e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "q", TaskID: &big})
	e.fail(404, e.afif(), "GET", "/v1/escalations/42", nil)
	// Free text is allowed when no options are given.
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "Which name?"}, nil)
	e.ok(e.afif(), "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "Handloom"}, nil)
}

// fakeNotifier records notifications.
type fakeNotifier struct {
	mu  sync.Mutex
	got []notify.Notification
	ch  chan struct{}
}

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{ch: make(chan struct{}, 8)} }

func (f *fakeNotifier) Notify(_ context.Context, n notify.Notification) error {
	f.mu.Lock()
	f.got = append(f.got, n)
	f.mu.Unlock()
	f.ch <- struct{}{}
	return nil
}

// A new escalation notifies the human once, with a link and without the question.
func TestEscalationNotifiesWithoutContent(t *testing.T) {
	e := newEnv(t)
	f := newFakeNotifier()
	e.hub.opt.Notifier = f
	e.hub.opt.BaseURL = "https://handloom.example.com/"
	secret := "Should we fire the contractor named Smith?"
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: secret}, nil)
	select {
	case <-f.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no notification")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) != 1 {
		t.Fatalf("%d notifications", len(f.got))
	}
	n := f.got[0]
	if n.Kind != notify.KindEscalation || n.Link != "https://handloom.example.com/inbox" {
		t.Fatalf("notification: %+v", n)
	}
	if strings.Contains(n.Title+n.Text, "Smith") || strings.Contains(n.Title+n.Text, "contractor") {
		t.Fatalf("the notification leaks the question: %+v", n)
	}
	// A rejected ask notifies nobody.
	e.fail(403, e.worker(), "POST", "/v1/escalations", api.AskReq{Question: "x"})
	select {
	case <-f.ch:
		t.Fatal("a rejected ask sent a notification")
	case <-time.After(200 * time.Millisecond):
	}
}
