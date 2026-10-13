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
)

// Projects (D104): the hub asked this machine to get its own copy of a project's
// repository, or to create the repository. It happens here, under this machine's
// own git and gh login, so each person's permissions apply and no credential or
// code goes to the hub. The hub learns the outcome in words, an address, a
// branch name and the absolute path of the copy. Nothing is ever deleted: a
// folder that is in the way makes the request fail and is left as it was.

const (
	cloneTimeout  = 5 * time.Minute // a clone can wait on a slow network
	createTimeout = 2 * time.Minute // each command of a creation

	noGHText = "install the GitHub command line tool (gh) and run `gh auth login` on this machine"
)

var projectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

func (l *Link) runProjectRequests(ctx context.Context) {
	var ps []api.PendingCodebaseRequest
	if err := l.hub.Do(ctx, "GET", "/v1/device/project-requests", nil, &ps); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("project requests: %v", err)
		}
		return
	}
	for _, p := range ps {
		var rep api.CodebaseReport
		switch p.Kind {
		case "clone":
			rep = l.cloneProject(ctx, p)
		case "create":
			rep = l.createProject(ctx, p)
		default:
			rep = api.CodebaseReport{Status: "failed", Detail: "this link does not know how to " + p.Kind}
		}
		if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/project-requests/%d/report", p.ID), rep, nil); err != nil {
			l.opt.Log.Printf("report project request %d: %v", p.ID, err)
		}
	}
}

func projectFailed(format string, a ...any) api.CodebaseReport {
	return api.CodebaseReport{Status: "failed", Detail: fmt.Sprintf(format, a...)}
}

// projectDir is where this machine keeps the copy of a project.
func (l *Link) projectDir(name string) (string, error) {
	if !projectNameRE.MatchString(name) {
		return "", fmt.Errorf("refusing the project name %q", name)
	}
	return filepath.Join(l.opt.ProjectsRoot, name), nil
}

// existing says what is at dir: "none" (nothing, or an empty folder), "same" (a git
// repository whose origin is remote) or a sentence about what is in the way.
func (l *Link) existing(ctx context.Context, dir, remote string) (state, why string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "none", ""
		}
		return "other", fmt.Sprintf("%s cannot be read: %v", dir, err)
	}
	if len(entries) == 0 {
		return "none", ""
	}
	git := func(args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return l.opt.Git(cctx, "git", append([]string{"-C", dir}, args...)...)
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "other", fmt.Sprintf("%s already exists and is not a git repository; move it away or rename it, and ask again (nothing was changed)", dir)
	}
	a, _ := filepath.EvalSymlinks(strings.TrimSpace(top))
	b, _ := filepath.EvalSymlinks(dir)
	if a != b {
		return "other", fmt.Sprintf("%s already exists and is not a git repository of its own; move it away or rename it, and ask again (nothing was changed)", dir)
	}
	origin, err := git("remote", "get-url", "origin")
	if err != nil {
		return "other", fmt.Sprintf("%s is a git repository without an origin, so it is not this project; move it away and ask again (nothing was changed)", dir)
	}
	if !sameRemote(strings.TrimSpace(origin), remote) {
		return "other", fmt.Sprintf("%s is a different repository (its origin is %s, the project is %s); move it away or rename it, and ask again (nothing was changed)", dir, strings.TrimSpace(origin), remote)
	}
	return "same", ""
}

// sameRemote compares two addresses of one repository however they are written
// (https, ssh, git@host:path, with or without .git), which is how gh may clone it.
func sameRemote(a, b string) bool { return remoteKey(a) == remoteKey(b) }

func remoteKey(r string) string {
	r = strings.TrimSpace(r)
	if m := regexp.MustCompile(`^[^@/:]+@([^:/]+):(.+)$`).FindStringSubmatch(r); m != nil {
		r = m[1] + "/" + m[2]
	} else if i := strings.Index(r, "://"); i >= 0 {
		scheme := r[:i]
		r = r[i+3:]
		if scheme == "file" {
			return strings.TrimSuffix(strings.TrimRight(r, "/"), ".git")
		}
		if at := strings.LastIndex(strings.SplitN(r, "/", 2)[0], "@"); at >= 0 {
			r = r[at+1:]
		}
	}
	r = strings.TrimRight(r, "/")
	r = strings.TrimSuffix(r, ".git")
	return strings.ToLower(r)
}

// githubSlug is owner/name for an address on github.com, else "".
func githubSlug(remote string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`^https://github\.com/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`),
		regexp.MustCompile(`^ssh://(?:[a-z_][a-z0-9_-]*@)?github\.com/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`),
		regexp.MustCompile(`^[a-z_][a-z0-9_-]*@github\.com:([A-Za-z0-9._-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`),
	} {
		if m := re.FindStringSubmatch(remote); m != nil {
			return m[1]
		}
	}
	return ""
}

// ghRun runs gh with prompts and colour off.
func ghRun(ctx context.Context, timeout time.Duration, gh, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, gh, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if cctx.Err() != nil && err != nil {
		return string(out), fmt.Errorf("it took longer than %s and was stopped", timeout)
	}
	return string(out), err
}

func (l *Link) gitIn(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return l.opt.Git(cctx, "git", append([]string{"-C", dir}, args...)...)
}

