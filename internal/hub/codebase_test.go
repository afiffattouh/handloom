package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestProjectRemoteValidation(t *testing.T) {
	good := []string{
		"https://github.com/owner/name", "https://github.com/owner/name.git", "ssh://git@github.com/owner/name.git",
		"git@github.com:owner/name.git", "https://git.example.com:8443/team/sub/name", "ssh://example.com/x/y",
	}
	for _, r := range good {
		if err := checkRemote(r, false); err != nil {
			t.Errorf("%q refused: %v", r, err)
		}
	}
	bad := map[string]string{
		"user:pass@":      "https://user:pass@github.com/o/n",
		"token as user":   "https://ghp_abcdef0123456789@github.com/o/n",
		"x-access-token":  "https://x-access-token:ghp_abc@github.com/o/n",
		"ssh password":    "ssh://git:secret@github.com/o/n",
		"ssh token user":  "ssh://ghp_ABCdef@github.com/o/n",
		"query token":     "https://github.com/o/n?token=abc",
		"fragment":        "https://github.com/o/n#x",
		"local path":      "/home/me/repo",
		"relative path":   "../repo",
		"home path":       "~/repo",
		"bare word":       "github.com/o/n",
		"space":           "https://github.com/o/my repo",
		"newline":         "https://github.com/o/n\nx",
		"tab":             "https://github.com/o/n\tx",
		"dash":            "--upload-pack=x",
		"http":            "http://github.com/o/n",
		"file by default": "file:///tmp/repo.git",
		"ftp":             "ftp://example.com/x",
		"too long":        "https://github.com/o/" + strings.Repeat("a", 300),
		"dotdot":          "https://github.com/o/../n",
		"no path":         "https://github.com",
		"empty":           "",
		"scp with pw":     "git:pw@github.com:o/n",
		"scp dash host":   "git@-oProxyCommand=x:o/n",
	}
	for why, r := range bad {
		if err := checkRemote(r, false); err == nil {
			t.Errorf("%s: %q was accepted", why, r)
		}
	}
	if err := checkRemote("file:///tmp/repo.git", true); err != nil {
		t.Errorf("file:// with the flag: %v", err)
	}
	if err := checkRemote("file://host/tmp/repo.git", true); err == nil {
		t.Error("file:// with a host was accepted")
	}
	for _, n := range []string{"billing", "a.b_c-d", strings.Repeat("a", 40)} {
		if checkCodebaseName(n) != nil {
			t.Errorf("name %q refused", n)
		}
	}
	for _, n := range []string{"", "Billing", "-x", ".x", "a b", "a/b", strings.Repeat("a", 41), "é"} {
		if checkCodebaseName(n) == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestProjectsAttachListAndWhoMay(t *testing.T) {
	e := newEnv(t)
	var op, viewer api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "vera"}, &viewer)
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'vera'`)

	req := api.CodebaseReq{Name: "billing", Remote: "https://github.com/acme/billing.git", Base: "main"}
	for name, c := range map[string]caller{"viewer": {viewer.Token, ""}, "lead": e.lead(), "worker": e.worker(), "observer": e.observer(), "admin": {e.admin, ""}, "ai app": {op.Token, ""}} {
		for _, path := range []string{"/v1/codebases", "/v1/codebases/create", "/v1/codebases/billing/clone"} {
			if code, _ := e.do(c, "POST", path, req); code != 403 {
				t.Errorf("%s: POST %s gave %d, want 403", name, path, code)
			}
		}
	}
	var list []api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases", nil, &list)
	if len(list) != 0 {
		t.Fatalf("refused requests left projects: %+v", list)
	}
	e.fail(400, e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "x", Remote: "https://tok:en@github.com/a/b"})
	e.fail(400, e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "x", Remote: "file:///tmp/x.git"})
	e.fail(400, e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "Bad Name", Remote: "https://github.com/a/b"})
	if e.audited("project.attach", "") {
		t.Fatal("a refused attach is in the audit log as done")
	}

	var cb api.Codebase
	e.ok(e.afif(), "POST", "/v1/codebases", req, &cb)
	if cb.Name != "billing" || cb.Base != "main" || len(cb.Clones) != 0 {
		t.Fatalf("attached: %+v", cb)
	}
	if !e.audited("project.attach", "project:billing") {
		t.Fatal("project.attach is not in the audit log")
	}
	e.fail(409, e.afif(), "POST", "/v1/codebases", req)

	// An AI app, a viewer and an agent can read.
	for name, c := range map[string]caller{"ai app": {op.Token, ""}, "viewer": {viewer.Token, ""}, "lead": e.lead()} {
		var got api.Codebase
		e.ok(c, "GET", "/v1/codebases/billing", nil, &got)
		var all []api.Codebase
		e.ok(c, "GET", "/v1/codebases", nil, &all)
		if got.Remote != req.Remote || len(all) != 1 {
			t.Errorf("%s reads: %+v %+v", name, got, all)
		}
	}
	e.fail(404, e.afif(), "GET", "/v1/codebases/nothing", nil)
	// The hub's own tenancy list is not touched.
	var tenants []api.Project
	e.ok(e.afif(), "GET", "/v1/projects", nil, &tenants)
	if len(tenants) != 1 || tenants[0].Name != "default" {
		t.Fatalf("tenancy projects: %+v", tenants)
	}

	e.hub.opt.AllowFileRemotes = true
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "local", Remote: "file:///tmp/x.git"}, nil)
}

func TestProjectCloneLifecycle(t *testing.T) {
	e := newEnv(t)
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "billing", Remote: "https://github.com/acme/billing"}, nil)

	var q api.CodebaseRequest
	e.fail(404, e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "nope"})
	e.ok(e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
	if q.Status != "pending" || q.Kind != "clone" || q.Device != "d1" {
		t.Fatalf("request: %+v", q)
	}
	if !e.audited("project.clone", "project:billing") {
		t.Fatal("project.clone is not in the audit log")
	}
	// A second one while the first waits, and a request for a project with no address, are refused.
	if msg := e.fail(409, e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d1"}); !strings.Contains(msg, "already being fetched") {
		t.Fatalf("double clone: %s", msg)
	}
	var cb api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases/billing", nil, &cb)
	if len(cb.Clones) != 1 || cb.Clones[0].Status != "pending" {
		t.Fatalf("clones: %+v", cb.Clones)
	}

	// Devices: only the right one sees the request and reports on it, once, pending only.
	var pend []api.PendingCodebaseRequest
	e.fail(403, e.afif(), "GET", "/v1/device/project-requests", nil)
	e.ok(caller{e.d2, ""}, "GET", "/v1/device/project-requests", nil, &pend)
	if len(pend) != 0 {
		t.Fatalf("another device sees it: %+v", pend)
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/project-requests", nil, &pend)
	if len(pend) != 1 || pend[0].Remote != "https://github.com/acme/billing" || pend[0].Kind != "clone" || pend[0].Name != "billing" {
		t.Fatalf("pending: %+v", pend)
	}
	report := fmt.Sprintf("/v1/project-requests/%d/report", q.ID)
	rep := api.CodebaseReport{Status: "done", Path: "/home/x/projects/billing", Base: "trunk"}
	e.fail(403, e.afif(), "POST", report, rep)
	e.fail(403, caller{e.d2, ""}, "POST", report, rep)
	e.fail(403, e.lead(), "POST", report, rep) // an agent on d1 reaching the hub is still not the link
	e.fail(400, caller{e.d1, ""}, "POST", report, api.CodebaseReport{Status: "merged"})
	e.fail(400, caller{e.d1, ""}, "POST", report, api.CodebaseReport{Status: "done", Path: "relative"})
	e.fail(404, caller{e.d1, ""}, "POST", "/v1/project-requests/999/report", rep)
	e.ok(caller{e.d1, ""}, "POST", report, rep, nil)
	e.fail(409, caller{e.d1, ""}, "POST", report, rep)
	if !e.audited("project.clone_done", "project:billing") {
		t.Fatal("project.clone_done is not in the audit log")
	}
	e.ok(e.afif(), "GET", "/v1/codebases/billing", nil, &cb)
	if cb.Base != "trunk" || cb.Clones[0].Status != "ready" || cb.Clones[0].Path != "/home/x/projects/billing" {
		t.Fatalf("after the report: %+v", cb)
	}
	// Ready: asking again answers with the path and queues nothing.
	e.ok(e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
	if q.Status != "ready" || q.Path != "/home/x/projects/billing" {
		t.Fatalf("clone of a ready one: %+v", q)
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/project-requests", nil, &pend)
	if len(pend) != 0 {
		t.Fatalf("a ready clone queued work: %+v", pend)
	}

	// A failure is recorded in words, audited, and can be asked for again.
	e.ok(e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d2"}, &q)
	e.ok(caller{e.d2, ""}, "POST", fmt.Sprintf("/v1/project-requests/%d/report", q.ID), api.CodebaseReport{Status: "failed", Detail: "no access"}, nil)
	if !e.audited("project.clone_failed", "project:billing") {
		t.Fatal("project.clone_failed is not in the audit log")
	}
	e.ok(e.afif(), "GET", "/v1/codebases/billing", nil, &cb)
	if cb.Clones[1].Device != "d2" || cb.Clones[1].Status != "failed" || cb.Clones[1].Detail != "no access" {
		t.Fatalf("failed clone: %+v", cb.Clones)
	}
	e.ok(e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d2"}, &q)
	if q.Status != "pending" {
		t.Fatalf("asking again after a failure: %+v", q)
	}
}

func TestProjectCreateLifecycle(t *testing.T) {
	e := newEnv(t)
	e.hub.opt.AllowFileRemotes = true
	var op api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	for name, c := range map[string]caller{"ai app": {op.Token, ""}, "lead": e.lead(), "worker": e.worker()} {
		if code, _ := e.do(c, "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Device: "d1"}); code != 403 {
			t.Errorf("%s created a repository: %d", name, code)
		}
	}
	e.fail(400, e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Owner: "bad owner!", Device: "d1"})
	e.fail(404, e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Device: "nope"})

	no := false
	var q api.CodebaseRequest
	e.ok(e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Owner: "acme", Private: &no, Device: "d1"}, &q)
	if q.Kind != "create" || q.Status != "pending" {
		t.Fatalf("create: %+v", q)
	}
	if !e.audited("project.create", "project:fresh") {
		t.Fatal("project.create is not in the audit log")
	}
	var cb api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases/fresh", nil, &cb)
	if cb.Remote != "" {
		t.Fatalf("the address is known before the machine made the repository: %+v", cb)
	}
	e.fail(409, e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Device: "d1"})
	e.fail(409, e.afif(), "POST", "/v1/codebases/fresh/clone", api.CodebaseCloneReq{Device: "d2"})

	var pend []api.PendingCodebaseRequest
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/project-requests", nil, &pend)
	if len(pend) != 1 || pend[0].Kind != "create" || pend[0].Owner != "acme" || pend[0].Private {
		t.Fatalf("pending: %+v", pend)
	}
	// A done report must carry an acceptable address.
	report := fmt.Sprintf("/v1/project-requests/%d/report", q.ID)
	e.fail(400, caller{e.d1, ""}, "POST", report, api.CodebaseReport{Status: "done", Path: "/p/fresh", Remote: "https://tok:en@github.com/acme/fresh"})
	e.ok(caller{e.d1, ""}, "POST", report, api.CodebaseReport{Status: "done", Path: "/p/fresh", Remote: "https://github.com/acme/fresh", Base: "main"}, nil)
	e.ok(e.afif(), "GET", "/v1/codebases/fresh", nil, &cb)
	if cb.Remote != "https://github.com/acme/fresh" || cb.Base != "main" || len(cb.Clones) != 1 || cb.Clones[0].Status != "ready" || cb.Clones[0].Path != "/p/fresh" {
		t.Fatalf("after the report: %+v", cb)
	}
	if !e.audited("project.create_done", "project:fresh") {
		t.Fatal("project.create_done is not in the audit log")
	}
	e.fail(409, e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "fresh", Device: "d1"})

	// A creation that failed leaves a project without an address that can be asked for again.
	e.ok(e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "second", Device: "d1"}, &q)
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/project-requests/%d/report", q.ID), api.CodebaseReport{Status: "failed", Detail: "install gh"}, nil)
	if !e.audited("project.create_failed", "project:second") {
		t.Fatal("project.create_failed is not in the audit log")
	}
	e.ok(e.afif(), "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: "second", Device: "d1"}, &q)
	if q.Status != "pending" {
		t.Fatalf("second try: %+v", q)
	}
}

func TestAJobStartsFromAProject(t *testing.T) {
	e := newEnv(t)
	e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "billing", Remote: "https://github.com/acme/billing", Base: "develop"}, nil)

	// No ready copy on d1: refused in plain words that name the command.
	msg := e.fail(409, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J", Codebase: "billing", Device: "d1"})
	if !strings.Contains(msg, "handloom project clone billing --device d1") {
		t.Fatalf("no clone: %s", msg)
	}
	e.fail(404, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J", Codebase: "nothing", Device: "d1"})
	e.fail(400, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J", Codebase: "billing", Repo: "/srv/app", Device: "d1"})

	var q api.CodebaseRequest
	e.ok(e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
	e.fail(409, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J", Codebase: "billing", Device: "d1"}) // still pending
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/project-requests/%d/report", q.ID), api.CodebaseReport{Status: "done", Path: "/home/x/projects/billing"}, nil)
	e.fail(409, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J", Codebase: "billing", Device: "d2"}) // another machine has none

	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J", Codebase: "billing", Device: "d1", Lead: "lead"}, &j)
	if j.Repo != "/home/x/projects/billing" || j.Base != "develop" || j.Codebase != "billing" || j.Device != "d1" {
		t.Fatalf("job: %+v", j)
	}
	// A base the job names wins over the project's.
	var j2 api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "J2", Codebase: "billing", Device: "d1", Base: "main"}, &j2)
	if j2.Base != "main" {
		t.Fatalf("job base: %+v", j2)
	}
	var got api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	var list []api.Job
	e.ok(e.afif(), "GET", "/v1/jobs", nil, &list)
	if got.Codebase != "billing" || len(list) != 2 || list[0].Codebase != "billing" {
		t.Fatalf("get %+v list %+v", got, list)
	}
	// An AI app may start a job on a ready copy (it starts work) but cannot fetch one.
	var op api.TokenResp
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	e.ok(caller{op.Token, ""}, "POST", "/v1/jobs", api.JobNewReq{Title: "J3", Codebase: "billing", Device: "d1"}, nil)
}
