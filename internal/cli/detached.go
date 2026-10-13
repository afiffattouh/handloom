package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"handloom/internal/client"
	"handloom/internal/setting"
)

// Where there is no service manager (Linux without systemd, WSL without systemd), the link is
// started detached from the terminal (its own session, output to a log, its process number in a
// file) and a short block in the login file starts it again at the next login. Without a service
// manager nothing else can start a program at boot, so this is as close as it gets.

const (
	profileBegin = "# >>> handloom link (added by `handloom link install`; `handloom link uninstall` removes it) >>>"
	profileEnd   = "# <<< handloom link <<<"
)

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// profileBlock is the guarded login line: it starts the link when it is not running.
func profileBlock(bin, home string, customHome bool) string {
	env := ""
	if customHome {
		env = "HANDLOOM_HOME=" + shQuote(home) + " "
	}
	return profileBegin + "\n[ -x " + shQuote(bin) + " ] && " + env + shQuote(bin) + " link start --quiet >/dev/null 2>&1\n" + profileEnd + "\n"
}

// withProfileBlock adds the block to a login file's text, or replaces the one that is there (a
// moved program), and says whether anything changed. Running it twice changes nothing.
func withProfileBlock(content, block string) (string, bool) {
	if strings.Contains(content, profileBegin) {
		out := replaceProfileBlock(content, block)
		return out, out != content
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block, true
}

// replaceProfileBlock swaps the block that is there for a new one, in place.
func replaceProfileBlock(content, block string) string {
	stripped, ok := withoutProfileBlock(content)
	if !ok {
		return content
	}
	i := strings.Index(content, profileBegin)
	return stripped[:i] + block + stripped[i:]
}

// withoutProfileBlock removes the block, leaving every other line as it was.
func withoutProfileBlock(content string) (string, bool) {
	i := strings.Index(content, profileBegin)
	if i < 0 {
		return content, false
	}
	j := strings.Index(content[i:], profileEnd)
	if j < 0 {
		return content, false
	}
	end := i + j + len(profileEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return content[:i] + content[end:], true
}

// profileFileFor picks the login file the person's shell reads: .zprofile for zsh; for bash,
// .bash_profile if there is one (bash then ignores .profile), else .profile.
func profileFileFor(shell, home string, exists func(string) bool) string {
	switch filepath.Base(shell) {
	case "zsh":
		return filepath.Join(home, ".zprofile")
	case "bash":
		if exists(filepath.Join(home, ".bash_profile")) {
			return filepath.Join(home, ".bash_profile")
		}
	}
	return filepath.Join(home, ".profile")
}

func editProfile(path string, change func(string) (string, bool)) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	out, changed := change(string(b))
	if !changed {
		return false, nil
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	return true, os.WriteFile(path, []byte(out), mode)
}

var selfPath = os.Executable

func linkPidFile() string { return filepath.Join(client.Home(), "link.pid") }
func linkLogFile() string { return filepath.Join(client.Home(), "link.log") }

var startDetachedFn = startDetached

// startDetached starts `handloom link run` in its own session and returns its process number.
func startDetached(bin, home string) (int, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(linkLogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(bin, "link", "run")
	cmd.Env = append(os.Environ(), "HANDLOOM_HOME="+home)
	cmd.Stdout, cmd.Stderr = logf, logf // stdin stays the null device
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid, os.WriteFile(linkPidFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// stopDetached stops the link that startDetached started, if its process number is still ours.
// It reports whether it stopped something.
func stopDetached() (bool, error) {
	b, err := os.ReadFile(linkPidFile())
	if err != nil {
		return false, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	defer os.Remove(linkPidFile())
	if err != nil || pid <= 1 {
		return false, nil
	}
	// the number may have been reused by something else since: only stop a handloom link
	out, err := runCmd("ps", "-o", "command=", "-p", strconv.Itoa(pid))
	if err != nil || !strings.Contains(string(out), "handloom") || !strings.Contains(string(out), "link") {
		return false, nil
	}
	if p, err := os.FindProcess(pid); err == nil && p.Signal(syscall.SIGTERM) == nil {
		return true, nil
	}
	return false, nil
}

// installDetached is the no-service way: start the link detached, and have the login file start
// it at the next login. It says what it did in plain words.
func (e *env) installDetached(why string) error {
	bin, err := selfPath()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	home, err := filepath.Abs(client.Home())
	if err != nil {
		return err
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if why != "" {
		e.say("%s", why)
	}
	if linkUp() {
		e.say("the link is already running")
	} else {
		pid, err := startDetachedFn(bin, home)
		if err != nil {
			return fmt.Errorf("could not start the link: %w", err)
		}
		for i := 0; i < 40 && !linkUp(); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if !linkUp() {
			return fmt.Errorf("the link was started (process %d) but did not come up; see its log: %s", pid, linkLogFile())
		}
		e.say("the link is running in the background (process %d); closing this terminal does not stop it", pid)
	}
	file := profileFileFor(os.Getenv("SHELL"), uh, func(p string) bool { _, err := os.Stat(p); return err == nil })
	block := profileBlock(bin, home, setting.Get("HOME") != "")
	changed, err := editProfile(file, func(c string) (string, bool) { return withProfileBlock(c, block) })
	if err != nil {
		e.say("could not add the start-at-login line to %s (%v); start the link yourself after a restart: handloom link start", file, err)
		return nil
	}
	if changed {
		e.say("added one line to %s so the link starts again the next time you log in", file)
	} else {
		e.say("%s already starts the link at login", file)
	}
	e.say("this machine has no service manager, so the link does not start by itself at boot, only at login. Remove all of this with: handloom link uninstall")
	return nil
}

// uninstallDetached stops a detached link and removes the login line from the files that could hold it.
func (e *env) uninstallDetached() error {
	uh, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	stopped, _ := stopDetached()
	if stopped {
		fmt.Fprintln(e.out, "Stopped the link that was running in the background.")
	}
	for _, f := range []string{".profile", ".bash_profile", ".zprofile"} {
		path := filepath.Join(uh, f)
		if changed, err := editProfile(path, withoutProfileBlock); err != nil {
			fmt.Fprintf(e.out, "Could not edit %s: %v\n", path, err)
		} else if changed {
			fmt.Fprintf(e.out, "Removed the start-at-login line from %s.\n", path)
		}
	}
	return nil
}

// linkStart makes sure the link is running, without a service: handloom link start [--quiet] [--wait].
// --wait stays until the link stops, which keeps a Windows session (WSL) open while the link works.
func (e *env) linkStart(args []string) error {
	fs := e.flags("link start")
	quiet := fs.Bool("quiet", false, "print nothing unless it fails")
	wait := fs.Bool("wait", false, "stay until the link stops")
	if _, err := fs.need(args, 0, 0, "link start [--quiet] [--wait]"); err != nil {
		return err
	}
	if _, err := client.LoadLinkConfig(); err != nil {
		return err
	}
	out := e.out
	if *quiet {
		e.out = discard{}
		defer func() { e.out = out }()
	}
	if !linkUp() {
		bin, err := selfPath()
		if err != nil {
			return err
		}
		home, err := filepath.Abs(client.Home())
		if err != nil {
			return err
		}
		pid, err := startDetachedFn(bin, home)
		if err != nil {
			return err
		}
		for i := 0; i < 40 && !linkUp(); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if !linkUp() {
			return fmt.Errorf("the link was started (process %d) but did not come up; see its log: %s", pid, linkLogFile())
		}
		e.say("started the link in the background (process %d)", pid)
	} else {
		e.say("the link is already running")
	}
	for *wait && linkUp() {
		time.Sleep(2 * time.Second)
	}
	return nil
}

// linkStop stops a link that `link start` or join started in the background.
func (e *env) linkStop(args []string) error {
	fs := e.flags("link stop")
	if _, err := fs.need(args, 0, 0, "link stop"); err != nil {
		return err
	}
	stopped, _ := stopDetached()
	if !stopped {
		fmt.Fprintln(e.out, "No background link of this program's own was found to stop (a link run by a service is stopped by the service: handloom link uninstall).")
		return nil
	}
	fmt.Fprintln(e.out, "Stopped the link.")
	return nil
}
