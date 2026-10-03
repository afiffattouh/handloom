package link

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

// HeadlessTarget is the wake target of an agent that has no terminal: it is
// woken by running one headless turn on its saved session (wake ladder step 3).
const HeadlessTarget = "headless"

// headlessCommands is how each agent kind resumes a session for one turn.
// {session_id} and {prompt} are replaced. Only the claude line has been run
// against the real CLI; the others follow each CLI's --help.
var headlessCommands = map[string][]string{
	"claude":   {"claude", "-p", "--resume", "{session_id}", "{prompt}"},
	"codex":    {"codex", "exec", "resume", "{session_id}", "{prompt}"},
	"pi":       {"pi", "-p", "--session", "{session_id}", "{prompt}"},
	"omp":      {"omp", "-p", "--resume", "{session_id}", "{prompt}"},
	"opencode": {"opencode", "run", "--session", "{session_id}", "{prompt}"},
}

// LoadHeadlessOverrides reads $HANDLOOM_HOME/headless.json, which may replace
// the command for a kind, for example to give an absolute path or extra flags:
//
//	{"claude": ["/root/.local/bin/claude", "-p", "--resume", "{session_id}", "{prompt}"]}
func LoadHeadlessOverrides() (map[string][]string, error) {
	path := filepath.Join(client.Home(), "headless.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// headlessArgv builds the command for one headless turn.
func (l *Link) headlessArgv(a api.DeviceAgent, prompt string) ([]string, error) {
	tmpl := l.opt.Headless[a.Kind]
	if tmpl == nil {
		tmpl = headlessCommands[a.Kind]
	}
	if tmpl == nil {
		return nil, fmt.Errorf("no headless command for agent kind %q", a.Kind)
	}
	if a.SessionID == "" {
		return nil, fmt.Errorf("no session id to resume")
	}
	if a.Dir == "" {
		return nil, fmt.Errorf("no working directory recorded")
	}
	argv := make([]string, len(tmpl))
	for i, arg := range tmpl {
		argv[i] = strings.NewReplacer("{session_id}", a.SessionID, "{prompt}", prompt).Replace(arg)
	}
	return argv, nil
}

// HeadlessRun starts a headless turn and returns a function that waits for
// it. Tests replace it.
type HeadlessRun func(ctx context.Context, agent, dir string, argv []string) (wait func() error, err error)

// execHeadless runs the command in the agent's directory with no terminal.
// Output goes to $HANDLOOM_HOME/headless-<agent>.log.
func execHeadless(ctx context.Context, agent, dir string, argv []string) (func() error, error) {
	logFile, err := os.OpenFile(filepath.Join(client.Home(), "headless-"+agent+".log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logFile, "\n--- %s %s\n", time.Now().Format(time.RFC3339), strings.Join(argv[:len(argv)-1], " "))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// The agent's hooks must know who they are and that no terminal exists.
	cmd.Env = append(os.Environ(), "HANDLOOM_AGENT="+agent, "HANDLOOM_HEADLESS=1")
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	return func() error {
		defer logFile.Close()
		return cmd.Wait()
	}, nil
}

// startHeadless runs one headless turn for the agent in the background. It
// returns an error if the turn could not be started.
func (l *Link) startHeadless(ctx context.Context, a api.DeviceAgent, m *memo, prompt string) error {
	argv, err := l.headlessArgv(a, prompt)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, l.opt.HeadlessTimeout)
	wait, err := l.opt.HeadlessRun(runCtx, a.Name, a.Dir, argv)
	if err != nil {
		cancel()
		return err
	}
	l.mu.Lock()
	m.headlessRunning = true
	l.mu.Unlock()
	go func() {
		err := wait()
		cancel()
		l.mu.Lock()
		m.headlessRunning = false
		l.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			l.opt.Log.Printf("headless turn for %s ended with an error: %v", a.Name, err)
			l.fail(ctx, a, m, "driver_error: headless turn failed: "+reasonText(err.Error()))
		}
		l.Kick()
	}()
	return nil
}