func (l *Link) defaultBranch(ctx context.Context, dir string) string {
	out, err := l.gitIn(ctx, 30*time.Second, dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (l *Link) cloneProject(ctx context.Context, p api.PendingCodebaseRequest) api.CodebaseReport {
	dir, err := l.projectDir(p.Name)
	if err != nil {
		return projectFailed("%v", err)
	}
	if p.Remote == "" {
		return projectFailed("the project has no repository address yet")
	}
	if p.Hosted {
		if err := l.hostedRemoteOK(p.Remote); err != nil {
			return projectFailed("%v", err)
		}
	}
	state, why := l.existing(ctx, dir, p.Remote)
	switch state {
	case "other":
		return projectFailed("%s", why)
	case "same":
		if p.Hosted {
			if err := l.configureHosted(ctx, dir, p.Remote); err != nil {
				return projectFailed("%v", err)
			}
		}
		return api.CodebaseReport{Status: "done", Path: dir, Base: l.defaultBranch(ctx, dir), Detail: "this machine already had it"}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return projectFailed("could not make the folder %s: %v", filepath.Dir(dir), err)
	}
	how := "git"
	var out string
	if p.Hosted {
		how = "git, with this machine's credential for the hub"
		if out, err = l.cloneHosted(ctx, p.Remote, dir); err != nil {
			return projectFailed("this machine could not get the project from the hub (is its link joined and not revoked?): %s", gitMessage(out, err))
		}
		return api.CodebaseReport{Status: "done", Path: dir, Base: l.defaultBranch(ctx, dir), Detail: "fetched with " + how}
	}
	if slug := githubSlug(p.Remote); slug != "" {
		if gh, lerr := exec.LookPath("gh"); lerr == nil {
			how = "gh"
			out, err = ghRun(ctx, cloneTimeout, gh, filepath.Dir(dir), "repo", "clone", slug, dir)
		}
	}
	if how == "git" {
		var cctx context.Context
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, cloneTimeout)
		out, err = l.opt.Git(cctx, "git", "clone", "--", p.Remote, dir)
		cancel()
	}
	if err != nil {
		return projectFailed("%s could not get the repository (is this machine logged in with access to it? for GitHub run `gh auth login`): %s", how, gitMessage(out, err))
	}
	return api.CodebaseReport{Status: "done", Path: dir, Base: l.defaultBranch(ctx, dir), Detail: "fetched with " + how}
}

func (l *Link) createProject(ctx context.Context, p api.PendingCodebaseRequest) api.CodebaseReport {
	dir, err := l.projectDir(p.Name)
	if err != nil {
		return projectFailed("%v", err)
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return projectFailed("%s", noGHText)
	}
	if _, serr := os.Lstat(dir); serr == nil {
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			return projectFailed("%s already exists and is not empty; move it away or choose another name (nothing was changed)", dir)
		}
		return projectFailed("%s already exists; move it away or choose another name (nothing was changed)", dir)
	}
	root := filepath.Dir(dir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return projectFailed("could not make the folder %s: %v", root, err)
	}
	target := p.Name
	if p.Owner != "" {
		target = p.Owner + "/" + p.Name
	}
	vis := "--public"
	if p.Private {
		vis = "--private"
	}
	if out, err := ghRun(ctx, createTimeout, gh, root, "repo", "create", target, vis, "--clone"); err != nil {
		return projectFailed("gh could not create the repository (run `gh auth status` on this machine; the account needs the right to create repositories%s): %s", ownerHint(p.Owner), gitMessage(out, err))
	}
	if st, _ := os.Stat(filepath.Join(dir, ".git")); st == nil {
		return projectFailed("gh said it created the repository but %s is not a git clone", dir)
	}
	origin, err := l.gitIn(ctx, 30*time.Second, dir, "remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(origin) == "" {
		return projectFailed("the repository was created but the copy at %s has no origin", dir)
	}
	remote := strings.TrimSpace(origin)
	rep := api.CodebaseReport{Status: "done", Path: dir, Remote: remote}
	var notes []string

	// An empty repository has no branch to start jobs from: make the first commit and push it.
	if _, err := l.gitIn(ctx, 30*time.Second, dir, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		readme := filepath.Join(dir, "README.md")
		if _, serr := os.Stat(readme); serr != nil {
			if werr := os.WriteFile(readme, []byte("# "+p.Name+"\n"), 0o644); werr != nil {
				notes = append(notes, "could not write README.md: "+werr.Error())
			}
		}
		ident := []string{}
		if out, _ := l.gitIn(ctx, 30*time.Second, dir, "config", "user.email"); strings.TrimSpace(out) == "" {
			ident = []string{"-c", "user.name=Handloom", "-c", "user.email=handloom@localhost"}
		}
		if out, err := l.gitIn(ctx, createTimeout, dir, "add", "README.md"); err != nil {
			notes = append(notes, "could not stage README.md: "+gitMessage(out, err))
		} else if out, err := l.gitIn(ctx, createTimeout, dir, append(ident, "commit", "-q", "-m", "Start the project", "--", "README.md")...); err != nil {
			notes = append(notes, "could not make the first commit: "+gitMessage(out, err))
		} else if branch := l.defaultBranch(ctx, dir); branch == "" {
			notes = append(notes, "could not tell which branch the first commit is on")
		} else if out, err := l.gitIn(ctx, createTimeout, dir, "push", "-q", "-u", "origin", "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
			notes = append(notes, "the first commit is made but pushing it failed: "+gitMessage(out, err)+"; push it by hand in "+dir)
		}
	}
	rep.Base = l.defaultBranch(ctx, dir)
	rep.Detail = "created with gh"
	if len(notes) > 0 {
		rep.Detail += "; " + strings.Join(notes, "; ")
	}
	return rep
}

func ownerHint(owner string) string {
	if owner == "" {
		return ""
	}
	return " in " + owner
}
