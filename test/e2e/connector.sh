#!/usr/bin/env bash
# The remote connector (D109), with NO model and NO real Claude: a real hub process on 127.0.0.1 and the whole
# OAuth + MCP flow driven the way Claude drives it (discovery, registration, sign-in, consent with PKCE, token,
# /mcp, refresh with rotation, disconnect). The hub's public address is an http loopback one, which the hub allows
# for tests. Needs: go (to build), python3, curl. HOME is not touched.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
PORT="${PORT:-17951}"
OUT="$ROOT/test/e2e/out/connector"
H="http://127.0.0.1:$PORT"
fails=0
say() { printf '\n== %s\n' "$*"; }
ok()  { printf '   ok: %s\n' "$*"; }
bad() { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }

rm -rf "$OUT"; mkdir -p "$OUT/data" "$OUT/bin"
BIN="$OUT/bin/handloom"
(cd "$ROOT" && PATH="$PATH:$(dirname "$GO")" "$GO" build -o "$BIN" ./cmd/handloom) || { echo "build failed"; exit 1; }
CLEAN="env -u TMUX -u TMUX_PANE -u HANDLOOM_TOKEN -u HANDLOOM_HUB"
finish() { kill ${HUB_PID:-} 2>/dev/null; }
trap finish EXIT

say "a hub whose public address is $H"
HANDLOOM_BASE_URL="$H" HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "$H/healthz" >/dev/null && ok "the hub is up" || { bad "the hub did not start"; tail -5 "$OUT/hub.log"; exit 1; }
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
$CLEAN "$BIN" hub create-owner olive --data "$OUT/data" > "$OUT/owner.txt" 2>&1 || { bad "create-owner: $(cat "$OUT/owner.txt")"; exit 1; }
PW="$(sed -n '2p' "$OUT/owner.txt")"
[ -n "$PW" ] && ok "an owner exists, with a password to sign in" || { bad "no password"; exit 1; }

say "what a client sees before it has a token (curl)"
HDRS="$(curl -s -D - -o "$OUT/401.body" -X POST -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' "$H/mcp")"
echo "$HDRS" | head -1 | grep -q " 401" && ok "POST /mcp without a token is 401" || bad "no 401: $(echo "$HDRS" | head -1)"
echo "$HDRS" | grep -qi "^www-authenticate: Bearer resource_metadata=\"$H/.well-known/oauth-protected-resource\"" && ok "it points at the protected-resource metadata" || bad "WWW-Authenticate: $(echo "$HDRS" | grep -i www-auth)"
curl -s "$H/.well-known/oauth-protected-resource" | python3 -c "
import json,sys; d=json.load(sys.stdin); b='$H'
assert d['resource']==b+'/mcp' and d['authorization_servers']==[b] and d['scopes_supported']==['handloom'] and d['bearer_methods_supported']==['header'], d" && ok "protected resource metadata (RFC 9728)" || bad "resource metadata"
curl -s "$H/.well-known/oauth-protected-resource/mcp" | grep -q '"resource"' && ok "the /mcp spelling of it works" || bad "resource metadata /mcp spelling"
curl -s "$H/.well-known/oauth-authorization-server" | python3 -c "
import json,sys; d=json.load(sys.stdin); b='$H'
assert d['issuer']==b and d['authorization_endpoint']==b+'/oauth/authorize' and d['token_endpoint']==b+'/oauth/token' and d['registration_endpoint']==b+'/oauth/register'
assert d['code_challenge_methods_supported']==['S256'] and d['token_endpoint_auth_methods_supported']==['none'] and d['authorization_response_iss_parameter_supported'] is True
assert 'client_id_metadata_document_supported' not in d, d" && ok "authorization server metadata (RFC 8414)" || bad "server metadata"
curl -s "$H/.well-known/openid-configuration" | grep -q '"issuer"' && ok "the OpenID spelling of it works" || bad "openid spelling"
[ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ADMIN" -X POST -d '{}' "$H/mcp")" = 401 ] && ok "the admin token is not welcome on /mcp" || bad "admin token on /mcp"

