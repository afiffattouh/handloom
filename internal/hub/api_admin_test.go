package hub

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/store"
)

func TestPricesThroughTheAPI(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.apiOK(e.admin, "", "POST", "/v1/prices", api.Price{Model: "claude-sonnet", Input: 3, Output: 15, CacheRead: 0.3}, nil)
	var ps []api.Price
	e.apiOK(e.admin, "", "GET", "/v1/prices", nil, &ps)
	if len(ps) != 1 || ps[0].Model != "claude-sonnet" || ps[0].Output != 15 {
		t.Fatalf("prices: %+v", ps)
	}
	if code := e.status(e.admin, "POST", "/v1/prices", api.Price{Model: "x"}); code != 400 {
		t.Fatalf("a price with no numbers: %d", code)
	}
	if code := e.status(e.humanAPI(), "POST", "/v1/prices", api.Price{Model: "m", Input: 1, Output: 1}); code != 403 {
		t.Fatalf("a member sets a price: %d", code)
	}
	e.apiOK(e.admin, "", "POST", "/v1/prices/delete", api.Price{Model: "claude-sonnet"}, nil)
	e.apiOK(e.admin, "", "GET", "/v1/prices", nil, &ps)
	if len(ps) != 0 {
		t.Fatalf("after remove: %+v", ps)
	}
}

func TestPeopleThroughTheAPI(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.owner() // an owner exists
	var r api.PersonResp
	e.apiOK(e.admin, "", "POST", "/v1/people", api.PersonReq{Name: "mia", Role: "viewer"}, &r)
	if r.Role != "viewer" || !strings.Contains(r.InviteURL, "/invite/") {
		t.Fatalf("add: %+v", r)
	}
	if code := e.status(e.admin, "POST", "/v1/people", api.PersonReq{Name: "mia"}); code != 409 {
		t.Fatalf("the same name twice: %d", code)
	}
	if code := e.status(e.admin, "POST", "/v1/people", api.PersonReq{Name: "bo", Role: "owner"}); code != 400 {
		t.Fatalf("making an owner through the API: %d", code)
	}
	var ps []api.Person
	e.apiOK(e.admin, "", "GET", "/v1/people", nil, &ps)
	seen := map[string]string{}
	for _, p := range ps {
		seen[p.Name] = p.Role
	}
	if seen["mia"] != "viewer" || seen["afif"] != "owner" {
		t.Fatalf("people: %+v", ps)
	}
	e.apiOK(e.admin, "", "POST", "/v1/people/mia/role", api.PersonReq{Role: "member"}, nil)
	e.apiOK(e.admin, "", "POST", "/v1/people/mia/invite", nil, &r)
	if r.Role != "member" || r.InviteURL == "" {
		t.Fatalf("invite: %+v", r)
	}
	// The invite works: she sets a password and signs in.
	inv := e.req("POST", r.InviteURL[strings.Index(r.InviteURL, "/invite/"):], url.Values{"password": {goodPassword}, "password2": {goodPassword}}, nil, nil)
	if inv.status != 303 {
		t.Fatalf("accepting the invite: %d", inv.status)
	}
	if code := e.status(e.admin, "POST", "/v1/people/afif/role", api.PersonReq{Role: "viewer"}); code != 404 {
		t.Fatalf("changing the owner's role: %d", code)
	}
	if code := e.status(e.humanAPI(), "GET", "/v1/people", nil); code != 403 {
		t.Fatalf("a member lists people: %d", code)
	}
}

func TestNotificationsThroughTheAPI(t *testing.T) {
	e := newWebEnv(t, Options{})
	var n api.Notifications
	e.apiOK(e.admin, "", "GET", "/v1/notifications", nil, &n)
	if n.NtfyURL != "" {
		t.Fatalf("a new hub has none: %+v", n)
	}
	e.apiOK(e.admin, "", "POST", "/v1/notifications", api.Notifications{NtfyURL: "https://ntfy.example.org", NtfyTopic: "a-long-random-topic"}, nil)
	e.apiOK(e.admin, "", "GET", "/v1/notifications", nil, &n)
	if n.NtfyURL != "https://ntfy.example.org" || n.NtfyTopic != "a-long-random-topic" {
		t.Fatalf("saved: %+v", n)
	}
	if code := e.status(e.admin, "POST", "/v1/notifications", api.Notifications{NtfyURL: "not a url", NtfyTopic: "t"}); code != 400 {
		t.Fatalf("a bad server: %d", code)
	}
	if code := e.status(e.humanAPI(), "POST", "/v1/notifications", api.Notifications{}); code != 403 {
		t.Fatalf("a member changes notifications: %d", code)
	}
}

