package hub

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/api"
)

// Adding the kit's checks to a project the hub holds: prepared on a side ref, applied by a person only.

func (e *env) checksState(name string) api.ChecksState {
	e.t.Helper()
	var st api.ChecksState
	e.ok(e.afif(), "GET", "/v1/codebases/"+name+"/checks", nil, &st)
	return st
}

func (e *env) checksDo(c caller, name, action, preset string) (int, api.ChecksState, string) {
	e.t.Helper()
	code, raw := e.do(c, "POST", "/v1/codebases/"+name+"/checks", api.ChecksReq{Action: action, Preset: preset})
	var st api.ChecksState
	if code == 200 {
		if err := json.Unmarshal(raw, &st); err != nil {
			e.t.Fatal(err)
		}
	}
	return code, st, string(raw)
}

func TestChecksPrepareApplyDiscard(t *testing.T) {
	e := hostedEnv(t)
	e.host("site")
	if st := e.checksState("site"); st.State != "none" {
		t.Fatalf("a new project: %+v", st)
	}
	mainBefore := e.bareGit("site", "rev-parse", "main")

	// Who may: nobody but a person for either step.
	var op api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	for name, c := range map[string]caller{"lead": e.lead(), "worker": e.worker(), "machine": {e.d1, ""}, "ai app": {op.Token, ""}} {
		for _, action := range []string{"prepare", "apply", "discard"} {
			if code, _, raw := e.checksDo(c, "site", action, ""); code != 403 {
				t.Errorf("%s %s: status %d, want 403: %s", name, action, code, raw)
			}
		}
	}

	// Prepare: a side ref has the kit; the main branch has not moved.
	code, st, raw := e.checksDo(e.afif(), "site", "prepare", "web")
	if code != 200 || st.State != "ready" || st.Preset != "web" {
		t.Fatalf("prepare: %d %s", code, raw)
	}
	for _, want := range []string{"check", "factory.toml", ".factory/check.py", ".factory/checks/web.py", "AGENTS.md", "tests/acceptance/.manifest", ".claude/settings.json"} {
		if !hasString(st.Files, want) {
			t.Errorf("the checks do not add %s: %v", want, st.Files)
		}
	}
	if !strings.Contains(st.Toml, `web-links = "python3 .factory/checks/web.py links"`) {
		t.Errorf("factory.toml: %s", st.Toml)
	}
	if got := e.bareGit("site", "rev-parse", "main"); got != mainBefore {
		t.Fatal("preparing moved the main branch")
	}
	if got := e.bareGit("site", "ls-tree", "refs/handloom/checks", "check"); !strings.HasPrefix(got, "100755 blob") {
		t.Fatalf("./check is not executable in the commit: %q", got)
	}
	if code, _, _ := e.checksDo(e.afif(), "site", "prepare", ""); code != 409 {
		t.Errorf("a second prepare: status %d, want 409", code)
	}
	// A machine can neither write nor see its way to the side ref: the hook takes only handloom/... branches.
	dir := e.cloneOf("site")
	commitFile(t, dir, "x.txt", "x\n", "x")
	if out, err := gitAs(t, dir, e.d1, "push", "-q", "origin", "HEAD:refs/handloom/checks"); err == nil {
		t.Fatalf("a machine pushed the side ref: %s", out)
	}

	// Discard puts everything back.
	if code, st, raw := e.checksDo(e.afif(), "site", "discard", ""); code != 200 || st.State != "none" {
		t.Fatalf("discard: %d %s", code, raw)
	}
	if st := e.checksState("site"); st.State != "none" {
		t.Fatalf("after discard: %+v", st)
	}

	// Apply moves the main branch to the prepared commit, once, and sets the project's check.
	if code, _, raw := e.checksDo(e.afif(), "site", "apply", ""); code != 409 {
		t.Fatalf("apply with nothing prepared: %d %s", code, raw)
	}
	e.checksDo(e.afif(), "site", "prepare", "")
	code, st, raw = e.checksDo(e.afif(), "site", "apply", "")
	if code != 200 || !st.Applied || st.State != "has" {
		t.Fatalf("apply: %d %s", code, raw)
	}
	if e.bareGit("site", "rev-parse", "main") == mainBefore {
		t.Fatal("applying did not move the main branch")
	}
	if got := e.bareGit("site", "rev-parse", "main^"); got != mainBefore {
		t.Fatalf("the checks are not on top of the old main: %s", got)
	}
	if got := e.bareGit("site", "log", "-1", "--format=%s", "main"); got != "Add the checks (Handloom kit)" {
		t.Errorf("commit message: %q", got)
	}
	if e.bareGit("site", "ls-tree", "-r", "--name-only", "main", ".factory/checks/web.py") != "" {
		t.Error("the generic kit carries the web checks")
	}
	var cb api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases/site", nil, &cb)
	if cb.Check != "./check" {
		t.Errorf("the project's check is %q, want ./check", cb.Check)
	}
	if st := e.checksState("site"); st.State != "has" {
		t.Fatalf("state after apply: %+v", st)
	}
	if code, _, _ := e.checksDo(e.afif(), "site", "prepare", ""); code != 409 {
		t.Errorf("prepare on a project that has the checks: status %d, want 409", code)
	}
	if out := e.bareGit("site", "for-each-ref", "refs/handloom"); out != "" {
		t.Errorf("a side ref is left behind: %s", out)
	}
}

