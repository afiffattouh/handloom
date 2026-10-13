package cli

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// macOS keeps a program running with a launchd LaunchAgent: a plist in ~/Library/LaunchAgents that
// starts at login and is restarted if it stops. It runs as the person, so it can reach their
// agent CLIs and logins.

// launchdLabel is the agent's name: handloom-link becomes ai.handloom.link.
func launchdLabel(name string) string { return "ai.handloom." + strings.TrimPrefix(name, "handloom-") }

func launchdPlistPath(home, name string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel(name)+".plist")
}

func launchdLogPath(home, name string) string {
	return filepath.Join(home, "Library", "Logs", name+".log")
}

// launchdMinimalPath is what launchd gives a program: almost nothing. Homebrew's folders (Apple
// silicon and Intel) are added so tmux, git and gh are found.
const launchdHomebrewPath = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin"

// launchdPath is the PATH the agent runs with: the program's own folder, Homebrew's folders, the PATH
// of the shell that installed it (so agent CLIs are found where they were), and the system folders,
// each folder once.
func launchdPath(binDir, installerPath string) string {
	seen := map[string]bool{}
	var out []string
	add := func(list string) {
		for _, d := range strings.Split(list, ":") {
			if d != "" && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	add(binDir)
	add(launchdHomebrewPath)
	add(installerPath)
	add("/usr/bin:/bin:/usr/sbin:/sbin")
	return strings.Join(out, ":")
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// plistText is the LaunchAgent file. Pure: everything it needs is passed in.
func (s service) plistText(bin, home, path, logFile string) string {
	var b strings.Builder
	str := func(indent, v string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, xmlText(v)) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	cmd := strings.TrimPrefix(s.name, "handloom-")
	fmt.Fprintf(&b, "<!-- Written by `handloom %s install`. Remove with `handloom %s uninstall`. -->\n", cmd, cmd)
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n")
	str("\t", launchdLabel(s.name))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{bin}, s.args...) {
		str("\t\t", a)
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	env := map[string]string{"PATH": path, "HOME": home}
	for k, v := range s.env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n", xmlText(k))
		str("\t\t", env[k])
	}
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>WorkingDirectory</key>\n")
	str("\t", home)
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>3</integer>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n")
	str("\t", logFile)
	b.WriteString("\t<key>StandardErrorPath</key>\n")
	str("\t", logFile)
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func (e *env) installLaunchd(s service) error {
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
	plist := launchdPlistPath(home, s.name)
	logFile := launchdLogPath(home, s.name)
	for _, d := range []string{filepath.Dir(plist), filepath.Dir(logFile)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(plist, []byte(s.plistText(bin, home, launchdPath(filepath.Dir(bin), os.Getenv("PATH")), logFile)), 0o644); err != nil {
		return err
	}
	if err := launchdLoad(plist, launchdLabel(s.name)); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Installed and started %s (%s).\n  status: launchctl print gui/%d/%s\n  logs:   tail -f %s\n",
		s.name, plist, getuid(), launchdLabel(s.name), logFile)
	return nil
}

// launchdLoad starts the agent, replacing one that is already loaded. `bootstrap` is the current
// command; older macOS versions only have `load`.
func launchdLoad(plist, label string) error {
	domain := fmt.Sprintf("gui/%d", getuid())
	runCmd("launchctl", "bootout", domain+"/"+label) // best effort: it may not be loaded
	out, err := runCmd("launchctl", "bootstrap", domain, plist)
	if err == nil {
		return nil
	}
	if out2, err2 := runCmd("launchctl", "load", "-w", plist); err2 != nil {
		return fmt.Errorf("launchctl could not start %s: bootstrap: %v: %s; load: %v: %s\n(it needs a logged-in desktop session; from ssh, log in on the Mac once, or run `handloom link run` yourself)",
			label, err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
	}
	return nil
}

func (e *env) uninstallLaunchd(name string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plist := launchdPlistPath(home, name)
	if _, err := os.Stat(plist); err != nil {
		fmt.Fprintf(e.out, "%s is not installed (%s).\n", name, plist)
		return nil
	}
	label := launchdLabel(name)
	if _, err := runCmd("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", getuid(), label)); err != nil {
		runCmd("launchctl", "unload", "-w", plist) // best effort: an older macOS, or it is not loaded
	}
	if err := os.Remove(plist); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Stopped and removed %s (%s).\n", name, plist)
	return nil
}
