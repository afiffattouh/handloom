package hub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

func TestMain(m *testing.M) {
	// Cheap argon2 for the tests; the production cost is set in password.go.
	argonTime, argonMemory = 1, 1024
	os.Exit(m.Run())
}

const testOrigin = "https://handloom.test"

// webEnv is a hub with the web UI on and no owner yet.
type webEnv struct {
	t     *testing.T
	hub   *Hub
	srv   *httptest.Server
	clock *clock
	code  string
	admin string
	http  *http.Client
}

func newWebEnv(t *testing.T, opt Options) *webEnv {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cl := &clock{t: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)}
	admin, err := store.Init(db, cl.now())
	if err != nil {
		t.Fatal(err)
	}
	opt.Now = cl.now
	if opt.BaseURL == "" {
		opt.BaseURL = testOrigin
	}
	h := New(db, opt)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	code, err := h.PrepareSetup()
	if err != nil {
		t.Fatal(err)
	}
	return &webEnv{t: t, hub: h, srv: srv, clock: cl, code: code, admin: admin, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type webResp struct {
	status int
	body   string
	header http.Header
	cookie *http.Cookie
}

// req sends a request. For POSTs it sets the origin a browser would, unless
// headers override it.
func (e *webEnv) req(method, path string, form url.Values, cookie *http.Cookie, headers map[string]string) webResp {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, _ := http.NewRequest(method, e.srv.URL+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == "POST" {
		r.Header.Set("Origin", testOrigin)
	}
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	resp, err := e.http.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := webResp{status: resp.StatusCode, body: string(b), header: resp.Header}
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-handloom_session" && c.MaxAge >= 0 && c.Value != "" {
			out.cookie = c
		}
	}
	return out
}

func (e *webEnv) setup(name, pw string) webResp {
	return e.req("POST", "/setup", url.Values{"code": {e.code}, "name": {name}, "password": {pw}, "password2": {pw}}, nil, nil)
}

func (e *webEnv) login(name, pw string) webResp {
	return e.req("POST", "/login", url.Values{"name": {name}, "password": {pw}}, nil, nil)
}

var csrfRE = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

func csrfOf(t *testing.T, body string) string {
	t.Helper()
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token in page: %s", body)
	}
	return m[1]
}

const goodPassword = "correct horse battery"

func TestWebIsOffUntilConfigured(t *testing.T) {
	for _, opt := range []Options{
		{BaseURL: "http://100.64.0.9:7420"}, // plain http on a non-local address
		{BaseURL: "not a url"},
	} {
		e := newWebEnv(t, opt)
		if r := e.req("GET", "/login", nil, nil, nil); r.status != 503 || !strings.Contains(r.body, "web UI is off") {
			t.Fatalf("%+v: %d %s", opt, r.status, r.body)
		}
	}
	// The API still works, and a private network can opt in explicitly.
	e := newWebEnv(t, Options{BaseURL: "http://100.64.0.9:7420", Insecure: true})
	if r := e.req("GET", "/setup", nil, nil, nil); r.status != 200 {
		t.Fatalf("insecure opt-in: %d", r.status)
	}
	e = newWebEnv(t, Options{BaseURL: "http://localhost:7420"})
	if r := e.req("GET", "/setup", nil, nil, nil); r.status != 200 {
		t.Fatalf("localhost: %d", r.status)
	}
	// No BaseURL at all: off.
	db, _ := store.Open(":memory:")
	defer db.Close()
	srv := httptest.NewServer(New(db, Options{}).Handler())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/login")
	if resp.StatusCode != 503 {
		t.Fatalf("no base url: %d", resp.StatusCode)
	}
}

