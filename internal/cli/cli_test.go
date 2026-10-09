package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/hub"
	"handloom/internal/link"
	"handloom/internal/mcp"
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
			Socket: client.SocketPath(), Log: log.New(io.Discard, "", 0), PollWait: 1,
			// Never start real terminals from a test.
			Tmux: func(context.Context, string, ...string) (string, error) { return "", nil }}).Run(ctx)
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

// hookJSON builds the JSON an agent CLI sends to a hook.
func hookJSON(event, cwd string, extra string) *bytes.Buffer {
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
	for _, ev := range adapterSpecs["claude"].events {
		groups := settings.Hooks[ev.event]
		want := 1
		if ev.event == "Stop" {
			want = 2 // the project's own Stop hook stays
		}
		if len(groups) != want || !strings.HasSuffix(groups[len(groups)-1].Hooks[0].Command, " hook claude") {
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
		if err := e.agentHook("claude", hookJSON(event, dir, extra)); err != nil {
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
	if err := (&env{out: &buf, err: io.Discard}).agentHook("claude", hookJSON("Stop", t.TempDir(), "")); err != nil || buf.Len() != 0 {
		t.Fatalf("hook outside a handloom project: %v %q", err, buf.String())
	}

	// Remove puts the project back.
	r.run("adapter", "remove", "claude", "--dir", dir)
	b, _ = os.ReadFile(settingsPath)
	md, _ = os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.Contains(string(b), " hook claude") || !strings.Contains(string(b), "/usr/bin/true") ||
		string(md) != "# My project\n\nKeep this.\n" {
		t.Fatalf("after remove:\n%s\n%s", b, md)
	}
}

// Codex has the same hook shape as Claude Code; Pi, OMP and OpenCode get a
// shim that forwards their events to `handloom hook <kind>`.
func TestOtherAdapters(t *testing.T) {
	r := newRig(t)
	for kind, want := range map[string]struct{ file, contains, instructions string }{
		"codex":    {".codex/hooks.json", " hook codex", "AGENTS.md"},
		"pi":       {".pi/extensions/handloom.ts", `["hook", KIND]`, "AGENTS.md"},
		"omp":      {".handloom/omp-extension.ts", `pi.on("agent_end"`, "AGENTS.md"},
		"opencode": {".opencode/plugins/handloom.js", `"session.idle"`, "AGENTS.md"},
	} {
		dir := t.TempDir()
		name := "agent-" + kind
		r.run("adapter", "install", kind, "--name", name, "--dir", dir)
		r.run("adapter", "install", kind, "--name", name, "--dir", dir)
		b, err := os.ReadFile(filepath.Join(dir, want.file))
		if err != nil || !strings.Contains(string(b), want.contains) || strings.Contains(string(b), "{{") {
			t.Fatalf("%s: %s: %v\n%s", kind, want.file, err, b)
		}
		md, _ := os.ReadFile(filepath.Join(dir, want.instructions))
		if !strings.Contains(string(md), "You are "+name+", a worker") || strings.Count(string(md), blockBegin) != 1 {
			t.Fatalf("%s: instructions:\n%s", kind, md)
		}
		if kind == "codex" {
			var doc struct {
				Hooks map[string][]struct {
					Hooks []struct{ Command string }
				}
			}
			json.Unmarshal(b, &doc)
			for _, ev := range adapterSpecs["codex"].events {
				if len(doc.Hooks[ev.event]) != 1 { // exactly once: two Stop hooks would ask the hub twice
					t.Fatalf("codex %s hooks: %+v", ev.event, doc.Hooks[ev.event])
				}
			}
		}

		// The hook handler is the same for every kind; it records kind,
		// session and directory, and a headless agent's wake target.
		t.Setenv("HANDLOOM_HEADLESS", "1")
		var out bytes.Buffer
		e := &env{out: &out, err: io.Discard}
		if err := e.agentHook(kind, hookJSON("SessionStart", dir, "")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HANDLOOM_HEADLESS", "")
		var agents []api.Agent
		json.Unmarshal([]byte(r.as(r.human, "agents", "--json")), &agents)
		var a api.Agent
		for _, x := range agents {
			if x.Name == name {
				a = x
			}
		}
		if a.Kind != kind || a.SessionID != "sess-1" || a.Dir != dir || a.WakeTarget != "headless" || a.State != "idle" {
			t.Fatalf("%s: registered as %+v", kind, a)
		}
		if out.Len() != 0 { // only Claude Code takes context from SessionStart output
			t.Fatalf("%s: SessionStart printed %q", kind, out.String())
		}
		r.run("adapter", "remove", kind, "--dir", dir)
		if _, err := os.Stat(filepath.Join(dir, want.file)); err == nil && kind != "codex" {
			t.Fatalf("%s: %s left behind", kind, want.file)
		}
	}
	if code, _, errs := r.exec("adapter", "install", "emacs", "--name", "x"); code != 1 || !strings.Contains(errs, "no adapter") {
		t.Fatalf("unknown kind: %d %s", code, errs)
	}
}

// The MCP server runs the same verbs as the command line, as the same agent.
func TestMCPThroughRealHub(t *testing.T) {
	r := newRig(t)
	r.run("register", "lead", "--kind", "shell")
	r.run("register", "worker", "--kind", "shell")
	r.as(r.admin, "agent", "role", "lead", "lead")
	r.agentOK("lead", "task", "create", "Do it", "--assign", "worker")

	t.Setenv("HANDLOOM_AGENT", "worker")
	call := func(name, arguments string) (string, bool) {
		t.Helper()
		var out bytes.Buffer
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + arguments + `}}` + "\n"
		err := mcp.Serve(strings.NewReader(req), &out, "test", func(argv []string) (string, string, int) {
			var o, e strings.Builder
			code := Main(argv, &o, &e)
			return o.String(), e.String(), code
		})
		if err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Result struct {
				Content []struct{ Text string }
				IsError bool
			}
		}
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return resp.Result.Content[0].Text, resp.Result.IsError
	}
	if text, isErr := call("handloom_inbox", `{}`); isErr || !strings.Contains(text, "Task #1 is assigned to you") {
		t.Fatalf("inbox: %v %s", isErr, text)
	}
	if text, isErr := call("handloom_task_claim", `{"id":1}`); isErr || !strings.Contains(text, "claimed") {
		t.Fatalf("claim: %v %s", isErr, text)
	}
	if text, isErr := call("handloom_task_submit", `{"id":1,"evidence":[]}`); !isErr || !strings.Contains(text, "evidence is required") {
		t.Fatalf("submit without evidence: %v %s", isErr, text)
	}
	if text, isErr := call("handloom_task_submit", `{"id":1,"evidence":["test:x -> ok"],"note":"-n starts with a dash"}`); isErr || !strings.Contains(text, "submitted") {
		t.Fatalf("submit: %v %s", isErr, text)
	}
	// Scopes hold: a worker cannot accept through MCP either.
	if text, isErr := call("handloom_task_accept", `{"id":1}`); !isErr || !strings.Contains(text, "may not task.manage") {
		t.Fatalf("accept as worker: %v %s", isErr, text)
	}
	if text, isErr := call("handloom_send", `{"to":"role:lead","text":"-- done --"}`); isErr || !strings.Contains(text, "Sent to lead") {
		t.Fatalf("send: %v %s", isErr, text)
	}
	t.Setenv("HANDLOOM_AGENT", "")
	if out := r.agentOK("lead", "inbox"); !strings.Contains(out, "-- done --") {
		t.Fatalf("lead inbox: %s", out)
	}
}

func TestServiceUnit(t *testing.T) {
	s := service{name: "handloom-link", description: "Handloom link (device daemon)", args: []string{"link", "run"},
		env: map[string]string{"HANDLOOM_HOME": "/home/a b/.config/handloom"}}
	unit := s.unitText("/usr/local/bin/handloom", "/root", "/usr/bin:/bin", true)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/handloom link run\n",
		"Restart=always\n",
		"Environment=PATH=/usr/local/bin:/usr/bin:/bin\n", // the binary's directory first: agents call `handloom`
		`Environment="HANDLOOM_HOME=/home/a b/.config/handloom"` + "\n",
		"Environment=HOME=/root\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("system unit lacks %q:\n%s", want, unit)
		}
	}
	hub := service{name: "handloom-hub", description: "Handloom hub", args: []string{"hub", "serve", "--data", "/var/lib/handloom", "--addr", "100.1.2.3:7420"}}
	unit = hub.unitText("/home/u/.local/bin/handloom", "/home/u", "/usr/bin", false)
	if !strings.Contains(unit, "ExecStart=/home/u/.local/bin/handloom hub serve --data /var/lib/handloom --addr 100.1.2.3:7420\n") ||
		!strings.Contains(unit, "WantedBy=default.target\n") {
		t.Fatalf("user unit:\n%s", unit)
	}
}

// The lead asks the human; the answer comes back as a message from the human.
func TestAskAndAnswerThroughCLI(t *testing.T) {
	r := newRig(t)
	r.run("register", "lead", "--kind", "shell")
	r.run("register", "worker", "--kind", "shell")
	r.as(r.admin, "agent", "role", "lead", "lead")

	if code, _, errs := r.agent("worker", "ask", "Can", "I", "delete", "it?"); code != 1 || !strings.Contains(errs, "may not") {
		t.Fatalf("worker ask: %d %s", code, errs)
	}
	out := r.agentOK("lead", "ask", "Delete the old migrations?", "--option", "yes", "--option", "no")
	if !strings.Contains(out, "Question #1 sent to the human") {
		t.Fatalf("ask: %s", out)
	}
	if code, _, errs := r.agent("lead", "answer", "1", "yes"); code != 1 || !strings.Contains(errs, "may not") {
		t.Fatalf("lead answering its own question: %d %s", code, errs)
	}
	if out := r.as(r.human, "escalations"); !strings.Contains(out, "lead asks (open): Delete the old migrations?") || !strings.Contains(out, "yes / no") {
		t.Fatalf("escalations: %s", out)
	}
	r.as(r.human, "answer", "1", "yes")
	if out := r.agentOK("lead", "inbox"); !strings.Contains(out, "from human:") || !strings.Contains(out, "Answer to your question #1") || !strings.Contains(out, ": yes") {
		t.Fatalf("lead inbox after answer: %s", out)
	}
	if out := r.as(r.human, "escalations", "--status", "answered"); !strings.Contains(out, "answered: yes") {
		t.Fatalf("answered list: %s", out)
	}
}

// A job with its own lead, through the CLI.
func TestJobsThroughCLI(t *testing.T) {
	r := newRig(t)
	r.run("register", "lead1", "--kind", "shell")
	r.run("register", "w1", "--kind", "shell")
	if out := r.as(r.human, "job", "new", "Scan competitors", "--lead", "lead1", "--body", "Twelve of them"); !strings.Contains(out, "Started job #") || !strings.Contains(out, "lead: lead1") {
		t.Fatalf("job new: %s", out)
	}
	if out := r.agentOK("lead1", "inbox"); !strings.Contains(out, "lead of job #") {
		t.Fatalf("lead1 inbox: %s", out)
	}
	if code, _, errs := r.agent("lead1", "job", "new", "x"); code != 1 || !strings.Contains(errs, "for a human") {
		t.Fatalf("a lead starting a job: %d %s", code, errs)
	}
	r.agentOK("lead1", "task", "create", "Fetch pages", "--assign", "w1")
	if out := r.as(r.human, "task", "list", "--job", "1"); !strings.Contains(out, "Fetch pages") {
		t.Fatalf("task list --job: %s", out)
	}
	if out := r.as(r.human, "job", "show", "1"); !strings.Contains(out, "Twelve of them") || !strings.Contains(out, "Fetch pages") {
		t.Fatalf("job show: %s", out)
	}
	if out := r.as(r.human, "job", "list"); !strings.Contains(out, "1 open") {
		t.Fatalf("job list: %s", out)
	}
	r.as(r.human, "job", "close", "1", "--cancel")
	if out := r.as(r.human, "agent", "job", "w1", "0"); !strings.Contains(out, "in no job") {
		t.Fatalf("agent job: %s", out)
	}
}

// An adapter install gives the agent a run token in a 0600 file next to its
// identity; its commands carry it, and the hub can tell.
func TestAdapterInstallGivesTheAgentARunToken(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	r.run("adapter", "install", "claude", "--name", "tok-agent", "--dir", dir)
	file := filepath.Join(dir, ".handloom", "tokens", "tok-agent")
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", st, err)
	}
	first, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(first), "hvr_") {
		t.Fatalf("token file content %q", first)
	}

	t.Chdir(dir)
	if out := r.agentOK("tok-agent", "whoami", "--json"); !strings.Contains(out, `"via": "run-token"`) {
		t.Fatalf("whoami in the project: %s", out)
	}
	t.Chdir(t.TempDir())
	if out := r.agentOK("tok-agent", "whoami", "--json"); !strings.Contains(out, `"via": "device-asserted"`) {
		t.Fatalf("whoami outside the project: %s", out)
	}

	// Installing again rotates the token, and the files stay in step.
	r.run("adapter", "install", "claude", "--name", "tok-agent", "--dir", dir)
	second, _ := os.ReadFile(file)
	if string(second) == string(first) {
		t.Fatal("a second install kept the old token")
	}
	t.Chdir(dir)
	if out := r.agentOK("tok-agent", "whoami", "--json"); !strings.Contains(out, `"via": "run-token"`) {
		t.Fatalf("whoami after rotation: %s", out)
	}
	// A stale file for the same agent is refused, not quietly ignored.
	os.WriteFile(file, first, 0o600)
	if code, _, errs := r.agent("tok-agent", "whoami"); code == 0 || !strings.Contains(errs, "run token") {
		t.Fatalf("a stale token: %d %s", code, errs)
	}
	os.WriteFile(file, second, 0o600)

	r.run("adapter", "remove", "claude", "--dir", dir)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("remove left the token file")
	}
}

