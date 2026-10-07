package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	{"opencode", "", ""},
}

func (e *env) doctor(args []string) error {
	fs := e.flags("doctor")
	if _, err := fs.need(args, 0, 0, "doctor"); err != nil {
		return err
	}
	checks := runDoctor(exec.LookPath, func() (map[string]any, error) {
		var st map[string]any
		err := client.Socket(client.SocketPath(), "").Get("/local/status", &st)
		return st, err
	}, os.UserHomeDir, os.Stat)
	failed := false
	for _, c := range checks {
		failed = failed || c.Status == "fail"
	}
	e.print(checks, func() {
		for _, c := range checks {
			mark := map[string]string{"ok": "ok  ", "warn": "warn", "fail": "FAIL"}[c.Status]
			fmt.Fprintf(e.out, "%s  %-18s %s\n", mark, c.Name, c.Detail)
			if c.Fix != "" && c.Status != "ok" {
				fmt.Fprintf(e.out, "      %-18s fix: %s\n", "", c.Fix)
			}
		}
		if failed {
			fmt.Fprintln(e.out, "\nThis machine is not ready yet.")
		} else {
			fmt.Fprintln(e.out, "\nThis machine is ready.")
		}
	})
	if failed {
		return fmt.Errorf("this machine is not ready")
	}
	return nil
}

func runDoctor(look func(string) (string, error), linkStatus func() (map[string]any, error),
	home func() (string, error), stat func(string) (os.FileInfo, error)) []doctorCheck {
	var out []doctorCheck
	add := func(name, status, detail, fix string) { out = append(out, doctorCheck{name, status, detail, fix}) }

	if st, err := linkStatus(); err != nil {
		add("link", "fail", "the link is not running on this machine ("+client.SocketPath()+")",
			"join this machine: handloom link join <hub-url> <join-token> (the Devices page makes the command), then handloom link install")
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
		add("agent CLI", "fail", "no agent CLI that is installed and logged in", "install and log in to at least one of: claude, codex, pi, omp, opencode")
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
