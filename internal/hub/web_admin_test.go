package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
)

var joinCmdRE = regexp.MustCompile(`handloom link join (\S+) (hvj_[A-Za-z0-9_-]+)`)

func (e *webEnv) post(path string, c *http.Cookie, tok string, form url.Values) webResp {
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", tok)
	return e.req("POST", path, form, c, nil)
}

func (e *webEnv) join(token string) (int, string) {
	b, _ := json.Marshal(api.JoinReq{JoinToken: token})
	resp, err := http.Post(e.srv.URL+"/v1/devices/join", "application/json", strings.NewReader(string(b)))
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestAddDeviceFromTheWeb(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()

	// The password check comes first; nothing is created without it.
	bad := e.post("/devices", c, tok, url.Values{"name": {"laptop"}, "current_password": {"wrong wrong wrong"}})
	if bad.status != 403 || !strings.Contains(bad.body, "password is not right") {
		t.Fatalf("wrong password: %d", bad.status)
	}
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM device`).Scan(&n)
	if n != 0 {
		t.Fatal("a device was created without the password")
	}
	if !e.audited("web.stepup.fail", "human:afif") {
		t.Fatal("the failed check is not audited")
	}
	if r := e.post("/devices", c, tok, url.Values{"name": {"bad name!"}, "current_password": {goodPassword}}); r.status != 400 {
		t.Fatalf("bad name: %d", r.status)
	}

	ok := e.post("/devices", c, tok, url.Values{"name": {"laptop"}, "current_password": {goodPassword}})
	m := joinCmdRE.FindStringSubmatch(ok.body)
	if ok.status != 200 || m == nil || m[1] != testOrigin {
		t.Fatalf("add device: %d %s", ok.status, ok.body)
	}
	if again := e.req("GET", "/devices", nil, c, nil); strings.Contains(again.body, m[2]) || !strings.Contains(again.body, "waiting for it to join") {
		t.Fatal("the join token is shown again, or the pending device is not listed")
	}
	if code, body := e.join(m[2]); code != 200 || !strings.Contains(body, "credential") {
		t.Fatalf("join with the shown token: %d %s", code, body)
	}
	if code, _ := e.join(m[2]); code != 401 {
		t.Fatalf("a join token worked twice: %d", code)
	}
	if r := e.req("GET", "/devices", nil, c, nil); !strings.Contains(r.body, "laptop") || !strings.Contains(r.body, "joined") {
		t.Fatalf("devices page after join: %s", r.body)
	}
	// Revoke.
	if r := e.post("/devices/laptop/revoke", c, tok, nil); r.status != 303 {
		t.Fatalf("revoke: %d", r.status)
	}
	if r := e.req("GET", "/devices", nil, c, nil); !strings.Contains(r.body, "revoked") {
		t.Fatal("revoked device is not shown as revoked")
	}
}

func TestJoinTokenExpiresAndJoinIsRateLimited(t *testing.T) {
	e := newWebEnv(t, Options{JoinTTL: 10 * time.Minute})
	c, tok := e.owner()
	m := joinCmdRE.FindStringSubmatch(e.post("/devices", c, tok, url.Values{"name": {"d1"}, "current_password": {goodPassword}}).body)
	e.clock.advance(11 * time.Minute)
	if code, body := e.join(m[2]); code != 401 || !strings.Contains(body, "expired") {
		t.Fatalf("expired token: %d %s", code, body)
	}
	if r := e.req("GET", "/devices", nil, c, nil); !strings.Contains(r.body, "expired") {
		t.Fatal("the devices page does not say the token expired")
	}
	limited := false
	for i := 0; i < 15; i++ {
		if code, _ := e.join("hvj_nonsense"); code == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("join attempts are not rate limited")
	}
	// An old device row from before expiry existed (NULL) keeps working.
	e.clock.advance(5 * time.Minute)
	e.hub.db.Exec(`INSERT INTO device(name, join_hash, created_at) VALUES ('legacy', ?, 1)`, sha("x"))
}

func TestMembersAndInvites(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	add := func(name, role, pw string) webResp {
		return e.post("/settings/members", c, tok, url.Values{"name": {name}, "role": {role}, "current_password": {pw}})
	}
	if r := add("mia", "member", "nope nope nope"); r.status != 403 {
		t.Fatalf("add without the password: %d", r.status)
	}
	if r := add("mia", "owner", goodPassword); r.status != 400 {
		t.Fatalf("add as owner: %d", r.status)
	}
	r := add("mia", "member", goodPassword)
	inv := regexp.MustCompile(`https://handloom\.test/invite/([A-Za-z0-9_-]+)`).FindStringSubmatch(r.body)
	if r.status != 200 || inv == nil {
		t.Fatalf("add: %d %s", r.status, r.body)
	}
	if r := add("mia", "viewer", goodPassword); r.status != 409 {
		t.Fatalf("duplicate name: %d", r.status)
	}

	// Nobody can sign in as mia before she sets a password.
	if r := e.login("mia", ""); r.status == 303 {
		t.Fatal("signed in without a password")
	}
	if r := e.req("GET", "/invite/"+inv[1], nil, nil, nil); r.status != 200 || !strings.Contains(r.body, "Welcome, mia") {
		t.Fatalf("invite page: %d", r.status)
	}
	if r := e.req("GET", "/invite/nonsense", nil, nil, nil); r.status != 404 {
		t.Fatalf("unknown invite: %d", r.status)
	}
	pw := "mia has a long password"
	if r := e.req("POST", "/invite/"+inv[1], url.Values{"password": {"short"}, "password2": {"short"}}, nil, nil); r.status != 400 {
		t.Fatalf("weak password: %d", r.status)
	}
	if r := e.req("POST", "/invite/"+inv[1], url.Values{"password": {pw}, "password2": {pw}}, nil, map[string]string{"Origin": ""}); r.status != 403 {
		t.Fatalf("invite post without origin: %d", r.status)
	}
	done := e.req("POST", "/invite/"+inv[1], url.Values{"password": {pw}, "password2": {pw}}, nil, nil)
	if done.status != 303 || done.cookie == nil {
		t.Fatalf("accept invite: %d %s", done.status, done.body)
	}
	if r := e.req("GET", "/inbox", nil, done.cookie, nil); !strings.Contains(r.body, "<strong>mia</strong><div class=\"desc\">member</div>") {
		t.Fatalf("mia's inbox: %s", r.body)
	}
	if r := e.req("GET", "/invite/"+inv[1], nil, nil, nil); r.status != 404 {
		t.Fatalf("an invite worked twice: %d", r.status)
	}
	// A member sees the devices page without the add form, and cannot reach settings.
	if r := e.req("GET", "/devices", nil, done.cookie, nil); r.status != 200 || strings.Contains(r.body, "Add a device") {
		t.Fatalf("member devices page: %d", r.status)
	}
	if r := e.req("GET", "/settings", nil, done.cookie, nil); r.status != 403 {
		t.Fatalf("member settings: %d", r.status)
	}
	mtok := csrfOf(t, e.req("GET", "/inbox", nil, done.cookie, nil).body)
	if r := e.post("/devices", done.cookie, mtok, url.Values{"name": {"x"}, "current_password": {pw}}); r.status != 403 {
		t.Fatalf("member adds a device: %d", r.status)
	}
	if r := e.post("/settings/members", done.cookie, mtok, url.Values{"name": {"x"}, "role": {"member"}, "current_password": {pw}}); r.status != 403 {
		t.Fatalf("member adds a person: %d", r.status)
	}

	// Changing a role signs the person out at once; the owner's own role is fixed.
	if r := e.post("/settings/members/mia/role", c, tok, url.Values{"role": {"viewer"}, "current_password": {goodPassword}}); r.status != 303 {
		t.Fatalf("role change: %d", r.status)
	}
	if r := e.req("GET", "/inbox", nil, done.cookie, nil); r.status != 303 {
		t.Fatalf("mia stayed signed in after a role change: %d", r.status)
	}
	e.clock.advance(2 * time.Minute) // the password check allows five tries a minute
	if r := e.post("/settings/members/afif/role", c, tok, url.Values{"role": {"viewer"}, "current_password": {goodPassword}}); r.status != 400 {
		t.Fatalf("owner changes own role: %d", r.status)
	}
	// A fresh invite doubles as a password reset.
	r = e.post("/settings/members/mia/invite", c, tok, url.Values{"current_password": {goodPassword}})
	inv2 := regexp.MustCompile(`/invite/([A-Za-z0-9_-]+)`).FindStringSubmatch(r.body)
	if r.status != 200 || inv2 == nil || inv2[1] == inv[1] {
		t.Fatalf("reissue: %d", r.status)
	}
}

func TestInviteExpires(t *testing.T) {
	e := newWebEnv(t, Options{InviteTTL: time.Hour})
	c, tok := e.owner()
	r := e.post("/settings/members", c, tok, url.Values{"name": {"mia"}, "role": {"viewer"}, "current_password": {goodPassword}})
	inv := regexp.MustCompile(`/invite/([A-Za-z0-9_-]+)`).FindStringSubmatch(r.body)
	e.clock.advance(2 * time.Hour)
	if r := e.req("GET", "/invite/"+inv[1], nil, nil, nil); r.status != 404 {
		t.Fatalf("expired invite: %d", r.status)
	}
}

func TestNotificationSettingsReachNtfy(t *testing.T) {
	var mu sync.Mutex
	var got []string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.URL.Path+"|"+r.Header.Get("Authorization")+"|"+string(b))
		mu.Unlock()
	}))
	defer ntfy.Close()
	wait := func(n int) []string {
		for i := 0; i < 60; i++ {
			mu.Lock()
			l := append([]string(nil), got...)
			mu.Unlock()
			if len(l) >= n {
				return l
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("expected %d pushes, got %v", n, got)
		return nil
	}

	e := newWebEnv(t, Options{NtfyToken: "tk_secret"})
	ag := e.agents()
	c, tok := e.owner()

	if r := e.post("/settings/notifications/test", c, tok, nil); r.status != 400 || !strings.Contains(r.body, "No notification is configured") {
		t.Fatalf("test before configuring: %d", r.status)
	}
	if r := e.post("/settings/notifications", c, tok, url.Values{"ntfy_url": {"ftp://x"}, "ntfy_topic": {"t"}}); r.status != 400 {
		t.Fatalf("bad url: %d", r.status)
	}
	if r := e.post("/settings/notifications", c, tok, url.Values{"ntfy_url": {ntfy.URL}, "ntfy_topic": {"my-topic"}}); r.status != 303 {
		t.Fatalf("save: %d", r.status)
	}
	if r := e.req("GET", "/settings", nil, c, nil); !strings.Contains(r.body, ntfy.URL) || strings.Contains(r.body, "tk_secret") {
		t.Fatal("settings page lacks the URL, or shows the token")
	}
	e.post("/settings/notifications/test", c, tok, nil)
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "Top secret question"}, nil)
	pushes := wait(2)
	for _, p := range pushes {
		if !strings.HasPrefix(p, "/my-topic|Bearer tk_secret|") || strings.Contains(p, "Top secret") {
			t.Fatalf("push %q", p)
		}
	}
	// Clearing the URL stops the pushes.
	e.post("/settings/notifications", c, tok, url.Values{"ntfy_url": {""}, "ntfy_topic": {""}})
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "another"}, nil)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("%d pushes after clearing the settings", n)
	}
}