// ---- spawn ----

func waitSpawn(t *testing.T, r *rig, id int, status string) api.Spawn {
	t.Helper()
	var s api.Spawn
	for i := 0; i < 300; i++ {
		json.Unmarshal([]byte(r.as(r.human, "spawns", "--json")), new([]api.Spawn))
		var all []api.Spawn
		json.Unmarshal([]byte(r.as(r.human, "spawns", "--json")), &all)
		for _, x := range all {
			if int(x.ID) == id {
				s = x
			}
		}
		if s.Status == status {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("spawn %d never became %s: %+v", id, status, s)
	return s
}

func TestSpawnExecInstallsTheAdapterAndBecomesTheAgent(t *testing.T) {
	r := newRig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups": 3, "projects": {"/elsewhere": {"hasTrustDialogAccepted": false, "keep": "me"}}}`), 0o600)
	work := t.TempDir()
	// A fake claude in PATH, and the terminal variables a link-made tmux window has.
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TMUX", "/tmp/tmux-1000/handloom,123,0")
	t.Setenv("TMUX_PANE", "%7")
	var gotArgv []string
	old := execFn
	execFn = func(path string, argv []string, env []string) error { gotArgv = argv; return nil }
	t.Cleanup(func() { execFn = old })

	r.run("register", "lead1", "--kind", "shell")
	r.as(r.human, "job", "new", "A job", "--lead", "lead1")
	out := r.as(r.human, "spawn", "tester", "--kind", "claude", "--model", "sonnet", "--device", "dev", "--job", "1", "--wait", "0")
	if !strings.Contains(out, "tester is pending") && !strings.Contains(out, "tester is launching") {
		t.Fatalf("spawn: %s", out)
	}
	waitSpawn(t, r, 1, "launching") // the rig's link claims it; its fake tmux starts nothing

	t.Chdir(work)
	r.run("spawn-exec", "1")

	// It became claude, with the unattended settings and isolation.
	line := strings.Join(gotArgv, " ")
	for _, want := range []string{"claude", "--setting-sources project,local", "--permission-mode dontAsk", "Bash(handloom:*)", "--model sonnet"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q: %s", want, line)
		}
	}
	// The adapter is installed in the work directory, with identity and token.
	for _, f := range []string{"CLAUDE.md", ".claude/settings.local.json", ".handloom/agent", ".handloom/tokens/tester"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	// The agent is registered with this terminal and joined the job; the hub knows it started.
	var agents []api.Agent
	json.Unmarshal([]byte(r.as(r.human, "agents", "--json")), &agents)
	var a *api.Agent
	for i := range agents {
		if agents[i].Name == "tester" {
			a = &agents[i]
		}
	}
	if a == nil || a.WakeTarget != "tmux:/tmp/tmux-1000/handloom:%7" || a.Job == nil || *a.Job != 1 || a.Role != "worker" {
		t.Fatalf("registered agent: %+v", a)
	}
	s := waitSpawn(t, r, 1, "started")
	if s.Pane != "tmux:/tmp/tmux-1000/handloom:%7" {
		t.Fatalf("spawn pane: %q", s.Pane)
	}
	// Claude Code's "trust this folder" question was answered in advance, and nothing else in its config changed.
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var doc struct {
		Num      int                       `json:"numStartups"`
		Projects map[string]map[string]any `json:"projects"`
	}
	json.Unmarshal(raw, &doc)
	if doc.Num != 3 || doc.Projects[work]["hasTrustDialogAccepted"] != true || doc.Projects["/elsewhere"]["keep"] != "me" {
		t.Fatalf("~/.claude.json: %s", raw)
	}
}

func TestSpawnExecRefusesWhatWasNotRequested(t *testing.T) {
	spawnFailPause = 0
	t.Cleanup(func() { spawnFailPause = 20 * time.Second })
	r := newRig(t)
	t.Setenv("HOME", t.TempDir())
	old := execFn
	execFn = func(string, []string, []string) error { t.Fatal("exec called"); return nil }
	t.Cleanup(func() { execFn = old })
	if code, _, errs := r.exec("spawn-exec", "99"); code == 0 || !strings.Contains(errs, "not waiting") {
		t.Fatalf("an unknown spawn: %d %s", code, errs)
	}
}

func TestTrustClaudeDirDoesNothingWithoutAClaudeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := trustClaudeDir("/w"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Fatal("created a Claude Code config that was not there")
	}
}

// ---- profiles ----

func writeProfileDir(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "skills", "cite"), 0o755)
	os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte("name: researcher\ndescription: reads and writes\nkind: claude\ntools:\n  allow: [read, edit, shell]\n  deny_commands: [rm]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "PROMPT.md"), []byte("Cite every source.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "cite", "SKILL.md"), []byte("---\nname: cite\ndescription: how to cite\n---\nCite it."), 0o644)
}

