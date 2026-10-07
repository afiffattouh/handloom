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
	if code := e.statusInto(e.admin, "POST", "/v1/starters/writer/add", api.StarterAddReq{Kind: "codex", Runtime: "cloud"}, &out); code != 400 || !strings.Contains(out.Error, "skills") {
		t.Fatalf("codex without adapt: %d %s", code, out.Error)
	}
	var adapted api.StarterAddResp
	e.apiOK(e.admin, "", "POST", "/v1/starters/writer/add", api.StarterAddReq{Kind: "codex", Runtime: "cloud", Adapt: true}, &adapted)
	if len(adapted.Adapted) == 0 || !strings.Contains(strings.Join(adapted.Adapted, " "), "skills") {
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
	if r.status != 400 || !strings.Contains(r.body, "skills") || !strings.Contains(r.body, "Add it without what Codex cannot do") {
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
	// Codex-only problems show up in the check too.
	chk = e.req("POST", "/profiles", url.Values{"name": {"x"}, "kind": {"codex"}, "tool": {"read"}, "check": {"1"}, "csrf": {tok}}, c, nil)
	if !strings.Contains(chk.body, "codex always has a shell") {
		t.Fatalf("codex rule not shown: %s", chk.body)
	}
}
