package link

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/api"
)

// publishFixture is a repo job with one accepted, merged task, a clone with a bare
// repository next to it as its remote "origin", and the base branch pushed there.
type publishFixture struct {
	*fixture
	repo, bare, base, integ string
	job                     int64
}

func newPublishFixture(t *testing.T, withRemote bool) *publishFixture {
	t.Helper()
	f := newFixture(t)
	ac, _, task := f.scopedAgentVerify(nil, "true")
	var jobs []api.Job
	f.must(f.human.Get("/v1/jobs", &jobs))
	pf := &publishFixture{fixture: f, repo: jobs[0].Repo, job: jobs[0].ID}
	pf.base = gitOut(t, pf.repo, "symbolic-ref", "--short", "HEAD")
	pf.integ = fmt.Sprintf("job/%d/integration", pf.job)
	wt := filepath.Join(f.link.opt.WorkRoot, fmt.Sprintf("job-%d", pf.job), "alpha")
	f.commitIn(wt, "src/new.txt", "new\n", "add new")
	f.must(f.submit(ac, task))
	f.waitCheck(task.ID)
	if m := f.acceptAndMerge(task.ID); m.Status != "merged" {
		t.Fatalf("merge: %+v", m)
	}
	if withRemote {
		pf.bare = filepath.Join(t.TempDir(), "remote.git")
		gitOut(t, t.TempDir(), "init", "-q", "--bare", pf.bare)
		gitOut(t, pf.repo, "remote", "add", "origin", pf.bare)
		gitOut(t, pf.repo, "push", "-q", "origin", pf.base)
	}
	return pf
}

// publish asks as a person and lets the link act; it returns the request as the hub shows it.
func (pf *publishFixture) publish(req api.PublishReq) api.Publish {
	pf.t.Helper()
	var p api.Publish
	pf.must(pf.human.Post(fmt.Sprintf("/v1/jobs/%d/publish", pf.job), req, &p))
	pf.ladder()
	var all []api.Publish
	pf.must(pf.human.Get(fmt.Sprintf("/v1/jobs/%d/publishes", pf.job), &all))
	for _, x := range all {
		if x.ID == p.ID {
			if x.Status == "pending" {
				pf.t.Fatalf("the link did not act on the request: %+v", x)
			}
			return x
		}
	}
	pf.t.Fatalf("publish %d not listed", p.ID)
	return p
}