func TestProfileVerbs(t *testing.T) {
	r := newRig(t)
	src := filepath.Join(t.TempDir(), "researcher")
	writeProfileDir(t, src)

	out := r.run("profile", "check", src)
	for _, want := range []string{"valid profile", "allowed: shell", "refused by Claude Code: web", "the shell command rm", "not enforced: file paths"} {
		if !strings.Contains(out, want) {
			t.Errorf("check lacks %q:\n%s", want, out)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "profile.yaml"), []byte("name: x\ntools:\n  allow: [root]\n"), 0o644)
	if code, out, _ := r.exec("profile", "check", bad); code == 0 || !strings.Contains(out, "unknown tool") {
		t.Fatalf("check of a bad profile: %d %s", code, out)
	}

	// Writing needs the owner or the admin; a member cannot.
	if code, _, errs := r.exec("profile", "new", src); code == 0 {
		t.Fatalf("no credentials: %s", errs)
	}
	if code, _, errs := (func() (int, string, string) {
		t.Setenv("HANDLOOM_HUB", r.hubURL)
		t.Setenv("HANDLOOM_TOKEN", r.human)
		return r.exec("profile", "new", src)
	})(); code == 0 || !strings.Contains(errs, "owner") {
		t.Fatalf("a member wrote a profile: %d %s", code, errs)
	}
	if out := r.as(r.admin, "profile", "new", src); !strings.Contains(out, "researcher: version 1") {
		t.Fatalf("new: %s", out)
	}
	if out := r.as(r.admin, "profile", "new", src); !strings.Contains(out, "version 1") {
		t.Fatalf("an unchanged profile should stay at version 1: %s", out)
	}
	if out := r.as(r.human, "profiles"); !strings.Contains(out, "researcher") || !strings.Contains(out, "skills: cite") {
		t.Fatalf("profiles: %s", out)
	}
	if out := r.as(r.human, "profile", "show", "researcher"); !strings.Contains(out, "denied commands: rm") || !strings.Contains(out, "Cite every source.") {
		t.Fatalf("show: %s", out)
	}
	// Export and import again gives the same version: nothing is lost on the way.
	back := filepath.Join(t.TempDir(), "again")
	r.as(r.human, "profile", "export", "researcher", back)
	if out := r.as(r.admin, "profile", "new", back); !strings.Contains(out, "version 1") {
		t.Fatalf("the exported profile differs from the original: %s", out)
	}
	if out := r.as(r.human, "profile", "versions", "researcher"); !strings.Contains(out, "v1") {
		t.Fatalf("versions: %s", out)
	}
}

