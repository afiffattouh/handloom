package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		{[]string{"project", "new", "bad", "--public"}, "go with --create", 2},
		{[]string{"project", "new", "bad", "--remote", "https://github.com/a/b", "--create"}, "usage", 2},
		{[]string{"project", "new", "bad", "--remote", "https://github.com/a/b", "--public"}, "go with --create", 2},
		{[]string{"project", "show", "nothing"}, "no project", 1},
		{[]string{"project", "clone", "nothing", "--device", "dev"}, "no project", 1},
		{[]string{"project", "frobnicate"}, "usage", 2},
		{[]string{"job", "new", "x", "--project", "shared", "--repo", "/srv/app", "--device", "dev"}, "not both", 2},
		{[]string{"job", "new", "x", "--project", "shared", "--device", "dev", "--worker-profile", "p", "--lead-profile", "q"}, "not both", 1},
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

// A project the hub holds (D106): `project new NAME` with no address makes it, a machine gets its copy
// through its own credential, and plain git works in the copy with no prompt.
func TestHostedProjectThroughTheCLI(t *testing.T) {
	if testBinary(t) == "" {
		t.Skip("could not build the handloom binary (the credential helper)")
	}
	fastWaits(t)
	r := newRig(t)
	if out := r.as(r.human, "project", "new", "demo", "--device", "dev"); !strings.Contains(out, "Created project demo on the hub") || !strings.Contains(out, r.hubURL+"/git/demo.git") || !strings.Contains(out, "dev has its copy at") {
		t.Fatalf("new: %s", out)
	}
	dir := filepath.Join(os.Getenv("HANDLOOM_HOME"), "projects", "demo")
	if got := cliGit(t, dir, "remote", "get-url", "origin"); got != r.hubURL+"/git/demo.git" {
		t.Fatalf("origin %q", got)
	}
	list := r.as(r.human, "project", "list")
	show := r.as(r.human, "project", "show", "demo")
	if !strings.Contains(list, "held by Handloom") || !strings.Contains(show, "held by Handloom:") || !strings.Contains(show, "git folder") {
		t.Fatalf("list %q show %q", list, show)
	}
	var js api.Codebase
	if err := json.Unmarshal([]byte(r.as(r.human, "project", "show", "demo", "--json")), &js); err != nil || !js.Hosted || js.SizeBytes <= 0 {
		t.Fatalf("json: %v %+v", err, js)
	}
	// Plain git in the copy, no prompt and no token anywhere.
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(env, "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x\n"), 0o644)
	if out, err := run("add", "-A"); err != nil {
		t.Fatal(out)
	}
	if out, err := run("commit", "-q", "-m", "x"); err != nil {
		t.Fatal(out)
	}
	if out, err := run("push", "-q", "origin", "HEAD:refs/heads/handloom/job-1-x"); err != nil {
		t.Fatalf("push: %s", out)
	}
	if out, err := run("push", "origin", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "cannot be pushed to") {
		t.Fatalf("push to main: %v %s", err, out)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if strings.Contains(string(cfg), "hvd_") {
		t.Fatal("the copy's config has a credential")
	}
	// The same name twice, and the options that do not belong to a hosted project.
	t.Setenv("HANDLOOM_HUB", r.hubURL)
	t.Setenv("HANDLOOM_TOKEN", r.human)
	defer t.Setenv("HANDLOOM_TOKEN", "")
	if code, _, errs := r.exec("project", "new", "demo"); code == 0 || !strings.Contains(errs, "already a project") {
		t.Fatalf("again: %d %s", code, errs)
	}
	if code, _, errs := r.exec("project", "new", "x", "--base", "dev"); code != 2 || !strings.Contains(errs, "takes only a name") {
		t.Fatalf("--base: %d %s", code, errs)
	}
	// The kit's checks: prepared and shown, then put on the main branch by a person.
	if out := r.as(r.human, "project", "checks", "demo"); !strings.Contains(out, "Checks are prepared for demo") || !strings.Contains(out, "[stages]") || !strings.Contains(out, "--apply") {
		t.Fatalf("checks: %s", out)
	}
	if out := r.as(r.human, "project", "checks", "demo"); !strings.Contains(out, "Checks are prepared for demo") {
		t.Fatalf("checks again should show them, not fail: %s", out)
	}
	if code, _, errs := r.exec("project", "checks", "demo", "--apply", "--discard"); code != 2 || !strings.Contains(errs, "cannot be used together") {
		t.Fatalf("apply and discard: %d %s", code, errs)
	}
	if out := r.as(r.human, "project", "checks", "demo", "--apply"); !strings.Contains(out, "on the main branch of demo") {
		t.Fatalf("apply: %s", out)
	}
	if out := r.as(r.human, "project", "check", "demo"); !strings.Contains(out, "./check") {
		t.Fatalf("the project's check after apply: %s", out)
	}
	if out := r.as(r.human, "project", "checks", "demo"); !strings.Contains(out, "has the checks") {
		t.Fatalf("checks after apply: %s", out)
	}
	// The helper command itself answers git's protocol and prints nothing else.
	old := gitCredentialIn
	t.Cleanup(func() { gitCredentialIn = old })
	gitCredentialIn = strings.NewReader("protocol=http\nhost=" + strings.TrimPrefix(r.hubURL, "http://") + "\n\n")
	code, out, errs := r.exec("git-credential", "get")
	if code != 0 || !strings.HasPrefix(out, "username=handloom\npassword=hvd_") || errs != "" {
		t.Fatalf("git-credential get: %d %q %q", code, out, errs)
	}
	gitCredentialIn = strings.NewReader("protocol=http\nhost=github.com\n\n")
	if code, out, errs := r.exec("git-credential", "get"); code != 0 || out != "" || errs != "" {
		t.Fatalf("git-credential for another host: %d %q %q", code, out, errs)
	}
	gitCredentialIn = strings.NewReader("protocol=http\nhost=x\nusername=handloom\npassword=hvd_x\n\n")
	if code, out, errs := r.exec("git-credential", "store"); code != 0 || out != "" || errs != "" {
		t.Fatalf("git-credential store: %d %q %q", code, out, errs)
	}
}

