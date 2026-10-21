package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"handloom/internal/addon"
	"handloom/internal/client"
)

// doctor says whether this machine is ready to run agents for a hub, and what
// to do about each thing that is not. It changes nothing.
//
// Hard problems (the link, git, tmux, no agent CLI at all) make it exit 1.
// An agent CLI that is missing or not logged in is only reported: a machine
// needs one working CLI, not all five.

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// agentCLIs is every agent command handloom has an adapter for, with the file
// whose presence says the CLI is logged in (empty: handloom cannot tell).
var agentCLIs = []struct{ bin, login, loginHint string }{
	{"claude", ".claude/.credentials.json", "run claude and log in"},
	{"codex", ".codex/auth.json", "run codex login"},
	{"pi", "", ""},
	{"omp", "", ""},
}

// fixerFor is replaceable in tests.
var fixerFor = func(e *env) *fixer { return e.newFixer() }

func (e *env) doctor(args []string) error {
	fs := e.flags("doctor")
	fix := fs.Bool("fix", false, "install missing git and tmux with this machine's package manager (asks first), and say how to get an agent CLI")
	if _, err := fs.need(args, 0, 0, "doctor [--fix]"); err != nil {
		return err
	}
	checks := e.runChecks()
	if *fix && !e.json {
		e.fixChecks(checks)
		checks = e.runChecks()
	}
	failed := notReadyCount(checks) > 0
	e.print(checks, func() { fmt.Fprint(e.out, renderDoctor(checks, installHint)) })
	if failed {
		return fmt.Errorf("this machine is not ready")
	}
	return nil
}

func (e *env) runChecks() []doctorCheck {
	checks := runDoctor(exec.LookPath, func() (map[string]any, error) {
		var st map[string]any
		err := client.Socket(client.SocketPath(), "").Get("/local/status", &st)
		return st, err
	}, os.UserHomeDir, os.Stat)
	checks = append(checks, addonChecks()...)
	for i := range checks {
		if checks[i].Name == "link" && checks[i].Status == "fail" {
			if _, err := client.LoadLinkConfig(); err == nil {
				checks[i].Fix = "start it: handloom link install (or handloom link start where this machine has no systemd)"
			}
		}
	}
	return checks
}

// addonPrefix starts the name of every add-on line, so the answer can show them apart from the rest.
const addonPrefix = "add-on "

// addonChecks is one line for each add-on installed here: ready, or what is wrong with it. A problem is a
// warning, not a failure: the computer can still run helpers without the add-on. Nothing is started to
// ask it; `handloom addon doctor` does that.
func addonChecks() []doctorCheck {
	root := addonRoot()
	list, err := addon.Installed(root, addon.NewStore(root))
	if err != nil {
		return []doctorCheck{{addonPrefix + "list", "warn", "the add-ons on this computer could not be read: " + err.Error(), ""}}
	}
	var out []doctorCheck
	for _, i := range list {
		name := addonPrefix + i.Name
		if i.Version != "" {
			name += " " + i.Version
		}
		switch i.State {
		case "ready":
			out = append(out, doctorCheck{name, "ok", "ready", ""})
		case "missing":
			out = append(out, doctorCheck{name, "warn", "approved here, but its folder is gone: install it again or remove it", ""})
		default:
			out = append(out, doctorCheck{name, "warn", "changed since it was approved (" + i.Detail + "): approve it again with handloom addon add", ""})
		}
	}
	return out
}

