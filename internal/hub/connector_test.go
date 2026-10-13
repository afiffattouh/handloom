package hub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"handloom/internal/mcp"
	"handloom/internal/store"
)

// The remote connector (D109), tested the way Claude uses it: a Go client that does the whole OAuth flow
// (register, authorize with PKCE, consent, code, token, then /mcp, refresh and revoke) against a real hub.
// Paths, field names and addresses are written out in full on purpose: if the connector spec drifts, a
// test here fails loudly instead of a person finding out in Claude.

const claudeRedirect = "https://claude.ai/api/mcp/auth_callback"

// fakeRun stands in for the command line (which the hub cannot import): it turns the few commands the tests
// use into the API calls they make, as the person whose token it is given, through the hub's own handler.
func fakeRun(ctx context.Context, handler http.Handler, token string, argv []string) (string, string, int) {
	method, path, body := "GET", "", ""
	switch strings.Join(argv, " ") {
	case "digest":
		path = "/v1/digest"
	case "job list":
		path = "/v1/jobs"
	default:
		switch argv[0] {
		case "send":
			method, path, body = "POST", "/v1/messages", fmt.Sprintf(`{"to":%q,"text":%q}`, argv[1], argv[2])
		case "raw": // raw METHOD PATH: whatever the test wants to attempt
			method, path = argv[1], argv[2]
		default:
			return "", "unknown command in the test", 2
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		var e struct{ Error string }
		json.Unmarshal(rec.Body.Bytes(), &e)
		return "", "handloom: " + e.Error, 1
	}
	return rec.Body.String(), "", 0
}

func newConnectorEnv(t *testing.T) *webEnv {
	return newWebEnv(t, Options{OperatorRun: fakeRun, Version: "9.9.9-test"})
}

func pkce() (verifier, challenge string) {
	verifier = "v" + strings.Repeat("0123456789abcdef", 4)[:50] // 51 characters, all allowed
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (e *webEnv) jsonReq(method, path, body string, hdr map[string]string) webResp {
	e.t.Helper()
	r, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := e.http.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return webResp{status: resp.StatusCode, body: sb.String(), header: resp.Header}
}

func asMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v: %.300s", err, s)
	}
	return m
}

func (e *webEnv) register(redirects ...string) (string, webResp) {
	e.t.Helper()
	e.clock.advance(time.Minute)
	b, _ := json.Marshal(map[string]any{"client_name": "Claude", "redirect_uris": redirects, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
	r := e.jsonReq("POST", "/oauth/register", string(b), nil)
	if r.status != 201 {
		return "", r
	}
	return asMap(e.t, r.body)["client_id"].(string), r
}

func authzPath(clientID, redirect, challenge, state string, extra ...string) string {
	v := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			v.Del(extra[i])
		} else {
			v.Set(extra[i], extra[i+1])
		}
	}
	return "/oauth/authorize?" + v.Encode()
}

// consentForm takes the hidden fields off the consent page, as the browser would send them.
func consentForm(t *testing.T, body string) url.Values {
	t.Helper()
	f := url.Values{}
	for _, m := range regexpAll(`<input type="hidden" name="([a-z_]+)" value="([^"]*)">`, body) {
		f.Set(m[1], htmlUnescape(m[2]))
	}
	if f.Get("client_id") == "" {
		t.Fatalf("no consent form in: %.600s", body)
	}
	return f
}

// allow signs the person's consent and returns the redirect it ends in.
func (e *webEnv) allow(c *http.Cookie, path string) (*url.URL, webResp) {
	e.t.Helper()
	e.clock.advance(time.Minute) // the password check is rate limited
	page := e.req("GET", path, nil, c, nil)
	if page.status != 200 {
		e.t.Fatalf("consent page: %d %.300s", page.status, page.body)
	}
	f := consentForm(e.t, page.body)
	f.Set("csrf", csrfOf(e.t, page.body))
	f.Set("current_password", goodPassword)
	f.Set("decision", "allow")
	r := e.req("POST", "/oauth/authorize", f, c, nil)
	if r.status != 302 {
		e.t.Fatalf("allow: %d %.300s", r.status, r.body)
	}
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	return loc, r
}

func (e *webEnv) token(form url.Values) (webResp, map[string]any) {
	e.t.Helper()
	r := e.req("POST", "/oauth/token", form, nil, map[string]string{"Origin": ""})
	m := map[string]any{}
	json.Unmarshal([]byte(r.body), &m)
	return r, m
}

type appTokens struct {
	clientID, access, refresh, verifier, code string
}

// connect runs the whole flow for a signed-in person and returns the tokens.
func (e *webEnv) connect(c *http.Cookie, redirect string) appTokens {
	e.t.Helper()
	cid, r := e.register(redirect)
	if cid == "" {
		e.t.Fatalf("register: %d %s", r.status, r.body)
	}
	ver, ch := pkce()
	loc, _ := e.allow(c, authzPath(cid, redirect, ch, "st-1"))
	if loc.Query().Get("state") != "st-1" || loc.Query().Get("iss") != testOrigin || loc.Query().Get("code") == "" {
		e.t.Fatalf("redirect: %s", loc)
	}
	code := loc.Query().Get("code")
	tr, m := e.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {ver}, "redirect_uri": {redirect}, "client_id": {cid}})
	if tr.status != 200 {
		e.t.Fatalf("token: %d %s", tr.status, tr.body)
	}
	return appTokens{clientID: cid, access: m["access_token"].(string), refresh: m["refresh_token"].(string), verifier: ver, code: code}
}

