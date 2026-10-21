package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/addon"
	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/profile"
)

func needPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
}

// toolEnv is a hub and a link with echo installed and reported, and a way to start helpers that hold a tool token.
type toolEnv struct {
	*fixture
	root string
	tl   *tmuxLog
}

func newToolEnv(t *testing.T) *toolEnv {
	t.Helper()
	needPython(t)
	f, root := addonFixture(t)
	tl := &tmuxLog{}
	f.spawnLink(tl)
	if _, err := addon.Install(root, addon.NewStore(root), echoAddon, "", "tester", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.ladder() // reports echo to the hub
	t.Cleanup(f.link.closeAddons)
	return &toolEnv{fixture: f, root: root, tl: tl}
}

func (e *toolEnv) profile(name, runtime string) {
	e.t.Helper()
	e.must(e.admin.Post("/v1/profiles", api.ProfileReq{Name: name, Spec: profile.Spec{Kind: "claude", Runtime: runtime, Tools: profile.Tools{Allow: []string{"read"}}}}, nil))
}

func (e *toolEnv) grant(prof, tool string, leaves bool, max int) {
	e.t.Helper()
	e.must(e.human.Post("/v1/profiles/"+prof+"/addon-grants", api.AddonGrantReq{Addon: "echo", Tool: tool, LeavesMachine: &leaves, MaxCalls: max}, nil))
}

var toolTokenRE = regexp.MustCompile(`HANDLOOM_TOOL_TOKEN='(hlt_[0-9a-f]+)'`)

// helper starts a helper from the profile in a new job and returns a client that holds its tool token, taken
// from the command the link gave the terminal, as the helper's own process would find it in its environment.
func (e *toolEnv) helper(name, prof string, confidential bool) (*client.Client, api.Spawn) {
	e.t.Helper()
	var j api.Job
	e.must(e.human.Post("/v1/jobs", api.JobNewReq{Title: "Tools " + name, Device: "dev", Confidential: confidential}, &j))
	var s api.Spawn
	e.must(e.human.Post("/v1/spawns", api.SpawnReq{Name: name, Profile: prof, Job: j.ID, Device: "dev"}, &s))
	e.ladder()
	for _, c := range e.tl.all() {
		if strings.Contains(c, " -n "+name+" ") {
			if m := toolTokenRE.FindStringSubmatch(c); m != nil {
				tc := client.Socket(e.link.opt.Socket, "")
				tc.ToolToken = m[1]
				return tc, s
			}
		}
	}
	e.t.Fatalf("no tool token in the terminal commands: %q", e.tl.all())
	return nil, s
}

func call(c *client.Client, tool string, args map[string]any) (api.ToolCallResp, error) {
	var out api.ToolCallResp
	err := c.Post("/local/tools/call", api.ToolCallReq{Tool: tool, Args: args}, &out)
	return out, err
}

func (e *toolEnv) journalLines() []api.AddonCall {
	e.t.Helper()
	b, err := os.ReadFile(e.link.callJournal().path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var out []api.AddonCall
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var c api.AddonCall
		if err := json.Unmarshal([]byte(ln), &c); err != nil {
			e.t.Fatalf("journal line %q: %v", ln, err)
		}
		out = append(out, c)
	}
	return out
}

func phases(lines []api.AddonCall) string {
	var p []string
	for _, l := range lines {
		s := l.Phase
		if l.Code != "" {
			s += ":" + l.Code
		}
		p = append(p, s)
	}
	return strings.Join(p, " ")
}

func (e *toolEnv) shipped() int {
	e.t.Helper()
	n := 0
	for _, ph := range []string{"call", "result", "denied"} {
		n += len(e.audit("addon.echo." + ph))
	}
	return n
}

func TestAGrantedToolIsCalledAndAuditedAtTheHub(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, s := e.helper("alpha", "scribe", false)

	var list api.ToolList
	e.must(c.Get("/local/tools", &list))
	if len(list.Tools) != 1 || list.Tools[0].Tool != "say" || list.Tools[0].MaxCalls != 50 || len(list.Unavailable) != 0 {
		t.Fatalf("tools: %+v", list)
	}

	got, err := call(c, "echo.say", map[string]any{"text": "hello", "file": "note.txt"})
	if err != nil || !got.OK || got.Code != "ok" || got.Text != "hello" || len(got.Files) != 1 {
		t.Fatalf("call: %+v %v", got, err)
	}
	// The result file is where the design says: <worktree>/.handloom/results/ADDON/CALL/
	dir := filepath.Join(f(e).link.opt.WorkRoot, fmt.Sprintf("job-%d", *s.Job), "alpha")
	want := filepath.Join(dir, ".handloom", "results", "echo") + string(filepath.Separator)
	if !strings.HasPrefix(got.Files[0], want) || filepath.Base(got.Files[0]) != "note.txt" {
		t.Fatalf("file %q, want under %s", got.Files[0], want)
	}
	if b, err := os.ReadFile(got.Files[0]); err != nil || string(b) != "hello" {
		t.Fatalf("the result file: %q %v", b, err)
	}

	lines := e.journalLines()
	if phases(lines) != "call result:ok" || lines[0].CallID != lines[1].CallID || lines[0].Spawn != s.ID || lines[0].Agent != "alpha" || lines[0].Job != *s.Job {
		t.Fatalf("journal: %+v", lines)
	}
	e.ladder() // ships
	if n := len(e.audit("addon.echo.call")); n != 1 {
		t.Fatalf("call entries at the hub: %d", n)
	}
	res := e.audit("addon.echo.result")
	if len(res) != 1 || !strings.Contains(string(res[0].Payload), `"code":"ok"`) || !strings.Contains(string(res[0].Payload), `"tool":"say"`) {
		t.Fatalf("result entries at the hub: %+v", res)
	}
}

func f(e *toolEnv) *fixture { return e.fixture }

func TestRefusalsAreJournaledAndExplained(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0) // wait is not granted
	c, _ := e.helper("alpha", "scribe", false)

	cases := []struct{ tool, code, words string }{
		{"echo.wait", "denied-grant", "may not use that tool"},
		{"echo.nosuch", "not-installed", "does not have the add-on"},
		{"nothere.say", "not-installed", "does not have the add-on"},
	}
	for _, k := range cases {
		got, err := call(c, k.tool, map[string]any{"seconds": 1})
		if err != nil || got.OK || got.Code != k.code || !strings.Contains(got.Text, k.words) || !strings.Contains(got.Text, "do not guess") {
			t.Errorf("%s: %+v %v", k.tool, got, err)
		}
	}
	if p := phases(e.journalLines()); p != "denied:denied-grant denied:not-installed denied:not-installed" {
		t.Fatalf("journal: %s", p)
	}
	e.ladder()
	// the audit name carries the add-on's own name, so a call to an add-on that is not here is filed under it
	if n, m := len(e.audit("addon.echo.denied")), len(e.audit("addon.nothere.denied")); n != 2 || m != 1 {
		t.Fatalf("denied entries at the hub: echo %d, nothere %d", n, m)
	}

	// A mistake in the request is the caller's, not a result code, and nothing is journaled for it.
	before := len(e.journalLines())
	var out api.ToolCallResp
	err := c.Post("/local/tools/call", api.ToolCallReq{Tool: "echo.say", Args: map[string]any{"text": 3}}, &out)
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("bad arguments: %v", err)
	}
	if err := c.Post("/local/tools/call", api.ToolCallReq{Tool: "say"}, &out); !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("a tool name without an add-on: %v", err)
	}
	if len(e.journalLines()) != before {
		t.Fatal("a malformed call was journaled")
	}
}

