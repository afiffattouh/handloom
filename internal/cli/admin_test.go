package cli

import (
	"bytes"
	"encoding/json"
	"handloom/internal/api"
	"handloom/internal/client"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheOwnersSettingsAreCommands(t *testing.T) {
	r := newRig(t)
	// prices
	if out := r.as(r.admin, "prices"); !strings.Contains(out, "No prices yet") {
		t.Fatalf("prices (none): %s", out)
	}
	r.as(r.admin, "prices", "set", "claude-sonnet", "--input", "3", "--output", "15", "--cache-read", "0.3")
	if out := r.as(r.admin, "prices"); !strings.Contains(out, "claude-sonnet") || !strings.Contains(out, "out 15") {
		t.Fatalf("prices: %s", out)
	}
	r.as(r.admin, "prices", "remove", "claude-sonnet")
	if out := r.as(r.admin, "prices"); !strings.Contains(out, "No prices yet") {
		t.Fatalf("after remove: %s", out)
	}
	if code, _, errs := r.exec("prices", "set", "x"); code == 0 || errs == "" {
		t.Fatal("a price with no numbers should be refused")
	}
	// people
	out := r.as(r.admin, "people", "add", "mia", "--role", "viewer")
	if !strings.Contains(out, "Added mia (viewer)") || !strings.Contains(out, "/invite/") {
		t.Fatalf("people add: %s", out)
	}
	r.as(r.admin, "people", "role", "mia", "--role", "member")
	if out := r.as(r.admin, "people"); !strings.Contains(out, "mia") || !strings.Contains(out, "member") || !strings.Contains(out, "has not set a password yet") {
		t.Fatalf("people: %s", out)
	}
	if out := r.as(r.admin, "people", "invite", "mia"); !strings.Contains(out, "New invite for mia") {
		t.Fatalf("people invite: %s", out)
	}
	// notifications
	r.as(r.admin, "notifications", "set", "--url", "https://ntfy.example.org", "--topic", "a-long-random-topic")
	if out := r.as(r.admin, "notifications"); !strings.Contains(out, "ntfy.example.org") || !strings.Contains(out, "a-long-random-topic") {
		t.Fatalf("notifications: %s", out)
	}
	// metrics and the personal token
	if out := r.as(r.admin, "metrics", "--range", "24h"); !strings.Contains(out, "Last 24h") || !strings.Contains(out, "usage") {
		t.Fatalf("metrics: %s", out)
	}
	out = r.as(r.human, "token", "new")
	if !strings.Contains(out, "New token for afif") || !strings.Contains(out, "hvh_") {
		t.Fatalf("token new: %s", out)
	}
	// the old token no longer works
	r.t.Setenv("HANDLOOM_HUB", r.hubURL)
	r.t.Setenv("HANDLOOM_TOKEN", r.human)
	if code, _, _ := r.exec("metrics"); code == 0 {
		t.Fatal("the replaced token still works")
	}
	r.t.Setenv("HANDLOOM_TOKEN", "")
}

func TestAProfileInPlainWords(t *testing.T) {
	r := newRig(t)
	out := r.as(r.admin, "profile", "make", "scout", "--can", "look", "--kind", "pi", "--runtime", "local", "--model", "gb10/qwen3.8-27b", "--prompt", "Find things.")
	if !strings.Contains(out, "scout: version 1") || !strings.Contains(out, "adapted: Pi always has a shell") {
		t.Fatalf("make: %s", out)
	}
	out = r.as(r.admin, "profile", "make", "builder", "--can", "build", "--careful", "--kind", "pi", "--runtime", "local", "--model", "m")
	if !strings.Contains(out, "adapted: Pi cannot refuse specific commands") {
		t.Fatalf("careful on pi: %s", out)
	}
	if code, _, errs := r.exec("profile", "make", "x", "--can", "fly", "--kind", "claude", "--runtime", "cloud"); code == 0 || !strings.Contains(errs, "--can is one of") {
		t.Fatalf("a bad --can: %d %s", code, errs)
	}
	if show := r.as(r.admin, "profile", "show", "builder"); !strings.Contains(show, "asked in its instructions") && !strings.Contains(show, "rm, sudo") {
		t.Fatalf("show: %s", show)
	}
}

func TestLoginSavesAPersonsTokenForTheConsoleOnly(t *testing.T) {
	r := newRig(t)
	file := filepath.Join(t.TempDir(), "cfg", "human.env")
	t.Setenv("HANDLOOM_HUMAN_ENV", file)
	// an admin token is not a person's
	t.Setenv("HANDLOOM_TOKEN", r.admin)
	if code, _, errs := r.exec("login", r.hubURL); code == 0 || !strings.Contains(errs, "needs a person's token") {
		t.Fatalf("admin token: %d %s", code, errs)
	}
	t.Setenv("HANDLOOM_TOKEN", "hvh_nonsense")
	if code, _, errs := r.exec("login", r.hubURL); code == 0 || !strings.Contains(errs, "did not accept") {
		t.Fatalf("bad token: %d %s", code, errs)
	}
	t.Setenv("HANDLOOM_TOKEN", r.human)
	out := r.run("login", r.hubURL)
	if !strings.Contains(out, "Signed in to "+r.hubURL+" as afif") {
		t.Fatalf("login: %s", out)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", st, err)
	}
	// the console finds it with nothing in the environment; an agent's shell does not
	t.Setenv("HANDLOOM_TOKEN", "")
	t.Setenv("HANDLOOM_HUB", "")
	if hub, tok := consoleLogin(); hub != r.hubURL || tok != r.human {
		t.Fatalf("console login: %q %q", hub, tok)
	}
	t.Setenv("HANDLOOM_AGENT", "worker-1")
	if hub, tok := consoleLogin(); hub != "" || tok != "" {
		t.Fatalf("an agent's shell picked up a person's saved token: %q %q", hub, tok)
	}
	t.Setenv("HANDLOOM_AGENT", "")
	r.run("logout")
	if _, err := os.Stat(file); err == nil {
		t.Fatal("logout left the file")
	}
}

func TestUninstallRemovesOnlyWhatItShouldAndSaysSoFirst(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "handloom")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("x"), 0o755)
	os.Symlink("handloom", filepath.Join(dir, "bin", "hl"))
	other := filepath.Join(dir, "bin", "other-tool")
	os.WriteFile(other, []byte("keep"), 0o755)
	home := filepath.Join(dir, "cfg", "handloom")
	os.MkdirAll(filepath.Join(home, "work"), 0o755)
	os.WriteFile(filepath.Join(home, "link.json"), []byte("{}"), 0o600)
	repo := filepath.Join(dir, "repo", ".handloom")
	os.MkdirAll(repo, 0o755)
	t.Setenv("HANDLOOM_HOME", home)
	// never let a test touch the real machine's tmux sessions or services: no tmux, no systemctl on this PATH
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	e := &env{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	plain := e.uninstallPlan(exe, home, false)
	var text []string
	for _, s := range plain {
		text = append(text, s.what)
	}
	if strings.Contains(strings.Join(text, "\n"), home) {
		t.Fatalf("a plain uninstall must not touch the link folder: %v", text)
	}
	for _, s := range plain {
		s.do()
	}
	if _, err := os.Stat(exe); err == nil {
		t.Fatal("the program was not removed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "bin", "hl")); err == nil {
		t.Fatal("the hl link was not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("another program in the same folder was removed")
	}
	if _, err := os.Stat(filepath.Join(home, "link.json")); err != nil {
		t.Fatal("the credential was removed without --purge")
	}
	purged := e.uninstallPlan(exe, home, true)
	for _, s := range purged {
		s.do()
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("--purge left the link folder")
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatal("a project's .handloom folder was removed")
	}
	// not a folder it made: never deleted
	for _, bad := range []string{"", "/", t.TempDir()} {
		if safeToPurge(bad) {
			t.Fatalf("safeToPurge(%q)", bad)
		}
	}
	// a package manager's files are left to the package manager
	if got := managedBy("/usr/bin/handloom"); !strings.Contains(got, "apt remove") {
		t.Fatalf("managedBy: %q", got)
	}
	if managedBy("/usr/local/bin/handloom") != "" || managedBy("/home/u/.local/bin/handloom") != "" {
		t.Fatal("a script install is ours to remove")
	}
}

func TestUninstallNeedsYesWithoutATerminalAndDryRunChangesNothing(t *testing.T) {
	t.Setenv("HANDLOOM_HOME", t.TempDir()+"/none")
	var out, errb bytes.Buffer
	if code := Main([]string{"uninstall", "--dry-run"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "dry run") {
		t.Fatalf("dry run: %d %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Main([]string{"uninstall"}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "--yes") {
		t.Fatalf("without a terminal or --yes: %d %s", code, errb.String())
	}
}

func TestHandoffNotesThroughTheCLI(t *testing.T) {
	r := newRig(t)
	r.run("register", "lead", "--kind", "shell")
	r.run("register", "--kind", "shell", "worker")
	r.run("register", "--kind", "shell", "worker2")
	r.as(r.admin, "agent", "role", "lead", "lead")
	r.agentOK("lead", "task", "create", "Migrate", "--assign", "worker")
	r.agentOK("worker", "task", "claim", "1")

	// a release without a note is refused in words
	if code, _, errs := r.agent("worker", "task", "release", "1"); code != 2 || !strings.Contains(errs, "handoff note") {
		t.Fatalf("release without a note: %d %s", code, errs)
	}
	if code, _, errs := r.agent("worker", "task", "submit", "1", "--evidence", "file:x"); code != 2 || !strings.Contains(errs, "note") {
		t.Fatalf("submit without a note: %d %s", code, errs)
	}
	out := r.agentOK("worker", "task", "handoff", "1", "--done", "tables created", "--tried", "one big ALTER: timed out", "--next", "backfill in batches", "--verify", "select count(*)")
	if !strings.Contains(out, "#1") {
		t.Fatalf("handoff: %s", out)
	}
	r.agentOK("worker", "task", "release", "1", "--done", "stopping for the day", "--next", "backfill in batches")

	// the next owner, in its own folder, is told what to do first and gets the note saved
	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, ".handloom"), 0o755)
	t.Chdir(work)
	r.agentOK("lead", "task", "assign", "1", "worker2")
	out = r.agentOK("worker2", "task", "claim", "1")
	for _, want := range []string{"started before", "check the git history", "Handoff note", "stopping for the day", "backfill in batches", "handoff.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("claim output lacks %q:\n%s", want, out)
		}
	}
	b, err := os.ReadFile(filepath.Join(work, ".handloom", "handoff.md"))
	if err != nil || !strings.Contains(string(b), "Handoff for task #1: Migrate") || !strings.Contains(string(b), "backfill in batches") {
		t.Fatalf("saved handoff: %v %s", err, b)
	}
	if show := r.agentOK("lead", "task", "show", "1"); !strings.Contains(show, "Handoff note") || !strings.Contains(show, "stopping for the day") {
		t.Fatalf("show: %s", show)
	}
	r.agentOK("worker2", "task", "submit", "1", "--evidence", "test:go test -> ok", "--note", "backfilled", "--how-to-check", "go test ./migrate")
}

func TestJoinInOneCommand(t *testing.T) {
	r := newRig(t)
	out := r.as(r.admin, "device", "add", "second", "--json")
	var tok api.TokenResp
	json.Unmarshal([]byte(out), &tok)
	t.Setenv("HANDLOOM_HOME", filepath.Join(t.TempDir(), "other-machine")) // another machine, with its own folder
	got := r.run("join", r.hubURL+"/", tok.Token, "--no-start")
	if !strings.Contains(got, "joined as second") || !strings.Contains(got, "Not started") {
		t.Fatalf("join: %s", got)
	}
	cfg, err := client.LoadLinkConfig()
	if err != nil || cfg.Device != "second" || cfg.Hub != r.hubURL {
		t.Fatalf("saved: %+v %v", cfg, err)
	}
	// running it again is safe: the credential is kept, the used token is not needed
	again := r.run("join", r.hubURL, tok.Token, "--no-start")
	if !strings.Contains(again, "already joined as second") {
		t.Fatalf("second run: %s", again)
	}
	// another hub: refused in words, nothing overwritten
	code, _, errs := r.exec("join", "http://127.0.0.1:9", "hvj_x", "--no-start")
	if code == 0 || !strings.Contains(errs, "already joined") || !strings.Contains(errs, "uninstall --purge") {
		t.Fatalf("a second hub: %d %s", code, errs)
	}
	if cfg2, _ := client.LoadLinkConfig(); cfg2.Hub != r.hubURL {
		t.Fatal("the saved hub was overwritten")
	}
	// a bad token on a fresh machine says what to do
	t.Setenv("HANDLOOM_HOME", filepath.Join(t.TempDir(), "third"))
	code, _, errs = r.exec("join", r.hubURL, "hvj_nonsense", "--no-start")
	if code == 0 || !strings.Contains(errs, "Add a machine") {
		t.Fatalf("a bad token: %d %s", code, errs)
	}
	if hint := installHint("tmux"); !strings.Contains(hint, "tmux") {
		t.Fatalf("hint: %q", hint)
	}
}

func TestSearchNotesOnThisMachineAndEarlierJobsOnTheHub(t *testing.T) {
	r := newRig(t)
	notes := t.TempDir()
	os.MkdirAll(filepath.Join(notes, "clients"), 0o755)
	os.WriteFile(filepath.Join(notes, "clients", "acme.md"), []byte("# Acme\n\nInvoice prefix: ACM. See [[terms]].\n"), 0o644)
	os.WriteFile(filepath.Join(notes, "clients", "terms.md"), []byte("# Terms\n\nNet 30.\n"), 0o644)
	r.as(r.human, "job", "new", "Invoice numbers", "--body", "Acme wants the ACM prefix on every invoice number")

	out := r.as(r.human, "search", "invoice", "prefix", "--knowledge", notes)
	for _, want := range []string{"Notes in " + notes, "clients/acme.md", "Invoice prefix: ACM", "clients/terms.md", "(linked from a match)", "Earlier jobs (on the hub)", "[job]", "Invoice numbers", "used:"} {
		if !strings.Contains(out, want) {
			t.Errorf("search output lacks %q:\n%s", want, out)
		}
	}
	// only one kind
	if only := r.as(r.human, "search", "prefix", "--notes", "--knowledge", notes); strings.Contains(only, "Earlier jobs") || !strings.Contains(only, "acme.md") {
		t.Fatalf("--notes: %s", only)
	}
	if only := r.as(r.human, "search", "prefix", "--jobs"); strings.Contains(only, "Notes in") || !strings.Contains(only, "[job]") {
		t.Fatalf("--jobs: %s", only)
	}
	// from a folder with no notes above it, it says where to point it
	here := t.TempDir()
	t.Chdir(here)
	if out := r.as(r.human, "search", "prefix"); !strings.Contains(out, "No knowledge repository here") || !strings.Contains(out, "[job]") {
		t.Fatalf("without notes: %s", out)
	}
	// an agent's folder has them at .handloom/knowledge, found from a subfolder
	os.MkdirAll(filepath.Join(here, ".handloom", "knowledge"), 0o755)
	os.WriteFile(filepath.Join(here, ".handloom", "knowledge", "acme.md"), []byte("# Acme\n\nBilling contact is Dana.\n"), 0o644)
	os.MkdirAll(filepath.Join(here, "src", "deep"), 0o755)
	t.Chdir(filepath.Join(here, "src", "deep"))
	if out := r.as(r.human, "search", "billing", "contact", "--notes"); !strings.Contains(out, "acme.md") || !strings.Contains(out, "Dana") {
		t.Fatalf("found from a subfolder: %s", out)
	}
	// a bad query and --json
	if code, _, errs := r.exec("search", "the"); code != 2 || !strings.Contains(errs, "usage") {
		t.Fatalf("an empty query: %d %s", code, errs)
	}
	var res struct {
		Notes []struct{ Path string }
		Jobs  []struct{ Kind string }
	}
	if err := json.Unmarshal([]byte(r.as(r.human, "search", "prefix", "--knowledge", notes, "--json")), &res); err != nil || len(res.Notes) == 0 || len(res.Jobs) == 0 {
		t.Fatalf("json: %v %+v", err, res)
	}
}