func (e *webEnv) mcp(tok, body string, hdr map[string]string) webResp {
	h := map[string]string{"Authorization": "Bearer " + tok}
	if tok == "" {
		delete(h, "Authorization")
	}
	for k, v := range hdr {
		h[k] = v
	}
	return e.jsonReq("POST", "/mcp", body, h)
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude","version":"1"}}}`

// ---- the documents Claude reads first ----

func TestTheDiscoveryDocumentsArePinned(t *testing.T) {
	e := newConnectorEnv(t)
	res := e.jsonReq("GET", "/.well-known/oauth-protected-resource", "", nil)
	if res.status != 200 || res.header.Get("Content-Type") != "application/json" {
		t.Fatalf("resource metadata: %d %v", res.status, res.header)
	}
	m := asMap(t, res.body)
	if m["resource"] != testOrigin+"/mcp" || fmt.Sprint(m["authorization_servers"]) != "["+testOrigin+"]" ||
		fmt.Sprint(m["scopes_supported"]) != "[handloom]" || fmt.Sprint(m["bearer_methods_supported"]) != "[header]" || len(m) != 4 {
		t.Fatalf("resource metadata: %s", res.body)
	}
	if again := e.jsonReq("GET", "/.well-known/oauth-protected-resource/mcp", "", nil); again.body != res.body {
		t.Fatalf("the /mcp spelling differs: %s", again.body)
	}
	srv := e.jsonReq("GET", "/.well-known/oauth-authorization-server", "", nil)
	s := asMap(t, srv.body)
	want := map[string]string{
		"issuer": testOrigin, "authorization_endpoint": testOrigin + "/oauth/authorize", "token_endpoint": testOrigin + "/oauth/token", "registration_endpoint": testOrigin + "/oauth/register",
		"response_types_supported": "[code]", "grant_types_supported": "[authorization_code refresh_token]", "code_challenge_methods_supported": "[S256]",
		"token_endpoint_auth_methods_supported": "[none]", "scopes_supported": "[handloom]", "authorization_response_iss_parameter_supported": "true",
	}
	for k, v := range want {
		if fmt.Sprint(s[k]) != v {
			t.Errorf("%s = %v, want %s", k, s[k], v)
		}
	}
	if len(s) != len(want) {
		t.Errorf("unexpected fields in the server metadata: %s", srv.body) // client_id_metadata_document_supported must stay out (D109: no CIMD)
	}
	if strings.HasSuffix(fmt.Sprint(s["issuer"]), "/") {
		t.Error("the issuer has a trailing slash")
	}
	if oidc := e.jsonReq("GET", "/.well-known/openid-configuration", "", nil); oidc.body != srv.body {
		t.Errorf("the OpenID spelling differs: %s", oidc.body)
	}
}

func TestTheConnectorIsOffWithoutAPublicAddress(t *testing.T) {
	// The web UI works on a private network by HANDLOOM_INSECURE alone, but the connector needs a fixed issuer.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := New(db, Options{Insecure: true})
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
		resp, _ := http.Get(srv.URL + p)
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp, _ := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(initBody))
	if resp.StatusCode != 404 {
		t.Errorf("/mcp: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ---- the whole flow ----

func TestTheWholeConnectorFlow(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()

	// Not signed in or without a token: 401 and the pointer to the metadata, nothing else.
	r := e.mcp("", initBody, nil)
	if r.status != 401 || r.header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+testOrigin+`/.well-known/oauth-protected-resource"` {
		t.Fatalf("no token: %d %v", r.status, r.header)
	}

	app := e.connect(c, claudeRedirect)
	if !strings.HasPrefix(app.access, "hvt_") || !strings.HasPrefix(app.refresh, "hvf_") {
		t.Fatalf("token shapes: %q %q", app.access[:4], app.refresh[:4])
	}

	// initialize, then the notification, then the tools.
	r = e.mcp(app.access, initBody, nil)
	init := asMap(t, r.body)
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("initialize: %d %v", r.status, r.header)
	}
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" || res["serverInfo"].(map[string]any)["name"] != "handloom" || res["serverInfo"].(map[string]any)["version"] != "9.9.9-test" {
		t.Fatalf("initialize result: %s", r.body)
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("no tools capability: %s", r.body)
	}
	if r = e.mcp(app.access, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); r.status != 202 || r.body != "" {
		t.Fatalf("notification: %d %q", r.status, r.body)
	}
	r = e.mcp(app.access, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string
				Annotations map[string]any
			}
		}
	}
	json.Unmarshal([]byte(r.body), &list)
	got := map[string]bool{}
	for _, tl := range list.Result.Tools {
		got[tl.Name] = true
	}
	// The tool set is the one of `handloom mcp --operator` without approvals: the same table, so the same names.
	for _, tl := range mcp.OperatorTools(false) {
		if !got[tl.Name] {
			t.Errorf("tool %s is not offered over /mcp", tl.Name)
		}
	}
	if len(got) != len(mcp.OperatorTools(false)) {
		t.Errorf("%d tools over /mcp, want %d", len(got), len(mcp.OperatorTools(false)))
	}
	for _, n := range []string{"handloom_task_accept", "handloom_task_reject", "handloom_answer", "handloom_job_close"} {
		if got[n] {
			t.Errorf("%s is offered over /mcp", n)
		}
	}

	// A tool runs as the person.
	r = e.mcp(app.access, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, nil)
	var callRes struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
	}
	json.Unmarshal([]byte(r.body), &callRes)
	if r.status != 200 || callRes.Result.IsError || len(callRes.Result.Content) == 0 || !strings.Contains(callRes.Result.Content[0].Text, "needs_you") {
		t.Fatalf("digest: %d %s", r.status, r.body)
	}

	// The refresh token rotates.
	tr, m := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {app.refresh}, "client_id": {app.clientID}})
	if tr.status != 200 || m["refresh_token"] == app.refresh || m["access_token"] == app.access || m["token_type"] != "Bearer" || m["expires_in"] != float64(3600) || m["scope"] != "handloom" {
		t.Fatalf("refresh: %d %s", tr.status, tr.body)
	}
	if tr.header.Get("Cache-Control") != "no-store" {
		t.Errorf("token response cache header: %q", tr.header.Get("Cache-Control"))
	}
	newAccess := m["access_token"].(string)
	if r = e.mcp(newAccess, `{"jsonrpc":"2.0","id":4,"method":"ping"}`, nil); r.status != 200 {
		t.Fatalf("the new access token: %d", r.status)
	}

	// It is on the Connect page, and disconnecting ends everything the grant issued.
	page := e.req("GET", "/connect", nil, c, nil)
	if !strings.Contains(page.body, "Claude") || !strings.Contains(page.body, "/disconnect") {
		t.Fatalf("connect page lacks the app: %s", page.body)
	}
	gid := regexpAll(`/connect/apps/(\d+)/disconnect`, page.body)[0][1]
	if d := e.req("POST", "/connect/apps/"+gid+"/disconnect", url.Values{"csrf": {csrfOf(t, page.body)}}, c, nil); d.status != 303 {
		t.Fatalf("disconnect: %d %s", d.status, d.body)
	}
	for _, tok := range []string{app.access, newAccess} {
		if r = e.mcp(tok, `{"jsonrpc":"2.0","id":5,"method":"ping"}`, nil); r.status != 401 {
			t.Fatalf("after disconnect: %d", r.status)
		}
	}
	if tr, _ = e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {m["refresh_token"].(string)}, "client_id": {app.clientID}}); tr.status != 400 {
		t.Fatalf("refresh after disconnect: %d %s", tr.status, tr.body)
	}
	if !e.audited("app.connect", "human:afif") || !e.audited("app.revoke", "human:afif") {
		t.Error("app.connect and app.revoke must be in the audit log")
	}
	if strings.Contains(e.auditText(), "hvt_") || strings.Contains(e.auditText(), "hvf_") || strings.Contains(e.auditText(), "hvk_") {
		t.Error("a token is in the audit log")
	}
	if page = e.req("GET", "/connect", nil, c, nil); strings.Contains(page.body, "/disconnect") {
		t.Error("a disconnected app is still listed")
	}
}

// auditText is the whole audit log as one string, to look for things that must never be in it.
func (e *webEnv) auditText() string {
	rows, err := e.hub.db.Query(`SELECT actor || ' ' || action || ' ' || target || ' ' || payload || ' ' || COALESCE(via, '') FROM audit`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var s string
		rows.Scan(&s)
		sb.WriteString(s + "\n")
	}
	return sb.String()
}

// ---- the protocol on /mcp ----

