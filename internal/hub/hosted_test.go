package hub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Hosted projects (D106). These tests use a real git and a real hub on a local port: a machine
// clones, fetches and pushes over smart HTTP with a device credential, and the hub's own hook and
// merge decide what stays. They are skipped when git is not installed.

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// hostedEnv is the standard cast with a data folder and a public address, so the hub can host.
func hostedEnv(t *testing.T) *env {
	t.Helper()
	needGit(t)
	e := newEnv(t)
	e.hub.opt.DataDir = t.TempDir()
	e.hub.opt.BaseURL = e.srv.URL
	e.hub.opt.AllowConfidential = true
	return e
}

func (e *env) host(name string) api.Codebase {
	e.t.Helper()
	var cb api.Codebase
	e.ok(e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: name}, &cb)
	return cb
}

// gitAs runs git the way a machine's git would, with the credential sent as HTTP Basic (the user name
// is ignored by the hub). The credential is passed as a header for this one command, never in an address.
func gitAs(t *testing.T, dir, cred string, args ...string) (string, error) {
	t.Helper()
	pre := []string{}
	if cred != "" {
		pre = []string{"-c", "http.extraHeader=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:"+cred))}
	}
	if dir != "" {
		pre = append([]string{"-C", dir}, pre...)
	}
	cmd := exec.Command("git", append(pre, args...)...)
	home := t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustGit(t *testing.T, dir, cred string, args ...string) string {
	t.Helper()
	out, err := gitAs(t, dir, cred, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

// bareGit looks into the hub's own repository.
func (e *env) bareGit(name string, args ...string) string {
	e.t.Helper()
	out, err := gitAs(e.t, "", "", append([]string{"--git-dir", e.hub.repoPath(name)}, args...)...)
	if err != nil {
		e.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func (e *env) cloneOf(name string) string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), name)
	mustGit(e.t, "", e.d1, "clone", "-q", e.srv.URL+"/git/"+name+".git", dir)
	return dir
}

// commitFile adds one file on top of the clone's current commit.
func commitFile(t *testing.T, dir, file, text, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "", "add", "-A")
	mustGit(t, dir, "", "commit", "-q", "-m", msg)
}

func TestHostedCreateSeedsAndGuardsTheRepository(t *testing.T) {
	e := hostedEnv(t)
	cb := e.host("demo")
	if !cb.Hosted || cb.Name != "demo" || cb.Base != "main" || cb.Remote != e.srv.URL+"/git/demo.git" {
		t.Fatalf("project: %+v", cb)
	}
	if strings.Contains(cb.Remote, "@") || strings.Contains(cb.Remote, store.PrefixDevice) {
		t.Fatalf("the address carries a login: %s", cb.Remote)
	}
	if !e.audited("project.create", "project:demo") {
		t.Fatal("project.create is not in the audit log")
	}
	// A first commit on main: the README with the project's name, by the fixed identity.
	if got := e.bareGit("demo", "show", "main:README.md"); got != "# demo" {
		t.Fatalf("README: %q", got)
	}
	if got := e.bareGit("demo", "log", "-1", "--format=%an <%ae> %s", "main"); got != "Handloom <handloom@localhost> Start the project" {
		t.Fatalf("first commit: %q", got)
	}
	if got := e.bareGit("demo", "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD: %q", got)
	}
	hook := filepath.Join(e.hub.repoPath("demo"), "hooks", "pre-receive")
	st, err := os.Stat(hook)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("pre-receive hook: %v %v", st, err)
	}
	body, _ := os.ReadFile(hook)
	if !strings.HasPrefix(string(body), "#!/bin/sh\n") || strings.Contains(string(body), "bash") {
		t.Fatalf("the hook is not plain sh: %.80s", body)
	}
	// The listing and the single view say it is hosted; the list shows its size.
	var list []api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases", nil, &list)
	var one api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases/demo", nil, &one)
	if len(list) != 1 || !list[0].Hosted || !one.Hosted || one.SizeBytes <= 0 {
		t.Fatalf("list %+v one %+v", list, one)
	}
	// The same name twice, a bad name and a name that is only another's case.
	e.fail(409, e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: "demo"})
	for _, bad := range []string{"", "Demo", "../x", "a b", "-x", strings.Repeat("a", 41)} {
		e.fail(400, e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: bad})
	}
	entries, _ := os.ReadDir(filepath.Join(e.hub.opt.DataDir, "git"))
	if len(entries) != 1 {
		t.Fatalf("refused names left things on disk: %v", entries)
	}
	// Attaching an address by hand is unchanged: a plain http address is still refused.
	e.fail(400, e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "mine", Remote: e.srv.URL + "/git/demo.git"})
}

func TestHostedCreateIsRefusedWithoutGitOrDataFolder(t *testing.T) {
	e := hostedEnv(t)
	t.Setenv("PATH", t.TempDir())
	msg := e.fail(503, e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: "demo"})
	if !strings.Contains(msg, "hosting needs git on the hub") {
		t.Fatalf("message: %s", msg)
	}
	var list []api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases", nil, &list)
	if len(list) != 0 {
		t.Fatalf("a refused creation left a project: %+v", list)
	}
}

