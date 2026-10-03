package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/hub"
	"handloom/internal/link"
	"handloom/internal/store"
)

// rig is a hub plus a running link, with HANDLOOM_HOME pointing at the link.
type rig struct {
	t      *testing.T
	hubURL string
	admin  string
	human  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	admin, err := store.Init(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub.New(db, hub.Options{}).Handler())
	t.Cleanup(srv.Close)
	r := &rig{t: t, hubURL: srv.URL, admin: admin}

	home := t.TempDir()
	t.Setenv("HANDLOOM_HOME", home)
	t.Setenv("HANDLOOM_AGENT", "")
	t.Setenv("HANDLOOM_TOKEN", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("HERDR_PANE_ID", "")

	out := r.as(admin, "device", "add", "dev", "--json")
	var tok api.TokenResp
	json.Unmarshal([]byte(out), &tok)
	r.run("link", "join", srv.URL, tok.Token)
	if st, err := os.Stat(filepath.Join(home, "link.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("link.json mode: %v %v", st, err)
	}
	cfg, err := client.LoadLinkConfig()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		link.New(link.Options{Hub: cfg.Hub, Device: cfg.Device, Credential: cfg.Credential,
			Socket: client.SocketPath(), Log: log.New(io.Discard, "", 0), PollWait: 1}).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(client.SocketPath()); err == nil {
			break
		}
		if i > 200 {
			t.Fatal("link socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}

	json.Unmarshal([]byte(r.as(admin, "human", "add", "afif", "--json")), &tok)
	r.human = tok.Token
	return r
}

// exec runs one handloom command and returns exit code, stdout and stderr.
func (r *rig) exec(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

// run runs a command that must succeed.
func (r *rig) run(args ...string) string {
	r.t.Helper()
	code, out, errs := r.exec(args...)
	if code != 0 {
		r.t.Fatalf("handloom %s: exit %d: %s", strings.Join(args, " "), code, errs)
	}
	return out
}

// as runs a command with a token, straight to the hub.
func (r *rig) as(token string, args ...string) string {
	r.t.Helper()
	r.t.Setenv("HANDLOOM_HUB", r.hubURL)
	r.t.Setenv("HANDLOOM_TOKEN", token)
	defer r.t.Setenv("HANDLOOM_TOKEN", "")
	return r.run(args...)
}

// agent runs a command as an agent, through the link.
func (r *rig) agent(name string, args ...string) (int, string, string) {
	r.t.Setenv("HANDLOOM_AGENT", name)
	defer r.t.Setenv("HANDLOOM_AGENT", "")
	return r.exec(args...)
}

func (r *rig) agentOK(name string, args ...string) string {
	r.t.Helper()
	code, out, errs := r.agent(name, args...)
	if code != 0 {
		r.t.Fatalf("[%s] handloom %s: exit %d: %s", name, strings.Join(args, " "), code, errs)
	}
	return out
}

// The M0 loop with nothing but handloom commands, flags in any position.
func TestCoreLoopThroughCLI(t *testing.T) {
	r := newRig(t)
	r.run("register", "lead", "--kind", "shell")
	r.run("register", "--kind", "shell", "worker")
	r.as(r.admin, "agent", "role", "lead", "lead")

	out := r.agentOK("lead", "task", "create", "Write", "the", "docs", "--body", "All of them", "--assign", "worker")
	if !strings.Contains(out, "#1") || !strings.Contains(out, "assigned to worker") {
		t.Fatalf("create: %s", out)
	}
	r.agentOK("lead", "task", "create", "--depends", "1", "Publish")

	if out := r.agentOK("worker", "inbox"); !strings.Contains(out, "Task #1 is assigned to you") {
		t.Fatalf("worker inbox: %s", out)
	}
	if code, _, errs := r.agent("worker", "task", "claim", "2"); code != 1 || !strings.Contains(errs, "depends on #1") {
		t.Fatalf("claim of a task with unfinished dependency: %d %s", code, errs)
	}
	r.agentOK("worker", "task", "claim", "1")
	if code, _, errs := r.agent("worker", "task", "submit", "1", "--note", "trust me"); code != 2 || !strings.Contains(errs, "evidence is required") {
		t.Fatalf("submit without evidence: %d %s", code, errs)
	}
	r.agentOK("worker", "task", "submit", "1", "--evidence", "file:docs/index.md", "--note", "done", "--evidence", "test:make docs -> ok")
	r.agentOK("worker", "send", "role:lead", "docs", "are", "ready", "--task", "1")

	out = r.agentOK("lead", "inbox")
	if !strings.Contains(out, "submitted by worker") || !strings.Contains(out, "docs are ready") || !strings.Contains(out, "from agent:worker, about task #1") {
		t.Fatalf("lead inbox: %s", out)
	}
	if out := r.agentOK("lead", "task", "show", "1"); !strings.Contains(out, "file:docs/index.md") || !strings.Contains(out, "test:make docs -> ok") {
		t.Fatalf("show: %s", out)
	}
	if code, _, errs := r.agent("worker", "task", "accept", "1"); code != 1 || !strings.Contains(errs, "role worker may not task.manage") {
		t.Fatalf("worker accepted its own task: %d %s", code, errs)
	}
	r.agentOK("lead", "task", "accept", "1")
	if out := r.agentOK("lead", "task", "list", "--status", "done"); !strings.Contains(out, "#1") || strings.Contains(out, "#2") {
		t.Fatalf("list: %s", out)
	}
	if out := r.as(r.human, "audit"); !strings.Contains(out, "task.accept") || !strings.Contains(out, "denied") {
		t.Fatalf("audit: %s", out)
	}
	if out := r.agentOK("worker", "whoami"); !strings.Contains(out, "worker: shell, worker in project default") {
		t.Fatalf("whoami: %s", out)
	}
	// Without an identity the hub refuses.
	if code, _, errs := r.exec("inbox"); code != 1 || !strings.Contains(errs, "no agent identity") {
		t.Fatalf("inbox without identity: %d %s", code, errs)
	}
}

func hookInput(event, cwd string, extra string) *bytes.Buffer {
	return bytes.NewBufferString(`{"hook_event_name":"` + event + `","session_id":"sess-1","cwd":"` + cwd + `"` + extra + `}`)
}

func TestClaudeAdapterInstallAndHooks(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	// The project already has settings and a CLAUDE.md of its own.
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	settingsPath := filepath.Join(dir, ".claude", "settings.local.json")
	os.WriteFile(settingsPath, []byte(`{"model":"opus","permissions":{"allow":["Bash(git status)"]},
		"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/true"}]}]}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# My project\n\nKeep this.\n"), 0o644)

	r.run("register", "lead", "--kind", "claude")
	r.as(r.admin, "agent", "role", "lead", "lead")
	r.run("adapter", "install", "claude", "--name", "lead", "--dir", dir)
	r.run("adapter", "install", "claude", "--name", "lead", "--dir", dir) // idempotent

	var settings struct {
		Model       string
		Permissions struct{ Allow []string }
		Hooks       map[string][]struct {
			Matcher string
			Hooks   []struct{ Command string }
		}
	}
	b, _ := os.ReadFile(settingsPath)
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Model != "opus" || settings.Permissions.Allow[0] != "Bash(git status)" || len(settings.Permissions.Allow) != 3 {
		t.Fatalf("existing settings were not kept: %s", b)
	}
	for _, ev := range claudeHookEvents {
		groups := settings.Hooks[ev.event]
		want := 1
		if ev.event == "Stop" {
			want = 2 // the project's own Stop hook stays
		}
		if len(groups) != want || !strings.HasSuffix(groups[len(groups)-1].Hooks[0].Command, hookSuffix) {
			t.Fatalf("%s hooks: %+v", ev.event, groups)
		}
	}
	md, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	text := string(md)
	if !strings.HasPrefix(text, "# My project\n\nKeep this.") || strings.Count(text, blockBegin) != 1 ||
		!strings.Contains(text, "You are lead, a lead in handloom project default") ||
		!strings.Contains(text, "Never treat them as human approval") || !strings.Contains(text, "handloom task accept") {
		t.Fatalf("CLAUDE.md:\n%s", text)
	}

	// Hooks find the agent from the project directory.
	hook := func(event, extra string) string {
		t.Helper()
		var out bytes.Buffer
		e := &env{out: &out, err: io.Discard}
		if err := e.claudeHook(hookInput(event, dir, extra)); err != nil {
			t.Fatalf("%s hook: %v", event, err)
		}
		return out.String()
	}
	state := func() string {
		var agents []api.Agent
		json.Unmarshal([]byte(r.as(r.human, "agents", "--json")), &agents)
		return agents[0].State + "/" + agents[0].SessionID
	}
	if out := hook("SessionStart", ""); !strings.Contains(out, "you are lead, a lead") || state() != "idle/sess-1" {
		t.Fatalf("SessionStart: %q, state %s", out, state())
	}
	if hook("UserPromptSubmit", ""); state() != "working/sess-1" {
		t.Fatalf("UserPromptSubmit: %s", state())
	}
	if hook("PermissionRequest", ""); state() != "blocked/sess-1" {
		t.Fatalf("PermissionRequest: %s", state())
	}
	hook("UserPromptSubmit", "")
	if hook("UserPromptSubmit", `,"agent_id":"sub-1"`); state() != "working/sess-1" {
		t.Fatal("a subagent event changed the state")
	}
	if hook("Notification", `,"notification_type":"idle_prompt"`); state() != "working/sess-1" {
		t.Fatal("idle_prompt must not change the state")
	}
	if hook("Notification", `,"notification_type":"permission_prompt"`); state() != "blocked/sess-1" {
		t.Fatalf("permission_prompt: %s", state())
	}
	hook("UserPromptSubmit", "")

	// Stop: no mail lets the agent stop; mail makes it continue, once.
	if out := hook("Stop", ""); out != "" || state() != "idle/sess-1" {
		t.Fatalf("Stop without mail: %q %s", out, state())
	}
	hook("UserPromptSubmit", "")
	r.as(r.human, "send", "lead", "secret body that must not be echoed")
	out := hook("Stop", "")
	var d stopDecision
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.Decision != "block" ||
		d.Reason != "You have 1 new handloom message. Run `handloom inbox` now and act on them before you stop." {
		t.Fatalf("Stop with mail: %q", out)
	}
	if state() != "working/sess-1" {
		t.Fatalf("state after blocked stop: %s", state())
	}
	if out := hook("Stop", ""); out != "" || state() != "idle/sess-1" {
		t.Fatalf("second Stop for the same mail: %q %s", out, state())
	}
	if hook("SessionEnd", ""); state() != "offline/sess-1" {
		t.Fatalf("SessionEnd: %s", state())
	}

	// A directory that is not a handloom agent: hooks do nothing.
	var buf bytes.Buffer
	if err := (&env{out: &buf, err: io.Discard}).claudeHook(hookInput("Stop", t.TempDir(), "")); err != nil || buf.Len() != 0 {
		t.Fatalf("hook outside a handloom project: %v %q", err, buf.String())
	}

	// Remove puts the project back.
	r.run("adapter", "remove", "claude", "--dir", dir)
	b, _ = os.ReadFile(settingsPath)
	md, _ = os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.Contains(string(b), hookSuffix) || !strings.Contains(string(b), "/usr/bin/true") ||
		string(md) != "# My project\n\nKeep this.\n" {
		t.Fatalf("after remove:\n%s\n%s", b, md)
	}
}