func TestATokenIsTheOnlyWayIn(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	good, _ := e.helper("alpha", "scribe", false)

	for name, c := range map[string]*client.Client{
		"no token": client.Socket(e.link.opt.Socket, "alpha"),
		"another token": func() *client.Client {
			x := client.Socket(e.link.opt.Socket, "alpha")
			x.ToolToken = "hlt_" + strings.Repeat("0", 64)
			return x
		}(),
	} {
		var out api.ToolList
		var ce *client.Error
		if err := c.Get("/local/tools", &out); !errors.As(err, &ce) || ce.Status != 403 {
			t.Errorf("%s, list: %v", name, err)
		}
		if _, err := call(c, "echo.say", map[string]any{"text": "x"}); !errors.As(err, &ce) || ce.Status != 403 {
			t.Errorf("%s, call: %v", name, err)
		}
	}
	// The name the caller sends is not what it is known by: the right token with another agent's name is still alpha.
	good.Agent = "somebody-else"
	if got, err := call(good, "echo.say", map[string]any{"text": "x"}); err != nil || !got.OK {
		t.Fatalf("the token should be enough: %+v %v", got, err)
	}
	if l := e.journalLines(); len(l) != 2 || l[0].Agent != "alpha" {
		t.Fatalf("journal: %+v", l)
	}
	// Refusals with no token leave no journal line: there is no spawn to attribute them to.
}