func TestHostedCreateNeedsADataFolderAndAPublicAddress(t *testing.T) {
	needGit(t)
	e := newEnv(t)
	if msg := e.fail(503, e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: "demo"}); !strings.Contains(msg, "no data folder") {
		t.Fatalf("no data folder: %s", msg)
	}
	// Without a configured public address it uses the address the request came to.
	e.hub.opt.DataDir = t.TempDir()
	var cb api.Codebase
	e.ok(e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: "demo"}, &cb)
	if cb.Remote != e.srv.URL+"/git/demo.git" {
		t.Fatalf("derived address: %s", cb.Remote)
	}
	// A configured address wins, with a trailing slash tolerated.
	e.hub.opt.BaseURL = "https://hub.example.test/"
	e.ok(e.afif(), "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: "second"}, &cb)
	if cb.Remote != "https://hub.example.test/git/second.git" {
		t.Fatalf("configured address: %s", cb.Remote)
	}
}

func TestHostedWhoMayCreate(t *testing.T) {
	e := hostedEnv(t)
	var op, viewer api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "vera"}, &viewer)
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'vera'`)
	for name, c := range map[string]caller{"viewer": {viewer.Token, ""}, "lead": e.lead(), "worker": e.worker(), "observer": e.observer(), "admin": {e.admin, ""}, "ai app": {op.Token, ""}, "a machine": {e.d1, ""}} {
		if code, _ := e.do(c, "POST", "/v1/codebases/host", api.CodebaseHostReq{Name: "demo"}); code != 403 {
			t.Errorf("%s: status %d, want 403", name, code)
		}
	}
	if _, err := os.Stat(e.hub.repoPath("demo")); err == nil {
		t.Fatal("a refused request made a repository")
	}
}

func TestHostedGitSignInMatrix(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "elsewhere", Remote: "https://github.com/acme/elsewhere"}, nil)
	var op api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	d3 := e.device("d3")
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/devices/d3/revoke", nil, nil)

	get := func(path, user, pass string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if pass != "" || user != "" {
			req.SetBasicAuth(user, pass)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 4096)
		n, _ := resp.Body.Read(b)
		return resp, string(b[:n])
	}
	const refs = "/git/demo.git/info/refs?service=git-upload-pack"

	resp, _ := get(refs, "", "", nil)
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("no credentials: %d %v", resp.StatusCode, resp.Header)
	}
	resp, body := get(refs, "whoever", e.d1, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-git-upload-pack-advertisement" || !strings.Contains(body, "refs/heads/main") {
		t.Fatalf("a machine: %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := get(refs, "", e.d2, nil); resp.StatusCode != 200 {
		t.Fatalf("another machine, no user name: %d", resp.StatusCode)
	}
	// Everything that is not a machine's credential is refused, and none of it is a 200.
	for name, tc := range map[string]struct {
		pass string
		want int
	}{
		"a person's token":        {e.human, 403},
		"the admin token":         {e.admin, 403},
		"an AI app's token":       {op.Token, 403},
		"an agent's run token":    {"hrt_" + strings.Repeat("a", 40), 403},
		"a wrong password":        {store.PrefixDevice + strings.Repeat("0", 43), 401},
		"a revoked machine":       {d3, 401},
		"a similar-looking token": {e.d1 + "x", 401},
	} {
		resp, body := get(refs, "x", tc.pass, nil)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", name, resp.StatusCode, tc.want, body)
		}
		if strings.Contains(body, tc.pass) {
			t.Errorf("%s: the answer repeats the credential", name)
		}
	}
	// An agent speaking through its machine's credential is not a machine's git.
	if resp, _ := get(refs, "x", e.d1, map[string]string{api.AgentHeader: "worker"}); resp.StatusCode != 403 {
		t.Fatalf("an agent header: %d", resp.StatusCode)
	}
	// Unknown, not hosted, malformed and not-git paths are 404; wrong methods 405.
	for _, p := range []string{"/git/nope.git/info/refs?service=git-upload-pack", "/git/elsewhere.git/info/refs?service=git-upload-pack",
		"/git/demo/info/refs", "/git/demo.git/config", "/git/demo.git/objects/info/packs", "/git/..%2f..%2fx.git/info/refs", "/git/Demo.git/info/refs", "/git/", "/git/demo.git/"} {
		if resp, _ := get(p, "x", e.d1, nil); resp.StatusCode != 404 {
			t.Errorf("GET %s: %d, want 404", p, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", e.srv.URL+refs, strings.NewReader("x"))
	req.SetBasicAuth("x", e.d1)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 405 {
		t.Fatalf("POST info/refs: %d", resp.StatusCode)
	}
	// No CSRF or session is involved: a cross-site Origin does not matter to git.
	if resp, _ := get(refs, "x", e.d1, map[string]string{"Origin": "https://evil.test"}); resp.StatusCode != 200 {
		t.Fatalf("with a foreign Origin: %d", resp.StatusCode)
	}
}

func TestHostedClonePushAndTheRulesOfTheHook(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	// Cloning needs the credential; without it git fails and nothing is written.
	if out, err := gitAs(t, "", "", "clone", "-q", e.srv.URL+"/git/demo.git", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatalf("an anonymous clone worked: %s", out)
	}
	if out, err := gitAs(t, "", e.human, "clone", "-q", e.srv.URL+"/git/demo.git", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatalf("a clone with a person's token worked: %s", out)
	}
	dir := e.cloneOf("demo")
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("the clone lacks the README: %v", err)
	}
	if remote := mustGit(t, dir, "", "remote", "get-url", "origin"); strings.Contains(remote, "@") || strings.Contains(remote, e.d1) {
		t.Fatalf("the clone's address carries a login: %s", remote)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if strings.Contains(string(cfg), e.d1) {
		t.Fatal("the clone's config has the credential")
	}

	// A new branch under handloom/ is accepted, and so is a fast-forward of it.
	commitFile(t, dir, "a.txt", "one\n", "one")
	mustGit(t, dir, e.d1, "push", "-q", "origin", "HEAD:refs/heads/handloom/job-1-x")
	commitFile(t, dir, "a.txt", "two\n", "two")
	mustGit(t, dir, e.d1, "push", "-q", "origin", "HEAD:refs/heads/handloom/job-1-x")
	if got := e.bareGit("demo", "show", "handloom/job-1-x:a.txt"); got != "two" {
		t.Fatalf("branch content: %q", got)
	}
	mainBefore := e.bareGit("demo", "rev-parse", "main")
	// A fetch from another copy sees it.
	other := e.cloneOf("demo")
	mustGit(t, other, e.d2, "fetch", "-q", "origin")
	if mustGit(t, other, "", "rev-parse", "origin/handloom/job-1-x") != e.bareGit("demo", "rev-parse", "handloom/job-1-x") {
		t.Fatal("a second machine does not see the branch")
	}

	refused := func(what, wantWords string, args ...string) {
		t.Helper()
		out, err := gitAs(t, dir, e.d1, args...)
		if err == nil {
			t.Errorf("%s: the push was accepted: %s", what, out)
			return
		}
		if !strings.Contains(out, wantWords) {
			t.Errorf("%s: refused, but not in words (%q): %s", what, wantWords, out)
		}
	}
	refused("main", "cannot be pushed to", "push", "origin", "HEAD:refs/heads/main")
	refused("a branch outside handloom/", "cannot be pushed to", "push", "origin", "HEAD:refs/heads/feature")
	refused("a branch called handloom", "cannot be pushed to", "push", "origin", "HEAD:refs/heads/handloom")
	mustGit(t, dir, "", "tag", "v1")
	refused("a tag", "only branches named handloom", "push", "origin", "refs/tags/v1")
	refused("a note ref", "only branches named handloom", "push", "origin", "HEAD:refs/notes/x")
	refused("a delete", "cannot be deleted", "push", "origin", "--delete", "handloom/job-1-x")
	refused("a delete by empty source", "cannot be deleted", "push", "origin", ":refs/heads/handloom/job-1-x")
	// A rewrite: a branch that does not contain what is there.
	mustGit(t, dir, "", "checkout", "-q", "--orphan", "other")
	commitFile(t, dir, "z.txt", "z\n", "unrelated")
	refused("a forced rewrite", "overwrite", "push", "--force", "origin", "HEAD:refs/heads/handloom/job-1-x")
	refused("a forced rewrite with a lease", "overwrite", "push", "--force-with-lease", "origin", "HEAD:refs/heads/handloom/job-1-x")
	refused("a forced '+' refspec", "overwrite", "push", "origin", "+HEAD:refs/heads/handloom/job-1-x")
	// A refused push in a batch refuses the whole batch.
	refused("a mixed push", "cannot be pushed to", "push", "origin", "HEAD:refs/heads/handloom/ok-but-not-alone", "HEAD:refs/heads/feature")
	if _, err := e.hub.runGit(e.hub.repoPath("demo"), "", "rev-parse", "--verify", "-q", "refs/heads/handloom/ok-but-not-alone"); err == nil {
		t.Error("a branch of a refused batch exists")
	}

	if e.bareGit("demo", "rev-parse", "main") != mainBefore {
		t.Fatal("main moved")
	}
	if got := e.bareGit("demo", "show", "handloom/job-1-x:a.txt"); got != "two" {
		t.Fatalf("the branch changed: %q", got)
	}
	if refs := e.bareGit("demo", "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "refs/tags") || strings.Contains(refs, "refs/notes") {
		t.Fatalf("refs: %s", refs)
	}
}

func TestHostedPushWithANewMachineAndRevocation(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	dir := e.cloneOf("demo")
	commitFile(t, dir, "a.txt", "x\n", "x")
	// Revoking the machine stops its pushes at once.
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/devices/d1/revoke", nil, nil)
	if out, err := gitAs(t, dir, e.d1, "push", "-q", "origin", "HEAD:refs/heads/handloom/a"); err == nil {
		t.Fatalf("a revoked machine pushed: %s", out)
	}
	mustGit(t, dir, e.d2, "push", "-q", "origin", "HEAD:refs/heads/handloom/a")
}

// ---- jobs, publishing and merging ----

// readyOn makes d1's copy of a project ready, as the machine would report it.
func (e *env) readyOn(project string) {
	e.t.Helper()
	var q api.CodebaseRequest
	e.ok(e.afif(), "POST", "/v1/codebases/"+project+"/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
	var pend []api.PendingCodebaseRequest
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/project-requests", nil, &pend)
	for _, p := range pend {
		if p.Name == project {
			e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/project-requests/%d/report", p.ID), api.CodebaseReport{Status: "done", Path: "/srv/" + project, Base: "main"}, nil)
		}
	}
}

// projectJob starts a job of a project with one accepted and merged task (as the machine reports it).
func (e *env) hostedJob(project, title string, confidential bool) api.Job {
	e.t.Helper()
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: title, Codebase: project, Device: "d1", Lead: "lead", Confidential: confidential}, &j)
	var task api.Task
	e.ok(e.lead(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "do it", AssignedTo: "worker"}, &task)
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)
	e.ok(e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"test:ok"}, Note: "done"}, nil)
	e.ok(e.afif(), "POST", taskPath(task.ID, "accept"), nil, nil)
	var ms []api.PendingMerge
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/merges", nil, &ms)
	for _, m := range ms {
		if m.Job == j.ID {
			e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/merges/%d/report", m.ID), api.MergeReport{Status: "merged", Head: "abc"}, nil)
		}
	}
	return j
}

// published asks for the publish and answers it as the link would after pushing branch content to the hub.
func (e *env) published(j api.Job, dir string) api.Publish {
	e.t.Helper()
	var p api.Publish
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", j.ID), api.PublishReq{}, &p)
	if dir != "" {
		mustGit(e.t, dir, e.d1, "push", "-q", "origin", "HEAD:refs/heads/"+p.Branch)
	}
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/report", p.ID), api.PublishReport{Status: "done", Detail: "pushed"}, nil)
	return p
}

func (e *env) merge(c caller, project string, req api.CodebaseMergeReq) (int, api.CodebaseMerge, string) {
	e.t.Helper()
	code, raw := e.do(c, "POST", "/v1/codebases/"+project+"/merge", req)
	var m api.CodebaseMerge
	if code == 200 {
		if err := json.Unmarshal(raw, &m); err != nil {
			e.t.Fatal(err)
		}
	}
	return code, m, string(raw)
}

func TestHostedMergeFastForwardMergeCommitAndAlreadyMerged(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	e.readyOn("demo")
	root := e.bareGit("demo", "rev-parse", "main")

	// Job 1 simply continues main: a fast-forward.
	j1 := e.hostedJob("demo", "Add a\nline: the greeting", false)
	d1 := e.cloneOf("demo")
	commitFile(t, d1, "a.txt", "a\n", "add a")
	p1 := e.published(j1, d1)
	// Job 2 started from the same point and touches another file: needs a merge commit once job 1 is in.
	j2 := e.hostedJob("demo", "Add b", false)
	d2 := e.cloneOf("demo")
	commitFile(t, d2, "b.txt", "b\n", "add b")
	p2 := e.published(j2, d2)

	// Merging is a person's act; an agent, an AI app, a machine and a viewer are refused, and nothing moves.
	var op, viewer api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "vera"}, &viewer)
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'vera'`)
	req := api.CodebaseMergeReq{Job: j1.ID}
	for name, c := range map[string]caller{"lead": e.lead(), "worker": e.worker(), "observer": e.observer(), "a machine": {e.d1, ""}, "admin": {e.admin, ""}, "ai app": {op.Token, ""}, "viewer": {viewer.Token, ""}} {
		if code, _, body := e.merge(c, "demo", req); code != 403 {
			t.Errorf("%s: status %d, want 403 (%s)", name, code, body)
		}
	}
	if e.bareGit("demo", "rev-parse", "main") != root {
		t.Fatal("a refused merge moved main")
	}
	if e.audited("project.merge", "project:demo") {
		t.Fatal("a refused merge is audited as done")
	}

	code, m, body := e.merge(e.afif(), "demo", req)
	if code != 200 || m.How != "fast-forward" || m.Into != "main" || m.Branch != p1.Branch || m.Project != "demo" {
		t.Fatalf("merge 1: %d %s", code, body)
	}
	if got := e.bareGit("demo", "rev-parse", "main"); got != e.bareGit("demo", "rev-parse", p1.Branch) || got != m.Commit {
		t.Fatalf("main is %s, branch %s, answer %s", got, e.bareGit("demo", "rev-parse", p1.Branch), m.Commit)
	}
	if !e.audited("project.merge", "project:demo") {
		t.Fatal("project.merge is not in the audit log")
	}
	// The activity feed says it, and the job and the publish carry the mark.
	var dg api.Digest
	e.ok(e.afif(), "GET", "/v1/digest", nil, &dg)
	found := false
	for _, a := range dg.Activity {
		found = found || a.Text == fmt.Sprintf("job %d merged into demo", j1.ID)
	}
	if !found {
		t.Fatalf("the activity feed lacks the line: %+v", dg.Activity)
	}
	var j api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j1.ID), nil, &j)
	var pubs []api.Publish
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d/publishes", j1.ID), nil, &pubs)
	if j.MergedInto != "main" || len(pubs) != 1 || pubs[0].MergedInto != "main" || pubs[0].MergedAt == nil || pubs[0].MergedCommit != m.Commit {
		t.Fatalf("job %+v publishes %+v", j, pubs)
	}
	var events int
	e.hub.db.QueryRow(`SELECT count(*) FROM event WHERE type = 'project.merge'`).Scan(&events)
	if events != 1 {
		t.Fatalf("events: %d", events)
	}

	// Job 2 now needs a merge commit, with the title in its message on one line.
	mainBefore := m.Commit
	code, m2, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: j2.ID})
	if code != 200 || m2.How != "merge commit" {
		t.Fatalf("merge 2: %d %s", code, body)
	}
	if got := e.bareGit("demo", "log", "-1", "--format=%s", "main"); got != "Merge job "+fmt.Sprint(j2.ID)+": Add b" {
		t.Fatalf("merge message: %q", got)
	}
	if parents := strings.Fields(e.bareGit("demo", "log", "-1", "--format=%P", "main")); len(parents) != 2 || parents[0] != mainBefore || parents[1] != e.bareGit("demo", "rev-parse", p2.Branch) {
		t.Fatalf("parents: %v", parents)
	}
	for _, f := range []string{"README.md", "a.txt", "b.txt"} {
		e.bareGit("demo", "cat-file", "-e", "main:"+f)
	}
	if got := e.bareGit("demo", "log", "-1", "--format=%an <%ae>", "main"); got != "Handloom <handloom@localhost>" {
		t.Fatalf("merge identity: %q", got)
	}
	// A title with a line break never makes a second line in a message (job 1 was a fast-forward; check by hand).
	if strings.Contains(oneLine("a\nb", 100), "\n") {
		t.Fatal("oneLine keeps a newline")
	}

	// Merging again says it is already in and changes nothing.
	head := e.bareGit("demo", "rev-parse", "main")
	code, m3, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: j1.ID})
	if code != 200 || m3.How != "already merged" || e.bareGit("demo", "rev-parse", "main") != head {
		t.Fatalf("again: %d %s", code, body)
	}
	// No temporary working folders or registered worktrees are left behind.
	if left, _ := filepath.Glob(filepath.Join(e.hub.gitRoot(), ".merge-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	if wt := e.bareGit("demo", "worktree", "list", "--porcelain"); strings.Count(wt, "worktree ") != 1 {
		t.Fatalf("worktrees: %s", wt)
	}
	// Other machines see the new main by a plain fetch.
	other := e.cloneOf("demo")
	if mustGit(t, other, "", "rev-parse", "HEAD") != head {
		t.Fatal("a new clone is not at the merged main")
	}
}

