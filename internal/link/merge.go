package link

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"handloom/internal/api"
)

// runMerges does what the hub queued: merge an accepted task's branch into the
// job's integration branch, in a worktree of its own, and check the result.
func (l *Link) runMerges(ctx context.Context) {
	var ms []api.PendingMerge
	if err := l.hub.Do(ctx, "GET", "/v1/device/merges", nil, &ms); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("merges: %v", err)
		}
		return
	}
	for _, m := range ms {
		rep := l.merge(ctx, m)
		if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/merges/%d/report", m.ID), rep, nil); err != nil {
			l.opt.Log.Printf("report merge %d: %v", m.ID, err)
		}
	}
}

func (l *Link) merge(ctx context.Context, m api.PendingMerge) api.MergeReport {
	failed := func(format string, a ...any) api.MergeReport {
		return api.MergeReport{Status: "failed", Detail: fmt.Sprintf(format, a...)}
	}
	rec := l.loadScope(m.Agent)
	if rec == nil || rec.Job != m.Job {
		return failed("this device has no record of %s's worktree in job %d", m.Agent, m.Job)
	}
	branch := fmt.Sprintf("job/%d/%s", m.Job, m.Agent)
	integ := fmt.Sprintf("job/%d/integration", m.Job)
	dir := filepath.Join(l.opt.WorkRoot, fmt.Sprintf("job-%d", m.Job), "_integration")
	git := func(args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append([]string{"-C", dir}, args...)...)
	}
	if _, err := os.Stat(dir); err != nil {
		if _, err := l.opt.Git(ctx, "git", "-C", rec.Dir, "worktree", "add", "-b", integ, dir, rec.Base); err != nil {
			if _, err2 := l.opt.Git(ctx, "git", "-C", rec.Dir, "worktree", "add", dir, integ); err2 != nil {
				return failed("could not make the integration worktree: %v", err)
			}
		}
	}
	// The integration worktree is the link's own: whatever is in it is leftover.
	git("reset", "--hard", "-q")
	git("clean", "-fdq")
	prev, err := git("rev-parse", "HEAD")
	if err != nil {
		return failed("%v", err)
	}
	prev = strings.TrimSpace(prev)
	if _, err := git("merge-base", "--is-ancestor", branch, "HEAD"); err == nil {
		return api.MergeReport{Status: "merged", Detail: "nothing new: " + branch + " is already in " + integ, Head: short(prev)}
	}
	if _, err := git("rev-parse", "--verify", "-q", branch); err != nil {
		return failed("the branch %s does not exist", branch)
	}
	_, err = git("-c", "user.name=handloom", "-c", "user.email=handloom@handloom.local", "-c", "core.hooksPath=/dev/null",
		"merge", "--no-ff", "-m", fmt.Sprintf("Merge task #%d: %s (%s)", m.Task, m.Title, m.Agent), branch)
	if err != nil {
		files, _ := git("diff", "--name-only", "--diff-filter=U")
		git("merge", "--abort")
		files = strings.Join(strings.Fields(files), ", ")
		if files == "" {
			return failed("git merge failed: %v", err)
		}
		return api.MergeReport{Status: "conflict", Detail: "conflicting files: " + files}
	}
	if m.Verify != "" {
		res := l.execVerify(dir, m.Verify, fmt.Sprintf("merge-%d-%d", m.Job, m.Task))
		if res.exit != 0 || res.timedOut {
			git("reset", "--hard", "-q", prev) // the integration branch stays good
			why := fmt.Sprintf("the check %q failed after merging (exit %d)", m.Verify, res.exit)
			if res.timedOut {
				why = fmt.Sprintf("the check %q timed out after merging", m.Verify)
			}
			return failed("%s:\n%s", why, lastN(res.tail, 12))
		}
	}
	head, _ := git("rev-parse", "HEAD")
	return api.MergeReport{Status: "merged", Head: short(strings.TrimSpace(head))}
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func lastN(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