// Two helpers: one is granted the tool and one is not. The second cannot use the first's grant, with its own
// token or with the first's name, and each has its own count.
func TestTokensAreIsolatedBetweenSpawns(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.profile("plain", profile.Cloud)
	e.grant("scribe", "say", false, 2)
	a, sa := e.helper("alpha", "scribe", false)
	b, sb := e.helper("beta", "plain", false)
	a2, sa2 := e.helper("gamma", "scribe", false)

	if got, _ := call(b, "echo.say", map[string]any{"text": "x"}); got.Code != "denied-grant" {
		t.Fatalf("beta used alpha's grant: %+v", got)
	}
	b.Agent = "alpha"
	if got, _ := call(b, "echo.say", map[string]any{"text": "x"}); got.Code != "denied-grant" {
		t.Fatalf("beta used alpha's grant by name: %+v", got)
	}
	var lb api.ToolList
	e.must(b.Get("/local/tools", &lb))
	if len(lb.Tools) != 0 {
		t.Fatalf("beta lists %+v", lb)
	}

	// the counter is per spawn and per tool: two calls each, a third is over the limit, and gamma is not alpha
	for i := 0; i < 2; i++ {
		if got, _ := call(a, "echo.say", map[string]any{"text": "x"}); got.Code != "ok" {
			t.Fatalf("alpha call %d: %+v", i, got)
		}
	}
	if got, _ := call(a, "echo.say", map[string]any{"text": "x"}); got.Code != "denied-budget" || !strings.Contains(got.Text, "call limit") {
		t.Fatalf("alpha's third call: %+v", got)
	}
	if got, _ := call(a2, "echo.say", map[string]any{"text": "x"}); got.Code != "ok" {
		t.Fatalf("gamma shares alpha's count: %+v", got)
	}
	// each denied or made call is attributed to the spawn that made it
	for _, l := range e.journalLines() {
		switch l.Agent {
		case "alpha":
			if l.Spawn != sa.ID {
				t.Errorf("alpha's line is for spawn %d", l.Spawn)
			}
		case "beta":
			if l.Spawn != sb.ID || l.Phase != "denied" {
				t.Errorf("beta's line: %+v", l)
			}
		case "gamma":
			if l.Spawn != sa2.ID {
				t.Errorf("gamma's line is for spawn %d", l.Spawn)
			}
		default:
			t.Errorf("a line for %q", l.Agent)
		}
	}
}