func TestChecksApplyIsRefusedWhenMainMoved(t *testing.T) {
	e := hostedEnv(t)
	e.host("site")
	if code, _, raw := e.checksDo(e.afif(), "site", "prepare", ""); code != 200 {
		t.Fatalf("prepare: %d %s", code, raw)
	}
	// Main moves on after the checks were made from the old one.
	tree := e.bareGit("site", "rev-parse", "main^{tree}")
	moved := e.bareGit("site", "commit-tree", tree, "-p", "main", "-m", "someone else")
	e.bareGit("site", "update-ref", "refs/heads/main", moved)
	code, _, raw := e.checksDo(e.afif(), "site", "apply", "")
	if code != 409 || !strings.Contains(raw, "Nothing was changed") {
		t.Fatalf("apply after main moved: %d %s", code, raw)
	}
	if got := e.bareGit("site", "rev-parse", "main"); got != moved {
		t.Fatal("main was changed by a refused apply")
	}
	if st := e.checksState("site"); st.State != "ready" {
		t.Fatalf("the prepared checks should stay until discarded: %+v", st)
	}
	e.checksDo(e.afif(), "site", "discard", "")
	if code, _, raw := e.checksDo(e.afif(), "site", "prepare", ""); code != 200 {
		t.Fatalf("prepare again: %d %s", code, raw)
	}
}

func TestChecksRefusedForProjectsTheHubDoesNotHold(t *testing.T) {
	e := hostedEnv(t)
	e.hub.opt.AllowFileRemotes = true
	var cb api.Codebase
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "elsewhere", Remote: "https://github.com/example/elsewhere"}, &cb)
	if code, _, raw := e.checksDo(e.afif(), "elsewhere", "prepare", ""); code != 409 || !strings.Contains(raw, "handloom kit init") {
		t.Fatalf("a project held elsewhere: %d %s", code, raw)
	}
	e.host("site")
	if code, _, _ := e.checksDo(e.afif(), "site", "prepare", "mystery"); code != 400 {
		t.Errorf("an unknown preset: status %d, want 400", code)
	}
	if code, _, _ := e.checksDo(e.afif(), "site", "fly", ""); code != 400 {
		t.Errorf("an unknown action: status %d, want 400", code)
	}
}

