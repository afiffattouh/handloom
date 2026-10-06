package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"handloom/internal/api"
)

// withToken sends a request as an agent with a run token.
func (e *env) withToken(c caller, token, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(api.AgentHeader, c.agent)
	if token != "" {
		req.Header.Set(api.RunTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (e *env) rotate(c caller) string {
	e.t.Helper()
	var a api.Agent
	e.ok(c, "POST", "/v1/agents", api.RegisterReq{Name: c.agent, Kind: "shell", RotateToken: true}, &a)
	if !strings.HasPrefix(a.RunToken, "hvr_") {
		e.t.Fatalf("no run token in %+v", a)
	}
	return a.RunToken
}

func (e *env) via(action string) string {
	var via string
	e.hub.db.QueryRow(`SELECT via FROM audit WHERE action = ? ORDER BY seq DESC LIMIT 1`, action).Scan(&via)
	return via
}

func TestRunTokenIsMintedOnceAndStoredHashed(t *testing.T) {
	e := newEnv(t)
	tok := e.rotate(e.worker())
	var stored string
	e.hub.db.QueryRow(`SELECT run_token_hash FROM agent WHERE name = 'worker'`).Scan(&stored)
	if stored == "" || strings.Contains(stored, tok) || stored == tok {
		t.Fatalf("stored value %q", stored)
	}
	// Ordinary reads of agents never show it.
	_, raw := e.do(e.afif(), "GET", "/v1/agents", nil)
	if strings.Contains(string(raw), "run_token") || strings.Contains(string(raw), tok) {
		t.Fatalf("the token leaks through the agent list: %s", raw)
	}
	// Re-registering without asking to rotate returns none and keeps the token.
	var a api.Agent
	e.ok(e.worker(), "POST", "/v1/agents", api.RegisterReq{Name: "worker", Kind: "shell"}, &a)
	if a.RunToken != "" {
		t.Fatal("a plain re-register returned a token")
	}
	if code, _ := e.withToken(e.worker(), tok, "GET", "/v1/whoami", nil); code != 200 {
		t.Fatalf("the token stopped working: %d", code)
	}
	if !e.audited("agent.token", "agent:worker") {
		t.Fatal("minting is not audited")
	}
}

func TestRunTokenAuthenticatesAndIsAudited(t *testing.T) {
	e := newEnv(t)
	tok := e.rotate(e.worker())

	code, raw := e.withToken(e.worker(), tok, "GET", "/v1/whoami", nil)
	var who api.WhoAmI
	json.Unmarshal(raw, &who)
	if code != 200 || who.Via != "run-token" {
		t.Fatalf("with token: %d %s", code, raw)
	}
	code, raw = e.withToken(e.worker(), "", "GET", "/v1/whoami", nil)
	json.Unmarshal(raw, &who)
	if code != 200 || who.Via != "device-asserted" {
		t.Fatalf("without token (the old way): %d %s", code, raw)
	}
	// The audit log says which way each action came in.
	e.withToken(e.worker(), tok, "POST", "/v1/tasks", api.TaskCreateReq{Title: "x"}) // forbidden for a worker, audited as denied
	if got := e.via("denied"); got != "run-token" {
		t.Fatalf("denied via %q", got)
	}
	e.ok(e.worker(), "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "hi"}, nil)
	if got := e.via("message.send"); got != "device-asserted" {
		t.Fatalf("message.send via %q", got)
	}
}

func TestRunTokenCannotBeBorrowed(t *testing.T) {
	e := newEnv(t)
	tok := e.rotate(e.worker())
	other := e.rotate(e.lead())

	// Another agent's token, or none that matches, is refused outright:
	// there is no fallback to the device-asserted path.
	if code, _ := e.withToken(e.worker(), other, "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("worker with lead's token: %d", code)
	}
	if code, _ := e.withToken(e.lead(), tok, "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("lead with worker's token: %d", code)
	}
	if code, _ := e.withToken(e.worker(), "hvr_nonsense", "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("made-up token: %d", code)
	}
	if code, _ := e.withToken(e.worker(), "not-a-run-token", "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("wrong prefix: %d", code)
	}
	// Replayed from another device: the token is for worker on d1; d2's credential is wrong.
	if code, _ := e.withToken(caller{e.d2, "worker"}, tok, "GET", "/v1/whoami", nil); code == 200 {
		t.Fatalf("a token worked from another device: %d", code)
	}
	// Hooks use the path-based endpoints; the token is checked there too.
	if code, _ := e.withToken(e.worker(), other, "POST", "/v1/agents/worker/state", api.StateReq{State: "idle"}); code != 401 {
		t.Fatalf("state with someone else's token: %d", code)
	}
	if code, _ := e.withToken(e.worker(), tok, "POST", "/v1/agents/worker/state", api.StateReq{State: "idle"}); code != 200 {
		t.Fatalf("state with its own token: %d", code)
	}
}

func TestRotationReplacesTheOldToken(t *testing.T) {
	e := newEnv(t)
	first := e.rotate(e.worker())
	second := e.rotate(e.worker())
	if first == second {
		t.Fatal("rotation returned the same token")
	}
	if code, _ := e.withToken(e.worker(), first, "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("the old token still works: %d", code)
	}
	if code, _ := e.withToken(e.worker(), second, "GET", "/v1/whoami", nil); code != 200 {
		t.Fatalf("the new token: %d", code)
	}
}

func TestRevokedDeviceKillsItsTokens(t *testing.T) {
	e := newEnv(t)
	tok := e.rotate(e.worker2())
	if code, _ := e.withToken(e.worker2(), tok, "GET", "/v1/whoami", nil); code != 200 {
		t.Fatalf("before revoke: %d", code)
	}
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/devices/d2/revoke", nil, nil)
	if code, _ := e.withToken(e.worker2(), tok, "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("after revoke: %d", code)
	}
}

func TestHubCanRequireRunTokens(t *testing.T) {
	e := newEnv(t)
	tok := e.rotate(e.worker())
	e.rotate(e.lead())
	e.hub.opt.RequireRunToken = true

	// The old way is refused for agents...
	e.fail(401, e.worker(), "GET", "/v1/whoami", nil)
	e.fail(401, e.worker(), "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "hi"})
	e.fail(401, e.worker(), "POST", "/v1/agents", api.RegisterReq{Name: "worker", Kind: "shell"})
	if code, _ := e.withToken(e.worker(), tok, "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "hi"}); code != 200 {
		t.Fatalf("with token: %d", code)
	}
	// ...but not for the link's own calls, for humans, or for minting a first token.
	e.ok(e.afif(), "GET", "/v1/agents", nil, nil)
	e.ok(e.worker(), "GET", "/v1/device/agents", nil, nil)
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "fresh", Kind: "shell", RotateToken: true}, nil)
}