func TestNewAPIToken(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, tok := e.owner()
	if r := e.post("/settings/token", c, tok, url.Values{"current_password": {"nope nope nope"}}); r.status != 403 {
		t.Fatalf("token without the password: %d", r.status)
	}
	r := e.post("/settings/token", c, tok, url.Values{"current_password": {goodPassword}})
	m := regexp.MustCompile(`hvh_[A-Za-z0-9_-]+`).FindString(r.body)
	if r.status != 200 || m == "" {
		t.Fatalf("token: %d %s", r.status, r.body)
	}
	var d api.Digest
	e.apiOK(m, "", "GET", "/v1/digest", nil, &d) // the token works as the owner
	r2 := e.post("/settings/token", c, tok, url.Values{"current_password": {goodPassword}})
	m2 := regexp.MustCompile(`hvh_[A-Za-z0-9_-]+`).FindString(r2.body)
	if m2 == m {
		t.Fatal("two tokens are equal")
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/digest", nil)
	req.Header.Set("Authorization", "Bearer "+m)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("the replaced token still works: %d", resp.StatusCode)
	}
	if !e.audited("human.token", "human:afif") {
		t.Fatal("token creation is not audited")
	}
}

func TestEveryWebRouteNeedsASession(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.owner()
	for _, rt := range []struct{ method, path string }{
		{"GET", "/inbox"}, {"GET", "/inbox/fragment"}, {"GET", "/devices"}, {"GET", "/settings"},
		{"POST", "/inbox/escalations/1/answer"}, {"POST", "/inbox/tasks/1/accept"}, {"POST", "/inbox/tasks/1/reject"},
		{"POST", "/devices"}, {"POST", "/devices/x/revoke"}, {"POST", "/settings/members"},
		{"POST", "/settings/members/x/role"}, {"POST", "/settings/members/x/invite"},
		{"POST", "/settings/notifications"}, {"POST", "/settings/notifications/test"}, {"POST", "/settings/token"}, {"POST", "/logout"},
	} {
		var form url.Values
		if rt.method == "POST" {
			form = url.Values{"csrf": {"x"}}
		}
		r := e.req(rt.method, rt.path, form, nil, nil)
		wantGet := r.status == 303 && r.header.Get("Location") == "/login"
		wantPost := r.status == 401
		if (rt.method == "GET" && !wantGet) || (rt.method == "POST" && !wantPost) {
			t.Errorf("%s %s without a session: %d", rt.method, rt.path, r.status)
		}
	}
	_ = fmt.Sprint
}
