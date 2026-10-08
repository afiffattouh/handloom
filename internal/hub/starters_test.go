package hub

import (
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/starters"
)

func TestTheStarterLibraryIsListedAndAddedLikeAnyProfile(t *testing.T) {
	e := newWebEnv(t, Options{})
	var list []api.StarterInfo
	e.apiOK(e.admin, "", "GET", "/v1/starters", nil, &list)
	if len(list) < 40 {
		t.Fatalf("only %d starters", len(list))
	}
	for _, s := range list {
		if s.Added || len(s.Tools) == 0 {
			t.Fatalf("a new hub: %+v", s)
		}
	}
	var full api.StarterFull
	e.apiOK(e.admin, "", "GET", "/v1/starters/coder", nil, &full)
	if full.Spec.Kind != "" || full.Spec.Runtime != "" || full.WhatItCanDo == "" || len(full.Spec.Skills) == 0 {
		t.Fatalf("a starter has no CLI or runtime yet: %+v", full.Spec)
	}

	var added api.StarterAddResp
	e.apiOK(e.admin, "", "POST", "/v1/starters/coder/add", api.StarterAddReq{Kind: "claude", Runtime: "cloud"}, &added)
	if added.Profile.Name != "coder" || added.Profile.Version != 1 {
		t.Fatalf("added: %+v", added)
	}
	// It is an ordinary profile: the same hash a hand-made copy of the spec would get.
	want, _ := starters.Build("coder", starters.Choice{Kind: "claude", Runtime: "cloud"})
	if added.Profile.Hash != profile.Hash(want.Spec) {
		t.Fatalf("hash differs from the template's")
	}
	var p api.ProfileFull
	e.apiOK(e.admin, "", "GET", "/v1/profiles/coder", nil, &p)
	if p.Spec.Kind != "claude" || p.Spec.Prompt == "" {
		t.Fatalf("profile: %+v", p.Spec)
	}
	e.apiOK(e.admin, "", "GET", "/v1/starters", nil, &list)
	for _, s := range list {
		if s.Name == "coder" != s.Added {
			t.Fatalf("only coder is added: %+v", s)
		}
	}
	// Twice is refused, not silently versioned.
	if code := e.status(e.admin, "POST", "/v1/starters/coder/add", api.StarterAddReq{Kind: "claude", Runtime: "cloud"}); code != 409 {
		t.Fatalf("adding it again: %d", code)
	}
	e.apiOK(e.admin, "", "POST", "/v1/starters/coder/add", api.StarterAddReq{Name: "coder-2", Kind: "claude", Runtime: "local"}, nil)

	// Choices are required, and Codex's limits are said in words.
	if code := e.status(e.admin, "POST", "/v1/starters/writer/add", api.StarterAddReq{}); code != 400 {
		t.Fatalf("no choice: %d", code)
	}
	var out struct{ Error string }
	if code := e.statusInto(e.admin, "POST", "/v1/starters/writer/add", api.StarterAddReq{Kind: "codex", Runtime: "cloud"}, &out); code != 400 || !strings.Contains(out.Error, "always has a shell") {
		t.Fatalf("codex without adapt: %d %s", code, out.Error)
	}
	var adapted api.StarterAddResp
	e.apiOK(e.admin, "", "POST", "/v1/starters/writer/add", api.StarterAddReq{Kind: "codex", Runtime: "cloud", Adapt: true}, &adapted)
	if len(adapted.Adapted) == 0 || !strings.Contains(strings.Join(adapted.Adapted, " "), "shell") {
		t.Fatalf("adapted: %+v", adapted)
	}
	// Not the owner: refused.
	if code := e.status(e.humanAPI(), "POST", "/v1/starters/editor/add", api.StarterAddReq{Kind: "claude", Runtime: "cloud"}); code != 403 {
		t.Fatalf("a member adds a starter: %d", code)
	}
	if code := e.status(e.admin, "GET", "/v1/starters/nope", nil); code != 404 {
		t.Fatalf("unknown starter: %d", code)
	}
}

