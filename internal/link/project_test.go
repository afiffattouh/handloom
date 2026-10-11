package link

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/api"
)

// projectFixture is a link whose projects folder is a temporary one, and a bare repository
// with one commit that stands for the shared remote.
type projectFixture struct {
	*fixture
	root, bare, remote, branch string
}

func newProjectFixture(t *testing.T) *projectFixture {
	t.Helper()
	f := newFixture(t)
	pf := &projectFixture{fixture: f, root: filepath.Join(t.TempDir(), "projects")}
	f.link.opt.ProjectsRoot = pf.root
	pf.bare = filepath.Join(t.TempDir(), "shared.git")
	gitOut(t, t.TempDir(), "init", "-q", "--bare", pf.bare)
	seed := filepath.Join(t.TempDir(), "seed")
	gitOut(t, t.TempDir(), "init", "-q", seed)
	gitOut(t, seed, "config", "user.email", "t@example.test")
	gitOut(t, seed, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, seed, "add", ".")
	gitOut(t, seed, "commit", "-q", "-m", "first")
	pf.branch = gitOut(t, seed, "symbolic-ref", "--short", "HEAD")
	gitOut(t, seed, "push", "-q", pf.bare, "HEAD")
	gitOut(t, pf.bare, "symbolic-ref", "HEAD", "refs/heads/"+pf.branch)
	pf.remote = "file://" + pf.bare
	return pf
}

func (pf *projectFixture) attach(name, remote string) {
	pf.t.Helper()
	pf.must(pf.human.Post("/v1/codebases", api.CodebaseReq{Name: name, Remote: remote}, nil))
}

// clone asks as a person and lets the link act; it returns the project as the hub shows it.
func (pf *projectFixture) clone(name string) api.CodebaseClone {
	pf.t.Helper()
	var q api.CodebaseRequest
	pf.must(pf.human.Post("/v1/codebases/"+name+"/clone", api.CodebaseCloneReq{Device: "dev"}, &q))
	if q.Status == "pending" {
		pf.link.runProjectRequests(context.Background())
	}
	return pf.state(name)
}

func (pf *projectFixture) state(name string) api.CodebaseClone {
	pf.t.Helper()
	var cb api.Codebase
	pf.must(pf.human.Get("/v1/codebases/"+name, &cb))
	if len(cb.Clones) != 1 {
		pf.t.Fatalf("clones: %+v", cb.Clones)
	}
	if cb.Clones[0].Status == "pending" {
		pf.t.Fatalf("the link did not act on the request: %+v", cb.Clones[0])
	}
	return cb.Clones[0]
}

// fakeBin puts scripts on PATH (and nothing else of the real PATH's gh).
func fakeBin(t *testing.T, scripts map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// git must stay reachable; gh must be only what the test says.
	git, err := lookGit()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	if _, has := scripts["gh"]; has {
		t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
		return
	}
	// no gh wanted: a machine that has one (a CI runner) must not leak it in,
	// so give the scripts only the tools they need
	for _, tool := range []string{"sh", "cat", "printf", "echo", "mkdir", "dirname", "env", "rm", "ls", "test", "head", "tr", "sed", "grep", "date", "sleep"} {
		if p, err := exec.LookPath(tool); err == nil {
			os.Symlink(p, filepath.Join(dir, tool))
		}
	}
	t.Setenv("PATH", dir)
}

func lookGit() (string, error) { return exec.LookPath("git") }

func TestCloneFromABareRemote(t *testing.T) {
	pf := newProjectFixture(t)
	fakeBin(t, nil) // no gh on this machine: git does it
	pf.attach("shared", pf.remote)
	c := pf.clone("shared")
	dir := filepath.Join(pf.root, "shared")
	if c.Status != "ready" || c.Path != dir {
		t.Fatalf("clone: %+v", c)
	}
	if got := gitOut(t, dir, "remote", "get-url", "origin"); got != pf.remote {
		t.Fatalf("origin %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(b) != "a\n" {
		t.Fatalf("the clone lacks the code: %v %q", err, b)
	}
	var cb api.Codebase
	pf.must(pf.human.Get("/v1/codebases/shared", &cb))
	if cb.Base != pf.branch {
		t.Fatalf("base %q, want %q", cb.Base, pf.branch)
	}
	if len(pf.audit("project.clone_done")) != 1 {
		t.Fatal("project.clone_done is not in the audit log")
	}

	// Asking again changes nothing: no second clone, local work is kept.
	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, dir, "rev-parse", "HEAD")
	// The hub answers "ready" itself for a second request; the link's own check is tried directly.
	rep := pf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{Name: "shared", Kind: "clone", Remote: pf.remote})
	if rep.Status != "done" || rep.Path != dir || !strings.Contains(rep.Detail, "already had it") || rep.Base != pf.branch {
		t.Fatalf("second clone: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, "mine.txt")); err != nil || gitOut(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("the existing copy was touched")
	}
}

