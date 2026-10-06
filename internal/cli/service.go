package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// service is one handloom daemon run by systemd.
type service struct {
	name        string // unit name without ".service"
	description string
	args        []string // arguments after the binary
	env         map[string]string
}

// unitText is the systemd unit. A root install is a system unit; anyone
// else gets a user unit. PATH is copied from the installing shell, because
// the link starts agent CLIs for headless turns and must find them.
func (s service) unitText(bin, home, path string, system bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by `handloom %s install`. Remove with `handloom %s uninstall`.\n", strings.TrimPrefix(s.name, "handloom-"), strings.TrimPrefix(s.name, "handloom-"))
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nAfter=network-online.target tailscaled.service\nWants=network-online.target\n\n", s.description)
	fmt.Fprintf(&b, "[Service]\nExecStart=%s\nRestart=always\nRestartSec=3\n", systemdQuote(append([]string{bin}, s.args...)))
	fmt.Fprintf(&b, "WorkingDirectory=%s\nEnvironment=HOME=%s\nEnvironment=%s\n", home, home, systemdQuote([]string{"PATH=" + filepath.Dir(bin) + ":" + path}))
	for _, k := range sortedKeys(s.env) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote([]string{k + "=" + s.env[k]}))
	}
	target := "default.target"
	if system {
		target = "multi-user.target"
	}
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", target)
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// systemdQuote quotes words for a unit file line.
func systemdQuote(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if strings.ContainsAny(w, " \t\"'\\$%") {
			w = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`).Replace(w)
			w = `"` + w + `"`
		}
		out[i] = w
	}
	return strings.Join(out, " ")
}

// unitPath says where the unit goes and whether it is a system unit.
func unitPath(name string) (path string, system bool, err error) {
	if os.Geteuid() == 0 {
		return "/etc/systemd/system/" + name + ".service", true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	return filepath.Join(home, ".config", "systemd", "user", name+".service"), false, nil
}

func systemctl(system bool, args ...string) error {
	if !system {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// install writes the unit and starts the service now and at every boot.
func (e *env) installService(s service) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("no systemd on this machine: keep `handloom %s` running another way, for example in tmux",
			strings.Join(s.args, " "))
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path, system, err := unitPath(s.name)
	if err != nil {
		return err
	}
	// A service installed under the old name (handloom-hub, handloom-link) would
	// fight the new one for the port and the socket: replace it.
	if err := e.removeLegacyService(s.name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(s.unitText(bin, home, os.Getenv("PATH"), system)), 0o644); err != nil {
		return err
	}
	if err := systemctl(system, "daemon-reload"); err != nil {
		return err
	}
	if err := systemctl(system, "enable", s.name); err != nil {
		return err
	}
	if err := systemctl(system, "restart", s.name); err != nil {
		return err
	}
	scope := "systemctl"
	if !system {
		scope = "systemctl --user"
		// Without lingering a user service stops when the user logs out.
		if u, err := user.Current(); err == nil {
			if out, _ := exec.Command("loginctl", "show-user", u.Username, "-p", "Linger").Output(); !strings.Contains(string(out), "Linger=yes") {
				if exec.Command("loginctl", "enable-linger", u.Username).Run() != nil {
					fmt.Fprintf(e.out, "Note: run `sudo loginctl enable-linger %s`, or the service stops when you log out.\n", u.Username)
				}
			}
		}
	}
	fmt.Fprintf(e.out, "Installed and started %s (%s).\n  status: %s status %s\n  logs:   journalctl %s-u %s -f\n",
		s.name, path, scope, s.name, map[bool]string{true: "", false: "--user "}[system], s.name)
	return nil
}

// removeLegacyService stops and removes the unit the product installed under
// its old name, if there is one.
func (e *env) removeLegacyService(name string) error {
	old := strings.Replace(name, "handloom", "handloom", 1)
	if old == name {
		return nil
	}
	path, system, err := unitPath(old)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	systemctl(system, "disable", "--now", old)
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Replaced the old service %s (%s) by %s.\n", old, path, name)
	return systemctl(system, "daemon-reload")
}

func (e *env) uninstallService(name string) error {
	if err := e.removeLegacyService(name); err != nil {
		return err
	}
	path, system, err := unitPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(e.out, "%s is not installed (%s).\n", name, path)
		return nil
	}
	systemctl(system, "disable", "--now", name) // best effort: it may already be stopped
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := systemctl(system, "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Stopped and removed %s (%s).\n", name, path)
	return nil
}
