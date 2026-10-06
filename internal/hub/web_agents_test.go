package hub

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/profile"
)

// member makes a signed-in member (not the owner) and returns cookie and token.
func (e *webEnv) member(name, role string) (*http.Cookie, string) {
	e.t.Helper()
	hash, _ := HashPassword(goodPassword)
	e.hub.db.Exec(`INSERT INTO human(name, token_hash, created_at, password_hash, role) VALUES (?, ?, 1, ?, ?)`, name, name, hash, role)
	c := e.login(name, goodPassword).cookie
	if c == nil {
		e.t.Fatalf("login of %s failed", name)
	}
	return c, csrfOf(e.t, e.req("GET", "/inbox", nil, c, nil).body)
}

func profileForm(extra url.Values) url.Values {
	f := url.Values{"name": {"researcher"}, "description": {"reads"}, "runtime": {"cloud"}, "tool": {"read", "shell"},
		"deny": {"rm\ngit push"}, "prompt": {"Be careful."}, "skill_name": {"cite", ""}, "skill_text": {"---\nname: cite\n---\nCite.", ""},
		"current_password": {goodPassword}}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func TestProfilePagesForTheOwner(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	if r := e.req("GET", "/profiles", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "No profiles yet") || !strings.Contains(r.body, "New profile") {
		t.Fatalf("empty list: %d", r.status)
	}
	if r := e.req("GET", "/profiles/new", nil, c, nil); r.status != 200 || !strings.Contains(r.body, `name="deny"`) {
		t.Fatalf("new form: %d", r.status)
	}

	// The password is asked again; a bad profile says what is wrong and keeps what was typed.
	f := profileForm(nil)
	f.Set("current_password", "wrong wrong wrong")
	f.Set("csrf", tok)
	if r := e.req("POST", "/profiles", f, c, nil); r.status != 403 {
		t.Fatalf("wrong password: %d", r.status)
	}
	e.clock.advance(2 * 60 * 1e9)
	f = profileForm(url.Values{"deny": {"rm *"}, "prompt": {"Keep this text"}})
	f.Set("csrf", tok)
	r := e.req("POST", "/profiles", f, c, nil)
	if r.status != 400 || !strings.Contains(r.body, "deny_commands entry") || !strings.Contains(r.body, "Keep this text") {
		t.Fatalf("a bad profile: %d %s", r.status, r.body)
	}

	f = profileForm(nil)
	f.Set("csrf", tok)
	if r := e.req("POST", "/profiles", f, c, nil); r.status != 303 || r.header.Get("Location") != "/profiles/researcher?done=saved" {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	page := e.req("GET", "/profiles/researcher?done=saved", nil, c, nil)
	for _, want := range []string{"researcher", "version 1", "Saved.", "allowed: shell", "refused by Claude Code: web", "the shell command rm",
		"not enforced: file paths", `name="skill_text"`, "Cite."} {
		if !strings.Contains(page.body, want) {
			t.Errorf("profile page lacks %q", want)
		}
	}
	if r := e.req("GET", "/profiles", nil, c, nil); !strings.Contains(r.body, "researcher") || !strings.Contains(r.body, "skills: cite") {
		t.Fatalf("list: %s", r.body)
	}

	// Saving the same thing again is not a new version; a change is.
	e.clock.advance(2 * 60 * 1e9)
	same := profileForm(nil)
	same.Set("csrf", tok)
	e.req("POST", "/profiles/researcher", same, c, nil)
	var vs []api.ProfileInfo
	e.apiOK(e.admin, "", "GET", "/v1/profiles/researcher/versions", nil, &vs)
	if len(vs) != 1 {
		t.Fatalf("an unchanged save made versions: %+v", vs)
	}
	changed := profileForm(url.Values{"prompt": {"Be very careful."}, "tool": {"read", "shell", "web"}})
	changed.Set("csrf", tok)
	if r := e.req("POST", "/profiles/researcher", changed, c, nil); r.status != 303 {
		t.Fatalf("edit: %d %s", r.status, r.body)
	}
	e.apiOK(e.admin, "", "GET", "/v1/profiles/researcher/versions", nil, &vs)
	if len(vs) != 2 {
		t.Fatalf("versions after a change: %+v", vs)
	}
	if r := e.req("GET", "/profiles/researcher?version=1", nil, c, nil); !strings.Contains(r.body, "version 1") || !strings.Contains(r.body, "Be careful.") {
		t.Fatalf("an old version: %s", r.body)
	}
}

func TestEditingKeepsTheOtherFilesOfASkill(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	spec := researcher()
	spec.Skills = append(spec.Skills, profile.File{Path: "cite/examples/one.md", Content: "an example"})
	var p api.ProfileFull
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "researcher", Spec: spec}, &p)

	f := profileForm(url.Values{"skill_name": {"cite", ""}, "skill_text": {"---\nname: cite\n---\nCite better.", ""}})
	f.Set("csrf", tok)
	if r := e.req("POST", "/profiles/researcher", f, c, nil); r.status != 303 {
		t.Fatalf("edit: %d %s", r.status, r.body)
	}
	var latest api.ProfileFull
	e.apiOK(e.admin, "", "GET", "/v1/profiles/researcher", nil, &latest)
	got := map[string]string{}
	for _, file := range latest.Spec.Skills {
		got[file.Path] = file.Content
	}
	if got["cite/examples/one.md"] != "an example" || !strings.Contains(got["cite/SKILL.md"], "Cite better.") {
		t.Fatalf("skill files after an edit: %v", got)
	}
	// Clearing a skill's text removes the skill, and adding one adds it.
	e.clock.advance(2 * 60 * 1e9)
	f = profileForm(url.Values{"skill_name": {"cite", "tidy"}, "skill_text": {"", "---\nname: tidy\n---\nTidy up."}})
	f.Set("csrf", tok)
	e.req("POST", "/profiles/researcher", f, c, nil)
	e.apiOK(e.admin, "", "GET", "/v1/profiles/researcher", nil, &latest)
	names := profile.SkillNames(&latest.Spec)
	if len(names) != 1 || names[0] != "tidy" {
		t.Fatalf("skills after a remove and an add: %v", names)
	}
}