func TestCloneNeverTouchesWhatIsInTheWay(t *testing.T) {
	pf := newProjectFixture(t)
	fakeBin(t, nil)
	other := filepath.Join(t.TempDir(), "other.git")
	gitOut(t, t.TempDir(), "init", "-q", "--bare", other)

	// A different repository already at the place.
	dir := filepath.Join(pf.root, "shared")
	gitOut(t, t.TempDir(), "clone", "-q", other, dir)
	if err := os.WriteFile(filepath.Join(dir, "precious.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pf.attach("shared", pf.remote)
	c := pf.clone("shared")
	if c.Status != "failed" || !strings.Contains(c.Detail, "different repository") || !strings.Contains(c.Detail, "nothing was changed") {
		t.Fatalf("different origin: %+v", c)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "precious.txt")); string(b) != "keep\n" {
		t.Fatal("the folder in the way was touched")
	}
	if got := gitOut(t, dir, "remote", "get-url", "origin"); got != other {
		t.Fatalf("origin changed to %q", got)
	}
	if len(pf.audit("project.clone_failed")) != 1 {
		t.Fatal("project.clone_failed is not in the audit log")
	}

	// A folder that is not a repository at all.
	plain := filepath.Join(pf.root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := pf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{Name: "plain", Kind: "clone", Remote: pf.remote})
	if rep.Status != "failed" || !strings.Contains(rep.Detail, "not a git repository") {
		t.Fatalf("plain folder: %+v", rep)
	}
	if b, _ := os.ReadFile(filepath.Join(plain, "notes.txt")); string(b) != "mine\n" {
		t.Fatal("the plain folder was touched")
	}

	// A folder inside another repository is not a repository of its own.
	inner := filepath.Join(dir, "sub")
	os.MkdirAll(inner, 0o755)
	os.WriteFile(filepath.Join(inner, "x"), []byte("x"), 0o644)
	// (the project name must be a single folder name, so reach it through a symlink-free check of the function)
	if state, _ := pf.link.existing(context.Background(), inner, pf.remote); state != "other" {
		t.Fatalf("a subfolder of another repository counted as the project: %q", state)
	}

	// A refused name never reaches the disk.
	if rep := pf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{Name: "../escape", Kind: "clone", Remote: pf.remote}); rep.Status != "failed" {
		t.Fatalf("escape: %+v", rep)
	}
	// A remote that cannot be reached fails in words and leaves nothing behind.
	rep = pf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{Name: "gone", Kind: "clone", Remote: "file://" + filepath.Join(t.TempDir(), "missing.git")})
	if rep.Status != "failed" || !strings.Contains(rep.Detail, "could not get the repository") {
		t.Fatalf("missing remote: %+v", rep)
	}
}