func TestHostedMergeConflictWritesNothingAndNamesTheFiles(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	e.readyOn("demo")
	jA := e.hostedJob("demo", "Change the title", false)
	dA := e.cloneOf("demo")
	commitFile(t, dA, "README.md", "# demo, version A\n", "A")
	commitFile(t, dA, "only-a.txt", "a\n", "A2")
	e.published(jA, dA)
	jB := e.hostedJob("demo", "Rename it", false)
	dB := e.cloneOf("demo")
	commitFile(t, dB, "README.md", "# demo, version B\n", "B")
	e.published(jB, dB)

	if code, _, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: jA.ID}); code != 200 {
		t.Fatalf("first merge: %d %s", code, body)
	}
	main := e.bareGit("demo", "rev-parse", "main")
	refs := e.bareGit("demo", "for-each-ref", "--format=%(refname) %(objectname)")
	code, _, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: jB.ID})
	if code != 409 || !strings.Contains(body, "README.md") || !strings.Contains(body, "Nothing was changed") {
		t.Fatalf("a conflict: %d %s", code, body)
	}
	if e.bareGit("demo", "rev-parse", "main") != main || e.bareGit("demo", "for-each-ref", "--format=%(refname) %(objectname)") != refs {
		t.Fatal("a failed merge changed the repository")
	}
	if left, _ := filepath.Glob(filepath.Join(e.hub.gitRoot(), ".merge-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	if wt := e.bareGit("demo", "worktree", "list", "--porcelain"); strings.Count(wt, "worktree ") != 1 {
		t.Fatalf("worktrees: %s", wt)
	}
	// The refusal is audited (and only that), and the job is not marked merged.
	if !e.audited("project.merge_failed", "project:demo") {
		t.Fatal("project.merge_failed is not in the audit log")
	}
	var j api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", jB.ID), nil, &j)
	var done int
	e.hub.db.QueryRow(`SELECT count(*) FROM audit WHERE action = 'project.merge'`).Scan(&done)
	if j.MergedInto != "" || done != 1 {
		t.Fatalf("job %+v, project.merge rows %d", j, done)
	}
}