func TestAConfidentialJobIsRefusedATapThatLeavesTheComputer(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe-local", profile.Local)
	e.grant("scribe-local", "say", true, 0) // the owner says it leaves
	e.grant("scribe-local", "wait", false, 0)
	c, _ := e.helper("alpha", "scribe-local", true)

	var list api.ToolList
	e.must(c.Get("/local/tools", &list))
	if !list.Confidential || len(list.Tools) != 1 || list.Tools[0].Tool != "wait" || len(list.Unavailable) != 1 || list.Unavailable[0].Cause != "denied-confidential" {
		t.Fatalf("list: %+v", list)
	}
	got, _ := call(c, "echo.say", map[string]any{"text": "secret"})
	if got.OK || got.Code != "denied-confidential" || !strings.Contains(got.Text, "confidential") {
		t.Fatalf("a confidential job's tool that leaves: %+v", got)
	}
	// even a snapshot that wrongly lets a leaving tool through is stopped here
	for _, s := range e.link.gate.sessions() {
		s.tools["echo.say"] = api.SpawnTool{Addon: "echo", Tool: "say", LeavesMachine: true, MaxCalls: 5}
		delete(s.unavailable, "echo.say")
	}
	if got, _ := call(c, "echo.say", map[string]any{"text": "secret"}); got.Code != "denied-confidential" {
		t.Fatalf("the link trusted a bad snapshot: %+v", got)
	}
}

func TestAChangedAddOnIsRefusedAtTheCall(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)
	svc := filepath.Join(e.root, "echo", "service.py")
	body, _ := os.ReadFile(svc)
	os.WriteFile(svc, append(body, []byte("\n# edited\n")...), 0o644)
	got, _ := call(c, "echo.say", map[string]any{"text": "x"})
	if got.OK || got.Code != "changed" || !strings.Contains(got.Text, "Approve it again") {
		t.Fatalf("changed: %+v", got)
	}
	os.WriteFile(svc, body, 0o644)
	os.RemoveAll(filepath.Join(e.root, "echo"))
	if got, _ = call(c, "echo.say", map[string]any{"text": "x"}); got.Code != "not-installed" {
		t.Fatalf("removed: %+v", got)
	}
}

// An owner who approves a changed add-on after a helper started does not hand the helper the new code: the
// grant was made for the old manifest, so the call is refused until the owner grants the tool again.
func TestAGrantStopsWhenTheAddOnIsApprovedAgainWithAnotherManifest(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)
	if got, _ := call(c, "echo.say", map[string]any{"text": "x"}); !got.OK {
		t.Fatalf("before the change: %+v", got)
	}
	e.link.closeAddons() // the service is idle and stopped, as it would be after ten minutes
	src := t.TempDir()
	for _, f := range []string{"addon.yaml", "service.py"} {
		b, err := os.ReadFile(filepath.Join(echoAddon, f))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(src, f), b, 0o644)
	}
	mf := filepath.Join(src, "addon.yaml")
	b, _ := os.ReadFile(mf)
	os.WriteFile(mf, []byte(strings.Replace(string(b), "Return the text it is given", "Return the text it is given, changed", 1)), 0o644)
	if _, err := addon.Install(e.root, addon.NewStore(e.root), src, "", "tester", e.clock()); err != nil {
		t.Fatal(err)
	}
	got, _ := call(c, "echo.say", map[string]any{"text": "x"})
	if got.OK || got.Code != "changed" {
		t.Fatalf("after the owner approved another manifest: %+v", got)
	}
}

// A link that stopped in the middle of a call leaves a call line with no result. The next start closes it.
func TestALinkStartClosesACallTheLastLinkLeftOpen(t *testing.T) {
	j := &callJournal{path: filepath.Join(t.TempDir(), "addon-calls.jsonl")}
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	add := func(id, phase string) {
		if err := j.append(api.AddonCall{CallID: id, Spawn: 3, Agent: "alpha", Addon: "echo", Tool: "wait", Phase: phase, Time: now}); err != nil {
			t.Fatal(err)
		}
	}
	add("c1", "call")
	add("c1", "result")
	add("c2", "denied")
	add("c3", "call") // cut short
	n, err := j.closeDangling(now.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("closed %d: %v", n, err)
	}
	calls, _, _, _ := j.unsent(100)
	last := calls[len(calls)-1]
	if len(calls) != 5 || last.CallID != "c3" || last.Phase != "result" || last.Code != codeError {
		t.Fatalf("journal: %+v", calls)
	}
	if n, _ := j.closeDangling(now); n != 0 {
		t.Fatalf("closed again: %d", n)
	}
}