func TestCloneOfAGitHubProjectUsesGh(t *testing.T) {
	pf := newProjectFixture(t)
	args := filepath.Join(t.TempDir(), "args")
	// A fake gh that "clones" by making a repository whose origin is the address gh would configure.
	fakeBin(t, map[string]string{"gh": `printf '%s\n' "$@" > ` + args + `
dest="$4"
git init -q "$dest"
git -C "$dest" remote add origin "git@github.com:$3.git"
`})
	remote := "https://github.com/acme/billing.git"
	pf.attach("billing", remote)
	c := pf.clone("billing")
	dir := filepath.Join(pf.root, "billing")
	if c.Status != "ready" || c.Path != dir {
		t.Fatalf("clone: %+v", c)
	}
	got, _ := os.ReadFile(args)
	if string(got) != "repo\nclone\nacme/billing\n"+dir+"\n" {
		t.Fatalf("gh was called with %q", got)
	}
	// gh may configure ssh while the project says https: still the same repository.
	rep := pf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{Name: "billing", Kind: "clone", Remote: remote})
	if rep.Status != "done" || !strings.Contains(rep.Detail, "already had it") {
		t.Fatalf("second: %+v", rep)
	}
	// A failing gh is a failure in words.
	fakeBin(t, map[string]string{"gh": "echo 'HTTP 404: Not Found'; exit 1\n"})
	rep = pf.link.cloneProject(context.Background(), api.PendingCodebaseRequest{Name: "nope", Kind: "clone", Remote: "https://github.com/acme/nope"})
	if rep.Status != "failed" || !strings.Contains(rep.Detail, "Not Found") || !strings.Contains(rep.Detail, "gh auth login") {
		t.Fatalf("failing gh: %+v", rep)
	}
}

func TestGitHubAddressesAndRemoteKeys(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/billing":       "acme/billing",
		"https://github.com/acme/billing.git":   "acme/billing",
		"ssh://git@github.com/acme/billing.git": "acme/billing",
		"git@github.com:acme/billing.git":       "acme/billing",
		"https://gitlab.com/acme/billing":       "",
		"https://github.com.evil.test/acme/b":   "",
		"https://github.com/acme/billing/extra": "",
		"file:///tmp/x.git":                     "",
	} {
		if got := githubSlug(in); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", in, got, want)
		}
	}
	same := [][2]string{
		{"https://github.com/acme/billing.git", "git@github.com:acme/billing"},
		{"https://github.com/Acme/Billing/", "ssh://git@github.com/acme/billing.git"},
		{"file:///tmp/x.git", "file:///tmp/x"},
	}
	for _, p := range same {
		if !sameRemote(p[0], p[1]) {
			t.Errorf("%q and %q should be the same repository", p[0], p[1])
		}
	}
	if sameRemote("https://github.com/acme/billing", "https://github.com/acme/other") || sameRemote("https://github.com/a/b", "https://gitlab.com/a/b") {
		t.Error("different repositories compared equal")
	}
}

// fakeCreatingGH makes "gh repo create [owner/]name --private|--public --clone" work against local bare
// repositories: it records its arguments, makes <remotes>/<name>.git, and clones it into ./<name>.
func fakeCreatingGH(t *testing.T) (argsFile, remotes string) {
	t.Helper()
	remotes = t.TempDir()
	argsFile = filepath.Join(t.TempDir(), "args")
	fakeBin(t, map[string]string{"gh": `printf '%s ' "$@" >> ` + argsFile + `; echo >> ` + argsFile + `
[ "$1 $2" = "repo create" ] || exit 2
name="${3##*/}"
git init -q --bare "` + remotes + `/$name.git" || exit 1
git clone -q "file://` + remotes + `/$name.git" "$name" 2>/dev/null
echo "https://github.com/$3"
`})
	return argsFile, remotes
}