// `job merge` asks the job's project to merge and says what happened; the hub's refusals reach the person in words.
func TestJobMergeThroughTheCLI(t *testing.T) {
	answer := api.CodebaseMerge{Project: "demo", Job: 7, Branch: "handloom/job-7-add-it", Into: "main", Commit: "0123456789abcdef", How: "merge commit"}
	var got api.CodebaseMergeReq
	var mode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/jobs/7":
			json.NewEncoder(w).Encode(api.Job{ID: 7, Title: "Add it", Status: "open", Codebase: "demo", MergedInto: map[bool]string{true: "main"}[mode == "merged"]})
		case r.URL.Path == "/v1/jobs":
			json.NewEncoder(w).Encode([]api.Job{{ID: 7, Title: "Add it", Status: "open", Codebase: "demo", MergedInto: map[bool]string{true: "main"}[mode == "merged"]}})
		case r.URL.Path == "/v1/tasks" || r.URL.Path == "/v1/jobs/7/publishes":
			w.Write([]byte("[]"))
		case r.URL.Path == "/v1/jobs/8":
			json.NewEncoder(w).Encode(api.Job{ID: 8, Title: "No project", Status: "open"})
		case r.URL.Path == "/v1/codebases/demo/merge" && r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&got)
			switch mode {
			case "conflict":
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(api.Error{Error: "job 7 conflicts with the current main in: README.md. Nothing was changed.", Code: "conflict"})
			case "already":
				a := answer
				a.How = "already merged"
				json.NewEncoder(w).Encode(a)
			default:
				json.NewEncoder(w).Encode(answer)
			}
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(api.Error{Error: "not found", Code: "not_found"})
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HANDLOOM_HUB", srv.URL)
	t.Setenv("HANDLOOM_TOKEN", "x")
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	code, out, errs := run("job", "merge", "7")
	if code != 0 || got.Job != 7 || got.Branch != "" || !strings.Contains(out, "Merged job #7 (handloom/job-7-add-it) into main of demo: merge commit, now at 01234567.") || !strings.Contains(out, "next job") {
		t.Fatalf("merge: %d %q %q %+v", code, out, errs, got)
	}
	if code, _, _ := run("job", "merge", "7", "--branch", "handloom/job-7-add-it"); code != 0 || got.Branch != "handloom/job-7-add-it" {
		t.Fatalf("--branch: %d %+v", code, got)
	}
	mode = "already"
	if code, out, _ := run("job", "merge", "7"); code != 0 || !strings.Contains(out, "was already in main of demo; nothing changed") {
		t.Fatalf("already: %d %q", code, out)
	}
	mode = "conflict"
	if code, _, errs := run("job", "merge", "7"); code != 1 || !strings.Contains(errs, "README.md") || !strings.Contains(errs, "Nothing was changed") {
		t.Fatalf("conflict: %d %q", code, errs)
	}
	if code, _, errs := run("job", "merge", "8"); code != 1 || !strings.Contains(errs, "does not belong to a project") {
		t.Fatalf("no project: %d %q", code, errs)
	}
	if code, _, errs := run("job", "merge"); code != 2 || !strings.Contains(errs, "usage: handloom job merge") {
		t.Fatalf("usage: %d %q", code, errs)
	}
	mode = "merged"
	if code, out, _ := run("job", "list"); code != 0 {
		t.Fatalf("list: %d", code)
	} else if !strings.Contains(out, "[merged into main]") {
		t.Fatalf("job list: %q", out)
	}
	if code, out, _ := run("job", "show", "7"); code != 0 || !strings.Contains(out, "[merged into main]") {
		t.Fatalf("show: %d %q", code, out)
	}
}