// The call line is on disk before the add-on is asked, and when the add-on dies in the middle of the call the
// result line says so.
func TestTheCallIsJournaledBeforeTheAddOnHearsOfIt(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "wait", false, 0)
	c, _ := e.helper("alpha", "scribe", false)

	var wg sync.WaitGroup
	var got api.ToolCallResp
	var callErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, callErr = call(c, "echo.wait", map[string]any{"seconds": 60})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(e.journalLines()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p := phases(e.journalLines()); p != "call" {
		t.Fatalf("while the call is running the journal holds %q", p)
	}
	// The add-on is asked after the line is written: wait until it is running, then kill it.
	for !e.link.supervisor().Running("echo") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	e.link.closeAddons() // kills the service in the middle of the call
	wg.Wait()
	if callErr != nil || got.OK || got.Code != "plugin-error" || !strings.Contains(got.Text, "do not guess") {
		t.Fatalf("the killed call: %+v %v", got, callErr)
	}
	lines := e.journalLines()
	if phases(lines) != "call result:plugin-error" || lines[0].CallID != lines[1].CallID {
		t.Fatalf("journal: %+v", lines)
	}
}

func TestACallIsNotMadeWhenItsLineCannotBeWritten(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 1)
	c, _ := e.helper("alpha", "scribe", false)
	// the journal's place is a folder, so no line can be written
	jp := e.link.callJournal().path
	os.Remove(jp)
	if err := os.Mkdir(jp, 0o700); err != nil {
		t.Fatal(err)
	}
	var out api.ToolCallResp
	var ce *client.Error
	if err := c.Post("/local/tools/call", api.ToolCallReq{Tool: "echo.say", Args: map[string]any{"text": "x"}}, &out); !errors.As(err, &ce) || ce.Status != 500 {
		t.Fatalf("with no journal: %v", err)
	}
	if e.link.supervisor().Running("echo") {
		t.Fatal("the add-on was started for a call that was not journaled")
	}
	os.Remove(jp)
	// the failed attempt did not use up the one call the grant allows
	if got, _ := call(c, "echo.say", map[string]any{"text": "x"}); got.Code != "ok" {
		t.Fatalf("after the journal came back: %+v", got)
	}
}

// What leaves the computer: ids, names, a code, a size and a time. Not what was asked and not what came back.
func TestNoQueryTextOrResultIsJournaledOrShipped(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)
	secret := "QUERY-TEXT-the-client-is-called-acme-9731"
	if got, err := call(c, "echo.say", map[string]any{"text": secret, "file": "out.txt"}); err != nil || got.Text != secret {
		t.Fatalf("call: %+v %v", got, err)
	}
	e.ladder()
	raw, _ := os.ReadFile(e.link.callJournal().path)
	if bytes.Contains(raw, []byte("acme")) || bytes.Contains(raw, []byte("QUERY")) || bytes.Contains(raw, []byte("out.txt")) {
		t.Fatalf("the journal holds query text or a file name: %s", raw)
	}
	var rows []api.AuditRow
	e.must(e.human.Get("/v1/audit", &rows))
	seen := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Action, "addon.echo.") {
			seen++
		}
		if strings.Contains(string(r.Payload)+r.Target, "acme") || strings.Contains(string(r.Payload), "out.txt") {
			t.Fatalf("the hub holds query text: %+v", r)
		}
	}
	if seen != 2 {
		t.Fatalf("expected a call and a result entry at the hub, saw %d", seen)
	}
	// what is shipped is the struct and nothing more: no field is free text
	typ := fmt.Sprintf("%T", api.AddonCall{})
	_ = typ
	b, _ := json.Marshal(api.AddonCall{})
	var keys map[string]any
	json.Unmarshal(b, &keys)
	for k := range keys {
		switch k {
		case "call_id", "spawn", "agent", "job", "addon", "tool", "phase", "code", "size", "millis", "time":
		default:
			t.Errorf("the journal line has a field %q", k)
		}
	}
}