func TestCreateMakesTheRepositoryAndTheFirstCommit(t *testing.T) {
	pf := newProjectFixture(t)
	argsFile, remotes := fakeCreatingGH(t)

	private := true
	var q api.CodebaseRequest
	pf.must(pf.human.Post("/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Owner: "acme", Private: &private, Device: "dev"}, &q))
	pf.link.runProjectRequests(context.Background())

	var cb api.Codebase
	pf.must(pf.human.Get("/v1/codebases/fresh", &cb))
	dir := filepath.Join(pf.root, "fresh")
	wantRemote := "file://" + remotes + "/fresh.git"
	if cb.Remote != wantRemote || len(cb.Clones) != 1 || cb.Clones[0].Status != "ready" || cb.Clones[0].Path != dir || cb.Base == "" {
		t.Fatalf("after create: %+v", cb)
	}
	if got, _ := os.ReadFile(argsFile); strings.TrimSpace(string(got)) != "repo create acme/fresh --private --clone" {
		t.Fatalf("gh was called with %q", got)
	}
	// The first commit is on the remote: the base branch exists there.
	if got := gitOut(t, remotes+"/fresh.git", "show", cb.Base+":README.md"); got != "# fresh" {
		t.Fatalf("README on the remote: %q", got)
	}
	if gitOut(t, dir, "rev-parse", "HEAD") != gitOut(t, remotes+"/fresh.git", "rev-parse", cb.Base) {
		t.Fatal("the local copy and the remote differ after the first push")
	}
	if len(pf.audit("project.create_done")) != 1 {
		t.Fatal("project.create_done is not in the audit log")
	}

	// Public, no owner: the account gh is logged in as.
	no := false
	pf.must(pf.human.Post("/v1/codebases/create", api.CodebaseCreateReq{Name: "open", Private: &no, Device: "dev"}, &q))
	pf.link.runProjectRequests(context.Background())
	got, _ := os.ReadFile(argsFile)
	if !strings.HasSuffix(strings.TrimSpace(string(got)), "repo create open --public --clone") {
		t.Fatalf("gh was called with %q", got)
	}

	// A job can start from the new project at once, and publish still has an origin to push to.
	var j api.Job
	pf.must(pf.human.Post("/v1/jobs", api.JobNewReq{Title: "J", Codebase: "fresh", Device: "dev"}, &j))
	if j.Repo != dir || j.Base != cb.Base {
		t.Fatalf("job: %+v", j)
	}
	if gitOut(t, dir, "remote", "get-url", "origin") != wantRemote {
		t.Fatal("the clone has no origin")
	}
}

func TestCreateWithoutGhOrWithAFolderInTheWay(t *testing.T) {
	pf := newProjectFixture(t)
	fakeBin(t, nil) // no gh
	var q api.CodebaseRequest
	pf.must(pf.human.Post("/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Device: "dev"}, &q))
	pf.link.runProjectRequests(context.Background())
	var cb api.Codebase
	pf.must(pf.human.Get("/v1/codebases/fresh", &cb))
	if cb.Clones[0].Status != "failed" || !strings.Contains(cb.Clones[0].Detail, "install the GitHub command line tool (gh) and run `gh auth login` on this machine") || cb.Remote != "" {
		t.Fatalf("no gh: %+v", cb)
	}
	if _, err := os.Stat(filepath.Join(pf.root, "fresh")); err == nil {
		t.Fatal("a folder was made although nothing was created")
	}
	if len(pf.audit("project.create_failed")) != 1 {
		t.Fatal("project.create_failed is not in the audit log")
	}

	// With gh, a folder that is already there is never touched and gh is not even called.
	argsFile, _ := fakeCreatingGH(t)
	dir := filepath.Join(pf.root, "fresh")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0o644)
	pf.must(pf.human.Post("/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Device: "dev"}, &q)) // retry of a failed creation
	pf.link.runProjectRequests(context.Background())
	pf.must(pf.human.Get("/v1/codebases/fresh", &cb))
	if cb.Clones[0].Status != "failed" || !strings.Contains(cb.Clones[0].Detail, "already exists") {
		t.Fatalf("folder in the way: %+v", cb)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("gh was called although the folder was in the way")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "keep.txt")); string(b) != "keep\n" {
		t.Fatal("the folder in the way was touched")
	}

	// gh that fails: its words reach the person.
	os.RemoveAll(dir)
	fakeBin(t, map[string]string{"gh": "echo 'HTTP 403: not allowed to create repositories in acme'; exit 1\n"})
	pf.must(pf.human.Post("/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Owner: "acme", Device: "dev"}, &q))
	pf.link.runProjectRequests(context.Background())
	pf.must(pf.human.Get("/v1/codebases/fresh", &cb))
	if cb.Clones[0].Status != "failed" || !strings.Contains(cb.Clones[0].Detail, "not allowed to create") {
		t.Fatalf("failing gh: %+v", cb)
	}
}

func TestAnAgentCannotReachTheProjectRequestEndpoints(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/device/project-requests"}, {"POST", "/v1/project-requests/1/report"}} {
		var err error
		if c.method == "GET" {
			err = f.worker.Get(c.path, nil)
		} else {
			err = f.worker.Post(c.path, api.CodebaseReport{Status: "done"}, nil)
		}
		if err == nil || !strings.Contains(err.Error(), "only the link") {
			t.Fatalf("%s %s from an agent: %v", c.method, c.path, err)
		}
	}
}