say "the whole flow, as Claude does it (python)"
H="$H" PW="$PW" ADMIN="$ADMIN" python3 - <<'PY' | tee "$OUT/flow.txt"
import base64, hashlib, http.client, json, os, re, secrets, sys, urllib.parse

H = os.environ["H"]; PW = os.environ["PW"]; ADMIN = os.environ["ADMIN"]
host, port = urllib.parse.urlparse(H).netloc.split(":")
fails = 0
def ok(m): print("   ok: " + m)
def bad(m):
    global fails; fails += 1; print("   FAIL: " + m)
def check(cond, good, fail):
    ok(good) if cond else bad(fail)

def call(method, path, body=None, headers=None):
    c = http.client.HTTPConnection(host, int(port), timeout=20)
    c.request(method, path, body=body, headers=headers or {})
    r = c.getresponse(); data = r.read().decode()
    h = {k.lower(): v for k, v in r.getheaders()}
    c.close()
    return r.status, h, data

def jcall(method, path, obj=None, headers=None, tok=None):
    hd = {"Content-Type": "application/json"}; hd.update(headers or {})
    if tok: hd["Authorization"] = "Bearer " + tok
    st, h, d = call(method, path, json.dumps(obj) if obj is not None else None, hd)
    try: return st, h, json.loads(d) if d else None
    except ValueError: return st, h, d

def form(path, fields, headers=None):
    hd = {"Content-Type": "application/x-www-form-urlencoded"}; hd.update(headers or {})
    return call("POST", path, urllib.parse.urlencode(fields), hd)

REDIRECT = "https://claude.ai/api/mcp/auth_callback"

# 1. register (RFC 7591)
st, h, reg = jcall("POST", "/oauth/register", {"client_name": "Claude", "redirect_uris": [REDIRECT], "token_endpoint_auth_method": "none",
                                               "grant_types": ["authorization_code", "refresh_token"], "response_types": ["code"]})
check(st == 201 and reg["client_id"].startswith("hvc_") and h.get("cache-control") == "no-store", "registered as a public client", "register: %s %s" % (st, reg))
cid = reg["client_id"]
st, _, bad_reg = jcall("POST", "/oauth/register", {"redirect_uris": ["https://evil.example/cb"]})
check(st == 400 and bad_reg["error"] == "invalid_redirect_uri", "an address that is not Claude's is refused", "bad redirect accepted: %s %s" % (st, bad_reg))

# 2. authorize without a session: sent to sign in, and back
verifier = secrets.token_urlsafe(48)
challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
state = secrets.token_urlsafe(12)
q = urllib.parse.urlencode({"response_type": "code", "client_id": cid, "redirect_uri": REDIRECT, "code_challenge": challenge,
                            "code_challenge_method": "S256", "state": state, "resource": H + "/mcp", "scope": "handloom"})
auth_path = "/oauth/authorize?" + q
st, h, _ = call("GET", auth_path)
loc = h.get("location", "")
check(st == 303 and loc.startswith("/login?next="), "no session: sent to the sign-in page, with the way back", "authorize without session: %s %s" % (st, loc))

# 3. sign in (the browser's POST carries the hub's own Origin)
st, h, _ = call("POST", "/login", urllib.parse.urlencode({"name": "olive", "password": PW, "next": auth_path}),
                {"Content-Type": "application/x-www-form-urlencoded", "Origin": H})
cookie = (h.get("set-cookie") or "").split(";")[0]
check(st == 303 and h.get("location") == auth_path and cookie.startswith("handloom_session="), "signed in, and returned to the authorize request", "login: %s %s" % (st, h.get("location")))

