package hub

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestProjectsPageAndForms(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.hub.opt.AllowFileRemotes = false
	ag := e.agents() // a joined device d1
	c, tok := e.owner()

	r := e.req("GET", "/projects", nil, c, nil)
	for _, want := range []string{"No projects yet", "I already have a repository", "Make it on GitHub", "Let Handloom hold the code", `name="h_name"`, `name="a_remote"`, `name="c_private" value="1" checked`, `<option value="d1"`, `href="/projects"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("empty page lacks %q", want)
		}
	}
	if !strings.Contains(r.body, `aria-current="page"`) {
		t.Error("the menu does not mark the page")
	}
	if strings.Contains(r.body, "<script") && strings.Contains(r.body, "onclick") {
		t.Error("inline script handlers on the page")
	}

	attach := func(extra url.Values) webResp {
		f := url.Values{"a_name": {"billing"}, "a_remote": {"https://github.com/acme/billing"}, "a_base": {"main"}, "csrf": {tok}}
		for k, v := range extra {
			f[k] = v
		}
		return e.req("POST", "/projects/attach", f, c, nil)
	}
	// A refused form shows the reason in words and what was typed, escaped.
	evil := `https://u:p@evil.test/"><script>alert(1)</script>`
	r = attach(url.Values{"a_remote": {evil}})
	if r.status != 400 || !strings.Contains(r.body, "password or token") || strings.Contains(r.body, "<script>alert(1)") || !strings.Contains(r.body, `value="billing"`) {
		t.Fatalf("a credential-bearing address: %d %s", r.status, r.body)
	}
	if r := attach(url.Values{"a_remote": {"/home/me/repo"}}); r.status != 400 || !strings.Contains(r.body, "not a shared address") {
		t.Fatalf("a local path: %d", r.status)
	}
	if r := e.req("POST", "/projects/attach", url.Values{"a_name": {"x"}, "a_remote": {"https://github.com/a/b"}}, c, nil); r.status == 303 {
		t.Fatal("attached without the CSRF token")
	}
	var list []api.Codebase
	e.apiOK(e.admin, "", "GET", "/v1/codebases", nil, &list)
	if len(list) != 0 {
		t.Fatalf("refused forms left projects: %+v", list)
	}

	if r := attach(nil); r.status != 303 || r.header.Get("Location") != "/projects?done=attached" {
		t.Fatalf("attach: %d %s", r.status, r.body)
	}
	if r := attach(nil); r.status != 409 || !strings.Contains(r.body, "already a project") {
		t.Fatalf("attach twice: %d", r.status)
	}
	if !e.audited("project.attach", "project:billing") {
		t.Fatal("project.attach is not in the audit log")
	}
	page := e.req("GET", "/projects?done=attached", nil, c, nil)
	for _, want := range []string{"billing", `href="https://github.com/acme/billing"`, "Project added.", "No computer has a copy yet", "Clone to a computer"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	// ssh addresses are plain text, never links.
	e.apiOK(e.humanAPI(), "", "POST", "/v1/codebases", api.CodebaseReq{Name: "sshy", Remote: "git@github.com:acme/sshy.git"}, nil)
	page = e.req("GET", "/projects", nil, c, nil)
	if !strings.Contains(page.body, "git@github.com:acme/sshy.git") || strings.Contains(page.body, `href="git@`) {
		t.Fatal("an ssh address is rendered wrongly")
	}

	// Clone to a machine.
	clone := func(name, dev string) webResp {
		return e.req("POST", "/projects/"+name+"/clone", url.Values{"device": {dev}, "csrf": {tok}}, c, nil)
	}
	if r := clone("billing", "d1"); r.status != 303 || r.header.Get("Location") != "/projects?done=cloning" {
		t.Fatalf("clone: %d %s", r.status, r.body)
	}
	if r := clone("billing", "d1"); r.status != 409 || !strings.Contains(r.body, "already being fetched") {
		t.Fatalf("clone twice: %d", r.status)
	}
	if r := clone("nothing", "d1"); r.status != 404 {
		t.Fatalf("unknown project: %d", r.status)
	}
	var pend []api.PendingCodebaseRequest
	e.apiOK(ag.device, "", "GET", "/v1/device/project-requests", nil, &pend)
	if len(pend) != 1 {
		t.Fatalf("pending: %+v", pend)
	}
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/project-requests/%d/report", pend[0].ID), api.CodebaseReport{Status: "done", Path: "/home/x/projects/billing", Base: "main"}, nil)
	page = e.req("GET", "/projects", nil, c, nil)
	if !strings.Contains(page.body, "ready") || !strings.Contains(page.body, "/home/x/projects/billing") {
		t.Fatalf("ready state not shown: %s", page.body)
	}
	if r := clone("billing", "d1"); r.status != 303 || r.header.Get("Location") != "/projects?done=ready" {
		t.Fatalf("clone of a ready one: %d %s", r.status, r.header.Get("Location"))
	}

	// The job form offers the project only on the machine where it is ready, and the project wins over the folder.
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	form := e.req("GET", "/jobs/new", nil, c, nil)
	if !strings.Contains(form.body, `<option value="billing|d1"`) || strings.Contains(form.body, `value="sshy|`) || !strings.Contains(form.body, "Not on any computer yet: sshy") {
		t.Fatalf("job form: %s", form.body)
	}
	jf := url.Values{"title": {"From a project"}, "device": {"d1"}, "repo": {"/ignored/path"}, "project": {"billing|d1"}, "lead_profile": {"boss"}, "csrf": {tok}}
	if r := e.req("POST", "/jobs", jf, c, nil); r.status != 303 {
		t.Fatalf("job from a project: %d %s", r.status, r.body)
	}
	var js []api.Job
	e.apiOK(e.admin, "", "GET", "/v1/jobs", nil, &js)
	if len(js) != 1 || js[0].Repo != "/home/x/projects/billing" || js[0].Codebase != "billing" || js[0].Base != "main" {
		t.Fatalf("job: %+v", js)
	}
	if r := e.req("GET", "/jobs", nil, c, nil); !strings.Contains(r.body, "billing") {
		t.Fatal("the jobs list does not show the project")
	}
	if r := e.req("GET", "/jobs/1", nil, c, nil); !strings.Contains(r.body, "project <strong>billing</strong>") {
		t.Fatal("the job page does not show the project")
	}

	// Create a repository.
	create := func(extra url.Values) webResp {
		f := url.Values{"c_name": {"fresh"}, "c_owner": {"acme"}, "c_device": {"d1"}, "c_private": {"1"}, "csrf": {tok}}
		for k, v := range extra {
			f[k] = v
		}
		return e.req("POST", "/projects/create", f, c, nil)
	}
	if r := create(url.Values{"c_owner": {"bad owner"}}); r.status != 400 || !strings.Contains(r.body, "bad owner") {
		t.Fatalf("bad owner: %d", r.status)
	}
	if r := create(url.Values{"c_device": {"nope"}}); r.status != 404 {
		t.Fatalf("bad machine: %d", r.status)
	}
	if r := create(nil); r.status != 303 || r.header.Get("Location") != "/projects?done=creating" {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	e.apiOK(ag.device, "", "GET", "/v1/device/project-requests", nil, &pend)
	if len(pend) != 1 || pend[0].Kind != "create" || pend[0].Owner != "acme" || !pend[0].Private {
		t.Fatalf("create request: %+v", pend)
	}
	if !e.audited("project.create", "project:fresh") {
		t.Fatal("project.create is not in the audit log")
	}
	if r := create(url.Values{"c_name": {"open"}, "c_private": {}}); r.status != 303 {
		t.Fatalf("create public: %d", r.status)
	}
	e.apiOK(ag.device, "", "GET", "/v1/device/project-requests", nil, &pend)
	var public int
	for _, p := range pend {
		if p.Name == "open" && !p.Private {
			public++
		}
	}
	if public != 1 {
		t.Fatalf("an unticked box did not make a public repository: %+v", pend)
	}
	if r := e.req("GET", "/projects", nil, c, nil); !strings.Contains(r.body, "the repository is not made yet") {
		t.Fatal("a project being created is not shown as such")
	}

	// A viewer reads and cannot change anything.
	vc, vtok := e.member("vera", "viewer")
	if r := e.req("GET", "/projects", nil, vc, nil); r.status != 200 || strings.Contains(r.body, `action="/projects/attach"`) || !strings.Contains(r.body, "billing") {
		t.Fatalf("a viewer's page: %d", r.status)
	}
	for _, path := range []string{"/projects/attach", "/projects/create", "/projects/billing/clone"} {
		if r := e.req("POST", path, url.Values{"a_name": {"v"}, "a_remote": {"https://github.com/a/b"}, "c_name": {"v"}, "device": {"d1"}, "csrf": {vtok}}, vc, nil); r.status != 403 {
			t.Errorf("a viewer posted to %s: %d", path, r.status)
		}
	}
}