func TestTheMCPEndpointSpeaksBothGenerations(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	tok := e.connect(c, claudeRedirect).access

	// Classic: the client's version is echoed when supported, our newest otherwise.
	for ask, want := range map[string]string{"2025-03-26": "2025-03-26", "2025-11-25": "2025-11-25", "2026-07-28": "2026-07-28", "2024-11-05": "2026-07-28", "": "2026-07-28"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + ask + `"}}`
		got := asMap(t, e.mcp(tok, body, nil).body)["result"].(map[string]any)["protocolVersion"]
		if got != want {
			t.Errorf("asked %q: answered %v, want %s", ask, got, want)
		}
	}
	// Stateless: no initialize, headers present. They are accepted and never required.
	hdr := map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/list"}
	r := e.mcp(tok, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`, hdr)
	if r.status != 200 || !strings.Contains(r.body, "handloom_digest") {
		t.Fatalf("stateless tools/list: %d %.200s", r.status, r.body)
	}
	hdr["Mcp-Name"] = "handloom_digest"
	hdr["Mcp-Method"] = "tools/call"
	r = e.mcp(tok, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, hdr)
	if r.status != 200 || strings.Contains(r.body, `"isError":true`) {
		t.Fatalf("stateless tools/call: %d %.200s", r.status, r.body)
	}
	// A header that disagrees with the body is a 400 with a JSON-RPC error.
	r = e.mcp(tok, `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`, map[string]string{"Mcp-Method": "tools/call"})
	if r.status != 400 || asMap(t, r.body)["error"].(map[string]any)["code"] == nil {
		t.Fatalf("mismatched Mcp-Method: %d %s", r.status, r.body)
	}
	// An unknown protocol version in the header lists the supported ones.
	r = e.mcp(tok, `{"jsonrpc":"2.0","id":9,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "1999-01-01"})
	if r.status != 400 {
		t.Fatalf("unknown version: %d", r.status)
	}
	for _, v := range []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"} {
		if !strings.Contains(r.body, v) {
			t.Errorf("the refusal does not list %s: %s", v, r.body)
		}
	}
	// Probes get empty lists, not errors.
	for method, key := range map[string]string{"resources/list": "resources", "prompts/list": "prompts", "ping": ""} {
		r = e.mcp(tok, `{"jsonrpc":"2.0","id":1,"method":"`+method+`"}`, nil)
		res := asMap(t, r.body)["result"].(map[string]any)
		if key != "" && fmt.Sprint(res[key]) != "[]" {
			t.Errorf("%s: %s", method, r.body)
		}
	}
	// Unknown methods are JSON-RPC errors; notifications and answers from the client are 202; GET and DELETE are 405.
	if r = e.mcp(tok, `{"jsonrpc":"2.0","id":1,"method":"nope"}`, nil); asMap(t, r.body)["error"].(map[string]any)["code"] != float64(-32601) {
		t.Errorf("unknown method: %s", r.body)
	}
	for _, body := range []string{`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`, `{"jsonrpc":"2.0","id":1,"result":{}}`} {
		if r = e.mcp(tok, body, nil); r.status != 202 || r.body != "" {
			t.Errorf("%s: %d %q", body, r.status, r.body)
		}
	}
	for _, m := range []string{"GET", "DELETE", "PUT"} {
		if r = e.jsonReq(m, "/mcp", "", map[string]string{"Authorization": "Bearer " + tok}); r.status != 405 || r.header.Get("Allow") != "POST" {
			t.Errorf("%s /mcp: %d %v", m, r.status, r.header)
		}
	}
	if r = e.mcp(tok, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, nil); r.status != 400 {
		t.Errorf("a batch: %d", r.status)
	}
	if r = e.mcp(tok, `not json`, nil); r.status != 400 || asMap(t, r.body)["error"].(map[string]any)["code"] != float64(-32700) {
		t.Errorf("garbage: %d %s", r.status, r.body)
	}
	// The trailing-slash spelling reaches the same place.
	if r = e.jsonReq("POST", "/mcp/", initBody, map[string]string{"Authorization": "Bearer " + tok}); r.status != 200 {
		t.Errorf("/mcp/: %d", r.status)
	}
}

func TestTheMCPOriginCheck(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	tok := e.connect(c, claudeRedirect).access
	for origin, want := range map[string]int{
		"": 200, testOrigin: 200, "https://claude.ai": 200, "https://www.claude.ai": 200, "https://claude.com": 200, "https://app.claude.com": 200,
		"https://evil.example": 403, "https://claude.ai.evil.example": 403, "http://claude.ai": 403, "https://notclaude.ai": 403, "https://evilclaude.ai": 403, "null": 403, "https://claude.ai:8443": 403,
	} {
		r := e.mcp(tok, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": origin})
		if r.status != want {
			t.Errorf("Origin %q: %d, want %d", origin, r.status, want)
		}
	}
	// The origin is checked before the token: a hostile page learns nothing about whether a token is good.
	if r := e.mcp("hvt_nope", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "https://evil.example"}); r.status != 403 {
		t.Errorf("hostile origin with a bad token: %d", r.status)
	}
}

// ---- who may use /mcp, and what a connected app can do through the API ----

func TestWhoMayUseTheConnectorAddress(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	ag := e.agents()
	app := e.connect(c, claudeRedirect)
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if r := e.mcp(app.access, ping, nil); r.status != 200 {
		t.Fatalf("a connected app: %d", r.status)
	}
	// The D80 token is the same kind of principal, so it is accepted here, and making it does not touch the connected app.
	mine := e.humanAPI()
	var op struct{ Token string }
	e.apiOK(mine, "", "POST", "/v1/me/operator-token", nil, &op)
	if r := e.mcp(op.Token, ping, nil); r.status != 200 {
		t.Errorf("a static hvo_ token: %d", r.status)
	}
	if r := e.mcp(app.access, ping, nil); r.status != 200 {
		t.Errorf("connecting a static token disconnected the app: %d", r.status)
	}
	e.apiOK(mine, "", "POST", "/v1/me/operator-token/remove", nil, nil)
	if r := e.mcp(app.access, ping, nil); r.status != 200 {
		t.Errorf("removing the static token disconnected the app: %d", r.status)
	}
	// Everything else is refused with the same 401 and pointer: the admin token, a person's own token, a machine's
	// credential, an agent's request, and the OAuth codes and refresh tokens.
	for name, tok := range map[string]string{"admin": e.admin, "human": mine, "device": ag.device, "garbage": "hvt_" + strings.Repeat("a", 43), "refresh": app.refresh, "code": app.code, "client id": app.clientID} {
		r := e.mcp(tok, ping, map[string]string{"Handloom-Agent": "lead"})
		if r.status != 401 || !strings.Contains(r.header.Get("WWW-Authenticate"), "resource_metadata=") {
			t.Errorf("%s token on /mcp: %d %v", name, r.status, r.header)
		}
	}
}