func TestChecksProjectsPageShowsThePreparedChecks(t *testing.T) {
	needGit(t)
	e := newWebEnv(t, Options{DataDir: t.TempDir()})
	c, tok := e.owner()
	if r := e.req("POST", "/projects/host", url.Values{"h_name": {"site"}, "csrf": {tok}}, c, nil); r.status != 303 {
		t.Fatalf("host: %d %s", r.status, r.body)
	}
	page := e.req("GET", "/projects", nil, c, nil).body
	for _, want := range []string{`action="/projects/site/checks"`, `name="action" value="prepare"`, "Add the checks"} {
		if !strings.Contains(page, want) {
			t.Errorf("a project without checks lacks %q", want)
		}
	}
	form := url.Values{"action": {"prepare"}, "preset": {"web"}, "csrf": {tok}}
	if r := e.req("POST", "/projects/site/checks", url.Values{"action": {"prepare"}}, c, nil); r.status == 303 {
		t.Fatal("prepared without the CSRF token")
	}
	if r := e.req("POST", "/projects/site/checks", form, c, nil); r.status != 303 || r.header.Get("Location") != "/projects?done=checks-ready" {
		t.Fatalf("prepare: %d %s", r.status, r.body)
	}
	page = e.req("GET", "/projects?done=checks-ready", nil, c, nil).body
	for _, want := range []string{"Checks are prepared", "Put on the main branch", "Throw away", "[stages]", "web-links"} {
		if !strings.Contains(page, want) {
			t.Errorf("a project with prepared checks lacks %q", want)
		}
	}
	// A refusal appears under the project, in words.
	if r := e.req("POST", "/projects/site/checks", form, c, nil); r.status != 409 || !strings.Contains(r.body, "already prepared") {
		t.Fatalf("prepare twice: %d", r.status)
	}
	// A viewer is offered nothing and cannot do it.
	vc, vtok := e.member("vera", "viewer")
	if r := e.req("GET", "/projects", nil, vc, nil); strings.Contains(r.body, "Put on the main branch") {
		t.Fatal("a viewer is offered to apply")
	}
	if r := e.req("POST", "/projects/site/checks", url.Values{"action": {"apply"}, "csrf": {vtok}}, vc, nil); r.status != 403 {
		t.Fatalf("a viewer applied: %d", r.status)
	}
	if r := e.req("POST", "/projects/site/checks", url.Values{"action": {"apply"}, "csrf": {tok}}, c, nil); r.status != 303 || r.header.Get("Location") != "/projects?done=checks-applied" {
		t.Fatalf("apply: %d %s", r.status, r.body)
	}
	if page := e.req("GET", "/projects", nil, c, nil).body; !strings.Contains(page, "has the checks") || !strings.Contains(page, "Check: ") && !strings.Contains(page, `value="./check"`) {
		t.Fatalf("after apply: %.800s", page)
	}
}

// ---- the Go copy of `factory init` against the real one ----

func needPython(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	return py
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		if err != nil || rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) || d.IsDir() {
			return err
		}
		info, _ := d.Info()
		b, _ := os.ReadFile(p)
		mode := "644"
		if info.Mode()&0o111 != 0 {
			mode = "755"
		}
		out[rel] = mode + "\n" + string(b)
		return nil
	})
	return out
}