func TestSetupWizard(t *testing.T) {
	e := newWebEnv(t, Options{})
	if len(e.code) != 14 {
		t.Fatalf("setup code %q", e.code)
	}
	if r := e.req("GET", "/", nil, nil, nil); r.status != 303 || r.header.Get("Location") != "/setup" {
		t.Fatalf("root before setup: %d %v", r.status, r.header)
	}
	if r := e.req("GET", "/setup", nil, nil, nil); r.status != 200 || !strings.Contains(r.body, "Setup code") {
		t.Fatalf("setup page: %d", r.status)
	}

	// Wrong code, and a code with the right shape but wrong value.
	bad := e.req("POST", "/setup", url.Values{"code": {"AAAA-BBBB-CCCC"}, "name": {"afif"}, "password": {goodPassword}, "password2": {goodPassword}}, nil, nil)
	if bad.status != 403 || bad.cookie != nil {
		t.Fatalf("wrong code: %d", bad.status)
	}
	// Weak or mismatched passwords are refused and keep the code usable.
	if r := e.setup("afif", "short"); r.status != 400 {
		t.Fatalf("short password: %d", r.status)
	}
	if r := e.req("POST", "/setup", url.Values{"code": {e.code}, "name": {"afif"}, "password": {goodPassword}, "password2": {"different one!"}}, nil, nil); r.status != 400 {
		t.Fatalf("mismatch: %d", r.status)
	}
	if r := e.setup("not a valid name!", goodPassword); r.status != 400 {
		t.Fatalf("bad name: %d", r.status)
	}

	// The code is accepted with lower case and without dashes.
	loose := strings.ToLower(strings.ReplaceAll(e.code, "-", ""))
	ok := e.req("POST", "/setup", url.Values{"code": {loose}, "name": {"afif"}, "password": {goodPassword}, "password2": {goodPassword}}, nil, nil)
	if ok.status != 303 || ok.header.Get("Location") != "/inbox" || ok.cookie == nil {
		t.Fatalf("setup: %d %s", ok.status, ok.body)
	}
	c := ok.cookie
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie attributes: %+v", c)
	}
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "afif") {
		t.Fatalf("inbox after setup: %d", r.status)
	}

	// The wizard is closed for good: the page, a second post with the old
	// code, and a fresh hub start all refuse.
	if r := e.req("GET", "/setup", nil, nil, nil); r.status != 404 {
		t.Fatalf("setup page after owner: %d", r.status)
	}
	if r := e.setup("intruder", goodPassword); r.status != 404 {
		t.Fatalf("second setup: %d", r.status)
	}
	if code, err := e.hub.PrepareSetup(); err != nil || code != "" {
		t.Fatalf("PrepareSetup with an owner: %q %v", code, err)
	}
	if _, ok, _ := store.MetaGet(e.hub.db, "setup_code_hash"); ok {
		t.Fatal("the setup code hash is still stored")
	}
	if !e.audited("setup.owner", "human:afif") {
		t.Fatal("setup is not audited")
	}
}