// The hub is away: calls go on, nothing is lost, and when it is back the lines go once. Sending the same
// lines again changes nothing at the hub.
func TestTheShipperRetriesAfterAnOutageAndIsIdempotent(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)

	// the hub goes away (the link's own connection to it breaks)
	good := e.link.hub
	var down atomicBool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.get() {
			http.Error(w, "away", http.StatusServiceUnavailable)
			return
		}
		req, _ := http.NewRequest(r.Method, e.url+r.URL.Path, r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		w.Write(buf.Bytes())
	}))
	defer proxy.Close()
	e.link.hub = client.Direct(proxy.URL, e.cred)
	defer func() { e.link.hub = good }()
	down.set(true)

	for i := 0; i < 3; i++ {
		if got, err := call(c, "echo.say", map[string]any{"text": "x"}); err != nil || !got.OK {
			t.Fatalf("a call while the hub is away: %+v %v", got, err)
		}
	}
	e.link.runAddonCalls(t.Context())
	e.link.runAddonCalls(t.Context())
	if e.shipped() != 0 {
		t.Fatal("the hub was away but has entries")
	}
	if got := e.link.callJournal().offset(1 << 30); got != 0 {
		t.Fatalf("lines were marked sent while the hub was away: %d", got)
	}
	if len(e.journalLines()) != 6 {
		t.Fatalf("journal: %d lines", len(e.journalLines()))
	}

	down.set(false)
	e.link.runAddonCalls(t.Context())
	if n := e.shipped(); n != 6 {
		t.Fatalf("after the hub came back it holds %d entries, want 6", n)
	}
	st, _ := os.Stat(e.link.callJournal().path)
	if got := e.link.callJournal().offset(st.Size()); got != st.Size() {
		t.Fatalf("sent mark %d of %d", got, st.Size())
	}

	// a lost sent mark (a crash after the hub took the batch) sends the lines again; the hub keeps one of each
	os.Remove(e.link.callJournal().sentPath())
	e.link.runAddonCalls(t.Context())
	if n := e.shipped(); n != 6 {
		t.Fatalf("sending the lines twice made %d entries", n)
	}
	// and a later call is shipped alone
	call(c, "echo.say", map[string]any{"text": "y"})
	e.link.runAddonCalls(t.Context())
	if n := e.shipped(); n != 8 {
		t.Fatalf("entries after one more call: %d", n)
	}
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }
func (a *atomicBool) set(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }

// A line the hub will never take (a spawn it has forgotten) is passed over; the lines after it still go.
func TestALineTheHubRefusesDoesNotBlockTheOthers(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)
	j := e.link.callJournal()
	if err := j.append(api.AddonCall{CallID: "orphan", Spawn: 99999, Agent: "ghost", Addon: "echo", Tool: "say", Phase: "call", Time: e.clock()}); err != nil {
		t.Fatal(err)
	}
	// a torn line from a crash, and then a good call
	f1, _ := os.OpenFile(j.path, os.O_APPEND|os.O_WRONLY, 0o600)
	f1.WriteString(`{"call_id":"torn","sp`)
	f1.Close()
	if got, _ := call(c, "echo.say", map[string]any{"text": "x"}); got.Code != "ok" {
		t.Fatalf("call: %+v", got)
	}
	e.ladder()
	if n := e.shipped(); n != 2 {
		t.Fatalf("the good call's two lines should have reached the hub, saw %d", n)
	}
	st, _ := os.Stat(j.path)
	if got := j.offset(st.Size()); got != st.Size() {
		t.Fatalf("sent mark %d of %d", got, st.Size())
	}
}