func TestSeedKitMatchesFactoryInit(t *testing.T) {
	py := needPython(t)
	needGit(t)
	fixtures := map[string]map[string]string{
		"empty":    {"README.md": "# demo\n"},
		"go":       {"README.md": "# demo\n", "go.mod": "module demo\n", "main.go": "package main\n"},
		"python":   {"README.md": "# demo\n", "tests/test_a.py": "import unittest\n", "requirements.txt": "pytest\n"},
		"node":     {"package.json": `{"scripts": {"test": "jest", "build": "tsc", "lint": "eslint ."}}`},
		"legacy":   {"check.sh": "#!/bin/sh\n", ".gitignore": "node_modules"},
		"makefile": {"Makefile": "check:\n\techo ok\n"},
	}
	kitBin, err := filepath.Abs("../../kit/bin/factory")
	if err != nil {
		t.Fatal(err)
	}
	for fname, files := range fixtures {
		for _, preset := range []string{"generic", "web"} {
			t.Run(fname+"/"+preset, func(t *testing.T) {
				mk := func() string {
					root := filepath.Join(t.TempDir(), "demo")
					writeTree(t, root, files)
					mustGit(t, "", "", "init", "-q", root)
					return root
				}
				want, got := mk(), mk()
				cmd := exec.Command(py, kitBin, "init", want, "--preset", preset)
				cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("factory init: %v: %s", err, out)
				}
				if _, _, err := seedKit(got, "demo", preset); err != nil {
					t.Fatal(err)
				}
				wt, gt := treeOf(t, want), treeOf(t, got)
				for rel, w := range wt {
					g, ok := gt[rel]
					if !ok {
						t.Errorf("%s: the Go version does not write it", rel)
					} else if g != w {
						t.Errorf("%s differs:\nfactory init:\n%s\nGo:\n%s", rel, w, g)
					}
				}
				for rel := range gt {
					if _, ok := wt[rel]; !ok {
						t.Errorf("%s: only the Go version writes it", rel)
					}
				}
			})
		}
	}
}

// The project the hub makes is one the kit's own gate accepts: ./check runs and passes in a clone of main.
func TestPreparedChecksPassTheKitsOwnCheck(t *testing.T) {
	needPython(t)
	e := hostedEnv(t)
	e.host("site")
	e.checksDo(e.afif(), "site", "prepare", "")
	e.checksDo(e.afif(), "site", "apply", "")
	dir := e.cloneOf("site")
	cmd := exec.Command("./check")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("./check in the prepared project: %v\n%s", err, out.String())
	}
	if st, err := os.Stat(filepath.Join(dir, "check")); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("./check is not executable in a clone: %v", err)
	}
}

// A project whose kit is older than the hub's is offered an update that refreshes only the kit's own files.
func TestChecksCanBeUpdated(t *testing.T) {
	e := hostedEnv(t)
	e.host("site")
	e.checksDo(e.afif(), "site", "prepare", "")
	e.checksDo(e.afif(), "site", "apply", "")
	if st := e.checksState("site"); st.State != "has" || st.Outdated {
		t.Fatalf("a fresh kit is not outdated: %+v", st)
	}
	if code, _, _ := e.checksDo(e.afif(), "site", "prepare", ""); code != 409 {
		t.Fatalf("an update when there is nothing to update: status %d, want 409", code)
	}
	// Make the project's kit old, and give it a toml of its own that an update must leave alone.
	dir := e.cloneOf("site")
	if err := os.WriteFile(filepath.Join(dir, ".factory", "VERSION"), []byte("0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".factory", "check.py"), []byte("# old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "", "add", "-A")
	mustGit(t, dir, "", "commit", "-q", "-m", "an old kit")
	mustGit(t, "", "", "--git-dir", e.hub.repoPath("site"), "fetch", "-q", dir, "main:main")
	toml := e.bareGit("site", "show", "main:factory.toml")
	if st := e.checksState("site"); !st.Outdated {
		t.Fatalf("an old kit should be outdated: %+v", st)
	}
	code, st, raw := e.checksDo(e.afif(), "site", "prepare", "")
	if code != 200 || !st.Update || len(st.Files) != 2 || !hasString(st.Files, ".factory/check.py") || !hasString(st.Files, ".factory/VERSION") {
		t.Fatalf("prepare update: %d %s", code, raw)
	}
	if code, st, _ := e.checksDo(e.afif(), "site", "apply", ""); code != 200 || !st.Applied {
		t.Fatalf("apply update: %d", code)
	}
	if got := e.bareGit("site", "show", "main:factory.toml"); got != toml {
		t.Fatal("an update changed the project's own factory.toml")
	}
	if got := strings.TrimSpace(e.bareGit("site", "show", "main:.factory/VERSION")); got != kitVersion() {
		t.Fatalf("version after update: %s", got)
	}
	if st := e.checksState("site"); st.Outdated {
		t.Fatalf("still outdated after the update: %+v", st)
	}
}