func TestHostedMergeRefusalsAndPublishRules(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	e.host("other")
	e.readyOn("demo")
	e.readyOn("other")
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "gh", Remote: "https://github.com/acme/gh"}, nil)

	j := e.hostedJob("demo", "Work", false)
	// Not published yet: say so, with the command.
	if code, _, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: j.ID}); code != 409 || !strings.Contains(body, "handloom job publish") {
		t.Fatalf("unpublished: %d %s", code, body)
	}
	d := e.cloneOf("demo")
	commitFile(t, d, "a.txt", "a\n", "a")
	p := e.published(j, d)
	// A pull request makes no sense for a project Handloom holds.
	var j0 api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "pr", Codebase: "demo", Device: "d1"}, &j0)
	// Wrong project, non-hosted project, unknown project, unknown job, bad branches.
	for name, tc := range map[string]struct {
		project string
		req     api.CodebaseMergeReq
		want    int
	}{
		"another project's job":     {"other", api.CodebaseMergeReq{Job: j.ID}, 400},
		"a project held elsewhere":  {"gh", api.CodebaseMergeReq{Job: j.ID}, 409},
		"no such project":           {"nope", api.CodebaseMergeReq{Job: j.ID}, 404},
		"no such job":               {"demo", api.CodebaseMergeReq{Job: 999}, 404},
		"a branch not published":    {"demo", api.CodebaseMergeReq{Job: j.ID, Branch: "handloom/job-9-other"}, 400},
		"main as the branch":        {"demo", api.CodebaseMergeReq{Job: j.ID, Branch: "main"}, 400},
		"dots in the branch":        {"demo", api.CodebaseMergeReq{Job: j.ID, Branch: "handloom/../main"}, 400},
		"a dash branch":             {"demo", api.CodebaseMergeReq{Job: j.ID, Branch: "-x"}, 400},
		"an empty-job-id request":   {"demo", api.CodebaseMergeReq{}, 404},
		"a hostile branch name":     {"demo", api.CodebaseMergeReq{Job: j.ID, Branch: "handloom/a;rm -rf /"}, 400},
		"the job but a wrong order": {"demo", api.CodebaseMergeReq{Job: j0.ID}, 409},
	} {
		if code, _, body := e.merge(e.afif(), tc.project, tc.req); code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", name, code, tc.want, body)
		}
	}
	e.fail(400, e.afif(), "POST", "/v1/codebases/demo/merge", "{not json")
	if got := e.bareGit("demo", "rev-parse", "main"); got == e.bareGit("demo", "rev-parse", p.Branch) {
		t.Fatal("a refused merge moved main")
	}
	// The named branch works.
	if code, m, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: j.ID, Branch: p.Branch}); code != 200 || m.Branch != p.Branch {
		t.Fatalf("named branch: %d %s", code, body)
	}

	// Pull requests are refused for a hosted project, with the way forward.
	jj := e.hostedJob("demo", "Second", false)
	if msg := e.fail(400, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", jj.ID), api.PublishReq{PR: true}); !strings.Contains(msg, "handloom job merge") {
		t.Fatalf("pr: %s", msg)
	}
	// The machine learns the project is hosted.
	var pr api.Publish
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", jj.ID), api.PublishReq{}, &pr)
	var pend []api.PendingPublish
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/publishes", nil, &pend)
	if len(pend) != 1 || !pend[0].Hosted {
		t.Fatalf("pending: %+v", pend)
	}
}