func TestProfilesAreReadOnlyForMembersAndEscaped(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.owner()
	spec := researcher()
	spec.Prompt = `<script>alert(1)</script> & "quotes"`
	spec.Skills = []profile.File{{Path: "x/SKILL.md", Content: "<img src=x onerror=alert(2)>"}}
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "researcher", Spec: spec}, nil)

	mc, mtok := e.member("mia", "member")
	r := e.req("GET", "/profiles/researcher", nil, mc, nil)
	if r.status != 200 || strings.Contains(r.body, `name="deny"`) || strings.Contains(r.body, "<script>alert") || strings.Contains(r.body, "<img src=x") {
		t.Fatalf("a member's view of a profile: %d", r.status)
	}
	if !strings.Contains(r.body, "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(r.body, "Only the owner can change profiles") {
		t.Fatalf("escaped text or the notice is missing")
	}
	if r := e.req("GET", "/profiles/new", nil, mc, nil); r.status != 403 {
		t.Fatalf("a member opens the new form: %d", r.status)
	}
	f := profileForm(nil)
	f.Set("csrf", mtok)
	if r := e.req("POST", "/profiles", f, mc, nil); r.status != 403 {
		t.Fatalf("a member creates a profile: %d", r.status)
	}
	if r := e.req("POST", "/profiles/researcher", f, mc, nil); r.status != 403 {
		t.Fatalf("a member edits a profile: %d", r.status)
	}
	// The owner's editor shows the same text escaped inside the textarea.
	oc, _ := e.login("afif", goodPassword), ""
	if body := e.req("GET", "/profiles/researcher", nil, oc.cookie, nil).body; strings.Contains(body, "<script>alert") {
		t.Fatal("the owner's editor holds live markup")
	}
	if r := e.req("GET", "/profiles/nobody", nil, mc, nil); r.status != 404 {
		t.Fatalf("an unknown profile: %d", r.status)
	}
}

func TestStartAnAgentFromTheWeb(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents() // a joined device d1
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "researcher", Spec: researcher()}, nil)
	mc, mtok := e.member("mia", "member")

	page := e.req("GET", "/agents", nil, mc, nil)
	for _, want := range []string{"Start an agent", `<option value="researcher"`, `<option value="d1"`, "lead", "worker"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("agents page lacks %q", want)
		}
	}
	post := func(form url.Values) webResp {
		form.Set("csrf", mtok)
		return e.req("POST", "/agents/spawn", form, mc, nil)
	}
	if r := post(url.Values{"name": {"bad name!"}, "device": {"d1"}}); r.status != 400 {
		t.Fatalf("bad name: %d", r.status)
	}
	if r := post(url.Values{"name": {"r1"}, "device": {"d1"}, "job": {"abc"}}); r.status != 400 || !strings.Contains(r.body, "must be a number") {
		t.Fatalf("bad job: %d", r.status)
	}
	if r := post(url.Values{"name": {"r1"}, "device": {"d1"}, "profile": {"nobody"}}); r.status != 404 || !strings.Contains(r.body, `value="r1"`) {
		t.Fatalf("an unknown profile should keep the form filled: %d", r.status)
	}
	if r := post(url.Values{"name": {"r1"}, "device": {"d1"}, "profile": {"researcher"}, "model": {"sonnet"}}); r.status != 303 || r.header.Get("Location") != "/agents?done=spawn" {
		t.Fatalf("spawn: %d %s", r.status, r.body)
	}
	var list []api.Spawn
	e.apiOK(e.admin, "", "GET", "/v1/spawns", nil, &list)
	if len(list) != 1 || list[0].Name != "r1" || list[0].Profile != "researcher@1" || list[0].CreatedBy != "human:mia" {
		t.Fatalf("spawns: %+v", list)
	}
	if r := e.req("GET", "/agents?done=spawn", nil, mc, nil); !strings.Contains(r.body, "Requested.") || !strings.Contains(r.body, "researcher@1") || !strings.Contains(r.body, "pending") {
		t.Fatalf("the page after starting: %s", r.body)
	}

	// A viewer sees the page without the form and cannot start anything.
	vc, vtok := e.member("vera", "viewer")
	if r := e.req("GET", "/agents", nil, vc, nil); r.status != 200 || strings.Contains(r.body, "Start an agent") {
		t.Fatalf("viewer's agents page: %d", r.status)
	}
	f := url.Values{"name": {"x"}, "device": {"d1"}, "csrf": {vtok}}
	if r := e.req("POST", "/agents/spawn", f, vc, nil); r.status != 403 {
		t.Fatalf("a viewer starts an agent: %d", r.status)
	}
	_ = fmt.Sprint
}
