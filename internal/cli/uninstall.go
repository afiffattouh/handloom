package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"

	"handloom/internal/client"
)

// uninstall removes what Handloom put on this machine, and only that: the link
// and hub services, the program, and (with --purge) the link's own folder and
// the tmux server it started agents in. It never touches a hub's data, your
// repositories, or the .handloom folders that adapters wrote into projects.
//
//	handloom uninstall               services and the program
//	handloom uninstall --purge       also the credential, work trees and logs, and the agents' tmux server
//	handloom uninstall --dry-run     say what it would do, change nothing
func (e *env) uninstall(args []string) error {
	fs := e.flags("uninstall")
	purge := fs.Bool("purge", false, "also remove the link's folder (its credential, work trees, logs) and stop the agents' tmux server")
	yes := fs.Bool("yes", false, "do not ask")
	dry := fs.Bool("dry-run", false, "say what would be done and change nothing")
	if _, err := fs.need(args, 0, 0, "uninstall [--purge] [--yes] [--dry-run]"); err != nil {
		return err
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	home := client.Home()
	steps := e.uninstallPlan(exe, home, *purge)
	if len(steps) == 0 {
		fmt.Fprintln(e.out, "Nothing to remove.")
		return nil
	}
	fmt.Fprintln(e.out, "This will:")
	for _, s := range steps {
		fmt.Fprintf(e.out, "  - %s\n", s.what)
	}
	if *dry {
		fmt.Fprintln(e.out, "(dry run: nothing was changed)")
		return nil
	}
	if !*yes {
		if !isatty.IsTerminal(os.Stdin.Fd()) {
			return fmt.Errorf("not a terminal: add --yes to go ahead, or --dry-run to look first")
		}
		fmt.Fprint(e.err, "Go ahead? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(e.out, "Nothing was changed.")
			return nil
		}
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			fmt.Fprintf(e.err, "  failed: %s: %v\n", s.what, err)
			continue
		}
		fmt.Fprintf(e.out, "  done: %s\n", s.what)
	}
	fmt.Fprintln(e.out, "\nLeft alone on purpose: a hub's data folder and backups, your repositories, and the .handloom folders agents' adapters wrote into projects.")
	fmt.Fprintln(e.out, "If this machine was joined to a hub, revoke it there too (web UI: Machines, Revoke) so its credential stops working.")
	return nil
}

type uninstallStep struct {
	what string
	do   func() error
}

// uninstallPlan lists what would be removed. exe is the running program, home
// the link's folder.
func (e *env) uninstallPlan(exe, home string, purge bool) []uninstallStep {
	var steps []uninstallStep
	for _, name := range []string{"handloom-link", "handloom-hub"} {
		name := name
		if path, _, err := unitPath(name); err == nil {
			if _, err := os.Stat(path); err == nil {
				steps = append(steps, uninstallStep{"stop and remove the " + name + " service (" + path + ")", func() error {
					saved := e.out
					defer func() { e.out = saved }()
					e.out = discard{}
					return e.uninstallService(name)
				}})
			}
		}
	}
	if out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output(); err == nil && strings.Contains("\n"+string(out), "\nhandloom-link\n") {
		steps = append(steps, uninstallStep{"stop the link running in the tmux session handloom-link", func() error {
			return exec.Command("tmux", "kill-session", "-t", "handloom-link").Run()
		}})
	}
	if purge {
		socket := "handloom"
		if out, err := exec.Command("tmux", "-L", socket, "list-sessions").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
			n := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
			steps = append(steps, uninstallStep{fmt.Sprintf("stop the agents' tmux server (tmux -L %s, %d session(s)): agents running there end", socket, n), func() error {
				return exec.Command("tmux", "-L", socket, "kill-server").Run()
			}})
		}
		if safeToPurge(home) {
			steps = append(steps, uninstallStep{"delete " + home + " (the link's credential, work trees, logs and your saved console sign-in)", func() error { return os.RemoveAll(home) }})
		}
	}
	if advice := managedBy(exe); advice != "" {
		steps = append(steps, uninstallStep{"leave the program in place (" + exe + "): " + advice, func() error { return nil }})
	} else if exe != "" {
		dir := filepath.Dir(exe)
		hl := filepath.Join(dir, "hl")
		if target, err := os.Readlink(hl); err == nil && target == filepath.Base(exe) {
			steps = append(steps, uninstallStep{"remove " + hl, func() error { return os.Remove(hl) }})
		}
		steps = append(steps, uninstallStep{"remove the program " + exe, func() error { return os.Remove(exe) }})
	}
	return steps
}

// safeToPurge refuses to delete anything that does not look like the link's folder.
func safeToPurge(home string) bool {
	if home == "" || home == "/" || home == "." {
		return false
	}
	if h, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == filepath.Clean(h) {
		return false
	}
	if _, err := os.Stat(home); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(home, "link.json")); err == nil {
		return true
	}
	return filepath.Base(home) == "handloom"
}

// managedBy says how to remove a program that a package manager owns.
func managedBy(exe string) string {
	switch {
	case strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/homebrew/"):
		return "installed by Homebrew: run `brew uninstall handloom`"
	case strings.Contains(exe, "/nix/store/"):
		return "installed by Nix: remove it with Nix"
	case strings.HasPrefix(exe, "/usr/bin/") || strings.HasPrefix(exe, "/bin/"):
		return "installed by a package: run `apt remove handloom` or `dnf remove handloom`"
	}
	return ""
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
