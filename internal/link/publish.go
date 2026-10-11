package link

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"handloom/internal/api"
)

// Publishing: a person asked for a finished job's integration branch to go to
// the project's shared remote. The link pushes it, under a branch name of its
// own, and never forces: a branch that is already there and is not an ancestor
// is refused and reported. With the pull-request option and gh installed it
// also opens the pull request. Nothing here touches the repository's own
// branches, and the hub learns the outcome in words, a link and a count.

// publishTimeout bounds each git or gh command of a publish (a push can wait on a slow network).
const publishTimeout = 3 * time.Minute

var publishBranchRE = regexp.MustCompile(`^handloom/job-[0-9]+(-[a-z0-9-]*)?$`)

func (l *Link) runPublishes(ctx context.Context) {
	var ps []api.PendingPublish
	if err := l.hub.Do(ctx, "GET", "/v1/device/publishes", nil, &ps); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("publishes: %v", err)
		}
		return
	}
	for _, p := range ps {
		rep := l.publish(ctx, p)
		if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/publishes/%d/report", p.ID), rep, nil); err != nil {
			l.opt.Log.Printf("report publish %d: %v", p.ID, err)
		}
	}
}

func (l *Link) publish(ctx context.Context, p api.PendingPublish) api.PublishReport {
	failed := func(format string, a ...any) api.PublishReport {
		return api.PublishReport{Status: "failed", Detail: fmt.Sprintf(format, a...)}
	}
	if !publishBranchRE.MatchString(p.Branch) || strings.HasPrefix(p.Remote, "-") {
		return failed("refusing the branch name %q or remote %q", p.Branch, p.Remote)
	}
	git := func(args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		return l.opt.Git(cctx, "git", append([]string{"-C", p.Repo}, args...)...)
	}
	if out, err := git("rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return failed("%s is not a git repository on this device", p.Repo)
	}
	integ := fmt.Sprintf("job/%d/integration", p.Job)
	if _, err := git("rev-parse", "--verify", "-q", "refs/heads/"+integ); err != nil {
		return failed("this device has no branch %s in %s: nothing has been merged here yet", integ, p.Repo)
	}
	// An agent's token file must never leave the machine. If one got committed (git add -A by hand), say how to remove it.
	if out, err := git("ls-tree", "-r", "--name-only", "refs/heads/"+integ, "--", ".handloom/"); err == nil && strings.TrimSpace(out) != "" {
		return failed("the branch %s contains files Handloom keeps for its agents (%s), including an access token, so it was not pushed. "+
			"Remove them and publish again: git -C %s checkout %s && git rm -r --cached .handloom && git commit -m \"remove agent files\"",
			integ, strings.Join(strings.Fields(out), ", "), p.Repo, integ)
	}
	remotes, err := git("remote")
	if err != nil {
		return failed("could not list the remotes of %s: %v", p.Repo, err)
	}
	have := strings.Fields(remotes)
	if len(have) == 0 {
		return failed("%s has no remote to publish to: add one with `git -C %s remote add origin <url>` and publish again", p.Repo, p.Repo)
	}
	known := false
	for _, r := range have {
		known = known || r == p.Remote
	}
	if !known {
		return failed("%q is not a remote of %s (it has: %s)", p.Remote, p.Repo, strings.Join(have, ", "))
	}

	var notes []string
	// Where the base branch stands on the remote now, so the person knows whether the pull request may conflict.
	base := p.Base
	if base == "" {
		if out, err := git("symbolic-ref", "--short", "-q", "HEAD"); err == nil {
			base = strings.TrimSpace(out)
		}
	}
	behind := 0
	if _, err := git("fetch", "--quiet", p.Remote); err != nil {
		notes = append(notes, "could not fetch "+p.Remote+" first, so the check for new commits on the base branch was skipped")
	} else if base != "" {
		if _, err := git("rev-parse", "--verify", "-q", "refs/remotes/"+p.Remote+"/"+base); err == nil {
			if _, err := git("rev-parse", "--verify", "-q", "refs/heads/"+base); err == nil {
				if out, err := git("rev-list", "--count", "refs/heads/"+base+"..refs/remotes/"+p.Remote+"/"+base); err == nil {
					behind, _ = strconv.Atoi(strings.TrimSpace(out))
				}
			}
		}
	}

	// Never forced: a plain push refuses anything that is not a fast-forward. The explicit refspec
	// overrides any push setting of the repository (a configured "+" would force).
	if out, err := git("push", p.Remote, "refs/heads/"+integ+":refs/heads/"+p.Branch); err != nil {
		return failed("the push was refused and nothing was forced: %s", gitMessage(out, err))
	}
	detail := fmt.Sprintf("pushed %s to %s as %s", integ, p.Remote, p.Branch)
	if behind > 0 {
		notes = append(notes, fmt.Sprintf("the base moved: %d new commit%s on %s/%s; the pull request may conflict", behind, plural(behind), p.Remote, base))
	}
	rep := api.PublishReport{Status: "done", Behind: behind}

	if p.PR {
		gh, err := exec.LookPath("gh")
		if err != nil {
			notes = append(notes, "gh is not installed: open the pull request from "+p.Branch)
		} else {
			args := []string{"pr", "create", "--head", p.Branch, "--title", p.Title, "--body", prBody(p)}
			if base != "" {
				args = append(args, "--base", base)
			}
			cctx, cancel := context.WithTimeout(ctx, publishTimeout)
			cmd := exec.CommandContext(cctx, gh, args...)
			cmd.Dir = p.Repo
			cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "NO_COLOR=1")
			out, err := cmd.CombinedOutput()
			cancel()
			url := lastURL(string(out))
			switch {
			case err == nil && url != "":
				rep.URL = url
			case url != "" && strings.Contains(string(out), "already exists"):
				rep.URL = url
				notes = append(notes, "a pull request for this branch already exists")
			case err == nil:
				notes = append(notes, "gh opened the pull request but printed no link")
			default:
				notes = append(notes, "the branch is pushed but gh could not open the pull request: "+gitMessage(string(out), err)+"; open it from "+p.Branch)
			}
		}
	}
	if len(notes) > 0 {
		detail += "; " + strings.Join(notes, "; ")
	}
	rep.Detail = detail
	return rep
}

// prBody is the job's brief and the tasks with who did them. Evidence and
// handoff notes stay in the hub: a pull request is read by people outside it.
func prBody(p api.PendingPublish) string {
	var b strings.Builder
	if body := strings.TrimSpace(p.Body); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	if len(p.Tasks) > 0 {
		b.WriteString("## Tasks\n\n")
		for _, t := range p.Tasks {
			owner := ""
			if t.Owner != "" {
				owner = " (" + t.Owner + ")"
			}
			fmt.Fprintf(&b, "- %s%s\n", t.Title, owner)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Published from Handloom job #%d.\n", p.Job)
	return b.String()
}

// lastURL is the last https link in gh's output (the pull request).
func lastURL(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		for _, w := range strings.Fields(lines[i]) {
			if strings.HasPrefix(w, "https://") {
				return strings.TrimRight(w, ".,;:)")
			}
		}
	}
	return ""
}

// gitMessage is what git said, shortened to something that fits a line of the job page.
func gitMessage(out string, err error) string {
	msg := strings.TrimSpace(out)
	if msg == "" {
		msg = err.Error()
	}
	if len(msg) > 800 {
		msg = msg[len(msg)-800:]
	}
	return msg
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
