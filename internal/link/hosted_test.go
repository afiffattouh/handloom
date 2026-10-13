package link

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"handloom/internal/api"
	"handloom/internal/client"
)

// Hosted projects (D106) on the machine: the link clones with its own credential, sets the clone up so
// plain git works without a prompt, and never puts the credential in an address or a config file.

var (
	builtOnce sync.Once
	builtBin  string
	builtErr  error
)

// handloomBinary builds the real binary once; git runs it as the credential helper.
func handloomBinary(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		dir, err := os.MkdirTemp("", "handloom-bin-")
		if err != nil {
			builtErr = err
			return
		}
		builtBin = filepath.Join(dir, "handloom")
		out, err := exec.Command("go", "build", "-o", builtBin, "handloom/cmd/handloom").CombinedOutput()
		if err != nil {
			builtErr = &buildError{err, string(out)}
		}
	})
	if builtErr != nil {
		t.Fatalf("could not build the handloom binary: %v", builtErr)
	}
	return builtBin
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.out }

type hostedFixture struct {
	*fixture
	root string
	bin  string
}

func newHostedFixture(t *testing.T) *hostedFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := newFixture(t)
	hf := &hostedFixture{fixture: f, root: filepath.Join(t.TempDir(), "projects"), bin: handloomBinary(t)}
	f.link.opt.ProjectsRoot = hf.root
	f.link.opt.Binary = hf.bin
	// What `link join` saves, in the (temporary) HANDLOOM_HOME the helper reads.
	if err := client.SaveLinkConfig(&client.LinkConfig{Hub: f.url, Device: "dev", Credential: f.cred}); err != nil {
		t.Fatal(err)
	}
	f.must(f.human.Post("/v1/codebases/host", api.CodebaseHostReq{Name: "demo"}, nil))
	return hf
}

func (hf *hostedFixture) cloneDemo() api.CodebaseClone {
	hf.t.Helper()
	var q api.CodebaseRequest
	hf.must(hf.human.Post("/v1/codebases/demo/clone", api.CodebaseCloneReq{Device: "dev"}, &q))
	if q.Status == "pending" {
		hf.link.runProjectRequests(context.Background())
	}
	var cb api.Codebase
	hf.must(hf.human.Get("/v1/codebases/demo", &cb))
	if len(cb.Clones) != 1 || cb.Clones[0].Status == "pending" {
		hf.t.Fatalf("clones: %+v", cb.Clones)
	}
	return cb.Clones[0]
}

// plainGit runs git the way a person at a terminal does: no prompts, no helper of the test machine,
// and nothing but what the clone's own config says.
func plainGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustPlainGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := plainGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return out
}