func TestAConnectedAppIsHeldToTheAIAppCalls(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	app := e.connect(c, claudeRedirect)
	// It reads.
	for _, p := range []string{"/v1/whoami", "/v1/digest", "/v1/jobs", "/v1/tasks", "/v1/agents", "/v1/escalations", "/v1/profiles", "/v1/metrics"} {
		if code := e.status(app.access, "GET", p, nil); code != 200 {
			t.Errorf("GET %s: %d", p, code)
		}
	}
	// It cannot approve, change settings, make tokens or reach the people list, and each refusal is audited.
	before := e.auditCount("denied")
	refused := [][2]string{
		{"POST", "/v1/tasks/1/accept"}, {"POST", "/v1/tasks/1/reject"}, {"POST", "/v1/escalations/1/answer"}, {"POST", "/v1/jobs/1/close"},
		{"POST", "/v1/prices"}, {"GET", "/v1/people"}, {"POST", "/v1/profiles"}, {"POST", "/v1/me/token"}, {"POST", "/v1/me/operator-token"}, {"GET", "/v1/notifications"},
		{"POST", "/v1/devices"}, {"POST", "/v1/codebases/x/merge"},
	}
	for _, rc := range refused {
		if code := e.status(app.access, rc[0], rc[1], map[string]any{}); code != 403 {
			t.Errorf("%s %s with a connected app: %d, want 403", rc[0], rc[1], code)
		}
	}
	if got := e.auditCount("denied") - before; got != len(refused) {
		t.Errorf("%d refusals audited, want %d", got, len(refused))
	}
	// Through a tool, the hub refuses the same way (the fake command line only forwards the call).
	r := e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, nil)
	if strings.Contains(r.body, "isError\":true") {
		t.Errorf("digest: %s", r.body)
	}
	// An approval tool is not in the table, and calling it by name says so.
	for _, n := range []string{"handloom_task_accept", "handloom_answer", "handloom_job_close", "handloom_task_reject"} {
		r = e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+n+`","arguments":{"id":1,"answer":"x","reason":"x"}}}`, nil)
		if !strings.Contains(r.body, `"isError":true`) || !strings.Contains(r.body, "unknown tool") {
			t.Errorf("%s by name: %s", n, r.body)
		}
	}
	// A tool's failure is a tool result with a plain message, never a trace or a token.
	e.hub.opt.OperatorRun = func(ctx context.Context, h http.Handler, token string, argv []string) (string, string, int) {
		return "", "handloom: boom " + token, 1
	}
	r = e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, nil)
	if !strings.Contains(r.body, `"isError":true`) || strings.Contains(r.body, app.access) || !strings.Contains(r.body, "[token]") {
		t.Errorf("a failing tool: %s", r.body)
	}
	e.hub.opt.OperatorRun = func(context.Context, http.Handler, string, []string) (string, string, int) {
		panic("goroutine 1 [running]: secret")
	}
	r = e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, nil)
	if !strings.Contains(r.body, `"isError":true`) || strings.Contains(r.body, "goroutine") {
		t.Errorf("a panicking tool: %s", r.body)
	}
	e.hub.opt.OperatorRun = nil
	r = e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, nil)
	if !strings.Contains(r.body, `"isError":true`) {
		t.Errorf("no command line: %s", r.body)
	}
	// A viewer who was connected and then lowered stays a viewer: the person's role applies, whatever the token says.
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'afif'`)
	if code := e.status(app.access, "POST", "/v1/messages", map[string]any{"to": "x", "text": "y"}); code != 403 {
		t.Errorf("a lowered person sends a message: %d", code)
	}
}

func (e *webEnv) auditCount(action string) int {
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM audit WHERE action = ?`, action).Scan(&n)
	return n
}

func TestAnAccessTokenExpiresAfterAnHourAndARefreshTokenAfterSixtyDays(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	app := e.connect(c, claudeRedirect)
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	e.clock.advance(59 * time.Minute)
	if r := e.mcp(app.access, ping, nil); r.status != 200 {
		t.Fatalf("at 59 minutes: %d", r.status)
	}
	e.clock.advance(2 * time.Minute)
	if r := e.mcp(app.access, ping, nil); r.status != 401 {
		t.Fatalf("at 61 minutes: %d", r.status)
	}
	// Refreshing slides the window: the new refresh token lasts sixty days from now.
	e.clock.advance(50 * 24 * time.Hour)
	tr, m := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {app.refresh}, "client_id": {app.clientID}})
	if tr.status != 200 {
		t.Fatalf("refresh at 50 days: %d %s", tr.status, tr.body)
	}
	e.clock.advance(59 * 24 * time.Hour)
	tr, m2 := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {m["refresh_token"].(string)}, "client_id": {app.clientID}})
	if tr.status != 200 {
		t.Fatalf("refresh at 109 days (59 after the last): %d %s", tr.status, tr.body)
	}
	e.clock.advance(61 * 24 * time.Hour)
	if tr, _ = e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {m2["refresh_token"].(string)}, "client_id": {app.clientID}}); tr.status != 400 || !strings.Contains(tr.body, "invalid_grant") {
		t.Fatalf("an expired refresh token: %d %s", tr.status, tr.body)
	}
}

// ---- the OAuth endpoints' refusals ----

