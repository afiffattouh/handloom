package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

func cliGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareWithCommit is a local bare repository with one commit, standing for a shared remote.
func bareWithCommit(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "shared.git")
	cliGit(t, t.TempDir(), "init", "-q", "--bare", bare)
	seed := filepath.Join(t.TempDir(), "seed")
	cliGit(t, t.TempDir(), "init", "-q", seed)
	os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644)
	cliGit(t, seed, "add", ".")
	cliGit(t, seed, "commit", "-q", "-m", "first")
	branch := cliGit(t, seed, "symbolic-ref", "--short", "HEAD")
	cliGit(t, seed, "push", "-q", bare, "HEAD")
	cliGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return "file://" + bare
}

func fastWaits(t *testing.T) {
	t.Helper()
	oldPoll, oldClone, oldCreate := projectPoll, cloneWait, createWait
	projectPoll, cloneWait, createWait = 50*time.Millisecond, 20*time.Second, 20*time.Second
	t.Cleanup(func() { projectPoll, cloneWait, createWait = oldPoll, oldClone, oldCreate })
}

func TestProjectsThroughTheCLI(t *testing.T) {
	fastWaits(t)
	r := newRig(t)
	if out := r.as(r.human, "project", "list"); !strings.Contains(out, "No projects yet") {
		t.Fatalf("empty list: %s", out)
	}
	remote := bareWithCommit(t)

	// Attach, and have the machine fetch it in the same command.
	out := r.as(r.human, "project", "new", "shared", "--remote", remote, "--device", "dev")
	home := os.Getenv("HANDLOOM_HOME")
	dir := filepath.Join(home, "projects", "shared")
	if !strings.Contains(out, "Attached project shared: "+remote) || !strings.Contains(out, "dev has its copy at "+dir) {
		t.Fatalf("new --remote --device: %s", out)
	}
	if cliGit(t, dir, "remote", "get-url", "origin") != remote {
		t.Fatal("the copy has the wrong origin")
	}
	list := r.as(r.human, "project", "list")
	if !strings.Contains(list, "shared") || !strings.Contains(list, "dev ready") {
		t.Fatalf("list: %s", list)
	}
	show := r.as(r.human, "project", "show", "shared")
	if !strings.Contains(show, "dev: ready at "+dir) {
		t.Fatalf("show: %s", show)
	}
	var js api.Codebase
	if err := json.Unmarshal([]byte(r.as(r.human, "project", "show", "shared", "--json")), &js); err != nil || js.Name != "shared" || len(js.Clones) != 1 {
		t.Fatalf("show --json: %v %+v", err, js)
	}
	if out := r.as(r.human, "project", "clone", "shared", "--device", "dev"); !strings.Contains(out, "ready on dev at "+dir) {
		t.Fatalf("clone of a ready one: %s", out)
	}

	// A job starts from the project with no cloning by hand; the base comes from the project.
	started := r.as(r.human, "job", "new", "Work on it", "--project", "shared", "--device", "dev")
	if !strings.Contains(started, "Started job #1") || !strings.Contains(started, "[project shared, "+dir+" on dev]") {
		t.Fatalf("job new --project: %s", started)
	}
	if show := r.as(r.human, "job", "show", "1"); !strings.Contains(show, "project shared") {
		t.Fatalf("job show: %s", show)
	}
	if list := r.as(r.human, "job", "list"); !strings.Contains(list, "project shared") {
		t.Fatalf("job list: %s", list)
	}

	// A project the machine has no copy of yet: job new fetches it first, and says so.
	remote2 := bareWithCommit(t)
	r.as(r.human, "project", "new", "second", "--remote", remote2)
	r.t.Setenv("HANDLOOM_HUB", r.hubURL)
	r.t.Setenv("HANDLOOM_TOKEN", r.human)
	code, out2, errs := r.exec("job", "new", "Auto fetch", "--project", "second", "--device", "dev")
	r.t.Setenv("HANDLOOM_TOKEN", "")
	if code != 0 || !strings.Contains(errs, "Getting project second onto dev") || !strings.Contains(out2, "Started job #2") {
		t.Fatalf("job new fetching first: %d %s %s", code, out2, errs)
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "second", "a.txt")); err != nil {
		t.Fatalf("the project was not fetched: %v", err)
	}

	// Refusals are in words and exit non-zero.
	r.t.Setenv("HANDLOOM_HUB", r.hubURL)
	r.t.Setenv("HANDLOOM_TOKEN", r.human)
	defer r.t.Setenv("HANDLOOM_TOKEN", "")
	for _, c := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"project", "new", "bad", "--remote", "https://user:secret@github.com/a/b"}, "password or token", 1},
		{[]string{"project", "new", "bad", "--remote", "/home/me/repo"}, "not a shared address", 1},
		{[]string{"project", "new", "bad"}, "usage", 2},
		{[]string{"project", "new", "bad", "--remote", "https://github.com/a/b", "--create"}, "usage", 2},
		{[]string{"project", "new", "bad", "--remote", "https://github.com/a/b", "--public"}, "go with --create", 2},
		{[]string{"project", "show", "nothing"}, "no project", 1},
		{[]string{"project", "clone", "nothing", "--device", "dev"}, "no project", 1},
		{[]string{"project", "frobnicate"}, "usage", 2},
		{[]string{"job", "new", "x", "--project", "shared", "--repo", "/srv/app", "--device", "dev"}, "not both", 2},
		{[]string{"job", "new", "x", "--project", "nothing", "--device", "dev"}, "no project", 1},
	} {
		code, _, errs := r.exec(c.args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%v: exit %d, %q (want exit %d with %q)", c.args, code, errs, c.code, c.want)
		}
	}
	if strings.Contains(r.as(r.human, "audit"), "secret") {
		t.Error("a refused credential reached the audit log")
	}
}

func TestProjectCreateThroughTheCLI(t *testing.T) {
	fastWaits(t)
	r := newRig(t)
	remotes := t.TempDir()
	bin := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s ' \"$@\" >> " + argsFile + "\nname=\"${3##*/}\"\ngit init -q --bare \"" + remotes + "/$name.git\" || exit 1\ngit clone -q \"file://" + remotes + "/$name.git\" \"$name\" 2>/dev/null\n"
	gitPath, _ := exec.LookPath("git")
	os.Symlink(gitPath, filepath.Join(bin, "git"))

	// No gh: said in words, with a failing exit.
	r.t.Setenv("PATH", bin)
	r.t.Setenv("HANDLOOM_HUB", r.hubURL)
	r.t.Setenv("HANDLOOM_TOKEN", r.human)
	code, _, errs := r.exec("project", "new", "fresh", "--create", "--device", "dev")
	if code != 1 || !strings.Contains(errs, "install the GitHub command line tool (gh)") {
		t.Fatalf("no gh: %d %s", code, errs)
	}

	os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755)
	code, out, errs := r.exec("project", "new", "fresh", "--create", "--owner", "acme", "--public", "--device", "dev")
	if code != 0 || !strings.Contains(out, "Created project fresh: file://"+remotes+"/fresh.git") || !strings.Contains(out, "dev has its copy at ") {
		t.Fatalf("create: %d %s %s", code, out, errs)
	}
	if got, _ := os.ReadFile(argsFile); strings.TrimSpace(string(got)) != "repo create acme/fresh --public --clone" {
		t.Fatalf("gh was called with %q", got)
	}
	// The next job starts from it.
	if started := r.as(r.human, "job", "new", "First work", "--project", "fresh", "--device", "dev"); !strings.Contains(started, "[project fresh, ") {
		t.Fatalf("job: %s", started)
	}
}