func TestAPersonCanReplaceTheirOwnToken(t *testing.T) {
	e := newWebEnv(t, Options{})
	old := e.humanAPI()
	var r api.TokenResp
	e.apiOK(old, "", "POST", "/v1/me/token", nil, &r)
	if r.Token == "" || r.Token == old || !strings.HasPrefix(r.Token, store.PrefixHuman) {
		t.Fatalf("token: %+v", r)
	}
	if code := e.status(old, "GET", "/v1/jobs", nil); code != 401 {
		t.Fatalf("the old token still works: %d", code)
	}
	if code := e.status(r.Token, "GET", "/v1/jobs", nil); code != 200 {
		t.Fatalf("the new token: %d", code)
	}
	if code := e.status(e.admin, "POST", "/v1/me/token", nil); code != 403 {
		t.Fatalf("the admin token has no personal token: %d", code)
	}
}

func TestAnAgentsScreenThroughTheAPI(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	e.apiOK(ag.device, "", "POST", "/v1/agents", api.RegisterReq{Name: "tty", Kind: "claude", WakeTarget: "tmux:/tmp/s:%1"}, nil)
	var s api.Screen
	tok := e.humanAPI()
	e.apiOK(tok, "", "GET", "/v1/agents/tty/screen", nil, &s)
	if !s.Pending {
		t.Fatalf("nothing has been read yet: %+v", s)
	}
	e.apiOK(ag.device, "", "POST", "/v1/device/tails/tty", api.TailReq{Text: "line\nSCREEN-API-1\n"}, nil)
	s = api.Screen{}
	e.apiOK(tok, "", "GET", "/v1/agents/tty/screen", nil, &s)
	if s.Pending || !strings.Contains(s.Text, "SCREEN-API-1") {
		t.Fatalf("screen: %+v", s)
	}
	e.clock.advance(2 * time.Minute)
	e.apiOK(tok, "", "GET", "/v1/agents/tty/screen", nil, &s)
	if !s.Pending {
		t.Fatalf("an old screen is forgotten: %+v", s)
	}
	if code := e.status(tok, "GET", "/v1/agents/nobody/screen", nil); code != 404 {
		t.Fatalf("unknown agent: %d", code)
	}
	e.apiOK(ag.device, "", "POST", "/v1/agents", api.RegisterReq{Name: "headless-one", Kind: "claude"}, nil)
	if code := e.status(tok, "GET", "/v1/agents/headless-one/screen", nil); code != 409 {
		t.Fatalf("an agent with no terminal: %d", code)
	}
	if code := e.status(ag.device, "GET", "/v1/agents/tty/screen", nil); code != 403 {
		t.Fatalf("a device reads a screen: %d", code)
	}
}

func TestTheOwnerCanBeCreatedFromTheCommandLine(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	pw, err := CreateOwner(db, "Afif", time.Unix(1, 0))
	if err != nil || len(pw) < 16 {
		t.Fatalf("create: %q %v", pw, err)
	}
	if _, err := CreateOwner(db, "Other", time.Unix(2, 0)); err == nil || !strings.Contains(err.Error(), "already has an owner") {
		t.Fatalf("a second owner: %v", err)
	}
	if owner, _ := store.OwnerExists(db); !owner {
		t.Fatal("no owner")
	}
	if _, err := CreateOwner(db, "bad name!", time.Unix(3, 0)); err == nil {
		t.Fatal("a bad name")
	}
}

func TestProfileAdaptThroughTheAPI(t *testing.T) {
	e := newWebEnv(t, Options{})
	spec := profile.Spec{Kind: "omp", Runtime: "cloud", Prompt: "p", Tools: profile.Tools{Allow: []string{"read"}, DenyCommands: []string{"rm"}}}
	if code := e.status(e.admin, "POST", "/v1/profiles", api.ProfileReq{Name: "strict", Spec: spec}); code != 400 {
		t.Fatalf("without adapt: %d", code)
	}
	var p api.ProfileFull
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "strict", Spec: spec, Adapt: true}, &p)
	if len(p.Adapted) != 2 || p.Spec.Tools.DenyCommands != nil || !strings.Contains(p.Spec.Prompt, "do not run these shell commands: rm") {
		t.Fatalf("adapted: %+v", p)
	}
}