func TestRegistrationRefusals(t *testing.T) {
	e := newConnectorEnv(t)
	post := func(body string) webResp {
		e.clock.advance(time.Minute)
		return e.jsonReq("POST", "/oauth/register", body, nil)
	}
	ok := func(body string) {
		t.Helper()
		if r := post(body); r.status != 201 {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
	bad := func(body, code string) {
		t.Helper()
		if r := post(body); r.status != 400 || asMap(t, r.body)["error"] != code {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
	ok(`{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"]}`)
	ok(`{"redirect_uris":["http://localhost:51234/callback"]}`)
	ok(`{"redirect_uris":["http://127.0.0.1:8123/cb"],"client_name":"Claude Code"}`)
	ok(`{"redirect_uris":["http://localhost/cb"]}`)
	bad(`{"redirect_uris":["https://evil.example/cb"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["https://claude.ai/api/mcp/auth_callback/"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["https://claude.ai.evil.example/api/mcp/auth_callback"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://claude.ai/api/mcp/auth_callback"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://evil.example:80/cb"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://localhost.evil.example:80/cb"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://[::1]:80/cb"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://user@localhost:80/cb"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://localhost:80/cb#frag"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["javascript:alert(1)"]}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":[]}`, "invalid_redirect_uri")
	bad(`{}`, "invalid_redirect_uri")
	bad(`{"redirect_uris":["http://localhost:80/cb"],"token_endpoint_auth_method":"client_secret_basic"}`, "invalid_client_metadata")
	bad(`{"redirect_uris":["http://localhost:80/cb"],"client_secret":"s"}`, "invalid_client_metadata")
	bad(`{"redirect_uris":["http://localhost:80/cb"],"grant_types":["password"]}`, "invalid_client_metadata")
	bad(`{"redirect_uris":["http://localhost:80/cb"],"response_types":["token"]}`, "invalid_client_metadata")
	bad(`{"redirect_uris":["http://localhost:80/cb"],"scope":"admin"}`, "invalid_client_metadata")
	bad(`not json`, "invalid_client_metadata")
	if r := post(`{"redirect_uris":["http://localhost:80/cb"],"client_name":"` + strings.Repeat("x", 5000) + `"}`); r.status != 400 {
		t.Errorf("a body over 4 KB: %d", r.status)
	}
	// The answer.
	r := post(`{"redirect_uris":["http://localhost:80/cb"],"client_name":"  Hostile\u0000\n<b>name</b>` + strings.Repeat("y", 100) + `"}`)
	m := asMap(t, r.body)
	name := m["client_name"].(string)
	if r.status != 201 || !strings.HasPrefix(m["client_id"].(string), "hvc_") || m["token_endpoint_auth_method"] != "none" || len([]rune(name)) > 60 ||
		strings.ContainsAny(name, "\x00\n") || fmt.Sprint(m["redirect_uris"]) != "[http://localhost:80/cb]" || fmt.Sprint(m["response_types"]) != "[code]" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("registration answer: %d %v %s", r.status, r.header, r.body)
	}
	// An address the owner listed is accepted.
	e2 := newWebEnv(t, Options{OAuthRedirects: []string{"https://chat.example.org/oauth/callback"}})
	if r := e2.jsonReq("POST", "/oauth/register", `{"redirect_uris":["https://chat.example.org/oauth/callback"]}`, nil); r.status != 201 {
		t.Errorf("a listed address: %d %s", r.status, r.body)
	}
	if r := e2.jsonReq("POST", "/oauth/register", `{"redirect_uris":["https://chat.example.org/other"]}`, nil); r.status != 400 {
		t.Errorf("an address that is not listed: %d", r.status)
	}
}

func TestRegistrationIsRateLimitedCappedAndCleanedUp(t *testing.T) {
	e := newConnectorEnv(t)
	body := `{"redirect_uris":["http://localhost:80/cb"]}`
	codes := map[int]int{}
	for i := 0; i < 15; i++ {
		codes[e.jsonReq("POST", "/oauth/register", body, nil).status]++
	}
	if codes[201] != 10 || codes[429] != 5 {
		t.Fatalf("a burst of 15 registrations from one address: %v", codes)
	}
	// The cap: 500 apps that never finished, then no more until the old ones are cleaned out after a week.
	for i := 0; i < 600; i++ {
		e.hub.db.Exec(`INSERT OR IGNORE INTO oauth_client(client_id, name, redirect_uris, created_at) VALUES (?, 'x', '[]', ?)`, fmt.Sprintf("hvc_fill%d", i), store.Millis(e.clock.now()))
	}
	e.clock.advance(time.Hour)
	if r := e.jsonReq("POST", "/oauth/register", body, nil); r.status != 503 {
		t.Fatalf("over the cap: %d %s", r.status, r.body)
	}
	e.clock.advance(8 * 24 * time.Hour)
	if r := e.jsonReq("POST", "/oauth/register", body, nil); r.status != 201 {
		t.Fatalf("after a week: %d %s", r.status, r.body)
	}
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM oauth_client`).Scan(&n)
	if n != 1 {
		t.Errorf("%d apps left after the clean-up, want only the new one", n)
	}
}

func TestAConnectedAppSurvivesTheCleanUp(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	app := e.connect(c, claudeRedirect)
	e.clock.advance(30 * 24 * time.Hour)
	e.register("http://localhost:80/cb") // runs the clean-up
	tr, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {app.refresh}, "client_id": {app.clientID}})
	if tr.status != 200 {
		t.Fatalf("a connected app after the clean-up: %d %s", tr.status, tr.body)
	}
}

func TestAuthorizeRefusals(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	cid, _ := e.register(claudeRedirect)
	lb, _ := e.register("http://localhost:80/cb")
	_, ch := pkce()

	// A wrong app or a wrong redirect address is a plain page: nothing is redirected anywhere.
	for name, p := range map[string]string{
		"unknown client":       authzPath("hvc_unknown", claudeRedirect, ch, "s"),
		"no client":            authzPath("", claudeRedirect, ch, "s", "client_id", ""),
		"other redirect":       authzPath(cid, "https://evil.example/cb", ch, "s"),
		"redirect near-miss":   authzPath(cid, claudeRedirect+"/", ch, "s"),
		"claude on a new port": authzPath(cid, "https://claude.ai:444/api/mcp/auth_callback", ch, "s"),
		"loopback other path":  authzPath(lb, "http://localhost:5555/other", ch, "s"),
		"loopback other host":  authzPath(lb, "http://127.0.0.1:80/cb", ch, "s"),
		"loopback https":       authzPath(lb, "https://localhost:80/cb", ch, "s"),
		"no redirect":          authzPath(cid, "", ch, "s", "redirect_uri", ""),
	} {
		r := e.req("GET", p, nil, c, nil)
		if r.status != 400 || r.header.Get("Location") != "" || !strings.Contains(r.body, "Cannot connect") {
			t.Errorf("%s: %d location=%q", name, r.status, r.header.Get("Location"))
		}
	}
	// Anything else wrong goes back to the registered address as an OAuth error, with the state.
	for name, c2 := range map[string]struct{ path, code string }{
		"plain PKCE":        {authzPath(cid, claudeRedirect, ch, "s1", "code_challenge_method", "plain"), "invalid_request"},
		"no PKCE":           {authzPath(cid, claudeRedirect, "", "s1", "code_challenge", ""), "invalid_request"},
		"short challenge":   {authzPath(cid, claudeRedirect, "abc", "s1"), "invalid_request"},
		"token response":    {authzPath(cid, claudeRedirect, ch, "s1", "response_type", "token"), "unsupported_response_type"},
		"other scope":       {authzPath(cid, claudeRedirect, ch, "s1", "scope", "handloom admin"), "invalid_scope"},
		"another resource":  {authzPath(cid, claudeRedirect, ch, "s1", "resource", "https://other.example/mcp"), "invalid_target"},
		"state far too big": {authzPath(cid, claudeRedirect, ch, strings.Repeat("s", 600)), "invalid_request"},
	} {
		r := e.req("GET", c2.path, nil, c, nil)
		loc, _ := url.Parse(r.header.Get("Location"))
		if r.status != 302 || loc == nil || !strings.HasPrefix(loc.String(), claudeRedirect) || loc.Query().Get("error") != c2.code || loc.Query().Get("iss") != testOrigin {
			t.Errorf("%s: %d %q", name, r.status, r.header.Get("Location"))
		}
		if name != "state far too big" && loc.Query().Get("state") != "s1" {
			t.Errorf("%s: state not echoed: %q", name, loc)
		}
	}
	// A resource that is the hub's address, or its /mcp address, and the one scope are accepted.
	for _, extra := range [][]string{{"resource", testOrigin}, {"resource", testOrigin + "/mcp"}, {"scope", "handloom"}} {
		if r := e.req("GET", authzPath(cid, claudeRedirect, ch, "s", extra...), nil, c, nil); r.status != 200 {
			t.Errorf("%v: %d", extra, r.status)
		}
	}
	// Loopback matches whatever port the app listens on this time, and nothing else about it.
	if r := e.req("GET", authzPath(lb, "http://localhost:61234/cb", ch, "s"), nil, c, nil); r.status != 200 {
		t.Errorf("loopback on another port: %d", r.status)
	}
	// A viewer is refused in words.
	vc, _ := e.member("vi", "viewer")
	if r := e.req("GET", authzPath(cid, claudeRedirect, ch, "s"), nil, vc, nil); r.status != 403 || !strings.Contains(r.body, "Viewers cannot connect") {
		t.Errorf("a viewer: %d", r.status)
	}
}

func TestSigningInOnTheWayToTheConsentPage(t *testing.T) {
	e := newConnectorEnv(t)
	e.owner()
	cid, _ := e.register(claudeRedirect)
	_, ch := pkce()
	path := authzPath(cid, claudeRedirect, ch, "s")
	r := e.req("GET", path, nil, nil, nil)
	if r.status != 303 || r.header.Get("Location") != "/login?next="+url.QueryEscape(path) {
		t.Fatalf("not signed in: %d %q", r.status, r.header.Get("Location"))
	}
	page := e.req("GET", r.header.Get("Location"), nil, nil, nil)
	if !strings.Contains(page.body, `name="next" value="`+htmlEscapeAttr(path)+`"`) {
		t.Fatalf("the sign-in page does not carry the way back: %s", page.body)
	}
	in := e.req("POST", "/login", url.Values{"name": {"afif"}, "password": {goodPassword}, "next": {path}}, nil, nil)
	if in.status != 303 || in.header.Get("Location") != path || in.cookie == nil {
		t.Fatalf("sign in: %d %q", in.status, in.header.Get("Location"))
	}
	// A wrong password keeps the way back; the way back is never anything but the authorize address.
	if bad := e.req("POST", "/login", url.Values{"name": {"afif"}, "password": {"wrong"}, "next": {path}}, nil, nil); bad.status != 401 || !strings.Contains(bad.body, `name="next"`) {
		t.Errorf("wrong password: %d", bad.status)
	}
	for _, evil := range []string{"https://evil.example/", "//evil.example/", "/oauth/authorize", "/oauth/authorizeX?a=b", "/command", "/oauth/authorize?a=b\r\nSet-Cookie: x=y", `/oauth/authorize?a=\evil`, "/oauth/authorize?a=b c", "javascript:alert(1)"} {
		if safeNext(evil) != "" {
			t.Errorf("safeNext(%q) is allowed", evil)
		}
		e.clock.advance(time.Minute) // sign-ins are rate limited
		in := e.req("POST", "/login", url.Values{"name": {"afif"}, "password": {goodPassword}, "next": {evil}}, nil, nil)
		if in.header.Get("Location") != "/command" {
			t.Errorf("next=%q went to %q", evil, in.header.Get("Location"))
		}
	}
	if safeNext(path) != path {
		t.Error("the authorize address itself must pass")
	}
}

func TestTheConsentPage(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	e.clock.advance(time.Minute)
	b, _ := json.Marshal(map[string]any{"client_name": `<script>alert(1)</script> "><img src=x onerror=alert(2)>`, "redirect_uris": []string{claudeRedirect}})
	reg := e.jsonReq("POST", "/oauth/register", string(b), nil)
	cid := asMap(t, reg.body)["client_id"].(string)
	_, ch := pkce()
	page := e.req("GET", authzPath(cid, claudeRedirect, ch, `s"><script>x</script>`), nil, c, nil)
	for _, want := range []string{"wants to connect to Handloom as", "<strong>afif</strong>", "look at jobs, tasks, agents, the inbox and the numbers; start jobs; message agents",
		"accept work, answer an agent's questions, close jobs or change settings", "disconnect it at any time", "Connect an AI app", `value="allow"`, `value="deny"`, `type="password"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the consent page lacks %q", want)
		}
	}
	if strings.Contains(page.body, "<script>alert") || strings.Contains(page.body, "<img src=x") || strings.Contains(page.body, "<script>x</script>") {
		t.Fatalf("the client name or state became markup: %s", page.body)
	}
	if csp := page.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://claude.ai;") || !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") || page.header.Get("Cache-Control") != "no-store" {
		t.Errorf("consent headers: %q", csp)
	}
	// Without the page's security token nothing happens; with a wrong password the page comes back with the reason.
	f := consentForm(t, page.body)
	f.Del("csrf")
	f.Set("current_password", goodPassword)
	f.Set("decision", "allow")
	if r := e.req("POST", "/oauth/authorize", f, c, nil); r.status != 403 {
		t.Errorf("no CSRF token: %d", r.status)
	}
	f.Set("csrf", csrfOf(t, page.body))
	if r := e.req("POST", "/oauth/authorize", f, c, map[string]string{"Origin": "https://evil.example"}); r.status != 403 {
		t.Errorf("a foreign origin: %d", r.status)
	}
	f.Set("current_password", "not my password")
	if r := e.req("POST", "/oauth/authorize", f, c, nil); r.status != 403 || !strings.Contains(r.body, "Your password is not right") || r.header.Get("Location") != "" {
		t.Errorf("wrong password: %d %.200s", r.status, r.body)
	}
	f.Set("current_password", "")
	if r := e.req("POST", "/oauth/authorize", f, c, nil); r.status != 403 || r.header.Get("Location") != "" {
		t.Errorf("no password: %d", r.status)
	}
	if n := e.grantCount(); n != 0 {
		t.Errorf("%d grants made without consent", n)
	}
	// A viewer cannot post it either, and a post that changes the redirect address after the page is checked again.
	vc, vtok := e.member("vi", "viewer")
	f.Set("csrf", vtok)
	f.Set("current_password", goodPassword)
	if r := e.req("POST", "/oauth/authorize", f, vc, nil); r.status != 403 || r.header.Get("Location") != "" {
		t.Errorf("a viewer posts: %d", r.status)
	}
	f.Set("csrf", csrfOf(t, page.body))
	f.Set("redirect_uri", "https://evil.example/cb")
	if r := e.req("POST", "/oauth/authorize", f, c, nil); r.status != 400 || r.header.Get("Location") != "" {
		t.Errorf("a changed redirect address: %d %q", r.status, r.header.Get("Location"))
	}
	// Cancel sends access_denied back, needs no password, and is audited.
	f.Set("redirect_uri", claudeRedirect)
	f.Set("decision", "deny")
	f.Set("current_password", "")
	r := e.req("POST", "/oauth/authorize", f, c, nil)
	loc, _ := url.Parse(r.header.Get("Location"))
	if r.status != 302 || loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != `s"><script>x</script>` || loc.Query().Get("iss") != testOrigin || loc.Query().Get("code") != "" {
		t.Fatalf("cancel: %d %q", r.status, r.header.Get("Location"))
	}
	if !e.audited("app.deny", "human:afif") || e.grantCount() != 0 {
		t.Error("cancel must be audited and make no grant")
	}
}

func (e *webEnv) grantCount() int {
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM oauth_grant`).Scan(&n)
	return n
}

func TestLoopbackRedirectsMatchByPortOnly(t *testing.T) {
	for _, c := range []struct {
		reg, got string
		want     bool
	}{
		{"http://localhost:1234/cb", "http://localhost:1234/cb", true},
		{"http://localhost:1234/cb", "http://localhost:9999/cb", true},
		{"http://127.0.0.1:1234/cb", "http://127.0.0.1:55/cb", true},
		{"http://localhost/cb", "http://localhost:8080/cb", true},
		{"http://localhost:1234/cb?x=1", "http://localhost:5678/cb?x=1", true},
		{"http://localhost:1234/cb", "http://localhost:5678/cb?x=1", false},
		{"http://localhost:1234/cb", "http://localhost:5678/CB", false},
		{"http://localhost:1234/cb", "http://127.0.0.1:1234/cb", false},
		{"http://localhost:1234/cb", "https://localhost:1234/cb", false},
		{"http://localhost:1234/cb", "http://localhost.evil.example:1234/cb", false},
		{"http://localhost:1234/cb", "http://evil@localhost:1234/cb", false},
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback", true},
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai:444/api/mcp/auth_callback", false},
		{"https://example.org:1/cb", "https://example.org:2/cb", false}, // the port is free for loopback only
	} {
		if got := redirectMatches(c.reg, c.got); got != c.want {
			t.Errorf("redirectMatches(%q, %q) = %v, want %v", c.reg, c.got, got, c.want)
		}
	}
}

func TestClaudeCodeConnectsOnAnyLoopbackPort(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	cid, _ := e.register("http://localhost:41111/callback")
	ver, ch := pkce()
	loc, _ := e.allow(c, authzPath(cid, "http://localhost:52222/callback", ch, "z"))
	if loc.Host != "localhost:52222" || loc.Path != "/callback" || loc.Query().Get("code") == "" {
		t.Fatalf("redirected to %s", loc)
	}
	tr, _ := e.token(url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "code_verifier": {ver}, "redirect_uri": {"http://localhost:52222/callback"}, "client_id": {cid}})
	if tr.status != 200 {
		t.Fatalf("token: %d %s", tr.status, tr.body)
	}
	// The code was made for that exact redirect address.
	loc, _ = e.allow(c, authzPath(cid, "http://localhost:52222/callback", ch, "z"))
	if tr, _ = e.token(url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "code_verifier": {ver}, "redirect_uri": {"http://localhost:41111/callback"}, "client_id": {cid}}); tr.status != 400 {
		t.Fatalf("a different port at the token step: %d", tr.status)
	}
}

func TestTokenEndpointRefusals(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	cid, _ := e.register(claudeRedirect)
	other, _ := e.register(claudeRedirect)
	ver, ch := pkce()
	freshCode := func() string {
		loc, _ := e.allow(c, authzPath(cid, claudeRedirect, ch, "s"))
		return loc.Query().Get("code")
	}
	exch := func(code string, mod func(url.Values)) (webResp, map[string]any) {
		f := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {ver}, "redirect_uri": {claudeRedirect}, "client_id": {cid}}
		if mod != nil {
			mod(f)
		}
		return e.token(f)
	}
	wantErr := func(name string, r webResp, m map[string]any, status int, code string) {
		t.Helper()
		if r.status != status || m["error"] != code || r.header.Get("Cache-Control") != "no-store" || m["error_description"] == "" {
			t.Errorf("%s: %d %s (want %d %s)", name, r.status, r.body, status, code)
		}
	}
	// Missing and wrong pieces.
	code := freshCode()
	r, m := exch(code, func(f url.Values) { f.Del("code_verifier") })
	wantErr("no verifier", r, m, 400, "invalid_request")
	r, m = exch(code, func(f url.Values) { f.Set("grant_type", "password") })
	wantErr("unsupported grant", r, m, 400, "unsupported_grant_type")
	r, m = exch(code, func(f url.Values) { f.Del("grant_type") })
	wantErr("no grant type", r, m, 400, "invalid_request")
	r, m = exch(code, func(f url.Values) { f.Set("client_id", "hvc_unknown") })
	wantErr("unknown client", r, m, 401, "invalid_client")
	r, m = exch(code, func(f url.Values) { f.Del("client_id") })
	wantErr("no client", r, m, 400, "invalid_request")
	r, m = exch(code, func(f url.Values) { f.Set("client_secret", "s3cret") })
	wantErr("a client secret", r, m, 401, "invalid_client")
	r, m = exch(code, func(f url.Values) { f.Set("client_id", other) })
	wantErr("another app's code", r, m, 400, "invalid_grant")
	r, m = exch(code, func(f url.Values) { f.Set("redirect_uri", "https://claude.ai/api/mcp/other") })
	wantErr("redirect mismatch", r, m, 400, "invalid_grant")
	r, m = exch(code, func(f url.Values) { f.Set("resource", "https://other.example") })
	wantErr("foreign resource", r, m, 400, "invalid_target")
	r, m = exch("hvk_notacode", nil)
	wantErr("unknown code", r, m, 400, "invalid_grant")
	// The verifier: wrong, too short, too long, bad characters. A wrong one ends the grant (the code may have leaked).
	for name, v := range map[string]string{"wrong": strings.Repeat("a", 50), "short": "abc", "long": strings.Repeat("a", 129), "bad characters": strings.Repeat("a", 43) + " !"} {
		code = freshCode()
		r, m = exch(code, func(f url.Values) { f.Set("code_verifier", v) })
		wantErr("verifier "+name, r, m, 400, "invalid_grant")
		if r, m = exch(code, nil); r.status != 400 {
			t.Errorf("the right verifier after a wrong one (%s): %d", name, r.status)
		}
	}
	// The same app and a good request still work: the refusals above left no state behind that blocks it.
	code = freshCode()
	if r, m = exch(code, nil); r.status != 200 || m["access_token"] == nil {
		t.Fatalf("a good exchange: %d %s", r.status, r.body)
	}
	// A replayed code ends the grant: the tokens it made stop working.
	access := m["access_token"].(string)
	refresh := m["refresh_token"].(string)
	if r := e.mcp(access, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); r.status != 200 {
		t.Fatalf("before the replay: %d", r.status)
	}
	r, m = exch(code, nil)
	wantErr("replayed code", r, m, 400, "invalid_grant")
	if r := e.mcp(access, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); r.status != 401 {
		t.Errorf("the access token after a replayed code: %d", r.status)
	}
	if r, _ = e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {cid}}); r.status != 400 {
		t.Errorf("the refresh token after a replayed code: %d", r.status)
	}
	if !e.audited("app.revoke", "human:afif") {
		t.Error("a replay must be audited")
	}
	// An expired code.
	code = freshCode()
	e.clock.advance(2*time.Minute + time.Second)
	r, m = exch(code, nil)
	wantErr("expired code", r, m, 400, "invalid_grant")
	// A code made to the minute is good until the 2 minutes are up.
	code = freshCode()
	e.clock.advance(119 * time.Second)
	if r, _ = exch(code, nil); r.status != 200 {
		t.Errorf("a code after 119 seconds: %d %s", r.status, r.body)
	}
}

func TestRefreshTokensRotateAndReuseEndsTheGrant(t *testing.T) {
	e := newConnectorEnv(t)
	c, _ := e.owner()
	app := e.connect(c, claudeRedirect)
	other, _ := e.register(claudeRedirect)
	refresh := func(rt, cid string) (webResp, map[string]any) {
		return e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {cid}})
	}
	if r, m := refresh(app.refresh, other); r.status != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("another app's refresh token: %d %s", r.status, r.body)
	}
	if r, m := refresh("hvf_unknown", app.clientID); r.status != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("unknown refresh token: %d %s", r.status, r.body)
	}
	if r, m := e.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {app.clientID}}); r.status != 400 || m["error"] != "invalid_request" {
		t.Fatalf("no refresh token: %d %s", r.status, r.body)
	}
	if r, m := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {app.refresh}, "client_id": {app.clientID}, "scope": {"admin"}}); r.status != 400 || m["error"] != "invalid_scope" {
		t.Fatalf("a bigger scope: %d %s", r.status, r.body)
	}
	r1, m1 := refresh(app.refresh, app.clientID)
	if r1.status != 200 {
		t.Fatalf("first refresh: %d %s", r1.status, r1.body)
	}
	r2, m2 := refresh(m1["refresh_token"].(string), app.clientID)
	if r2.status != 200 || m2["refresh_token"] == m1["refresh_token"] {
		t.Fatalf("second refresh: %d %s", r2.status, r2.body)
	}
	// The first one again: someone has a copy. The newest pair dies with it.
	if r, m := refresh(app.refresh, app.clientID); r.status != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("reuse: %d %s", r.status, r.body)
	}
	if r := e.mcp(m2["access_token"].(string), `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); r.status != 401 {
		t.Errorf("the newest access token after a reuse: %d", r.status)
	}
	if r, _ := refresh(m2["refresh_token"].(string), app.clientID); r.status != 400 {
		t.Errorf("the newest refresh token after a reuse: %d", r.status)
	}
}

func TestTokenAndRegistrationAnswersAreNotCached(t *testing.T) {
	e := newConnectorEnv(t)
	for name, r := range map[string]webResp{
		"register ok":    e.jsonReq("POST", "/oauth/register", `{"redirect_uris":["http://localhost:80/cb"]}`, nil),
		"register bad":   e.jsonReq("POST", "/oauth/register", `{}`, nil),
		"token bad":      func() webResp { r, _ := e.token(url.Values{"grant_type": {"authorization_code"}}); return r }(),
		"metadata":       e.jsonReq("GET", "/.well-known/oauth-authorization-server", "", nil),
		"mcp 401":        e.mcp("", initBody, nil),
		"token 429/else": func() webResp { r, _ := e.token(url.Values{}); return r }(),
	} {
		if r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", name, r.header.Get("Cache-Control"))
		}
	}
}

func TestTokenEndpointIsRateLimited(t *testing.T) {
	e := newConnectorEnv(t)
	n429 := 0
	for i := 0; i < 70; i++ {
		if r, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"hvf_x"}, "client_id": {"hvc_x"}}); r.status == 429 {
			n429++
		}
	}
	if n429 == 0 {
		t.Fatal("70 token requests in an instant were not limited")
	}
}