// remoteBranch is where a branch is on the bare remote, or "" if it is not there.
func (pf *publishFixture) remoteBranch(branch string) string {
	out, err := execGit(nil2(), "git", "-C", pf.bare, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// anotherClone is a teammate's clone of the remote.
func (pf *publishFixture) anotherClone() string {
	dir := filepath.Join(pf.t.TempDir(), "mate")
	gitOut(pf.t, pf.t.TempDir(), "clone", "-q", pf.bare, dir)
	return dir
}

func fakeGH(t *testing.T, output string, fail bool) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	exit := "0"
	if fail {
		exit = "1"
	}
	script := "#!/bin/sh\n[ \"$2\" = view ] || for a in \"$@\"; do printf '%s\\n' \"$a\"; printf '\\000'; done > " + argsFile + "\ncat <<'EOF'\n" + output + "\nEOF\nexit " + exit + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func readArgs(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("gh was not called: %v", err)
	}
	var out []string
	for _, a := range strings.Split(string(b), "\x00") {
		if a != "" {
			out = append(out, strings.TrimSuffix(a, "\n"))
		}
	}
	return out
}

func TestPublishPushesTheIntegrationBranchUnderANameOfItsOwn(t *testing.T) {
	pf := newPublishFixture(t, true)
	p := pf.publish(api.PublishReq{})
	branch := fmt.Sprintf("handloom/job-%d-fix-it", pf.job)
	if p.Status != "done" || p.Branch != branch || p.URL != "" || p.Behind != 0 || !strings.Contains(p.Detail, "pushed "+pf.integ+" to origin as "+branch) {
		t.Fatalf("publish: %+v", p)
	}
	if got, want := pf.remoteBranch(branch), gitOut(t, pf.repo, "rev-parse", pf.integ); got != want {
		t.Fatalf("the remote has %q, the integration branch is %q", got, want)
	}
	if got := gitOut(t, pf.bare, "show", branch+":src/new.txt"); got != "new" {
		t.Fatalf("the pushed branch lacks the job's work: %q", got)
	}
	if got, was := pf.remoteBranch(pf.base), gitOut(t, pf.repo, "rev-parse", pf.base); got != was {
		t.Fatal("the base branch on the remote was touched")
	}
	if len(pf.audit("job.published")) != 1 {
		t.Fatal("job.published is not in the audit log")
	}
	// Again with nothing new is fine; with more merged work it fast-forwards.
	if p := pf.publish(api.PublishReq{}); p.Status != "done" {
		t.Fatalf("a second publish of the same work: %+v", p)
	}
}

func TestPublishNeverForces(t *testing.T) {
	pf := newPublishFixture(t, true)
	branch := fmt.Sprintf("handloom/job-%d-fix-it", pf.job)
	if p := pf.publish(api.PublishReq{}); p.Status != "done" {
		t.Fatalf("first: %+v", p)
	}
	// A teammate replaces the branch on the remote with something that is not an ancestor of ours.
	mate := pf.anotherClone()
	gitOut(t, mate, "checkout", "-q", "-b", "other", "origin/"+pf.base)
	os.WriteFile(filepath.Join(mate, "theirs.txt"), []byte("theirs\n"), 0o644)
	gitOut(t, mate, "add", ".")
	gitOut(t, mate, "-c", "user.name=m", "-c", "user.email=m@m", "commit", "-q", "-m", "theirs")
	gitOut(t, mate, "push", "-q", "--force", "origin", "other:refs/heads/"+branch)
	theirs := pf.remoteBranch(branch)

	p := pf.publish(api.PublishReq{})
	if p.Status != "failed" || !strings.Contains(p.Detail, "nothing was forced") || !strings.Contains(strings.ToLower(p.Detail), "rejected") {
		t.Fatalf("a diverged remote branch: %+v", p)
	}
	if pf.remoteBranch(branch) != theirs {
		t.Fatal("the remote branch was overwritten")
	}
	if len(pf.audit("job.publish_failed")) != 1 {
		t.Fatal("job.publish_failed is not in the audit log")
	}
}

func TestPublishSaysWhenTheBaseMoved(t *testing.T) {
	pf := newPublishFixture(t, true)
	mate := pf.anotherClone()
	for i := 1; i <= 2; i++ {
		os.WriteFile(filepath.Join(mate, fmt.Sprintf("m%d.txt", i)), []byte("x\n"), 0o644)
		gitOut(t, mate, "add", ".")
		gitOut(t, mate, "-c", "user.name=m", "-c", "user.email=m@m", "commit", "-q", "-m", fmt.Sprintf("mate %d", i))
	}
	gitOut(t, mate, "push", "-q", "origin", "HEAD:refs/heads/"+pf.base)
	p := pf.publish(api.PublishReq{})
	want := fmt.Sprintf("the base moved: 2 new commits on origin/%s; the pull request may conflict", pf.base)
	if p.Status != "done" || p.Behind != 2 || !strings.Contains(p.Detail, want) {
		t.Fatalf("publish: %+v (want %q)", p, want)
	}
	// The local base branch itself was not moved.
	if gitOut(t, pf.repo, "rev-list", "--count", pf.base+"..origin/"+pf.base) != "2" {
		t.Fatal("unexpected state of the base branch")
	}
}

func TestPublishOpensAPullRequestWithTheBriefAndTheTasksButNoEvidence(t *testing.T) {
	pf := newPublishFixture(t, true)
	args := fakeGH(t, "Creating pull request for the branch\nhttps://github.com/acme/app/pull/12", false)
	p := pf.publish(api.PublishReq{PR: true})
	branch := fmt.Sprintf("handloom/job-%d-fix-it", pf.job)
	if p.Status != "done" || p.URL != "https://github.com/acme/app/pull/12" {
		t.Fatalf("publish: %+v", p)
	}
	got := readArgs(t, args)
	if len(got) != 10 || strings.Join(got[:2], " ") != "pr create" {
		t.Fatalf("gh was called with %q", got)
	}
	flag := map[string]string{}
	for i := 2; i+1 < len(got); i += 2 {
		flag[got[i]] = got[i+1]
	}
	if flag["--head"] != branch || flag["--base"] != pf.base || flag["--title"] != "Fix it" {
		t.Fatalf("gh flags: %v", flag)
	}
	body := flag["--body"]
	if !strings.Contains(body, "Make src better.") || !strings.Contains(body, "change src (alpha)") {
		t.Fatalf("the pull request body lacks the brief or the task list: %q", body)
	}
	if strings.Contains(body, "file:x") {
		t.Fatalf("evidence in the pull request body: %q", body)
	}
	// gh fails: the branch is still pushed and the problem is in words.
	fakeGH(t, "gh: not logged in", true)
	p = pf.publish(api.PublishReq{PR: true})
	if p.Status != "done" || p.URL != "" || !strings.Contains(p.Detail, "gh could not open the pull request") || !strings.Contains(p.Detail, "not logged in") {
		t.Fatalf("gh failing: %+v", p)
	}
	// A pull request that exists already is reported with its link.
	fakeGH(t, "a pull request for branch already exists:\nhttps://github.com/acme/app/pull/12", true)
	if p = pf.publish(api.PublishReq{PR: true}); p.Status != "done" || p.URL != "https://github.com/acme/app/pull/12" || !strings.Contains(p.Detail, "already exists") {
		t.Fatalf("existing pull request: %+v", p)
	}
}

func TestPublishWithoutGHPushesAndSaysSo(t *testing.T) {
	pf := newPublishFixture(t, true)
	bin := t.TempDir() // a PATH with git and nothing else: no gh
	git, err := exec_LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	p := pf.publish(api.PublishReq{PR: true})
	branch := fmt.Sprintf("handloom/job-%d-fix-it", pf.job)
	if p.Status != "done" || p.URL != "" || !strings.Contains(p.Detail, "gh is not installed: open the pull request from "+branch) {
		t.Fatalf("publish without gh: %+v", p)
	}
	if pf.remoteBranch(branch) == "" {
		t.Fatal("the branch was not pushed")
	}
}

func TestPublishWithoutAUsableRemoteFailsInWords(t *testing.T) {
	pf := newPublishFixture(t, false)
	p := pf.publish(api.PublishReq{})
	if p.Status != "failed" || !strings.Contains(p.Detail, "has no remote to publish to") || !strings.Contains(p.Detail, "git -C "+pf.repo+" remote add origin") {
		t.Fatalf("no remote: %+v", p)
	}
	pf.bare = filepath.Join(t.TempDir(), "remote.git")
	gitOut(t, t.TempDir(), "init", "-q", "--bare", pf.bare)
	gitOut(t, pf.repo, "remote", "add", "origin", pf.bare)
	p = pf.publish(api.PublishReq{Remote: "upstream"})
	if p.Status != "failed" || !strings.Contains(p.Detail, `"upstream" is not a remote`) || !strings.Contains(p.Detail, "origin") {
		t.Fatalf("unknown remote: %+v", p)
	}
	// A fetch that fails (the remote vanished) does not stop the publish by itself; the push then fails in git's words.
	os.RemoveAll(pf.bare)
	p = pf.publish(api.PublishReq{})
	if p.Status != "failed" || p.Detail == "" {
		t.Fatalf("remote gone: %+v", p)
	}
}

func TestPublishNeedsTheIntegrationBranch(t *testing.T) {
	pf := newPublishFixture(t, true)
	gitOut(t, filepath.Join(pf.link.opt.WorkRoot, fmt.Sprintf("job-%d", pf.job), "_integration"), "checkout", "-q", "--detach")
	gitOut(t, pf.repo, "branch", "-D", pf.integ)
	p := pf.publish(api.PublishReq{})
	if p.Status != "failed" || !strings.Contains(p.Detail, "no branch "+pf.integ) {
		t.Fatalf("missing integration branch: %+v", p)
	}
}

func TestAnAgentCannotReachThePublishEndpoints(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/device/publishes"}, {"POST", "/v1/publishes/1/report"}} {
		var err error
		if c.method == "GET" {
			err = f.worker.Get(c.path, nil)
		} else {
			err = f.worker.Post(c.path, api.PublishReport{Status: "done"}, nil)
		}
		if err == nil || !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "only the link") {
			t.Fatalf("%s %s from an agent: %v", c.method, c.path, err)
		}
	}
}