// A helper other than the builder drafts acceptance tests from the spec's Risks. The command checks what it
// can on the computer (the spec is there) and starts one job from the task branch with no lead.
func TestDraftTestsThroughTheCLI(t *testing.T) {
	fastWaits(t)
	r := newRig(t)
	remote := bareWithCommit(t)
	r.as(r.human, "project", "new", "shared", "--remote", remote, "--device", "dev")
	dir := filepath.Join(os.Getenv("HANDLOOM_HOME"), "projects", "shared")
	prof := filepath.Join(t.TempDir(), "researcher")
	writeProfileDir(t, prof)
	r.as(r.admin, "profile", "new", prof)
	t.Setenv("HANDLOOM_TOKEN", r.human)
	defer t.Setenv("HANDLOOM_TOKEN", "")

	draft := func(args ...string) (int, string) {
		code, out, errs := r.exec(append([]string{"job", "draft-tests", "x", "--project", "shared", "--device", "dev", "--worker-profile", "researcher"}, args...)...)
		return code, out + errs
	}
	if code, out := draft(); code == 0 || !strings.Contains(out, "has no branch task/x") {
		t.Fatalf("no task branch: %d %s", code, out)
	}
	cliGit(t, dir, "checkout", "-q", "-b", "task/x")
	writeSpec := func(text string) {
		if err := os.MkdirAll(filepath.Join(dir, "specs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "specs", "x.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		cliGit(t, dir, "add", "-A")
		cliGit(t, dir, "commit", "-q", "-m", "spec")
	}
	writeSpec("# Spec: <short title>\n\n## Risks\n\n- <risk, and the acceptance test that covers it>\n")
	if code, out := draft(); code == 0 || !strings.Contains(out, "template's placeholders") {
		t.Fatalf("template spec: %d %s", code, out)
	}
	writeSpec("# Spec: x\n\n## Goal\n\nSave a file.\n\n## Files\n\n- src/a.py\n\n## Risks\n\nnone\n\n## Checks\n\n- `true`\n")
	if code, out := draft(); code == 0 || !strings.Contains(out, "lists no risks") {
		t.Fatalf("no risks: %d %s", code, out)
	}
	writeSpec("# Spec: x\n\n## Goal\n\nSave a file.\n\n## Files\n\n- src/a.py\n\n## Risks\n\n- a name like ../x must not leave the folder: test it\n\n## Checks\n\n- `true`\n")
	if code, out := draft("--worker-profile", ""); code == 0 {
		// an empty profile is refused before anything starts
		t.Fatalf("no profile: %d %s", code, out)
	}
	if jobs := r.as(r.human, "job", "list"); strings.Contains(jobs, "Draft acceptance tests") {
		t.Fatalf("a refused command started a job: %s", jobs)
	}
	t.Setenv("HANDLOOM_TOKEN", r.human) // r.as put it back after the listing
	code, out := draft()
	if code != 0 || !strings.Contains(out, "Started job #1") || !strings.Contains(out, "specs/x/drafts/") {
		t.Fatalf("draft-tests: %d %s", code, out)
	}
	var js []api.Job
	if err := json.Unmarshal([]byte(r.as(r.human, "job", "list", "--json")), &js); err != nil || len(js) != 1 {
		t.Fatalf("jobs: %v %+v", err, js)
	}
	if js[0].Base != "task/x" || !strings.Contains(js[0].Body, "specs/x/drafts/") || !strings.Contains(js[0].Body, "never write there") {
		t.Fatalf("job: %+v", js[0])
	}
}