func TestHostedCloneSetsUpItsOwnLogin(t *testing.T) {
	hf := newHostedFixture(t)
	c := hf.cloneDemo()
	dir := filepath.Join(hf.root, "demo")
	if c.Status != "ready" || c.Path != dir || !strings.Contains(c.Detail, "credential") {
		t.Fatalf("clone: %+v", c)
	}
	if got := mustPlainGit(t, dir, "remote", "get-url", "origin"); got != hf.url+"/git/demo.git" {
		t.Fatalf("origin %q", got)
	}
	// No credential anywhere a person or a log could read it: the address, git's config, the remote listing.
	cfg, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
	for what, text := range map[string]string{"git config": string(cfg), "remote -v": mustPlainGit(t, dir, "remote", "-v"), "config --list": mustPlainGit(t, dir, "config", "--list", "--show-origin")} {
		if strings.Contains(text, hf.cred) || strings.Contains(text, "@") && strings.Contains(text, "://") && strings.Contains(text, "hvd_") {
			t.Errorf("%s has the credential:\n%s", what, text)
		}
	}
	// The clone's config asks this binary for a login, for the hub's address only, after an empty entry that
	// hides every other helper on the machine.
	helpers := mustPlainGit(t, dir, "config", "--local", "--get-all", "credential."+hf.url+".helper") // the empty first entry is trimmed away
	if strings.Count(string(cfg), "helper =") != 2 || !strings.Contains(string(cfg), "helper = \n") || !strings.HasPrefix(helpers, "!") || !strings.Contains(helpers, hf.bin) || !strings.HasSuffix(helpers, "git-credential") {
		t.Fatalf("helper entries: %q in\n%s", helpers, cfg)
	}
	lines := []string{"", helpers}
	if strings.Contains(helpers, hf.cred) {
		t.Fatal("the helper line has the credential")
	}
	if !filepath.IsAbs(hf.bin) || !strings.Contains(lines[1], "HANDLOOM_HOME=") {
		t.Fatalf("the helper must name the binary by its full path and the home it reads: %q", lines[1])
	}

	// Plain git works with no prompt: fetch, and a push of a branch under handloom/, in the clone ...
	mustPlainGit(t, dir, "fetch", "-q", "origin")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustPlainGit(t, dir, "add", "-A")
	mustPlainGit(t, dir, "commit", "-q", "-m", "a")
	mustPlainGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/handloom/job-1-a")
	// ... and in a worktree of it (agents work in worktrees).
	wt := filepath.Join(t.TempDir(), "wt")
	mustPlainGit(t, dir, "worktree", "add", "-q", "-b", "job/1/work", wt)
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustPlainGit(t, wt, "add", "-A")
	mustPlainGit(t, wt, "commit", "-q", "-m", "b")
	mustPlainGit(t, wt, "push", "-q", "origin", "HEAD:refs/heads/handloom/job-2-b")
	mustPlainGit(t, wt, "fetch", "-q", "origin")
	// The hub's main is not theirs to move, and the refusal reads as a sentence.
	out, err := plainGit(t, dir, "push", "origin", "HEAD:refs/heads/main")
	if err == nil || !strings.Contains(out, "cannot be pushed to") {
		t.Fatalf("push to main: %v %s", err, out)
	}
	if strings.Contains(out, hf.cred) {
		t.Fatal("git printed the credential")
	}

	// Asking again keeps the copy, its work and exactly one helper entry.
	again := hf.cloneDemo()
	if again.Status != "ready" || again.Path != dir {
		t.Fatalf("again: %+v", again)
	}
	var cb api.Codebase
	hf.must(hf.human.Get("/v1/codebases/demo", &cb))
	hf.must(hf.human.Post("/v1/codebases/demo/clone", api.CodebaseCloneReq{Device: "dev"}, nil))
	cfg, _ = os.ReadFile(filepath.Join(dir, ".git", "config"))
	if n := strings.Count(string(cfg), "helper ="); n != 2 {
		t.Fatalf("helper entries after a second request: %d", n)
	}
}

func TestHostedCloneSetsUpAnExistingCopyAndLeavesOtherRepositoriesAlone(t *testing.T) {
	hf := newHostedFixture(t)
	dir := filepath.Join(hf.root, "demo")
	// A copy made by hand, without the helper: the next request sets the login up and fetches nothing else.
	if err := os.MkdirAll(hf.root, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-c", "http.extraHeader=Authorization: Basic "+basic(hf.cred), "clone", "-q", hf.url+"/git/demo.git", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("clone by hand: %v %s", err, out)
	}
	if _, err := plainGit(t, dir, "fetch", "-q", "origin"); err == nil {
		t.Fatal("a copy without the helper could fetch")
	}
	c := hf.cloneDemo()
	if c.Status != "ready" || !strings.Contains(c.Detail, "already had it") {
		t.Fatalf("existing copy: %+v", c)
	}
	mustPlainGit(t, dir, "fetch", "-q", "origin")
	// A repository that is not this machine's hub gets nothing from the helper: another origin has no entry.
	other := t.TempDir()
	mustPlainGit(t, other, "init", "-q")
	if out, err := plainGit(t, other, "config", "--local", "--get-all", "credential.helper"); err == nil {
		t.Fatalf("helper in another repository: %s", out)
	}
}