func TestSpawnExecMaterializesAProfile(t *testing.T) {
	r := newRig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TMUX", "/tmp/tmux-1000/handloom,123,0")
	t.Setenv("TMUX_PANE", "%8")
	var gotArgv []string
	old := execFn
	execFn = func(path string, argv []string, env []string) error { gotArgv = argv; return nil }
	t.Cleanup(func() { execFn = old })

	src := filepath.Join(t.TempDir(), "researcher")
	writeProfileDir(t, src)
	r.as(r.admin, "profile", "new", src)
	r.as(r.human, "spawn", "tester", "--profile", "researcher", "--device", "dev", "--model", "sonnet", "--wait", "0")
	waitSpawn(t, r, 1, "launching")

	t.Chdir(work)
	r.run("spawn-exec", "1")

	line := strings.Join(gotArgv, " ")
	for _, want := range []string{"--allowedTools Bash(handloom:*) Write Edit Read Glob Grep Bash", "--disallowedTools Bash(rm:*)", "--model sonnet", "--setting-sources project,local"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q: %s", want, line)
		}
	}
	if b, err := os.ReadFile(filepath.Join(work, ".claude", "skills", "cite", "SKILL.md")); err != nil || !strings.Contains(string(b), "Cite it.") {
		t.Fatalf("the profile's skill is not in the work directory: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(work, "CLAUDE.md"))
	for _, want := range []string{"Your profile: researcher (version 1)", "Cite every source.", "You are tester"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("CLAUDE.md lacks %q:\n%s", want, b)
		}
	}
	if s := waitSpawn(t, r, 1, "started"); s.Profile != "researcher@1" {
		t.Fatalf("spawn: %+v", s)
	}
}