// ---- the Connect page ----

func TestTheConnectPageOffersTheConnectorFirst(t *testing.T) {
	e := newConnectorEnv(t)
	c, tok := e.owner()
	page := e.req("GET", "/connect", nil, c, nil)
	for _, want := range []string{"Connect Claude (nothing to install)", testOrigin + "/mcp", "Add custom connector", "claude mcp add --transport http handloom " + testOrigin + "/mcp",
		"Connected apps", "No app is connected through the connector yet", "Advanced: run a program on your computer", "command -v handloom", "does not use your shell", "add-a-machine.md",
		"claude mcp add handloom --env", "mcp_servers.handloom"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Index(page.body, "Connect Claude (nothing to install)") > strings.Index(page.body, "Advanced: run a program") {
		t.Error("the connector must come before the advanced block")
	}
	if !strings.Contains(page.body, `<details class="card options">`) { // collapsed unless a token was just made
		t.Errorf("the advanced block is not collapsed by default")
	}
	// The app is listed with its name escaped; a person cannot disconnect someone else's app.
	app := e.connect(c, claudeRedirect)
	_ = app
	e.hub.db.Exec(`UPDATE oauth_client SET name = '<i>Claude</i>'`)
	page = e.req("GET", "/connect", nil, c, nil)
	if !strings.Contains(page.body, "&lt;i&gt;Claude&lt;/i&gt;") || strings.Contains(page.body, "<i>Claude</i>") || !strings.Contains(page.body, "last used") {
		t.Errorf("the connected app: %s", page.body)
	}
	mc, mtok := e.member("mia", "member")
	if r := e.req("POST", "/connect/apps/1/disconnect", url.Values{"csrf": {mtok}}, mc, nil); r.status != 404 {
		t.Errorf("disconnecting another person's app: %d", r.status)
	}
	if r := e.req("POST", "/connect/apps/1/disconnect", url.Values{}, c, nil); r.status != 403 {
		t.Errorf("disconnect without CSRF: %d", r.status)
	}
	if r := e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); r.status != 200 {
		t.Errorf("the refused disconnects ended the app: %d", r.status)
	}
	if r := e.req("POST", "/connect/apps/1/disconnect", url.Values{"csrf": {tok}}, c, nil); r.status != 303 {
		t.Errorf("disconnect: %d", r.status)
	}
	if r := e.req("POST", "/connect/apps/1/disconnect", url.Values{"csrf": {tok}}, c, nil); r.status != 404 {
		t.Errorf("disconnect twice: %d", r.status)
	}
	// A member sees only their own connections.
	e.connect(mc, claudeRedirect)
	if mp := e.req("GET", "/connect", nil, mc, nil); strings.Count(mp.body, "/disconnect") != 1 {
		t.Errorf("a member's list: %d entries", strings.Count(mp.body, "/disconnect"))
	}
}

