// Package drivers types the wake nudge into an agent's terminal.
//
// A driver only ever types the fixed nudge line it is given. Message bodies
// never reach a terminal this way.
package drivers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Driver wakes an idle agent through its terminal.
type Driver interface {
	// Name is the wake method recorded in the audit log.
	Name() string
	// Nudge types line into the terminal at target and submits it.
	Nudge(ctx context.Context, target, line string) error
	// Alive reports whether the terminal at target still exists. A failed
	// check counts as "not alive"; callers tolerate a few misses.
	Alive(ctx context.Context, target string) bool
}

// Capturer is implemented by drivers that can read what is on the terminal.
type Capturer interface {
	// Capture returns the last lines lines of the terminal at target, as plain text.
	Capture(ctx context.Context, target string, lines int) (string, error)
}

// Runner runs a command and returns its combined output. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// For returns the driver for a wake target such as "tmux:/tmp/tmux-0/default:%3"
// or "herdr:w1:p2", and the driver's own part of the target.
func For(target string) (Driver, string, error) {
	kind, rest, _ := strings.Cut(target, ":")
	switch kind {
	case "tmux":
		return &Tmux{Run: execRunner, Pause: 400 * time.Millisecond}, rest, nil
	case "herdr":
		return &Herdr{Run: execRunner}, rest, nil
	}
	return nil, "", fmt.Errorf("no wake driver for target %q", target)
}

// Detect builds the wake target for the terminal this process runs in, from
// the environment. tmux wins over herdr: $TMUX_PANE is set by tmux for
// exactly this pane, while herdr's variables can be inherited from an outer
// terminal.
func Detect() string {
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		socket, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
		return "tmux:" + socket + ":" + pane
	}
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" && os.Getenv("HERDR_ENV") == "1" {
		return "herdr:" + pane
	}
	return ""
}

// ---- tmux ----

// Tmux types through `tmux send-keys`. Target: "<socket path>:<pane id>"; the
// socket path may be empty for the default server.
type Tmux struct {
	Run   Runner
	Pause time.Duration // between the text and Enter, so the TUI takes them apart
}

func (t *Tmux) Name() string { return "tmux" }

// shells are programs we refuse to type into: there the nudge would be run
// as a command instead of read by an agent.
var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true, "csh": true}

func (t *Tmux) Alive(ctx context.Context, target string) bool {
	i := strings.LastIndex(target, ":")
	if i < 0 || target[i+1:] == "" {
		return false
	}
	socket, pane := target[:i], target[i+1:]
	args := []string{}
	if socket != "" {
		args = append(args, "-S", socket)
	}
	// display-message answers for a pane that does not exist with an empty
	// line and exit status 0 (it falls back to the current pane), so the
	// answer must be the very pane asked about.
	// A pane that exited but is kept on screen (remain-on-exit) is dead too.
	out, err := t.Run(ctx, "tmux", append(args, "display-message", "-p", "-t", pane, "#{pane_id} #{pane_dead}")...)
	return err == nil && strings.TrimSpace(out) == pane+" 0"
}

func (t *Tmux) Capture(ctx context.Context, target string, lines int) (string, error) {
	i := strings.LastIndex(target, ":")
	if i < 0 || target[i+1:] == "" {
		return "", fmt.Errorf("bad tmux target %q", target)
	}
	socket, pane := target[:i], target[i+1:]
	args := []string{}
	if socket != "" {
		args = append(args, "-S", socket)
	}
	return t.Run(ctx, "tmux", append(args, "capture-pane", "-p", "-t", pane, "-S", fmt.Sprintf("-%d", lines))...)
}

func (t *Tmux) Nudge(ctx context.Context, target, line string) error {
	i := strings.LastIndex(target, ":")
	if i < 0 || target[i+1:] == "" {
		return fmt.Errorf("bad tmux target %q", target)
	}
	socket, pane := target[:i], target[i+1:]
	base := []string{}
	if socket != "" {
		base = append(base, "-S", socket)
	}
	tmux := func(args ...string) (string, error) {
		return t.Run(ctx, "tmux", append(append([]string{}, base...), args...)...)
	}
	cmd, err := tmux("display-message", "-p", "-t", pane, "#{pane_current_command}")
	if err != nil {
		return err
	}
	if cmd = strings.TrimSpace(cmd); shells[cmd] {
		return fmt.Errorf("pane %s runs %s, not an agent", pane, cmd)
	}
	if _, err := tmux("send-keys", "-t", pane, "-l", line); err != nil {
		return err
	}
	if t.Pause > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.Pause):
		}
	}
	_, err = tmux("send-keys", "-t", pane, "Enter")
	return err
}

// ---- herdr ----

// Herdr submits through `herdr agent prompt`. Target: the herdr pane id.
// Herdr itself refuses to type into an agent that is blocked.
type Herdr struct {
	Run Runner
}

func (h *Herdr) Name() string { return "herdr" }

func (h *Herdr) Alive(ctx context.Context, target string) bool {
	if target == "" {
		return false
	}
	out, err := h.Run(ctx, "herdr", "pane", "get", target)
	return err == nil && !strings.Contains(out, `"error"`)
}

func (h *Herdr) Nudge(ctx context.Context, target, line string) error {
	if target == "" {
		return fmt.Errorf("bad herdr target")
	}
	out, err := h.Run(ctx, "herdr", "agent", "prompt", target, line)
	if err != nil {
		return err
	}
	if strings.Contains(out, `"error"`) {
		return fmt.Errorf("herdr agent prompt: %s", strings.TrimSpace(out))
	}
	return nil
}