func TestSpawnExecStartsCodexThroughMCP(t *testing.T) {
	r := newRig(t)
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TMUX", "/tmp/tmux-1000/handloom,123,0")
	t.Setenv("TMUX_PANE", "%9")
	var gotArgv []string
	old := execFn
	execFn = func(path string, argv []string, env []string) error { gotArgv = argv; return nil }
	t.Cleanup(func() { execFn = old })

	spec := filepath.Join(t.TempDir(), "coder")
	os.MkdirAll(spec, 0o755)
	os.WriteFile(filepath.Join(spec, "profile.yaml"), []byte("name: coder\nkind: codex\ntools:\n  allow: [read, edit, shell]\n"), 0o644)
	os.WriteFile(filepath.Join(spec, "PROMPT.md"), []byte("Write small functions.\n"), 0o644)
	r.as(r.admin, "profile", "new", spec)
	r.as(r.human, "spawn", "tester", "--profile", "coder", "--device", "dev", "--wait", "0")
	waitSpawn(t, r, 1, "launching")

	t.Chdir(work)
	r.run("spawn-exec", "1")

	line := strings.Join(gotArgv, " ")
	for _, want := range []string{"codex --enable hooks --dangerously-bypass-hook-trust -a never -s workspace-write", "mcp_servers.handloom.command=", `HANDLOOM_AGENT="tester"`} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q: %s", want, line)
		}
	}
	for _, f := range []string{"AGENTS.md", ".codex/hooks.json", ".handloom/tokens/tester"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(work, "AGENTS.md")); !strings.Contains(string(b), "Write small functions.") || !strings.Contains(string(b), "Your profile: coder") {
		t.Errorf("AGENTS.md lacks the profile prompt:\n%s", b)
	}
	if s := waitSpawn(t, r, 1, "started"); s.Kind != "codex" || s.Profile != "coder@1" {
		t.Fatalf("spawn: %+v", s)
	}
}