func exec_LookPath(name string) (string, error) { return exec.LookPath(name) }

func nil2() context.Context { return context.Background() }

func TestPublishRefusesABranchThatCarriesAnAgentsToken(t *testing.T) {
	pf := newPublishFixture(t, true)
	// the integration branch is checked out in a worktree of its own; commit the token file there
	var wt string
	var cur string
	for _, l := range strings.Split(gitOut(t, pf.repo, "worktree", "list", "--porcelain"), "\n") {
		if strings.HasPrefix(l, "worktree ") {
			cur = strings.TrimPrefix(l, "worktree ")
		}
		if l == "branch refs/heads/"+pf.integ {
			wt = cur
		}
	}
	if wt == "" {
		t.Fatal("no worktree for the integration branch")
	}
	if err := os.MkdirAll(filepath.Join(wt, ".handloom", "tokens"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".handloom", "tokens", "alpha"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wt, "add", "-f", ".handloom")
	gitOut(t, wt, "-c", "user.name=a", "-c", "user.email=a@a", "commit", "-q", "-m", "oops")
	p := pf.publish(api.PublishReq{})
	if p.Status != "failed" || !strings.Contains(p.Detail, "access token") || !strings.Contains(p.Detail, "git rm -r --cached .handloom") {
		t.Fatalf("publish: %+v", p)
	}
	if pf.remoteBranch(fmt.Sprintf("handloom/job-%d-fix-it", pf.job)) != "" {
		t.Fatal("the branch with the token was pushed")
	}
}

func TestWorktreeFilesOfHandloomAreIgnoredByGit(t *testing.T) {
	pf := newPublishFixture(t, true)
	git := func(args ...string) (string, error) {
		return execGit(nil2(), "git", append([]string{"-C", pf.repo}, args...)...)
	}
	(&Link{}).excludeAdapterFiles(git)
	(&Link{}).excludeAdapterFiles(git) // twice adds nothing
	for _, f := range []string{".handloom/tokens/alpha", "CLAUDE.md", ".claude/settings.local.json"} {
		p := filepath.Join(pf.repo, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o600)
	}
	gitOut(t, pf.repo, "add", "-A")
	if staged := gitOut(t, pf.repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("git add -A staged Handloom's files: %q", staged)
	}
	b, _ := os.ReadFile(filepath.Join(pf.repo, ".git", "info", "exclude"))
	if strings.Count(string(b), "/.handloom/") != 1 {
		t.Fatalf("exclude file: %q", b)
	}
}