func basic(cred string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	in := []byte("anyone:" + cred)
	var out []byte
	for i := 0; i < len(in); i += 3 {
		var b [3]byte
		n := copy(b[:], in[i:])
		v := uint(b[0])<<16 | uint(b[1])<<8 | uint(b[2])
		out = append(out, tbl[v>>18&63], tbl[v>>12&63])
		if n > 1 {
			out = append(out, tbl[v>>6&63])
		} else {
			out = append(out, '=')
		}
		if n > 2 {
			out = append(out, tbl[v&63])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}

func TestHostedCloneRefusesAnAddressThatIsNotTheJoinedHub(t *testing.T) {
	hf := newHostedFixture(t)
	// The machine joined the hub as another name (here localhost, not 127.0.0.1): it will not send its
	// credential to an address it did not join, and says why.
	hf.link.opt.Hub = strings.Replace(hf.url, "127.0.0.1", "localhost", 1)
	c := hf.cloneDemo()
	if c.Status != "failed" || !strings.Contains(c.Detail, "not on the hub this machine joined") || !strings.Contains(c.Detail, "HANDLOOM_BASE_URL") {
		t.Fatalf("clone: %+v", c)
	}
	if strings.Contains(c.Detail, hf.cred) {
		t.Fatal("the failure repeats the credential")
	}
	if _, err := os.Stat(filepath.Join(hf.root, "demo")); err == nil {
		t.Fatal("something was cloned")
	}
}

func TestHostedCloneFailsInWordsForARevokedMachine(t *testing.T) {
	hf := newHostedFixture(t)
	hf.must(hf.admin.Post("/v1/admin/devices/dev/revoke", struct{}{}, nil))
	// The link's own calls now fail too, so ask the clone function directly.
	rep := hf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{ID: 1, Name: "demo", Kind: "clone", Remote: hf.url + "/git/demo.git", Hosted: true})
	if rep.Status != "failed" || !strings.Contains(rep.Detail, "could not get the project from the hub") {
		t.Fatalf("report: %+v", rep)
	}
	if strings.Contains(rep.Detail, hf.cred) {
		t.Fatal("the failure repeats the credential")
	}
	if _, err := os.Stat(filepath.Join(hf.root, "demo")); err == nil {
		t.Fatal("something was left behind")
	}
}

func TestHostedPublishTellsTheOwnerToMergeAndNeverUsesGH(t *testing.T) {
	hf := newHostedFixture(t)
	args := fakeGH(t, "https://example.test/pull/1", false)
	hf.cloneDemo()
	dir := filepath.Join(hf.root, "demo")
	mustPlainGit(t, dir, "checkout", "-q", "-b", "job/7/integration")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustPlainGit(t, dir, "add", "-A")
	mustPlainGit(t, dir, "commit", "-q", "-m", "new")
	rep := hf.link.publish(context.Background(), api.PendingPublish{ID: 1, Job: 7, Remote: "origin", Branch: "handloom/job-7-new", PR: true, Repo: dir, Base: "main", Title: "New", Hosted: true})
	if rep.Status != "done" || rep.URL != "" {
		t.Fatalf("report: %+v", rep)
	}
	if !strings.Contains(rep.Detail, "pushed; ask the owner to merge it with: handloom job merge 7, or the Merge button") {
		t.Fatalf("detail: %q", rep.Detail)
	}
	for _, bad := range []string{"gh", "no gh", "pull request"} {
		if strings.Contains(rep.Detail, bad) {
			t.Errorf("a hosted publish mentions %q: %q", bad, rep.Detail)
		}
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("gh was run for a hosted project")
	}
	if got := mustPlainGit(t, dir, "ls-remote", "origin", "refs/heads/handloom/job-7-new"); !strings.Contains(got, "refs/heads/handloom/job-7-new") {
		t.Fatalf("the branch is not on the hub: %q", got)
	}
	// A second publish of the same branch is a fast-forward or nothing; a rewritten one is refused in words.
	mustPlainGit(t, dir, "reset", "-q", "--hard", "HEAD~1")
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustPlainGit(t, dir, "add", "-A")
	mustPlainGit(t, dir, "commit", "-q", "-m", "different")
	rep = hf.link.publish(context.Background(), api.PendingPublish{ID: 2, Job: 7, Remote: "origin", Branch: "handloom/job-7-new", Repo: dir, Base: "main", Hosted: true})
	if rep.Status != "failed" || !strings.Contains(rep.Detail, "nothing was forced") {
		t.Fatalf("a rewrite: %+v", rep)
	}
}

func TestHostedFreshBaseSeesWhatTheOwnerMerged(t *testing.T) {
	hf := newHostedFixture(t)
	hf.cloneDemo()
	dir := filepath.Join(hf.root, "demo")
	// The owner merges something on the hub: here a commit lands on main inside the hub's repository.
	bare := filepath.Join(hf.dataDir, "git", "demo.git")
	mate := filepath.Join(t.TempDir(), "mate")
	mustPlainGit(t, t.TempDir(), "clone", "-q", bare, mate)
	if err := os.WriteFile(filepath.Join(mate, "from-mate.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustPlainGit(t, mate, "add", "-A")
	mustPlainGit(t, mate, "commit", "-q", "-m", "mate")
	mustPlainGit(t, bare, "fetch", "-q", mate, "main:refs/heads/main")
	rep := hf.link.updateBase(context.Background(), dir, "main")
	if rep.Status != api.BaseFresh || !strings.Contains(rep.Detail, "main moved forward 1 commit to ") {
		t.Fatalf("fresh base: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, "from-mate.txt")); err != nil {
		t.Fatal("the copy did not get the merged work")
	}
}