func TestHostedConfidentialJobsAreNotPublishedToTheHub(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	e.readyOn("demo")
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "gh", Remote: "https://github.com/acme/gh"}, nil)
	j := e.hostedJob("demo", "Client work", true)
	msg := e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", j.ID), api.PublishReq{})
	for _, want := range []string{"confidential", "must not sit on the hub", "handloom project new NAME --remote URL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	var pubs []api.Publish
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d/publishes", j.ID), nil, &pubs)
	if len(pubs) != 0 {
		t.Fatalf("a refused publish left a request: %+v", pubs)
	}
	// Merging one is refused as well (should one ever have been published).
	e.hub.db.Exec(`INSERT INTO publish(job_id, device_id, requested_by, remote, branch, status, created_at) VALUES (?, 1, 'x', 'origin', 'handloom/job-1', 'done', 1)`, j.ID)
	if code, _, body := e.merge(e.afif(), "demo", api.CodebaseMergeReq{Job: j.ID}); code != 409 || !strings.Contains(body, "confidential") {
		t.Fatalf("merge of a confidential job: %d %s", code, body)
	}
	// A confidential job of a project held elsewhere is unaffected.
	e.hub.db.Exec(`UPDATE task SET codebase_id = (SELECT id FROM codebase WHERE name = 'gh') WHERE id = ?`, j.ID)
	e.hub.db.Exec(`DELETE FROM publish`)
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", j.ID), api.PublishReq{}, nil)
}