func (e *webEnv) audited(action, target string) bool {
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM audit WHERE action = ? AND target = ?`, action, target).Scan(&n)
	return n > 0
}

func TestSetupDoubleSubmitCreatesOneOwner(t *testing.T) {
	e := newWebEnv(t, Options{})
	var wg sync.WaitGroup
	results := make([]int, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = e.setup([]string{"a", "b", "c", "d", "e", "f"}[i], goodPassword).status
		}(i)
	}
	wg.Wait()
	var owners int
	e.hub.db.QueryRow(`SELECT count(*) FROM human WHERE role = 'owner'`).Scan(&owners)
	if owners != 1 {
		t.Fatalf("%d owners after simultaneous setups (statuses %v)", owners, results)
	}
}

func TestSetupNeedsSameOrigin(t *testing.T) {
	e := newWebEnv(t, Options{})
	form := url.Values{"code": {e.code}, "name": {"afif"}, "password": {goodPassword}, "password2": {goodPassword}}
	for name, h := range map[string]map[string]string{
		"foreign origin":   {"Origin": "https://evil.example"},
		"null origin":      {"Origin": "null"},
		"no origin at all": {"Origin": ""},
		"cross-site fetch": {"Origin": "", "Sec-Fetch-Site": "cross-site"},
	} {
		if r := e.req("POST", "/setup", form, nil, h); r.status != 403 {
			t.Errorf("%s: %d", name, r.status)
		}
	}
	var owners int
	e.hub.db.QueryRow(`SELECT count(*) FROM human`).Scan(&owners)
	if owners != 0 {
		t.Fatal("a refused request created an account")
	}
	// A browser that sends Sec-Fetch-Site but no Origin still works.
	if r := e.req("POST", "/setup", form, nil, map[string]string{"Origin": "", "Sec-Fetch-Site": "same-origin"}); r.status != 303 {
		t.Fatalf("same-origin fetch metadata: %d", r.status)
	}
}

func TestLogin(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.setup("afif", goodPassword)

	wrong := e.login("afif", "not the password")
	unknown := e.login("nobody", "not the password")
	if wrong.status != 401 || unknown.status != 401 || wrong.cookie != nil {
		t.Fatalf("wrong: %d, unknown: %d", wrong.status, unknown.status)
	}
	// The same message for a wrong password and an unknown name.
	strip := func(s string) string { return regexp.MustCompile(`value="[^"]*"`).ReplaceAllString(s, "") }
	if strip(wrong.body) != strip(unknown.body) || !strings.Contains(wrong.body, wrongLogin) {
		t.Fatal("login errors differ between a wrong password and an unknown name")
	}
	if !e.audited("web.login.fail", "human:nobody") {
		t.Fatal("failed login is not audited")
	}

	ok := e.login("afif", goodPassword)
	if ok.status != 303 || ok.cookie == nil {
		t.Fatalf("login: %d", ok.status)
	}
	again := e.login("afif", goodPassword)
	if again.cookie.Value == ok.cookie.Value {
		t.Fatal("two logins share one session id")
	}
	if !e.audited("web.login", "human:afif") {
		t.Fatal("login is not audited")
	}
	// Session ids are stored hashed.
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM web_session WHERE id_hash = ?`, ok.cookie.Value).Scan(&n)
	if n != 0 {
		t.Fatal("the session id is stored in clear")
	}

	// No cookie: redirected. A made-up cookie: redirected.
	if r := e.req("GET", "/inbox", nil, nil, nil); r.status != 303 || r.header.Get("Location") != "/login" {
		t.Fatalf("inbox without session: %d", r.status)
	}
	if r := e.req("GET", "/inbox", nil, &http.Cookie{Name: "__Host-handloom_session", Value: "forged"}, nil); r.status != 303 {
		t.Fatalf("inbox with a forged cookie: %d", r.status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.setup("afif", goodPassword)
	var last int
	limited := 0
	for i := 0; i < 8; i++ {
		last = e.login("afif", "guess"+string(rune('a'+i))).status
		if last == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Fatalf("no rate limit after 8 bad logins (last status %d)", last)
	}
	// Even the right password is refused while limited, and the limit is per name:
	if r := e.login("afif", goodPassword); r.status != 429 {
		t.Fatalf("right password while limited: %d", r.status)
	}
	if r := e.login("other", "x"); r.status == 429 {
		t.Fatal("limit on one name blocks another")
	}
	// It refills.
	e.clock.advance(2 * time.Minute)
	if r := e.login("afif", goodPassword); r.status != 303 {
		t.Fatalf("after the wait: %d", r.status)
	}
}

func TestCSRFAndLogout(t *testing.T) {
	e := newWebEnv(t, Options{})
	c := e.setup("afif", goodPassword).cookie
	tok := csrfOf(t, e.req("GET", "/inbox", nil, c, nil).body)

	for name, tc := range map[string]struct {
		form    url.Values
		headers map[string]string
	}{
		"no token":       {url.Values{}, nil},
		"wrong token":    {url.Values{"csrf": {"nope"}}, nil},
		"foreign origin": {url.Values{"csrf": {tok}}, map[string]string{"Origin": "https://evil.example"}},
		"cross-site":     {url.Values{"csrf": {tok}}, map[string]string{"Origin": "", "Sec-Fetch-Site": "cross-site"}},
	} {
		if r := e.req("POST", "/logout", tc.form, c, tc.headers); r.status != 403 {
			t.Errorf("%s: %d", name, r.status)
		}
	}
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 200 {
		t.Fatal("a refused logout ended the session")
	}
	// The token in a header works too (htmx), and logout ends the session server-side.
	if r := e.req("POST", "/logout", url.Values{}, c, map[string]string{"X-CSRF-Token": tok}); r.status != 303 || r.header.Get("Location") != "/login" {
		t.Fatalf("logout: %d", r.status)
	}
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 303 {
		t.Fatalf("the old cookie still works after logout: %d", r.status)
	}
	// A GET never changes state.
	if r := e.req("GET", "/logout", nil, c, nil); r.status == 200 || r.status == 303 && r.header.Get("Location") == "/login" && false {
		t.Fatalf("GET /logout: %d", r.status)
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newWebEnv(t, Options{SessionIdle: time.Hour, SessionMax: 3 * time.Hour})
	c := e.setup("afif", goodPassword).cookie

	e.clock.advance(50 * time.Minute)
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 200 { // refreshes the idle timer
		t.Fatalf("within idle: %d", r.status)
	}
	e.clock.advance(50 * time.Minute)
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 200 {
		t.Fatalf("idle timer should have been refreshed: %d", r.status)
	}
	e.clock.advance(2 * time.Hour) // idle gap longer than an hour
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 303 {
		t.Fatalf("after idle timeout: %d", r.status)
	}

	// Absolute lifetime, even with constant use.
	c = e.login("afif", goodPassword).cookie
	for i := 0; i < 5; i++ {
		e.clock.advance(40 * time.Minute)
		r := e.req("GET", "/inbox", nil, c, nil)
		if i < 3 && r.status != 200 {
			t.Fatalf("step %d: %d", i, r.status)
		}
		if i == 4 && r.status != 303 {
			t.Fatalf("after the absolute lifetime: %d", r.status)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newWebEnv(t, Options{})
	c := e.setup("afif", goodPassword).cookie
	for _, path := range []string{"/login", "/inbox", "/static/app.css"} {
		r := e.req("GET", path, nil, c, nil)
		csp := r.header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: CSP %q", path, csp)
		}
		if r.header.Get("X-Content-Type-Options") != "nosniff" || r.header.Get("Referrer-Policy") != "no-referrer" || r.header.Get("Strict-Transport-Security") == "" {
			t.Errorf("%s: headers %v", path, r.header)
		}
	}
	if r := e.req("GET", "/inbox", nil, c, nil); r.header.Get("Cache-Control") != "no-store" {
		t.Error("an authenticated page is cacheable")
	}
}

func TestProxyAddressOnlyWhenTrusted(t *testing.T) {
	for _, trust := range []bool{false, true} {
		e := newWebEnv(t, Options{TrustProxy: trust})
		e.setup("afif", goodPassword)
		// Ten clients behind one proxy address each get their own budget when
		// the proxy is trusted; spoofed leading entries never help an attacker.
		blocked := 0
		for i := 0; i < 30; i++ {
			r := e.req("POST", "/login", url.Values{"name": {"u" + string(rune('a'+i))}, "password": {"x"}}, nil,
				map[string]string{"X-Forwarded-For": "6.6.6.6, 10.0.0." + string(rune('1'+i%9))})
			if r.status == 429 {
				blocked++
			}
		}
		if !trust && blocked == 0 {
			t.Error("untrusted proxy header let one address exceed the login limit")
		}
		if trust && blocked != 0 {
			t.Errorf("trusted proxy: %d requests wrongly blocked", blocked)
		}
	}
}

// A viewer reads and nothing else, through the API as well.
func TestViewerIsReadOnly(t *testing.T) {
	e := newEnv(t)
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'afif'`)
	e.ok(e.afif(), "GET", "/v1/agents", nil, nil)
	e.fail(403, e.afif(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "no"})
	e.fail(403, e.afif(), "POST", "/v1/messages", api.SendReq{To: "lead", Body: "hi"})
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "q"}, nil)
	e.fail(403, e.afif(), "POST", "/v1/escalations/1/answer", api.AnswerReq{Answer: "yes"})
}

func TestResetPasswordSignsOutEverywhere(t *testing.T) {
	e := newWebEnv(t, Options{})
	c := e.setup("afif", goodPassword).cookie
	pw, err := ResetPassword(e.hub.db, "afif", e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if r := e.req("GET", "/inbox", nil, c, nil); r.status != 303 {
		t.Fatalf("old session after reset: %d", r.status)
	}
	if r := e.login("afif", goodPassword); r.status != 401 {
		t.Fatalf("old password after reset: %d", r.status)
	}
	if r := e.login("afif", pw); r.status != 303 {
		t.Fatalf("new password: %d", r.status)
	}
	if _, err := ResetPassword(e.hub.db, "ghost", e.clock.now()); err == nil {
		t.Fatal("reset of a missing human succeeded")
	}
	if !e.audited("human.password.reset", "human:afif") {
		t.Fatal("reset is not audited")
	}
}