func TestTheStarterPagesAndTheAssistedForm(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	list := e.req("GET", "/profiles/starters", nil, c, nil)
	for _, want := range []string{"Starter library", "Leads", "Engineering", "Consulting", "Marketing", "Sales", "Finance", "HR", "Frontend designer", "prefer local", "plan-a-job"} {
		if !strings.Contains(list.body, want) {
			t.Errorf("starter list lacks %q", want)
		}
	}
	page := e.req("GET", "/profiles/starters/financial-analyst", nil, c, nil)
	for _, want := range []string{"What it can do", "Its instructions", "Skills it carries", "need a human check", `>Choose…</option>`, "confidential"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("starter page lacks %q", want)
		}
	}
	add := func(extra url.Values) webResp {
		f := url.Values{"name": {"coder"}, "kind": {"claude"}, "runtime": {"cloud"}, "current_password": {goodPassword}, "csrf": {tok}}
		for k, v := range extra {
			f[k] = v
		}
		return e.req("POST", "/profiles/starters/coder/add", f, c, nil)
	}
	r := add(url.Values{"kind": {"codex"}})
	if r.status != 400 || !strings.Contains(r.body, "cannot refuse specific shell commands") || !strings.Contains(r.body, "Add it without what that CLI cannot do") {
		t.Fatalf("codex: %d", r.status)
	}
	if r := add(url.Values{"kind": {""}}); r.status != 400 || !strings.Contains(r.body, "Choose the agent CLI") {
		t.Fatalf("no choice: %d", r.status)
	}
	if r := add(url.Values{"current_password": {"wrong password!"}}); r.status == 303 {
		t.Fatal("added without the password")
	}
	r = add(nil)
	if r.status != 303 || r.header.Get("Location") != "/profiles/coder?done=added" {
		t.Fatalf("add: %d %s", r.status, r.body)
	}
	if body := e.req("GET", "/profiles/coder?done=added", nil, c, nil).body; !strings.Contains(body, "Added from the starter library") || !strings.Contains(body, "In plain words") {
		t.Fatalf("profile page after adding")
	}
	if body := e.req("GET", "/profiles/starters", nil, c, nil).body; !strings.Contains(body, "Added") {
		t.Fatal("the list does not say it is added")
	}
	if r := add(nil); r.status != 409 || !strings.Contains(r.body, "already have a profile called") {
		t.Fatalf("again: %d", r.status)
	}

	// A member can look but not add.
	mc, mtok := e.member("mia", "member")
	if r := e.req("GET", "/profiles/starters/coder", nil, mc, nil); r.status != 200 || strings.Contains(r.body, `name="kind"`) {
		t.Fatalf("member view: %d", r.status)
	}
	if r := e.req("POST", "/profiles/starters/lead/add", url.Values{"kind": {"claude"}, "runtime": {"cloud"}, "csrf": {mtok}, "current_password": {goodPassword}}, mc, nil); r.status != 403 {
		t.Fatalf("a member adds: %d", r.status)
	}

	// The new-profile form: start from a starter, and check before saving without saving.
	form := e.req("GET", "/profiles/new", nil, c, nil).body
	for _, want := range []string{"Start from a ready-made profile", `name="kind"`, "Check before saving", "In plain words", `data-preset="read,edit"`} {
		if !strings.Contains(form, want) {
			t.Errorf("new form lacks %q", want)
		}
	}
	if from := e.req("GET", "/profiles/new?from=researcher", nil, c, nil).body; !strings.Contains(from, "Started from the") || !strings.Contains(from, "research-protocol") {
		t.Fatalf("from a starter")
	}
	chk := e.req("POST", "/profiles", url.Values{"name": {"scratch"}, "kind": {"claude"}, "runtime": {"cloud"}, "tool": {"edit", "shell"}, "write": {"**"}, "check": {"1"}, "csrf": {tok}}, c, nil)
	for _, want := range []string{"Before you save", "any shell command", "matches every file", "cannot read files", "There are no instructions"} {
		if !strings.Contains(chk.body, want) {
			t.Errorf("the check lacks %q", want)
		}
	}
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM profile WHERE name = 'scratch'`).Scan(&n)
	if chk.status != 200 || n != 0 {
		t.Fatalf("checking saved something (%d profiles)", n)
	}
	// What a CLI cannot do is fitted, not refused, and the check says so.
	chk = e.req("POST", "/profiles", url.Values{"name": {"x"}, "kind": {"codex"}, "tool": {"read"}, "check": {"1"}, "csrf": {tok}}, c, nil)
	if !strings.Contains(chk.body, "Fitted to this agent CLI") || !strings.Contains(chk.body, "always has a shell") || strings.Contains(chk.body, `role="alert"`) {
		t.Fatalf("codex fitting not shown: %s", chk.body)
	}
}

func TestADeniedCommandOnACLIThatCannotRefuseItIsAskedNotRefused(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	save := url.Values{"name": {"careful-pi"}, "kind": {"pi"}, "runtime": {"local"}, "model": {"gb10/qwen3.8-27b"}, "tool": {"read", "edit", "shell", "web"},
		"deny": {"rm\ngit push"}, "prompt": {"Be careful."}, "csrf": {tok}, "current_password": {goodPassword}}
	r := e.req("POST", "/profiles", save, c, nil)
	if r.status != 303 || !strings.Contains(r.header.Get("Location"), "done=adapted") {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	page := e.req("GET", "/profiles/careful-pi?done=adapted", nil, c, nil).body
	for _, want := range []string{"asked in its instructions, not enforced: do not run rm, git push", "Some settings were fitted"} {
		if !strings.Contains(page, want) {
			t.Errorf("profile page lacks %q", want)
		}
	}
	if strings.Contains(page, "Pi has no web tool: remove web") {
		t.Error("a refusal leaked")
	}
}

func TestAProfileIsNotReplacedByAccident(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	post := func(path string, f url.Values) webResp {
		f.Set("csrf", tok)
		f.Set("current_password", goodPassword)
		return e.req("POST", path, f, c, nil)
	}
	create := url.Values{"name": {"lead"}, "kind": {"claude"}, "runtime": {"cloud"}, "tool": {"read"}, "prompt": {"You lead."}}
	if r := post("/profiles", create); r.status != 303 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	// Making another profile under the same name would silently replace what new agents get: refused, in words.
	local := url.Values{"name": {"lead"}, "kind": {"omp"}, "runtime": {"local"}, "model": {"gb10/qwen3.8-27b"}, "tool": {"read", "shell"}, "prompt": {"You lead."}}
	if r := post("/profiles", local); r.status != 409 || !strings.Contains(r.body, "already have a profile called") {
		t.Fatalf("a second profile with the same name: %d", r.status)
	}
	// Editing it into a different CLI or runtime needs a yes, and says what changes.
	edit := url.Values{"kind": {"omp"}, "runtime": {"local"}, "model": {"gb10/qwen3.8-27b"}, "tool": {"read", "shell"}, "prompt": {"You lead."}}
	r := post("/profiles/lead", edit)
	if r.status != 409 || !strings.Contains(r.body, "This changes what lead is") || !strings.Contains(r.body, `name="confirm_change"`) || !strings.Contains(r.body, "under a new name") {
		t.Fatalf("an unconfirmed change: %d %s", r.status, r.body)
	}
	var vs []api.ProfileInfo
	e.apiOK(e.admin, "", "GET", "/v1/profiles/lead/versions", nil, &vs)
	if len(vs) != 1 {
		t.Fatalf("nothing was saved yet: %+v", vs)
	}
	edit.Set("confirm_change", "1")
	if r := post("/profiles/lead", edit); r.status != 303 {
		t.Fatalf("confirmed: %d", r.status)
	}
	e.apiOK(e.admin, "", "GET", "/v1/profiles/lead/versions", nil, &vs)
	if len(vs) != 2 || vs[0].Kind != "claude" && vs[1].Kind != "claude" {
		t.Fatalf("both versions are kept: %+v", vs)
	}
	if body := e.req("GET", "/profiles", nil, c, nil).body; !strings.Contains(body, "2 versions") {
		t.Fatal("the list does not say that a profile has more than one version")
	}
	if body := e.req("GET", "/profiles/lead", nil, c, nil).body; !strings.Contains(body, "v1 (claude, cloud)") || !strings.Contains(body, "v2 (omp, local)") {
		t.Fatalf("the profile page does not show what each version was")
	}
	// A change that keeps the CLI and the runtime needs no yes.
	same := url.Values{"kind": {"omp"}, "runtime": {"local"}, "model": {"gb10/qwen3.8-27b"}, "tool": {"read", "shell"}, "prompt": {"You lead, more carefully."}}
	if r := post("/profiles/lead", same); r.status != 303 {
		t.Fatalf("a plain edit: %d", r.status)
	}
}

func TestAnEarlierVersionCanBecomeAProfileOfItsOwn(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	f := url.Values{"name": {"lead"}, "kind": {"claude"}, "runtime": {"cloud"}, "tool": {"read"}, "prompt": {"You lead."}, "csrf": {tok}, "current_password": {goodPassword}}
	e.req("POST", "/profiles", f, c, nil)
	f.Set("kind", "omp")
	f.Set("runtime", "local")
	f.Set("model", "gb10/qwen3.8-27b")
	f.Add("tool", "shell")
	f.Set("confirm_change", "1")
	e.req("POST", "/profiles/lead", f, c, nil)
	if body := e.req("GET", "/profiles/lead?version=1", nil, c, nil).body; !strings.Contains(body, "Make a new profile from this version") || !strings.Contains(body, "copy=lead&version=1") {
		t.Fatalf("no way to copy an earlier version")
	}
	form := e.req("GET", "/profiles/new?copy=lead&version=1", nil, c, nil).body
	for _, want := range []string{"Started from lead v1", `value="lead-claude"`, `<option value="claude" selected>`} {
		if !strings.Contains(form, want) {
			t.Errorf("the copy form lacks %q", want)
		}
	}
	if r := e.req("GET", "/profiles/new?copy=lead&version=9", nil, c, nil); r.status != 404 {
		t.Fatalf("a version that does not exist: %d", r.status)
	}
}
