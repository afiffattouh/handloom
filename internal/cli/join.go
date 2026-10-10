package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// join it, keep the link running (as a service, or in tmux where there is no
// systemd), and check the machine, saying exactly what to fix.
//
//	handloom join <hub-url> <join-token> [--no-start] [--no-service]
func (e *env) join(args []string) error {
	fs := e.flags("join")
	noStart := fs.Bool("no-start", false, "only join: do not start the link or check the machine")
	noService := fs.Bool("no-service", false, "keep the link running in a tmux session instead of installing a systemd service")
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

	for _, tool := range []string{"git", "tmux"} {
		if _, err := exec.LookPath(tool); err != nil {
			e.say("%s is missing here: %s", tool, installHint(tool))
		}
	}

	e.step("Starting the link")
	if linkRunning() {
		e.say("the link is already running")
	} else if err := e.startLink(*noService); err != nil {
		return err
	}
	for i := 0; i < 40 && !linkRunning(); i++ {
		time.Sleep(500 * time.Millisecond)
	}

	e.step("Checking this machine")
	fmt.Fprintln(e.out)
	if err := e.doctor(nil); err != nil {
		fmt.Fprintln(e.out, "\nIt is joined and the link is running; fix what is listed above and run `handloom doctor` again. The web UI shows this machine as soon as the link has called in.")
		return err
	}
	fmt.Fprintln(e.out, "\nThe web UI now shows this machine as ready. Start a job on it from Jobs, New job.")
	return nil
}

func (e *env) step(format string, a ...any) { fmt.Fprintf(e.out, "\n== "+format+"\n", a...) }
func (e *env) say(format string, a ...any)  { fmt.Fprintf(e.out, "   "+format+"\n", a...) }

func linkRunning() bool {
	var st map[string]any
	return client.Socket(client.SocketPath(), "").Get("/local/status", &st) == nil
}

// startLink keeps the link running: as a systemd service when this machine has
// one that works for this user, otherwise inside a detached tmux session.
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
		e.say("the link runs as a service and starts at boot")
		return nil
	}
	if noService {
		e.say("keeping the link in tmux, as asked")
	} else {
		e.say("no working systemd service here (%s); using tmux instead", firstLine(err.Error()))
	}
	exe, _ := os.Executable()
	if _, terr := exec.LookPath("tmux"); terr != nil {
		return fmt.Errorf("cannot keep the link running: no systemd and no tmux. %s, then run `handloom join` again, or run `handloom link run` yourself", installHint("tmux"))
	}
	cmd := fmt.Sprintf("HANDLOOM_HOME=%s exec %s link run", shellQuote(home), shellQuote(exe))
	if out, terr := exec.Command("tmux", "new-session", "-d", "-s", "handloom-link", "sh", "-c", cmd).CombinedOutput(); terr != nil {
		return fmt.Errorf("could not start the link in tmux: %v: %s", terr, strings.TrimSpace(string(out)))
	}
	e.say("the link runs in the tmux session handloom-link. It will not restart after a reboot: run `handloom link install` on a machine with systemd for that")
	return nil
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
	has := func(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
	switch {
	case runtime.GOOS == "darwin" && has("brew"):
		return "brew install " + tool
	case runtime.GOOS == "darwin":
		return "install Homebrew (https://brew.sh), then brew install " + tool
	case has("apt-get"):
		return "sudo apt-get install -y " + tool
	case has("dnf"):
		return "sudo dnf install -y " + tool
	case has("apk"):
		return "sudo apk add " + tool
	case has("pacman"):
		return "sudo pacman -S " + tool
	}
	return "install " + tool + " with your package manager"
}