# 4. the consent page
st, h, page = call("GET", auth_path, headers={"Cookie": cookie})
check(st == 200 and "wants to connect to Handloom as" in page and "It will NOT be able to" in page, "the consent page names the app and the person", "consent page: %s" % st)
check("form-action 'self' https://claude.ai" in h.get("content-security-policy", ""), "its CSP lets the form end in a redirect to Claude", "csp: %s" % h.get("content-security-policy"))
fields = dict(re.findall(r'<input type="hidden" name="([a-z_]+)" value="([^"]*)">', page))
check(fields.get("client_id") == cid and "csrf" in fields, "it carries the request and a security token", "fields: %s" % list(fields))

# 5. a wrong password is not enough; Cancel needs none
bad_fields = dict(fields, decision="allow", current_password="not the password")
st, h, _ = call("POST", "/oauth/authorize", urllib.parse.urlencode(bad_fields), {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
check(st == 403 and "location" not in h, "a wrong password gives no code", "wrong password: %s" % st)
st, h, _ = call("POST", "/oauth/authorize", urllib.parse.urlencode(dict(fields, decision="deny")), {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
dq = urllib.parse.parse_qs(urllib.parse.urlparse(h.get("location", "")).query)
check(st == 302 and dq.get("error") == ["access_denied"] and dq.get("state") == [state], "Cancel sends access_denied with the state", "cancel: %s %s" % (st, h.get("location")))

# 6. Allow
st, h, _ = call("POST", "/oauth/authorize", urllib.parse.urlencode(dict(fields, decision="allow", current_password=PW)),
                {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
loc = urllib.parse.urlparse(h.get("location", ""))
rq = urllib.parse.parse_qs(loc.query)
check(st == 302 and (loc.scheme + "://" + loc.netloc + loc.path) == REDIRECT and rq.get("state") == [state] and rq.get("iss") == [H] and rq.get("code"),
      "redirected to Claude's address with code, state and iss", "allow: %s %s" % (st, h.get("location")))
code = rq["code"][0]

# 7. token: a wrong verifier fails (and ends the grant), a right one on a new code works
st, h, d = form("/oauth/token", {"grant_type": "authorization_code", "code": code, "code_verifier": secrets.token_urlsafe(48), "redirect_uri": REDIRECT, "client_id": cid})
check(st == 400 and json.loads(d)["error"] == "invalid_grant" and h.get("cache-control") == "no-store", "a wrong code_verifier is refused", "wrong verifier: %s %s" % (st, d))
st, h, _ = call("GET", auth_path, headers={"Cookie": cookie})
st, h, _ = call("POST", "/oauth/authorize", urllib.parse.urlencode(dict(fields, decision="allow", current_password=PW)),
                {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
code = urllib.parse.parse_qs(urllib.parse.urlparse(h["location"]).query)["code"][0]
st, h, d = form("/oauth/token", {"grant_type": "authorization_code", "code": code, "code_verifier": verifier, "redirect_uri": REDIRECT, "client_id": cid, "resource": H + "/mcp"})
tok = json.loads(d)
check(st == 200 and tok["token_type"] == "Bearer" and tok["expires_in"] == 3600 and tok["access_token"].startswith("hvt_") and tok["refresh_token"], "the code is traded for tokens", "token: %s %s" % (st, d))
st, _, d2 = form("/oauth/token", {"grant_type": "authorization_code", "code": code, "code_verifier": verifier, "redirect_uri": REDIRECT, "client_id": cid})
check(st == 400, "the same code again is refused", "replay: %s" % st)
st, _, _ = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 1, "method": "ping"}, tok=tok["access_token"])
check(st == 401, "and the replay ended the grant (the tokens stop working)", "after replay: %s" % st)

# 8. a fresh connection, then /mcp
def connect():
    v = secrets.token_urlsafe(48)
    ch = base64.urlsafe_b64encode(hashlib.sha256(v.encode()).digest()).rstrip(b"=").decode()
    p = "/oauth/authorize?" + urllib.parse.urlencode({"response_type": "code", "client_id": cid, "redirect_uri": REDIRECT, "code_challenge": ch, "code_challenge_method": "S256", "state": "x"})
    st, h, pg = call("GET", p, headers={"Cookie": cookie})
    f = dict(re.findall(r'<input type="hidden" name="([a-z_]+)" value="([^"]*)">', pg))
    st, h, _ = call("POST", "/oauth/authorize", urllib.parse.urlencode(dict(f, decision="allow", current_password=PW)),
                    {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
    c = urllib.parse.parse_qs(urllib.parse.urlparse(h["location"]).query)["code"][0]
    st, h, d = form("/oauth/token", {"grant_type": "authorization_code", "code": c, "code_verifier": v, "redirect_uri": REDIRECT, "client_id": cid})
    return json.loads(d)
tok = connect()
at = tok["access_token"]
st, h, init = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}}}, tok=at)
check(st == 200 and init["result"]["protocolVersion"] == "2025-06-18" and init["result"]["serverInfo"]["name"] == "handloom", "initialize", "initialize: %s %s" % (st, init))
st, _, d = call("POST", "/mcp", json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}), {"Content-Type": "application/json", "Authorization": "Bearer " + at})
check(st == 202 and d == "", "the initialized notification is 202 with no body", "notification: %s %r" % (st, d))
st, _, lst = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, tok=at)
names = [t["name"] for t in lst["result"]["tools"]]
check("handloom_digest" in names and "handloom_job_new" in names and "handloom_send" in names and not [n for n in names if n in ("handloom_task_accept", "handloom_answer", "handloom_job_close", "handloom_task_reject")],
      "tools/list: the operator tools, without the approvals (%d tools)" % len(names), "tools: %s" % names)
st, _, res = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "handloom_digest", "arguments": {}}}, tok=at)
text = res["result"]["content"][0]["text"]
check(st == 200 and not res["result"]["isError"] and text.strip(), "tools/call handloom_digest runs the real command line and answers", "digest: %s %s" % (st, res))
st, _, res = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "handloom_job", "arguments": {"id": 999}}}, tok=at)
check(res["result"]["isError"] and "goroutine" not in res["result"]["content"][0]["text"] and at not in json.dumps(res), "a failing tool is an error result in plain words", "failing tool: %s" % json.dumps(res)[:200])
st, _, res = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": {"name": "handloom_task_accept", "arguments": {"id": 1}}}, tok=at)
check(res["result"]["isError"] and "unknown tool" in res["result"]["content"][0]["text"], "an approval tool called by name is refused", "approval by name: %s" % json.dumps(res)[:200])
st, _, e = jcall("GET", "/v1/people", tok=at)
check(st == 403, "the hub itself refuses the app token anything outside the AI app's calls", "/v1/people: %s" % st)
st, _, _ = jcall("POST", "/v1/tasks/1/accept", {}, tok=at)
check(st == 403, "accepting work is refused by the hub for this token", "accept: %s" % st)
st, _, _ = call("GET", "/mcp", None, {"Authorization": "Bearer " + at})
check(st == 405, "GET /mcp is 405 (no event stream)", "GET /mcp: %s" % st)
st, _, _ = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 6, "method": "ping"}, headers={"Origin": "https://evil.example"}, tok=at)
check(st == 403, "a request from another site's page is refused", "origin: %s" % st)
st, _, r = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 7, "method": "tools/list"}, headers={"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/list"}, tok=at)
check(st == 200 and r["result"]["tools"], "the stateless style (no initialize, with headers) works", "stateless: %s" % st)

