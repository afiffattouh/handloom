package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// post sends one message to the HTTP half of the server, as the hub would after it checked the caller.
func post(t *testing.T, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", HTTPPath, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	run := func(argv []string) (string, string, int) { return strings.Join(argv, " ") + "\n", "", 0 }
	OperatorServer("test").HandleHTTP(rec, req, run)
	return rec
}

func TestTheHTTPPathAndVersionsArePinned(t *testing.T) {
	if HTTPPath != "/mcp" {
		t.Fatalf("path %q", HTTPPath)
	}
	if strings.Join(SupportedProtocols, " ") != "2026-07-28 2025-11-25 2025-06-18 2025-03-26" {
		t.Fatalf("versions %v", SupportedProtocols)
	}
}

func TestHTTPAnswersWithOneJSONBody(t *testing.T) {
	rec := post(t, `{"jsonrpc":"2.0","id":"abc","method":"tools/call","params":{"name":"handloom_job","arguments":{"id":3}}}`, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var resp struct {
		ID     string
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ID != "abc" || resp.Result.IsError || strings.TrimSpace(resp.Result.Content[0].Text) != "job show 3" {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestHTTPNotificationsAndClientAnswersGet202(t *testing.T) {
	for _, body := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":5,"result":{}}`, `{"jsonrpc":"2.0","id":5,"error":{"code":-1,"message":"x"}}`} {
		rec := post(t, body, nil)
		if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
			t.Errorf("%s: %d %q", body, rec.Code, rec.Body.String())
		}
	}
}

func TestHTTPVersionNegotiation(t *testing.T) {
	for ask, want := range map[string]string{"2025-03-26": "2025-03-26", "2025-06-18": "2025-06-18", "2025-11-25": "2025-11-25", "2026-07-28": "2026-07-28", "2024-11-05": "2026-07-28", "": "2026-07-28"} {
		rec := post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+ask+`"}}`, nil)
		var r struct {
			Result struct{ ProtocolVersion string }
		}
		json.Unmarshal(rec.Body.Bytes(), &r)
		if r.Result.ProtocolVersion != want {
			t.Errorf("asked %q: %q, want %q", ask, r.Result.ProtocolVersion, want)
		}
	}
	// The stdio server keeps echoing whatever the client asks (the behaviour before the HTTP half existed).
	srv := &Server{Version: "t"}
	if got := srv.negotiate("1999-01-01"); got != "1999-01-01" {
		t.Errorf("stdio echo: %q", got)
	}
	if got := srv.negotiate(""); got != defaultProtocol {
		t.Errorf("stdio default: %q", got)
	}
}

func TestHTTPHeadersAreCheckedWhenPresentAndNeverRequired(t *testing.T) {
	if rec := post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); rec.Code != 200 {
		t.Errorf("no headers: %d", rec.Code)
	}
	if rec := post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/list", "Mcp-Name": "x"}); rec.Code != 200 {
		t.Errorf("good headers: %d", rec.Code)
	}
	rec := post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"Mcp-Method": "tools/call"})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("mismatch: %d %s", rec.Code, rec.Body.String())
	}
	rec = post(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "2020-01-01"})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "2025-06-18") {
		t.Errorf("unknown version: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPRefusesWhatItCannotRead(t *testing.T) {
	for _, body := range []string{``, `nope`, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, `{"jsonrpc":"2.0","id":1}`, strings.Repeat("x", maxHTTPBody+10)} {
		rec := post(t, body, nil)
		if rec.Code != 400 {
			t.Errorf("%.30q: %d", body, rec.Code)
		}
		var r struct{ Error struct{ Code int } }
		if json.Unmarshal(rec.Body.Bytes(), &r) != nil || r.Error.Code == 0 {
			t.Errorf("%.30q: not a JSON-RPC error: %s", body, rec.Body.String())
		}
	}
}

func TestOnlyTheHTTPServerAnswersProbes(t *testing.T) {
	rec := post(t, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, nil)
	if !strings.Contains(rec.Body.String(), `"resources":[]`) {
		t.Errorf("resources/list: %s", rec.Body.String())
	}
	rec = post(t, `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`, nil)
	if !strings.Contains(rec.Body.String(), `"prompts":[]`) {
		t.Errorf("prompts/list: %s", rec.Body.String())
	}
	// stdio is unchanged: those are still unknown methods there.
	resps := operatorTalk(t, false, func([]string) (string, string, int) { return "", "", 0 }, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	if resps[0]["error"] == nil {
		t.Errorf("stdio resources/list: %v", resps[0])
	}
}

func TestTheHTTPToolsAreTheOperatorToolsWithoutApprovals(t *testing.T) {
	srv := OperatorServer("t")
	want := OperatorTools(false)
	if len(srv.Tools) != len(want) {
		t.Fatalf("%d tools, want %d", len(srv.Tools), len(want))
	}
	for i := range want {
		if srv.Tools[i].Name != want[i].Name || srv.Tools[i].Description != want[i].Description {
			t.Errorf("tool %d differs: %s", i, srv.Tools[i].Name)
		}
		if strings.Contains(srv.Tools[i].Description, "Approval:") {
			t.Errorf("%s is an approval tool", srv.Tools[i].Name)
		}
	}
}