func TestHostedBackupNoteAndSizeInTheListing(t *testing.T) {
	e := hostedEnv(t)
	e.host("demo")
	if e.hub.repoSize("demo") <= 0 || e.hub.repoSize("nothing") != 0 {
		t.Fatal("repoSize")
	}
	// The database holds the address and a flag, not the code.
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM codebase WHERE hosted = 1`).Scan(&n)
	if n != 1 {
		t.Fatal("hosted flag not stored")
	}
}

// ---- the web UI ----

func TestHostedWebFormMergeButtonAndEscaping(t *testing.T) {
	needGit(t)
	e := newWebEnv(t, Options{DataDir: t.TempDir(), AllowConfidential: true})
	ag := e.agents()
	c, tok := e.owner()

	r := e.req("GET", "/projects", nil, c, nil)
	for _, want := range []string{"Let Handloom hold the code", `action="/projects/host"`, `name="h_name"`, "I already have a repository", "Make it on GitHub", "git</code> folder must be part of your backups"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("projects page lacks %q", want)
		}
	}
	// The form needs the CSRF token; a bad name is refused in words with what was typed, escaped.
	if r := e.req("POST", "/projects/host", url.Values{"h_name": {"demo"}}, c, nil); r.status == 303 {
		t.Fatal("hosted without the CSRF token")
	}
	if r := e.req("POST", "/projects/host", url.Values{"h_name": {`"><script>alert(1)</script>`}, "csrf": {tok}}, c, nil); r.status != 400 || strings.Contains(r.body, "<script>alert(1)") || !strings.Contains(r.body, "bad project name") {
		t.Fatalf("hostile name: %d %s", r.status, r.body)
	}
	if r := e.req("POST", "/projects/host", url.Values{"h_name": {"demo"}, "csrf": {tok}}, c, nil); r.status != 303 || r.header.Get("Location") != "/projects?done=hosted" {
		t.Fatalf("host: %d %s", r.status, r.body)
	}
	if r := e.req("POST", "/projects/host", url.Values{"h_name": {"demo"}, "csrf": {tok}}, c, nil); r.status != 409 {
		t.Fatalf("host twice: %d", r.status)
	}
	page := e.req("GET", "/projects?done=hosted", nil, c, nil).body
	for _, want := range []string{"Project created on this hub", "held by Handloom", "on the hub's disk", testOrigin + "/git/demo.git"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, `href="`+testOrigin+`/git/demo.git"`) {
		t.Error("the hosted address is shown as a link")
	}
	// A viewer has no form and cannot host.
	vc, vtok := e.member("vera", "viewer")
	if r := e.req("GET", "/projects", nil, vc, nil); strings.Contains(r.body, `action="/projects/host"`) {
		t.Fatal("a viewer is offered the form")
	}
	if r := e.req("POST", "/projects/host", url.Values{"h_name": {"viewers"}, "csrf": {vtok}}, vc, nil); r.status != 403 {
		t.Fatalf("a viewer hosted: %d", r.status)
	}

	// A job of the project with a finished publish: the Publish card offers Merge.
	apiTok := e.humanAPI()
	var q api.CodebaseRequest
	e.apiOK(apiTok, "", "POST", "/v1/codebases/demo/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
	var pend []api.PendingCodebaseRequest
	e.apiOK(ag.device, "", "GET", "/v1/device/project-requests", nil, &pend)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/project-requests/%d/report", pend[0].ID), api.CodebaseReport{Status: "done", Path: "/srv/demo", Base: "main"}, nil)
	var j api.Job
	e.apiOK(apiTok, "", "POST", "/v1/jobs", api.JobNewReq{Title: `Fix <b>it</b>`, Codebase: "demo", Device: "d1", Lead: "lead"}, &j)
	jobPath := fmt.Sprintf("/jobs/%d", j.ID)
	page = e.req("GET", jobPath, nil, c, nil).body
	if strings.Contains(page, "/merge") || !strings.Contains(page, "which Handloom holds") || strings.Contains(page, "open pull request") {
		t.Fatalf("before any publish: merge form %v", strings.Contains(page, "/merge"))
	}
	// A finished publish whose branch holds work.
	branch := publishBranch(j.ID, j.Title)
	seedBranch(t, e.hub.repoPath("demo"), branch, "web.txt", "web\n")
	e.hub.db.Exec(`INSERT INTO publish(job_id, device_id, requested_by, remote, branch, status, detail, created_at, done_at) VALUES (?, (SELECT id FROM device WHERE name = 'd1'), 'x', 'origin', ?, 'done', 'pushed', 1, 2)`, j.ID, branch)
	page = e.req("GET", jobPath, nil, c, nil).body
	if !strings.Contains(page, `action="`+jobPath+`/merge"`) || !strings.Contains(page, "Merge into demo") || !strings.Contains(page, `name="csrf"`) {
		t.Fatalf("no merge button: %s", page)
	}
	if strings.Contains(page, "<b>it</b>") || strings.Contains(page, "Push and open pull request") {
		t.Fatal("job title became markup, or a pull request is offered")
	}
	// A second job, started from the same main, touches the same file: it will conflict once the first is in.
	var j2 api.Job
	e.apiOK(apiTok, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Second", Codebase: "demo", Device: "d1", Lead: "lead"}, &j2)
	j2id := j2.ID
	seedBranch(t, e.hub.repoPath("demo"), publishBranch(j2id, "Second"), "web.txt", "<script>alert(1)</script>\n")
	e.hub.db.Exec(`INSERT INTO publish(job_id, device_id, requested_by, remote, branch, status, detail, created_at, done_at) VALUES (?, (SELECT id FROM device WHERE name = 'd1'), 'x', 'origin', ?, 'done', 'pushed', 1, 2)`, j2id, publishBranch(j2id, "Second"))
	// A viewer sees no button and cannot merge; a merge without the token is refused.
	if vp := e.req("GET", jobPath, nil, vc, nil).body; strings.Contains(vp, "/merge") {
		t.Fatal("a viewer is offered Merge")
	}
	if r := e.req("POST", jobPath+"/merge", url.Values{"csrf": {vtok}}, vc, nil); r.status != 403 {
		t.Fatalf("a viewer merged: %d", r.status)
	}
	if r := e.req("POST", jobPath+"/merge", url.Values{}, c, nil); r.status != 403 {
		t.Fatalf("a merge without the CSRF token: %d", r.status)
	}
	if r := e.req("POST", jobPath+"/merge", url.Values{"csrf": {tok}}, c, map[string]string{"Origin": "https://evil.test"}); r.status != 403 {
		t.Fatalf("a merge from another origin: %d", r.status)
	}
	if got := mustGit(t, "", "", "--git-dir", e.hub.repoPath("demo"), "rev-parse", "main"); got == mustGit(t, "", "", "--git-dir", e.hub.repoPath("demo"), "rev-parse", branch) {
		t.Fatal("a refused request merged")
	}
	if r := e.req("POST", jobPath+"/merge", url.Values{"csrf": {tok}}, c, nil); r.status != 303 || r.header.Get("Location") != jobPath+"?done=merged" {
		t.Fatalf("merge: %d %s", r.status, r.body)
	}
	if mustGit(t, "", "", "--git-dir", e.hub.repoPath("demo"), "rev-parse", "main") != mustGit(t, "", "", "--git-dir", e.hub.repoPath("demo"), "rev-parse", branch) {
		t.Fatal("main did not move")
	}
	page = e.req("GET", jobPath+"?done=merged", nil, c, nil).body
	if !strings.Contains(page, "Merged into main") || strings.Contains(page, `action="`+jobPath+`/merge"`) || !strings.Contains(page, "Merged. The work is now") {
		t.Fatalf("after the merge: %s", page)
	}
	if jobs := e.req("GET", "/jobs", nil, c, nil).body; !strings.Contains(jobs, "merged into main") {
		t.Fatal("the jobs list does not mark the merged job")
	}
	if !e.audited("project.merge", "project:demo") {
		t.Fatal("project.merge is not in the audit log")
	}

	// The second job conflicts: it is shown on the page in words, escaped, and leaves main alone; the refusal is audited.
	mainBefore := mustGit(t, "", "", "--git-dir", e.hub.repoPath("demo"), "rev-parse", "main")
	r = e.req("POST", fmt.Sprintf("/jobs/%d/merge", j2id), url.Values{"csrf": {tok}}, c, nil)
	if r.status != 409 || !strings.Contains(r.body, "web.txt") || strings.Contains(r.body, "<script>alert(1)") {
		t.Fatalf("conflict: %d %s", r.status, r.body)
	}
	if mustGit(t, "", "", "--git-dir", e.hub.repoPath("demo"), "rev-parse", "main") != mainBefore {
		t.Fatal("a conflict moved main")
	}
	if !e.audited("project.merge_failed", "project:demo") {
		t.Fatal("the web refusal is not audited")
	}
}

// seedBranch puts a commit on top of main in the hub's repository, on a branch, through the repository's own hook.
func seedBranch(t *testing.T, bare, branch, file, text string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "w")
	mustGit(t, "", "", "clone", "-q", bare, dir)
	commitFile(t, dir, file, text, "work")
	mustGit(t, dir, "", "push", "-q", "origin", "HEAD:refs/heads/"+branch)
}