func TestTheConnectorNeedsAnAddress(t *testing.T) {
	// BaseURL is set in every web test; build a hub whose web UI works by HANDLOOM_INSECURE and no address.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := New(db, Options{Insecure: true})
	if h.connectorBase() != "" {
		t.Fatal("the connector must be off without an address")
	}
}

func TestThePageStillMakesAStaticTokenForTheProgram(t *testing.T) {
	e := newConnectorEnv(t)
	c, tok := e.owner()
	r := e.req("POST", "/connect/token", url.Values{"csrf": {tok}, "current_password": {goodPassword}}, c, nil)
	if r.status != 200 || !strings.Contains(r.body, "HANDLOOM_TOKEN=hvo_") || !strings.Contains(r.body, "<details class=\"card options\" open>") {
		t.Fatalf("the static token page: %d", r.status)
	}
}

func TestAnAccessTokenIsNeverInALog(t *testing.T) {
	var logged strings.Builder
	e := newWebEnv(t, Options{OperatorRun: fakeRun, Log: logWriter(&logged)})
	c, _ := e.owner()
	app := e.connect(c, claudeRedirect)
	e.mcp(app.access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_digest","arguments":{}}}`, nil)
	e.status(app.access, "POST", "/v1/tasks/1/accept", nil)
	for _, secret := range []string{app.access, app.refresh, app.code} {
		if strings.Contains(logged.String(), secret) || strings.Contains(e.auditText(), secret) {
			t.Fatalf("a secret is in a log: %.20s", secret)
		}
	}
	rows, _ := e.hub.db.Query(`SELECT hash FROM oauth_token`)
	defer rows.Close()
	for rows.Next() {
		var h string
		rows.Scan(&h)
		if strings.HasPrefix(h, "hv") || len(h) != 64 {
			t.Errorf("a token is stored as %q, not as a hash", h)
		}
	}
}

func regexpAll(pattern, s string) [][]string {
	return regexp.MustCompile(pattern).FindAllStringSubmatch(s, -1)
}

func htmlUnescape(s string) string { return html.UnescapeString(s) }

func htmlEscapeAttr(s string) string { return html.EscapeString(s) }

func logWriter(w io.Writer) *log.Logger { return log.New(w, "", 0) }

// TestTheAIAppAllowlistIsPinned fixes what an AI app may do. The static hvo_ token (D80) and the access
// token of a connected app (D109) are held to this one list, so a change to it is a change to what Claude
// can do on a person's behalf and has to be made here on purpose, with the person's trust in mind.
func TestTheAIAppAllowlistIsPinned(t *testing.T) {
	want := []string{
		"GET /v1/agents", "GET /v1/agents/{name}/screen", "GET /v1/codebases", "GET /v1/codebases/{name}", "GET /v1/devices", "GET /v1/digest",
		"GET /v1/escalations", "GET /v1/escalations/{id}", "GET /v1/jobs", "GET /v1/jobs/{id}", "GET /v1/jobs/{id}/publishes", "GET /v1/lessons",
		"GET /v1/metrics", "GET /v1/profiles", "GET /v1/profiles/{name}", "GET /v1/profiles/{name}/versions", "GET /v1/projects", "GET /v1/search",
		"GET /v1/spawns", "GET /v1/spawns/{id}", "GET /v1/starters", "GET /v1/starters/{name}", "GET /v1/tasks", "GET /v1/tasks/{id}", "GET /v1/whoami",
		"POST /v1/jobs", "POST /v1/messages", // the only two writes: start a job, message an agent
	}
	var got []string
	for k := range operatorCalls {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the AI app allowlist changed:\n got %v\nwant %v", got, want)
	}
	// Every entry is a real route (a typo would silently allow nothing), and every route not listed is refused for an app token.
	h := newWebEnv(t, Options{})
	c, _ := h.owner()
	app := h.connect(c, claudeRedirect).access
	for _, p := range []string{"/v1/people", "/v1/notifications", "/v1/prices", "/v1/audit", "/v1/admin/devices", "/v1/device/spawns", "/v1/inbox"} {
		if code := h.status(app, "GET", p, nil); code != 403 {
			t.Errorf("GET %s with a connected app: %d, want 403", p, code)
		}
	}
}
