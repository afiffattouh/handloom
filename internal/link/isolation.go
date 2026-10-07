package link

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"handloom/internal/api"
	"handloom/internal/client"
)

// Isolation is how a spawned agent's process is run on this device. Today
// there is one way, "none": the agent CLI runs on the machine, as the user
// running the link, in a tmux window. This seam is where a stricter way (the
// agent inside a container that sees only its work folder; see
// docs/isolation.md) is added: write an Isolator, register it in isolators,
// and the rest of the link (the tmux window, the wake ladder, the scope
// check, verification, merges, the terminal view) does not change.
type Isolator interface {
	// Name is what a person types to choose it (`handloom link run --isolation`).
	Name() string
	// Command returns the command line the agent's tmux window runs.
	Command(ctx context.Context, p LaunchPlan) (string, error)
}

// LaunchPlan is everything an Isolator needs to know about one spawn.
type LaunchPlan struct {
	Spawn  api.Spawn
	Dir    string // the agent's work directory, already made (a git worktree for repository jobs)
	Home   string // the link's Handloom home (credentials, the link socket, scope records)
	Binary string // the handloom binary
	Path   string // the PATH the agent should get
	// ExecLine is the command the native isolator runs: `handloom spawn-exec <id>`
	// with its environment. Another isolator runs the same line inside its box.
	ExecLine string
}

type native struct{}

func (native) Name() string { return "none" }
func (native) Command(_ context.Context, p LaunchPlan) (string, error) {
	return p.ExecLine, nil
}

// isolators are the ways this build can run an agent.
var isolators = map[string]Isolator{"none": native{}}

// planned are ways that are designed but not built yet: they get a clear
// message instead of a silent fallback to "none".
var planned = map[string]string{"container": "container isolation is designed (docs/isolation.md) but not built yet"}

// IsolationNames lists the ways this build can run an agent.
func IsolationNames() []string {
	var out []string
	for n := range isolators {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ValidIsolation says whether name can be used, and why not in words.
func ValidIsolation(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := isolators[name]; ok {
		return nil
	}
	if why, ok := planned[name]; ok {
		return fmt.Errorf("%s: choose one of %s", why, strings.Join(IsolationNames(), ", "))
	}
	return fmt.Errorf("unknown isolation %q: choose one of %s", name, strings.Join(IsolationNames(), ", "))
}

func (l *Link) isolator() Isolator {
	if iso, ok := isolators[l.opt.Isolation]; ok {
		return iso
	}
	return isolators["none"]
}

// plan describes a spawn for an Isolator.
func (l *Link) plan(s api.Spawn, dir string) LaunchPlan {
	home := client.Home()
	path := filepath.Dir(l.opt.Binary) + ":" + os.Getenv("PATH")
	return LaunchPlan{Spawn: s, Dir: dir, Home: home, Binary: l.opt.Binary, Path: path,
		ExecLine: fmt.Sprintf("env HANDLOOM_HOME=%s PATH=%s HANDLOOM_SPAWN=%d %s spawn-exec %d",
			shellQuote(home), shellQuote(path), s.ID, shellQuote(l.opt.Binary), s.ID)}
}