func TestAFullyShippedBigJournalIsMovedAside(t *testing.T) {
	e := newToolEnv(t)
	j := e.link.callJournal()
	pad := bytes.Repeat([]byte("\n"), journalRotate+10)
	os.MkdirAll(filepath.Dir(j.path), 0o700)
	os.WriteFile(j.path, pad, 0o600)
	j.markSent(int64(len(pad)))
	e.link.runAddonCalls(t.Context())
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		t.Fatalf("the big journal stayed: %v", err)
	}
	if _, err := os.Stat(j.path + ".old"); err != nil {
		t.Fatalf("the old journal was not kept: %v", err)
	}
	if err := j.append(api.AddonCall{CallID: "n", Phase: "call", Time: e.clock()}); err != nil {
		t.Fatal(err)
	}
	calls, _, _, _ := j.unsent(10)
	if len(calls) != 1 {
		t.Fatalf("after rotation: %+v", calls)
	}
}

func TestATokenEndsWithItsSpawn(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)
	// the helper has not registered yet: its token stands for the first minutes
	e.advance(time.Minute)
	e.ladder()
	if got, err := call(c, "echo.say", map[string]any{"text": "x"}); err != nil || !got.OK {
		t.Fatalf("too early: %+v %v", got, err)
	}
	// still nothing registered, past the grace: the agent is not on the hub's list, so the token is dropped
	e.advance(3 * time.Minute)
	e.ladder()
	var ce *client.Error
	if _, err := call(c, "echo.say", map[string]any{"text": "x"}); !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("a token outlived its spawn: %v", err)
	}
	// a helper started again under the same name gets a new token and the old one stops
	_, _, mh, err := addon.Load(echoAddon)
	if err != nil {
		t.Fatal(err)
	}
	mint := func() *client.Client {
		tok, err := e.link.gate.mint(api.Spawn{ID: 5, Name: "beta", Tools: []api.SpawnTool{{Addon: "echo", Tool: "say", MaxCalls: 9, ManifestHash: mh}}}, t.TempDir(), e.clock())
		if err != nil {
			t.Fatal(err)
		}
		c := client.Socket(e.link.opt.Socket, "")
		c.ToolToken = tok
		return c
	}
	first, second := mint(), mint()
	if _, err := call(first, "echo.say", map[string]any{"text": "x"}); !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("the old token still works: %v", err)
	}
	if got, err := call(second, "echo.say", map[string]any{"text": "x"}); err != nil || !got.OK {
		t.Fatalf("the new token: %+v %v", got, err)
	}
}

func TestATokenIsGoneWhenTheWindowIsGone(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	c, _ := e.helper("alpha", "scribe", false)
	// the agent registered (so it is on the hub's list) but its terminal window is not in the server's list
	e.must(client.Socket(e.link.opt.Socket, "alpha").Post("/v1/agents", api.RegisterReq{Name: "alpha", Kind: "claude"}, nil))
	e.advance(3 * time.Minute)
	e.link.opt.Tmux = func(_ context.Context, _ string, args ...string) (string, error) { return "other\n", nil }
	e.ladder()
	var ce *client.Error
	if _, err := call(c, "echo.say", map[string]any{"text": "x"}); !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("a token outlived its window: %v", err)
	}
}

func TestTheGatewayTokenIsInTheSpawnsEnvironmentOnly(t *testing.T) {
	e := newToolEnv(t)
	e.profile("scribe", profile.Cloud)
	e.grant("scribe", "say", false, 0)
	e.helper("alpha", "scribe", false)
	e.helper("beta", "scribe", false)
	var toks []string
	for _, c := range e.tl.all() {
		if m := toolTokenRE.FindStringSubmatch(c); m != nil {
			toks = append(toks, m[1])
		}
	}
	if len(toks) != 2 || toks[0] == toks[1] || len(toks[0]) < 60 {
		t.Fatalf("tokens: %v", toks)
	}
	for _, s := range e.link.gate.sessions() {
		for _, tok := range toks {
			if strings.Contains(fmt.Sprintf("%+v", s), tok) {
				t.Fatal("the session holds the token itself, not its hash")
			}
		}
	}
}
