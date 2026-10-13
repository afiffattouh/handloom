package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

// joinHub exchanges a one-time join token for this machine's credential and saves it.
func (e *env) joinHub(hub, token string) (*client.LinkConfig, error) {
	hub = strings.TrimRight(hub, "/")
	var resp api.JoinResp
	if err := client.Direct(hub, "").Post("/v1/devices/join", api.JoinReq{JoinToken: token}, &resp); err != nil {
		return nil, err
	}
	cfg := &client.LinkConfig{Hub: hub, Device: resp.Device, Credential: resp.Credential}
	if err := client.SaveLinkConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// join does everything a machine needs to become part of a hub, in one command:
// join it, keep the link running (as a service, or in the background where there is no
// service manager), and check the machine, saying exactly what to fix.
//
//	handloom join <hub-url> <join-token> [--no-start] [--no-service]
func (e *env) join(args []string) error {
	fs := e.flags("join")
	noStart := fs.Bool("no-start", false, "only join: do not start the link or check the machine")
	noService := fs.Bool("no-service", false, "keep the link running in the background (and start it at login) instead of installing a service")
	pos, err := fs.need(args, 2, 2, "join <hub-url> <join-token> [--no-start] [--no-service]")
	if err != nil {
		return err
	}
	hub, token := strings.TrimRight(pos[0], "/"), pos[1]

	e.step("Joining %s", hub)
	if cfg, err := client.LoadLinkConfig(); err == nil {
		if cfg.Hub != hub {
			return fmt.Errorf("this machine is already joined to %s as %s. To move it, run `handloom uninstall --purge` first (or use another HANDLOOM_HOME for a second hub)", cfg.Hub, cfg.Device)
		}
		e.say("already joined as %s; keeping its credential", cfg.Device)
	} else {
		cfg, err := e.joinHub(hub, token)
		if err != nil {
			return fmt.Errorf("could not join: %w\n(a join token works once, for a short time: make a new one in the web UI, Machines, Add a machine)", err)
		}
		e.say("joined as %s", cfg.Device)
	}
	if *noStart {
		fmt.Fprintln(e.out, "Not started (--no-start). Start the link with: handloom link run (or handloom link install)")
		return nil
	}

	e.step("Starting the link")
	if linkUp() {
		e.say("the link is already running")
	} else if err := e.startLink(*noService); err != nil {
		return err
	}
	for i := 0; i < 40 && !linkUp(); i++ {
		time.Sleep(500 * time.Millisecond)
	}

	e.step("Checking this machine")
	fmt.Fprintln(e.out)
	checks := e.runChecks()
	if notReadyCount(checks) > 0 {
		fmt.Fprint(e.out, renderDoctor(checks, installHint))
		fmt.Fprintln(e.out)
		e.fixChecks(checks) // asks first on a terminal; otherwise it only prints the commands
		checks = e.runChecks()
	}
	if notReadyCount(checks) > 0 {
		fmt.Fprint(e.out, "\n"+renderDoctor(checks, installHint))
		fmt.Fprintln(e.out, "\nIt is joined and the link is running. Do what is listed above, then run `handloom doctor` again. The web UI shows this machine as soon as the link has called in.")
		return fmt.Errorf("this machine is not ready")
	}
	fmt.Fprint(e.out, renderDoctor(checks, installHint))
	fmt.Fprintln(e.out, "\nThe web UI now shows this machine as ready. Start a job on it from Jobs, New job.")
	return nil
}

func (e *env) step(format string, a ...any) { fmt.Fprintf(e.out, "\n== "+format+"\n", a...) }
func (e *env) say(format string, a ...any)  { fmt.Fprintf(e.out, "   "+format+"\n", a...) }

// linkUp is replaceable in tests.
var linkUp = linkRunning

func linkRunning() bool {
	var st map[string]any
	return client.Socket(client.SocketPath(), "").Get("/local/status", &st) == nil
}

// startLink keeps the link running: as a service when this machine has a service manager that works
// for this user (systemd on Linux, launchd on macOS), otherwise started in the background with a line
// in the login file that starts it again at login.
func (e *env) startLink(noService bool) error {
	home, err := filepath.Abs(client.Home())
	if err != nil {
		return err
	}
	if noService {
		err = fmt.Errorf("--no-service was given")
	} else if err = e.installServiceQuiet(service{
		name: "handloom-link", description: "Handloom link (device daemon)",
		args: []string{"link", "run"}, env: map[string]string{"HANDLOOM_HOME": home},
	}); err == nil {
		e.say("the link runs as a service and starts at %s", map[bool]string{true: "login", false: "boot"}[goos == "darwin"])
		return nil
	}
	why := "no service manager works here (" + firstLine(err.Error()) + "); keeping the link running in the background instead"
	if noService {
		why = "keeping the link running in the background, as asked"
	}
	return e.installDetached(why)
}

// installServiceQuiet is installService with its own chatter muted, so join can say it in its own words.
func (e *env) installServiceQuiet(s service) error {
	saved := e.out
	defer func() { e.out = saved }()
	e.out = discard{}
	return e.installService(s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return s
}

// installHint says how to install a missing tool on this machine, without doing it.
func installHint(tool string) string {
	f := &fixer{goos: goos, euid: os.Geteuid(), osRelease: func() string { b, _ := os.ReadFile("/etc/os-release"); return string(b) }, has: hasTool}
	cmds, needsRoot, problem := f.plan([]string{tool})
	if problem != "" {
		return problem
	}
	var parts []string
	for _, c := range cmds {
		parts = append(parts, commandLine(c))
	}
	s := strings.Join(parts, " && ")
	if needsRoot {
		s += " (as root)"
	}
	return s
}