// fixChecks installs git and tmux when they are missing and the person says yes (or prints the
// commands when there is no one to ask). Agent CLIs are never installed: renderDoctor says how to get one.
func (e *env) fixChecks(checks []doctorCheck) {
	var missing []string
	for _, c := range checks {
		if c.Status == "fail" && (c.Name == "git" || c.Name == "tmux") {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) == 0 {
		fmt.Fprintln(e.out, "git and tmux are already installed, so there is nothing for me to install.")
		return
	}
	if fixerFor(e).fixTools(missing) {
		fmt.Fprintln(e.out, "Installed. Checking again.")
	}
	fmt.Fprintln(e.out)
}

func notReadyCount(checks []doctorCheck) int {
	n := 0
	for _, c := range checks {
		if c.Status == "fail" {
			n++
		}
	}
	return n
}

// doctorSentence says what a failed check means, in one plain sentence.
var doctorSentence = map[string]string{
	"link":      "The link, the small program that carries messages between this machine and the hub, is not running.",
	"git":       "git is not installed. Handloom uses it to give every agent its own copy of your code to work in.",
	"tmux":      "tmux is not installed. Handloom uses it to start agents and type into them.",
	"agent CLI": "No agent program (such as Claude Code or Codex) is installed and signed in on this machine yet, so there is nothing to run jobs with.",
}

// renderDoctor is doctor's answer in plain words: a first line that says ready or not, then each thing
// to do as one sentence and the exact step. hint gives the install command for git or tmux.
func renderDoctor(checks []doctorCheck, hint func(tool string) string) string {
	var b strings.Builder
	n := notReadyCount(checks)
	if n == 0 {
		b.WriteString("Ready: this machine can run agents\n")
	} else {
		fmt.Fprintf(&b, "Not ready yet: %d %s to do\n", n, map[bool]string{true: "thing", false: "things"}[n == 1])
	}
	i := 0
	agentMissing := false
	for _, c := range checks {
		if c.Status != "fail" {
			continue
		}
		i++
		sentence := doctorSentence[c.Name]
		if sentence == "" {
			sentence = c.Name + ": " + c.Detail + "."
		}
		step := c.Fix
		switch c.Name {
		case "git", "tmux":
			step = hint(c.Name)
		case "agent CLI":
			step = "install one agent program and sign in to it (the lines are below)"
			agentMissing = true
		}
		fmt.Fprintf(&b, "\n%d. %s\n   Do this: %s\n", i, sentence, step)
	}
	var notes []string
	for _, c := range checks {
		if c.Status == "warn" && c.Fix != "" && !strings.HasPrefix(c.Name, addonPrefix) {
			notes = append(notes, fmt.Sprintf("   %s is installed but not signed in. Do this: %s", c.Name, c.Fix))
		}
	}
	if len(notes) > 0 {
		b.WriteString("\nAlso:\n" + strings.Join(notes, "\n") + "\n")
	}
	var addons []string
	for _, c := range checks {
		if strings.HasPrefix(c.Name, addonPrefix) {
			addons = append(addons, "   "+strings.TrimPrefix(c.Name, addonPrefix)+": "+c.Detail)
		}
	}
	if len(addons) > 0 {
		b.WriteString("\nAdd-ons:\n" + strings.Join(addons, "\n") + "\n")
	}
	if agentMissing {
		b.WriteString("\n" + agentHelpText())
	}
	if n > 0 {
		b.WriteString("\nWhen you have done these, run `handloom doctor` again. `handloom doctor --fix` can do the git and tmux part for you.\n")
	}
	return b.String()
}

func runDoctor(look func(string) (string, error), linkStatus func() (map[string]any, error),
	home func() (string, error), stat func(string) (os.FileInfo, error)) []doctorCheck {
	var out []doctorCheck
	add := func(name, status, detail, fix string) { out = append(out, doctorCheck{name, status, detail, fix}) }

	if st, err := linkStatus(); err != nil {
		add("link", "fail", "the link is not running on this machine ("+client.SocketPath()+")",
			"paste the join line from the web page (Machines, Add a machine) on this machine")
	} else {
		add("link", "ok", fmt.Sprintf("running: device %v, hub %v", st["device"], st["hub"]), "")
	}
	for _, t := range []struct{ bin, fix string }{
		{"git", "install git: jobs on a repository give every agent its own worktree"},
		{"tmux", "install tmux: agents run in tmux windows the link opens and types into"},
	} {
		if p, err := look(t.bin); err != nil {
			add(t.bin, "fail", "not found", t.fix)
		} else {
			add(t.bin, "ok", p, "")
		}
	}
	h, herr := home()
	ready := 0
	for _, c := range agentCLIs {
		p, err := look(c.bin)
		if err != nil {
			add(c.bin, "warn", "not installed (fine if you do not use it)", "")
			continue
		}
		switch {
		case c.login == "" || herr != nil:
			add(c.bin, "ok", p+" (login not checked)", "")
			ready++
		default:
			if _, err := stat(filepath.Join(h, c.login)); err != nil && !loggedInElsewhere(c.bin, h, stat) {
				add(c.bin, "warn", p+", but no login found", c.loginHint)
			} else {
				add(c.bin, "ok", p+", logged in", "")
				ready++
			}
		}
	}
	if ready == 0 {
		add("agent CLI", "fail", "no agent CLI that is installed and logged in", "install and log in to at least one of: claude, codex, pi, omp")
	}
	return out
}

// loggedInElsewhere covers Claude Code, which keeps its account in
// ~/.claude.json on some installs instead of a credentials file.
func loggedInElsewhere(bin, home string, stat func(string) (os.FileInfo, error)) bool {
	if bin != "claude" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return false
	}
	var d struct {
		OAuth json.RawMessage `json:"oauthAccount"`
	}
	return json.Unmarshal(b, &d) == nil && len(strings.TrimSpace(string(d.OAuth))) > 4
}
