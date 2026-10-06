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
	s := service{name: "handloom-link", description: "handloom link (device daemon)", args: []string{"link", "run"},
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
	hub := service{name: "handloom-hub", description: "handloom hub", args: []string{"hub", "serve", "--data", "/var/lib/handloom", "--addr", "100.1.2.3:7420"}}
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
