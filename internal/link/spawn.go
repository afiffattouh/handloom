package link

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/profile"
)

// The link starts spawned agents in a tmux server of its own (tmux -L
// handloom). Every agent gets a window there, which is the terminal the wake
// ladder types into and the liveness lease vouches for, exactly as for an
// agent a person started by hand. The window runs `handloom spawn-exec <id>`:
// that installs the adapter and execs the agent CLI. What it runs is decided
// there, from a fixed table by kind; the hub only names an agent and a kind.

var spawnNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// execTmux runs tmux without the link's own terminal variables, so a link
// started inside tmux or herdr does not confuse the server it manages.
func execTmux(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "HERDR_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// execGit runs git with a fixed, quiet environment (no prompts, no pager).
func execGit(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// runSpawns starts what the hub has asked this device to start. A request is
// claimed first (pending -> launching); the hub refuses a second claim, so a
// restarted or duplicate link run cannot start an agent twice.
func (l *Link) runSpawns(ctx context.Context) {
	var spawns []api.Spawn
	if err := l.hub.Do(ctx, "GET", "/v1/device/spawns", nil, &spawns); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("spawns: %v", err)
		}
		return
	}
	for _, s := range spawns {
		if s.Status != api.SpawnPending {
			continue
		}
		path := fmt.Sprintf("/v1/spawns/%d/report", s.ID)
		if err := l.hub.Do(ctx, "POST", path, api.SpawnReport{Status: api.SpawnLaunching}, nil); err != nil {
			continue // somebody else has it, or the hub is unreachable: next tick
		}
		if err := l.launch(ctx, s); err != nil {
			l.opt.Log.Printf("spawn %d (%s): %v", s.ID, s.Name, err)
			l.hub.Do(ctx, "POST", path, api.SpawnReport{Status: api.SpawnFailed, Error: reasonText(err.Error())}, nil)
		}
	}
}

// launch opens the window. The agent is reported as started by spawn-exec
// itself once its adapter is installed.
func (l *Link) launch(ctx context.Context, s api.Spawn) error {
	if !spawnNameRE.MatchString(s.Name) {
		return fmt.Errorf("refusing agent name %q", s.Name)
	}
	if l.opt.Binary == "" {
		return fmt.Errorf("the link does not know its own binary")
	}
	job := "none"
	if s.Job != nil {
		job = fmt.Sprintf("job-%d", *s.Job)
	}
	dir := filepath.Join(l.opt.WorkRoot, job, s.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if s.Repo != "" {
		// A repo job: the agent works in its own git worktree of the repository,
		// on its own branch, so agents cannot trip over each other's files.
		if err := l.worktree(ctx, s, dir); err != nil {
			return err
		}
	}
	kbase := ""
	if s.Knowledge != "" {
		runtime, err := l.profileRuntime(ctx, s)
		if err != nil {
			return err
		}
		if kbase, err = l.mountKnowledge(ctx, s, dir, runtime); err != nil {
			return err
		}
	}
	if s.Repo != "" || s.Knowledge != "" {
		if err := l.recordScope(ctx, s, dir, kbase); err != nil {
			return err
		}
	}
	l.mu.Lock()
	if l.fresh == nil {
		l.fresh = map[string]time.Time{}
	}
	l.fresh[s.Name] = l.opt.Now()
	l.mu.Unlock()
	home := client.Home()
	cmd := fmt.Sprintf("env HANDLOOM_HOME=%s PATH=%s HANDLOOM_SPAWN=%d %s spawn-exec %d",
		shellQuote(home), shellQuote(filepath.Dir(l.opt.Binary)+":"+os.Getenv("PATH")), s.ID, shellQuote(l.opt.Binary), s.ID)
	tmux := func(args ...string) (string, error) {
		return l.opt.Tmux(ctx, "tmux", append([]string{"-L", l.opt.TmuxSocket}, args...)...)
	}
	if _, err := tmux("has-session", "-t", "handloom"); err != nil {
		if _, err = tmux("new-session", "-d", "-s", "handloom", "-n", s.Name, "-c", dir, "-x", "200", "-y", "50", cmd); err != nil {
			return err
		}
		// An agent CLI that dies with an error leaves its last screen behind
		// (a dead pane) instead of vanishing, so the reason can be read.
		tmux("set-option", "-g", "remain-on-exit", "failed")
		return nil
	}
	_, err := tmux("new-window", "-d", "-t", "handloom:", "-n", s.Name, "-c", dir, cmd)
	return err
}

// worktree makes dir a git worktree of the job's repository on the branch
// job/<id>/<name>, cut from the repository's current HEAD. dir was just
// created and is empty, which git accepts. If the branch already exists (an
// agent started again under the same name) it is reused.
func (l *Link) worktree(ctx context.Context, s api.Spawn, dir string) error {
	if s.Job == nil {
		return fmt.Errorf("a repository without a job")
	}
	git := func(args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append([]string{"-C", s.Repo}, args...)...)
	}
	if _, err := git("rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("%s is not a git repository on this device", s.Repo)
	}
	if out, err := git("status", "--porcelain", "--untracked-files=no"); err == nil && strings.TrimSpace(out) != "" {
		l.opt.Log.Printf("spawn %d: the repository %s has uncommitted changes; the worktree starts from its last commit", s.ID, s.Repo)
	}
	branch := fmt.Sprintf("job/%d/%s", *s.Job, s.Name)
	start := "HEAD"
	if s.Base != "" {
		if _, err := git("rev-parse", "--verify", "-q", s.Base+"^{commit}"); err != nil {
			return fmt.Errorf("the base branch %q does not exist in %s", s.Base, s.Repo)
		}
		start = s.Base
	}
	if _, err := git("worktree", "add", "-b", branch, dir, start); err != nil {
		if _, err2 := git("worktree", "add", dir, branch); err2 != nil {
			return fmt.Errorf("could not make a worktree for %s: %v", s.Name, err)
		}
	}
	return nil
}

// profileRuntime is whether the agent's profile runs on a model on this
// machine ("local") or on a cloud model. An agent without a profile counts as cloud.
func (l *Link) profileRuntime(ctx context.Context, s api.Spawn) (string, error) {
	if s.Profile == "" {
		return profile.Cloud, nil
	}
	var p api.ProfileFull
	if err := l.hub.Do(ctx, "GET", fmt.Sprintf("/v1/device/spawns/%d/profile", s.ID), nil, &p); err != nil {
		return "", fmt.Errorf("profile %s: %w", s.Profile, err)
	}
	return p.Spec.Runtime, nil
}
