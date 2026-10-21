package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"handloom/internal/api"
	"handloom/internal/client"
)

// fakeGateway stands in for the link's tool gateway on the socket where the link would be.
type fakeGateway struct {
	mu       sync.Mutex
	calls    []api.ToolCallReq
	token    string // the token the requests carried
	reply    api.ToolCallResp
	list     api.ToolList
	promotes []api.ToolPromoteReq
}

func startGateway(t *testing.T) *fakeGateway {
	t.Helper()
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	g := &fakeGateway{reply: api.ToolCallResp{OK: true, Code: "ok", Text: "hello"}}
	g.list = api.ToolList{Tools: []api.SpawnTool{{Addon: "echo", Tool: "wait", Summary: "Do nothing for a while.", MaxCalls: 5,
		Input: []api.ToolField{{Name: "seconds", Type: "number", Required: true, Summary: "How long"}}},
		{Addon: "echo", Tool: "say", Summary: "Return the text.", MaxCalls: 50,
			Input: []api.ToolField{{Name: "text", Type: "string", Required: true, Summary: "The text"}, {Name: "loud", Type: "boolean"}}}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /local/tools", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.token = r.Header.Get(api.ToolTokenHeader)
		g.mu.Unlock()
		json.NewEncoder(w).Encode(g.list)
	})
	mux.HandleFunc("POST /local/tools/call", func(w http.ResponseWriter, r *http.Request) {
		var req api.ToolCallReq
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		g.calls = append(g.calls, req)
		g.token = r.Header.Get(api.ToolTokenHeader)
		reply := g.reply
		g.mu.Unlock()
		json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("POST /local/tools/promote", func(w http.ResponseWriter, r *http.Request) {
		var req api.ToolPromoteReq
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		g.promotes = append(g.promotes, req)
		g.token = r.Header.Get(api.ToolTokenHeader)
		g.mu.Unlock()
		if strings.HasPrefix(req.To, "..") {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(api.Error{Error: "the destination is not inside the project", Code: "bad_promote"})
			return
		}
		json.NewEncoder(w).Encode(api.ToolPromoteResp{To: req.To, Message: "Copied into the project as " + req.To + "."})
	})
	ln, err := net.Listen("unix", client.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return g
}

func toolRun(args ...string) (int, string, string) {
	var out, errb strings.Builder
	code := Main(append([]string{"tool"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestToolCallPrintsTheAnswerAndSendsTheToken(t *testing.T) {
	g := startGateway(t)
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")
	g.reply = api.ToolCallResp{OK: true, Code: "ok", Text: "hello\n", Files: []string{"/work/.handloom/results/echo/c1/note.txt"}}

	code, out, errs := toolRun("call", "echo.say", "--arg", "text=hello world", "--arg", "loud=yes")
	if code != 0 || !strings.HasPrefix(out, "hello\n") || !strings.Contains(out, "Saved file: /work/.handloom/results/echo/c1/note.txt") || errs != "" {
		t.Fatalf("call: %d %q %q", code, out, errs)
	}
	if g.token != "hlt_abc" {
		t.Fatalf("the token that reached the link: %q", g.token)
	}
	last := g.calls[len(g.calls)-1]
	if last.Tool != "echo.say" || last.Args["text"] != "hello world" || last.Args["loud"] != true {
		t.Fatalf("what was sent: %+v", last)
	}

	// numbers are numbers, because the tool list says so
	if code, _, errs := toolRun("call", "echo.wait", "--arg", "seconds=2.5"); code != 0 {
		t.Fatalf("wait: %d %q", code, errs)
	}
	if v, ok := g.calls[len(g.calls)-1].Args["seconds"].(float64); !ok || v != 2.5 {
		t.Fatalf("seconds went as %#v", g.calls[len(g.calls)-1].Args["seconds"])
	}
	if code, _, errs := toolRun("call", "echo.wait", "--arg", "seconds=soon"); code != 2 || !strings.Contains(errs, "must be a number") {
		t.Fatalf("a word for a number: %d %q", code, errs)
	}
}

func TestToolCallTakesJSONArgumentsAndAsksForJSONOutputSeparately(t *testing.T) {
	g := startGateway(t)
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")

	// the form the design names: --json followed by an object is the arguments
	code, out, _ := toolRun("call", "echo.say", "--json", `{"text":"from json"}`)
	if code != 0 || strings.TrimSpace(out) != "hello" || g.calls[0].Args["text"] != "from json" {
		t.Fatalf("--json object: %d %q %+v", code, out, g.calls)
	}
	// a bare --json is output for a program, and the arguments can come as --args next to it
	code, out, _ = toolRun("call", "echo.say", "--args", `{"text":"a"}`, "--json")
	var resp api.ToolCallResp
	if code != 0 || json.Unmarshal([]byte(out), &resp) != nil || !resp.OK || resp.Text != "hello" || g.calls[1].Args["text"] != "a" {
		t.Fatalf("bare --json: %d %q", code, out)
	}
	// flags and --arg mix with the object; the same name twice is refused
	if code, _, errs := toolRun("call", "echo.say", "--json", `{"text":"a"}`, "--arg", "text=b"); code != 2 || !strings.Contains(errs, "twice") {
		t.Fatalf("twice: %d %q", code, errs)
	}
	if code, _, errs := toolRun("call", "echo.say", "--args", `[1]`); code != 2 || !strings.Contains(errs, "JSON object") {
		t.Fatalf("not an object: %d %q", code, errs)
	}
}

func TestAToolThatIsRefusedExitsNonZeroWithItsCodeWord(t *testing.T) {
	g := startGateway(t)
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")
	g.reply = api.ToolCallResp{Code: "denied-grant", Text: "This helper's profile may not use that tool. Grant the tool on the profile. This tool is not available, do not guess the answer."}

	code, out, errs := toolRun("call", "echo.say", "--arg", "text=x")
	if code == 0 || out != "" || !strings.Contains(errs, "denied-grant: This helper's profile may not use that tool.") || !strings.Contains(errs, "do not guess") {
		t.Fatalf("refused: %d %q %q", code, out, errs)
	}
	// for a program: the answer as JSON on standard output, and still a non-zero exit
	code, out, _ = toolRun("call", "echo.say", "--arg", "text=x", "--json")
	var resp api.ToolCallResp
	if code == 0 || json.Unmarshal([]byte(out), &resp) != nil || resp.Code != "denied-grant" || resp.OK {
		t.Fatalf("refused, json: %d %q", code, out)
	}
}

func TestToolCommandsNeedAHelperTokenAndNeverGoToTheHub(t *testing.T) {
	g := startGateway(t)
	if code, _, errs := toolRun("list"); code == 0 || !strings.Contains(errs, "HANDLOOM_TOOL_TOKEN") {
		t.Fatalf("without a token: %d %q", code, errs)
	}
	// a person's hub token in the environment does not send a tool call to the hub
	t.Setenv("HANDLOOM_HUB", "http://127.0.0.1:1")
	t.Setenv("HANDLOOM_TOKEN", "hlh_not-this")
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")
	if code, out, errs := toolRun("call", "echo.say", "--arg", "text=x"); code != 0 || !strings.Contains(out, "hello") {
		t.Fatalf("with a hub token set: %d %q %q", code, out, errs)
	}
	if len(g.calls) != 1 {
		t.Fatalf("calls that reached the link: %d", len(g.calls))
	}
	if code, _, errs := toolRun("call", "say"); code != 2 || !strings.Contains(errs, "ADDON.TOOL") {
		t.Fatalf("a bare tool name: %d %q", code, errs)
	}
}

func TestToolListInWords(t *testing.T) {
	g := startGateway(t)
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")
	g.list.Unavailable = []api.ToolUnavailable{{Addon: "web-fetch", Tool: "fetch", Cause: "denied-confidential", Why: "A confidential job may not use a tool that leaves the computer.", Action: "Use a different job."}}
	code, out, _ := toolRun("list")
	for _, want := range []string{"echo.say: Return the text.", "text (text, required) The text", "loud (true or false)", "call it: handloom tool call echo.say --arg text=...",
		"web-fetch.fetch is not available: A confidential job may not use a tool that leaves the computer. Do not guess the answer."} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	g.list = api.ToolList{}
	if _, out, _ := toolRun("list"); !strings.Contains(out, "No tools from add-ons") {
		t.Fatalf("empty list: %q", out)
	}
}

func TestWhatAHelperIsToldAboutItsTools(t *testing.T) {
	if got := toolsSection(api.ToolList{}); got != "" {
		t.Fatalf("a helper with none gets %q", got)
	}
	list := api.ToolList{
		Tools: []api.SpawnTool{{Addon: "echo", Tool: "say", Summary: "Return the text.", MaxCalls: 3,
			Input: []api.ToolField{{Name: "text", Type: "string", Required: true, Summary: "The text to return"}, {Name: "seconds", Type: "number"}}}},
		Unavailable: []api.ToolUnavailable{{Addon: "web-fetch", Tool: "fetch", Cause: "not-installed", Why: "This computer does not have the add-on.", Action: "Install the add-on on this computer."}},
	}
	got := toolsSection(list)
	for _, want := range []string{"## Tools from add-ons", "`echo.say`: Return the text.", "`text` (text, required), the text to return", "`seconds` (a number)",
		"handloom tool call echo.say --arg text=...", "At most 3 calls.", "## Tools that are not available",
		"`web-fetch.fetch`: this tool is not available, do not guess the answer. This computer does not have the add-on."} {
		if !strings.Contains(got, want) {
			t.Errorf("the section lacks %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"plugin", "machine", "gateway", "manifest", "Install the add-on"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(banned)) {
			t.Errorf("the section uses %q:\n%s", banned, got)
		}
	}
	// only unavailable ones: just the second section
	only := toolsSection(api.ToolList{Unavailable: list.Unavailable})
	if strings.Contains(only, "## Tools from add-ons") || !strings.Contains(only, "do not guess the answer") {
		t.Fatalf("only unavailable:\n%s", only)
	}
}

// A helper's instructions get the section when it was started with tools, through the same code spawn-exec uses.
func TestHelperToolListReadsTheLinkAndSurvivesItsAbsence(t *testing.T) {
	g := startGateway(t)
	var out, errb strings.Builder
	e := &env{out: &out, err: &errb}
	if l := e.helperToolList(); len(l.Tools) != 0 || errb.Len() != 0 {
		t.Fatalf("no token: %+v %q", l, errb.String())
	}
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")
	if l := e.helperToolList(); len(l.Tools) != 2 {
		t.Fatalf("with a token: %+v", l)
	}
	os.Remove(client.SocketPath())
	if l := e.helperToolList(); len(l.Tools) != 0 {
		t.Fatalf("with no link: %+v", l)
	}
	_ = g
}

func TestToolPromoteAsksTheLinkToCopyTheFile(t *testing.T) {
	g := startGateway(t)
	t.Setenv("HANDLOOM_TOOL_TOKEN", "hlt_abc")
	code, out, errs := toolRun("promote", "echo/c1/note.txt", "--to", "docs/note.txt", "--force")
	if code != 0 || !strings.Contains(out, "Copied into the project as docs/note.txt") || errs != "" {
		t.Fatalf("promote: %d %q %q", code, out, errs)
	}
	if len(g.promotes) != 1 || g.promotes[0] != (api.ToolPromoteReq{File: "echo/c1/note.txt", To: "docs/note.txt", Force: true}) || g.token != "hlt_abc" {
		t.Fatalf("what the link got: %+v token %q", g.promotes, g.token)
	}
	if code, _, errs := toolRun("promote", "echo/c1/note.txt", "--to", "../x"); code == 0 || !strings.Contains(errs, "not inside the project") {
		t.Fatalf("a refused destination: %d %q", code, errs)
	}
	if code, _, errs := toolRun("promote", "echo/c1/note.txt"); code == 0 || !strings.Contains(errs, "--to") {
		t.Fatalf("no destination: %d %q", code, errs)
	}
}