# 9. refresh rotates; reusing the old one ends the grant
st, h, d = form("/oauth/token", {"grant_type": "refresh_token", "refresh_token": tok["refresh_token"], "client_id": cid})
new = json.loads(d)
check(st == 200 and new["refresh_token"] != tok["refresh_token"] and new["access_token"] != at, "refresh issues a new pair", "refresh: %s %s" % (st, d))
st, _, _ = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 8, "method": "ping"}, tok=new["access_token"])
check(st == 200, "the new access token works", "new access token: %s" % st)
st, _, d = form("/oauth/token", {"grant_type": "refresh_token", "refresh_token": tok["refresh_token"], "client_id": cid})
check(st == 400 and json.loads(d)["error"] == "invalid_grant", "the old refresh token is refused", "reuse: %s" % st)
st, _, _ = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 9, "method": "ping"}, tok=new["access_token"])
check(st == 401, "and reusing it ended the grant: the newest token stops too", "after reuse: %s" % st)

# 10. connect once more, see it on the Connect page, disconnect it
tok = connect(); at = tok["access_token"]
st, _, page = call("GET", "/connect", headers={"Cookie": cookie})
m = re.search(r"/connect/apps/(\d+)/disconnect", page)
check(st == 200 and "Connect Claude (nothing to install)" in page and H + "/mcp" in page and "claude mcp add --transport http handloom " + H + "/mcp" in page and m, "the Connect page shows the address, the steps and the app", "connect page: %s" % st)
csrf = re.search(r'name="csrf-token" content="([^"]+)"', page).group(1)
st, _, _ = call("POST", m.group(0), urllib.parse.urlencode({"csrf": csrf}), {"Content-Type": "application/x-www-form-urlencoded", "Origin": H})
check(st in (303, 401), "disconnect needs the session", "disconnect without a session: %s" % st)
st, _, _ = call("POST", m.group(0), urllib.parse.urlencode({"csrf": "wrong"}), {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
check(st == 403, "disconnect needs the page's security token", "disconnect with a wrong csrf: %s" % st)
st, _, _ = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 10, "method": "ping"}, tok=at)
check(st == 200, "still connected after the refused attempts", "after refused disconnects: %s" % st)
st, _, _ = call("POST", m.group(0), urllib.parse.urlencode({"csrf": csrf}), {"Content-Type": "application/x-www-form-urlencoded", "Origin": H, "Cookie": cookie})
check(st == 303, "Disconnect", "disconnect: %s" % st)
st, h, _ = jcall("POST", "/mcp", {"jsonrpc": "2.0", "id": 11, "method": "ping"}, tok=at)
check(st == 401 and "resource_metadata" in h.get("www-authenticate", ""), "/mcp answers 401 at once for the disconnected app", "after disconnect: %s" % st)
st, _, d = form("/oauth/token", {"grant_type": "refresh_token", "refresh_token": tok["refresh_token"], "client_id": cid})
check(st == 400, "and its refresh token is dead too", "refresh after disconnect: %s" % st)
sys.exit(1 if fails else 0)
PY
[ "${PIPESTATUS[0]}" = 0 ] && ok "the python flow passed" || bad "the python flow failed (see above)"

say "the log and the audit trail never hold a token"
if grep -Eq 'hv[tfk]_[A-Za-z0-9_-]{20,}' "$OUT/hub.log"; then bad "a token is in the hub log"; else ok "no token in the hub log"; fi
if curl -s -H "Authorization: Bearer $ADMIN" "$H/v1/audit?limit=500" | grep -Eq 'hv[tfk]_[A-Za-z0-9_-]{20,}'; then bad "a token is in the audit log"; else ok "no token in the audit log"; fi
curl -s -H "Authorization: Bearer $ADMIN" "$H/v1/audit?limit=500" | grep -q 'app.connect' && ok "app.connect is audited" || bad "no app.connect in the audit log"
curl -s -H "Authorization: Bearer $ADMIN" "$H/v1/audit?limit=500" | grep -q 'app.revoke' && ok "app.revoke is audited" || bad "no app.revoke in the audit log"

[ "$fails" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL ($fails)"; exit 1; }
