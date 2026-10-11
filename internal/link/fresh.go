package link

import (
	"context"
	"fmt"
	"strings"
	"time"

	"handloom/internal/api"
)

// A fresh base (D105). When a job of a project starts on this machine, the
// machine first looks for new commits on the project's remote and brings its
// own copy of the job's base branch up to date, but only when that is a plain
// fast-forward: nothing is merged, rebased or reset, a copy with commits of its
// own is left alone, and so is a working tree with uncommitted changes. The
// outcome is told to the hub in a sentence. Whatever happens, the job starts.

const (
	baseFetchTimeout = time.Minute
	baseGitTimeout   = 30 * time.Second
)

// freshBase runs once per job in this process, before the job's first worktree is made.
func (l *Link) freshBase(ctx context.Context, s api.Spawn) {
	if s.Job == nil || !s.FreshBase || s.Repo == "" {
		return
	}
	l.mu.Lock()
	if l.baseDone == nil {
		l.baseDone = map[int64]bool{}
	}
	done := l.baseDone[*s.Job]
	l.baseDone[*s.Job] = true
	l.mu.Unlock()
	if done {
		return
	}
	rep := l.updateBase(ctx, s.Repo, s.Base)
	if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/jobs/%d/base", *s.Job), rep, nil); err != nil {
		l.opt.Log.Printf("report base of job %d: %v", *s.Job, err)
	}
}

// updateBase never fails: every problem is an answer.
func (l *Link) updateBase(ctx context.Context, repo, base string) api.BaseReport {
	run := func(timeout time.Duration, dir string, args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return l.opt.Git(cctx, "git", append([]string{"-C", dir}, args...)...)
	}
	git := func(args ...string) (string, error) { return run(baseGitTimeout, repo, args...) }
	stale := func(format string, a ...any) api.BaseReport {
		return api.BaseReport{Status: api.BaseStale, Detail: fmt.Sprintf(format, a...)}
	}
	if _, err := git("rev-parse", "--is-inside-work-tree"); err != nil {
		return stale("%s is not a git repository on this machine", repo)
	}
	branch := base
	if branch == "" {
		out, err := git("symbolic-ref", "--short", "-q", "HEAD")
		if branch = strings.TrimSpace(out); err != nil || branch == "" {
			return stale("this machine's copy is not on a branch, so it was not updated; the job starts from it as it is")
		}
	}
	asIs := "the job starts from this machine's copy of " + branch + " as it is"
	if out, err := run(baseFetchTimeout, repo, "fetch", "--quiet", "origin"); err != nil {
		return stale("could not get the latest from origin (is this machine offline?): %s; %s", lastLine(gitMessage(out, err)), asIs)
	}
	remoteRef, localRef := "refs/remotes/origin/"+branch, "refs/heads/"+branch
	remote, err := git("rev-parse", "--verify", "-q", remoteRef)
	if err != nil {
		return stale("origin has no branch %s; %s", branch, asIs)
	}
	remote = strings.TrimSpace(remote)
	local, err := git("rev-parse", "--verify", "-q", localRef)
	if err != nil {
		return stale("%s is not a branch of this machine's copy, so it was not updated; %s", branch, asIs)
	}
	local = strings.TrimSpace(local)
	if local == remote {
		return api.BaseReport{Status: api.BaseFresh, Commit: short(local), Detail: fmt.Sprintf("%s is already current at %s", branch, short(local))}
	}
	if _, err := git("merge-base", "--is-ancestor", local, remote); err != nil {
		return api.BaseReport{Status: api.BaseDiverged, Commit: short(local),
			Detail: fmt.Sprintf("this machine's copy of %s has commits that are not on origin; the job starts from it as it is", branch)}
	}
	moved := 0
	if out, err := git("rev-list", "--count", local+".."+remote); err == nil {
		fmt.Sscanf(strings.TrimSpace(out), "%d", &moved)
	}
	// A branch that is checked out somewhere is moved by git itself, in that folder, and only if it is clean.
	if dir := l.checkedOutAt(git, branch); dir != "" {
		if out, err := run(baseGitTimeout, dir, "status", "--porcelain", "--untracked-files=no"); err != nil || strings.TrimSpace(out) != "" {
			return stale("this machine's copy has uncommitted changes, so %s was not updated; %s", branch, asIs)
		}
		if out, err := run(baseGitTimeout, dir, "merge", "--ff-only", "--quiet", remote); err != nil {
			return stale("%s could not be fast-forwarded (%s); %s", branch, lastLine(gitMessage(out, err)), asIs)
		}
	} else if out, err := git("update-ref", "-m", "handloom: fast-forward "+branch, localRef, remote, local); err != nil {
		return stale("%s could not be fast-forwarded (%s); %s", branch, lastLine(gitMessage(out, err)), asIs)
	}
	return api.BaseReport{Status: api.BaseFresh, Commit: short(remote),
		Detail: fmt.Sprintf("%s moved forward %d commit%s to %s", branch, moved, plural(moved), short(remote))}
}

// checkedOutAt is the folder of the worktree that has the branch checked out, or "".
func (l *Link) checkedOutAt(git func(...string) (string, error), branch string) string {
	out, err := git("worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	dir := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			dir = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return dir
		}
	}
	return ""
}

// lastLine is the last non-empty line of git's answer: where it says what went wrong.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