func TestTrustCodexDir(t *testing.T) {
	codex := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", codex)
	// Codex has never run on this machine: nothing to do, nothing created.
	if err := trustCodexDir("/w/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(codex); err == nil {
		t.Fatal("created a Codex directory that was not there")
	}
	os.MkdirAll(codex, 0o700)
	os.WriteFile(filepath.Join(codex, "config.toml"), []byte("personality = \"pragmatic\"\n[projects.\"/root\"]\ntrust_level = \"trusted\""), 0o600)
	for i := 0; i < 2; i++ { // twice: the second time changes nothing
		if err := trustCodexDir("/w/a\"b"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(codex, "config.toml"))
	text := string(b)
	if !strings.Contains(text, "personality = \"pragmatic\"") || !strings.Contains(text, `[projects."/root"]`) {
		t.Fatalf("existing settings were changed:\n%s", text)
	}
	if strings.Count(text, `[projects."/w/a\"b"]`) != 1 || !strings.Contains(text, "trust_level = \"trusted\"\n") {
		t.Fatalf("the directory was not trusted exactly once:\n%s", text)
	}
	if st, _ := os.Stat(filepath.Join(codex, "config.toml")); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

// ---- repo jobs ----

func TestARepoJobStartsItsOwnLeadInAWorktree(t *testing.T) {
	r := newRig(t)
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TMUX", "/tmp/tmux-1000/handloom,123,0")
	t.Setenv("TMUX_PANE", "%5")
	old := execFn
	execFn = func(string, []string, []string) error { return nil }
	t.Cleanup(func() { execFn = old })

	repo := t.TempDir()
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	head := run("rev-parse", "HEAD")

	spec := filepath.Join(t.TempDir(), "boss")
	os.MkdirAll(spec, 0o755)
	os.WriteFile(filepath.Join(spec, "profile.yaml"), []byte("name: boss\ntools:\n  allow: [read]\n"), 0o644)
	os.WriteFile(filepath.Join(spec, "PROMPT.md"), []byte("You plan; you do not code.\n"), 0o644)
	r.as(r.admin, "profile", "new", spec)

	out := r.as(r.human, "job", "new", "Fix the bug", "--repo", repo, "--verify", "./check.sh", "--device", "dev", "--lead-profile", "boss", "--body", "app.go is broken")
	if !strings.Contains(out, "Started job #1") || !strings.Contains(out, "[repo "+repo) || !strings.Contains(out, "lead is starting") {
		t.Fatalf("job new: %s", out)
	}
	s := waitSpawn(t, r, 1, "launching") // the rig's link has made the worktree; its fake tmux started nothing
	if s.Role != "lead" || s.Name != "lead-1" || s.Repo != repo {
		t.Fatalf("spawn: %+v", s)
	}
	work := filepath.Join(client.Home(), "work", "job-1", "lead-1")
	// "launching" means the link has claimed the request, not that it has finished making the worktree
	for i := 0; ; i++ {
		_, errGit := os.Stat(filepath.Join(work, ".git"))
		_, errScope := os.Stat(filepath.Join(client.Home(), "scopes", "lead-1.json"))
		if errGit == nil && errScope == nil {
			break
		}
		if i > 500 {
			t.Fatalf("the worktree was not made (git: %v, scope: %v)", errGit, errScope)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != "job/1/lead-1" {
		t.Fatalf("the lead's worktree is on %q", got)
	}

	t.Chdir(work)
	r.run("spawn-exec", "1")

	// The work directory remembers where it started.
	b, err := os.ReadFile(filepath.Join(work, ".handloom", "scope.json"))
	if err != nil || !strings.Contains(string(b), head) || !strings.Contains(string(b), repo) {
		t.Fatalf("scope.json: %v %s", err, b)
	}
	// It is the job's lead, and its instructions say so (they were written again after the hub made it one).
	md, _ := os.ReadFile(filepath.Join(work, "CLAUDE.md"))
	for _, want := range []string{"As lead you create tasks", "Your profile: boss", "You plan; you do not code."} {
		if !strings.Contains(string(md), want) {
			t.Errorf("CLAUDE.md lacks %q:\n%s", want, md)
		}
	}
	if j := r.as(r.human, "job", "show", "1"); !strings.Contains(j, "lead: lead-1") {
		t.Fatalf("job show: %s", j)
	}
	// Commits will say who made them.
	if os.Getenv("GIT_AUTHOR_NAME") != "lead-1" || os.Getenv("GIT_COMMITTER_EMAIL") != "lead-1@handloom.local" {
		t.Fatalf("git identity: %q %q", os.Getenv("GIT_AUTHOR_NAME"), os.Getenv("GIT_COMMITTER_EMAIL"))
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
