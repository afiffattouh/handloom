package link

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
)

// Knowledge: a job may name a git repository on this device with what the team
// knows about the client. Every agent of the job gets a checkout of it at
// .handloom/knowledge and reads it. What an agent adds there is a proposal:
// the link gathers it, commit by commit, onto the branch job/<id> of that
// repository, and a human merges the branch. Nothing here goes to the hub but
// counts, and nothing is ever committed to the repository's own branch.

const (
	knowledgeDir     = "knowledge"    // under .handloom in an agent's directory
	confidentialDir  = "confidential" // folder of the repository that cloud models never get
	knowledgeBranch  = "job/%d"       // where the proposals of a job are gathered
	knowledgeCommits = "proposed notes for job %d"
)

// mountKnowledge makes the agent's checkout of the knowledge repository and
// returns the commit it is at. Agents whose profile does not run on a local
// model get the repository without its confidential/ folder.
func (l *Link) mountKnowledge(ctx context.Context, s api.Spawn, dir string, runtime string) (string, error) {
	git := func(args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append([]string{"-C", s.Knowledge}, args...)...)
	}
	if out, err := git("rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return "", fmt.Errorf("the knowledge repository %s is not a git repository on this device", s.Knowledge)
	}
	head, err := git("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("the knowledge repository %s has no commits yet", s.Knowledge)
	}
	head = strings.TrimSpace(head)
	mount := filepath.Join(dir, ".handloom", knowledgeDir)
	if _, err := os.Stat(filepath.Join(mount, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(mount), 0o700); err != nil {
			return "", err
		}
		if _, err := git("worktree", "add", "--detach", mount, head); err != nil {
			return "", fmt.Errorf("could not check out the knowledge repository: %v", err)
		}
	}
	if runtime != profile.Local {
		if _, err := l.opt.Git(ctx, "git", "-C", mount, "sparse-checkout", "set", "--no-cone", "/*", "!/"+confidentialDir+"/"); err != nil {
			os.RemoveAll(filepath.Join(mount, confidentialDir)) // never leave it where a cloud model can read it
			if _, serr := os.Stat(filepath.Join(mount, confidentialDir)); serr == nil {
				return "", fmt.Errorf("could not keep %s/ away from this agent: %v", confidentialDir, err)
			}
		}
	}
	return head, nil
}

// runCollects does what the hub queued: gather agents' proposed notes onto the job's branch.
func (l *Link) runCollects(ctx context.Context) {
	var ps []api.PendingCollect
	if err := l.hub.Do(ctx, "GET", "/v1/device/kcollects", nil, &ps); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("notes: %v", err)
		}
		return
	}
	for _, p := range ps {
		rep := l.collect(ctx, p)
		if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/kcollects/%d/report", p.ID), rep, nil); err != nil {
			l.opt.Log.Printf("report notes %d: %v", p.ID, err)
		}
	}
}

func (l *Link) collect(ctx context.Context, p api.PendingCollect) api.CollectReport {
	failed := func(format string, a ...any) api.CollectReport {
		return api.CollectReport{Status: "failed", Detail: fmt.Sprintf(format, a...)}
	}
	rec := l.loadScope(p.Agent)
	if rec == nil || rec.Knowledge == "" || rec.Job != p.Job {
		return api.CollectReport{Status: "done"}
	}
	mount := filepath.Join(rec.Dir, ".handloom", knowledgeDir)
	if _, err := os.Stat(filepath.Join(mount, ".git")); err != nil {
		return api.CollectReport{Status: "done"}
	}
	ident := []string{"-c", "user.name=" + p.Agent, "-c", "user.email=" + p.Agent + "@handloom.local", "-c", "core.hooksPath=/dev/null"}
	in := func(dir string, args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append(append([]string{"-C", dir}, ident...), args...)...)
	}
	if _, err := in(mount, "add", "-A"); err != nil {
		return failed("could not read %s's notes: %v", p.Agent, err)
	}
	if _, err := in(mount, "diff", "--cached", "--quiet"); err == nil {
		return api.CollectReport{Status: "done"} // nothing new
	}
	if _, err := in(mount, "commit", "-q", "--no-verify", "-m", fmt.Sprintf("%s: "+knowledgeCommits, p.Agent, p.Job)); err != nil {
		return failed("could not record %s's notes: %v", p.Agent, err)
	}
	sha, err := in(mount, "rev-parse", "HEAD")
	if err != nil {
		return failed("%v", err)
	}
	sha = strings.TrimSpace(sha)
	files, _ := in(mount, "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	n := len(strings.Fields(files))

	branch := fmt.Sprintf(knowledgeBranch, p.Job)
	wt := filepath.Join(l.opt.WorkRoot, fmt.Sprintf("job-%d", p.Job), "_knowledge")
	if _, err := os.Stat(wt); err != nil {
		if _, err := in(rec.Knowledge, "worktree", "add", "-b", branch, wt, rec.KBase); err != nil {
			if _, err2 := in(rec.Knowledge, "worktree", "add", wt, branch); err2 != nil {
				return failed("could not make the branch %s in %s: %v", branch, rec.Knowledge, err)
			}
		}
	}
	in(wt, "reset", "--hard", "-q")
	in(wt, "clean", "-fdq")
	if _, err := in(wt, "cherry-pick", "-x", sha); err != nil {
		conflicts, _ := in(wt, "diff", "--name-only", "--diff-filter=U")
		in(wt, "cherry-pick", "--abort")
		in(wt, "reset", "--hard", "-q")
		return api.CollectReport{Status: "conflict", Detail: fmt.Sprintf("%s's notes conflict with notes already on %s: %s", p.Agent, branch, strings.Join(strings.Fields(conflicts), ", "))}
	}
	return api.CollectReport{Status: "done", Notes: n}
}
