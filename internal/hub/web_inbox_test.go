package hub

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

// agents is a lead and a worker on one device, reachable through the API.
type webAgents struct {
	e      *webEnv
	device string
}

func (e *webEnv) agents() *webAgents {
	e.t.Helper()
	a := &webAgents{e: e}
	var tok api.TokenResp
	e.apiOK(e.admin, "", "POST", "/v1/admin/devices", api.NameReq{Name: "d1"}, &tok)
	var join api.JoinResp
	e.apiOK("", "", "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok.Token}, &join)
	a.device = join.Credential
	for _, n := range []string{"lead", "worker"} {
		e.apiOK(a.device, "", "POST", "/v1/agents", api.RegisterReq{Name: n, Kind: "shell"}, nil)
	}
	e.apiOK(e.admin, "", "POST", "/v1/agents/lead/role", api.RoleReq{Role: "lead"}, nil)
	return a
}

func (e *webEnv) apiOK(token, agent, method, path string, body, out any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(string(b)))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if agent != "" {
		r.Header.Set(api.AgentHeader, agent)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		raw.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if resp.StatusCode != 200 {
		e.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw.String())
	}
	if out != nil {
		if err := json.Unmarshal([]byte(raw.String()), out); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (a *webAgents) lead(method, path string, body, out any) {
	a.e.t.Helper()
	a.e.apiOK(a.device, "lead", method, path, body, out)
}

func (a *webAgents) worker(method, path string, body, out any) {
	a.e.t.Helper()
	a.e.apiOK(a.device, "worker", method, path, body, out)
}

func (e *webEnv) owner() (*http.Cookie, string) {
	e.t.Helper()
	c := e.setup("afif", goodPassword).cookie
	return c, csrfOf(e.t, e.req("GET", "/inbox", nil, c, nil).body)
}

func TestInboxShowsWhatNeedsYou(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()

	if r := e.req("GET", "/inbox", nil, c, nil); !strings.Contains(r.body, "Nothing needs you.") || !strings.Contains(r.body, "Needs you") {
		t.Fatalf("empty inbox: %s", r.body)
	}

	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "Delete the old migrations?", Options: []string{"yes", "no"}}, nil)
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "Which name should we use?"}, nil)
	var task api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "Write docs", AssignedTo: "worker"}, &task)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil)

	body := e.req("GET", "/inbox", nil, c, nil).body
	for _, want := range []string{"lead has a question", "Delete the old migrations?", `name="answer" value="yes"`, `name="answer" value="no"`,
		"Which name should we use?", "<textarea", "Needs you", "Write docs", "lead"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}

	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", task.ID), api.SubmitReq{Evidence: []string{"file:docs/index.md"}, Note: "done"}, nil)
	body = e.req("GET", "/inbox", nil, c, nil).body
	if !strings.Contains(body, "file:docs/index.md") || !strings.Contains(body, "Accept") || !strings.Contains(body, "To review") {
		t.Fatalf("review section: %s", body)
	}

	// The same facts through the API digest, which the TUI and phone will use.
	var d api.Digest
	e.apiOK(e.admin, "", "GET", "/v1/digest", nil, &d)
	if len(d.NeedsYou) != 2 || len(d.ToReview) != 1 || d.ToReview[0].ID != task.ID || d.Seq == 0 || len(d.Agents) != 2 {
		t.Fatalf("digest: %+v", d)
	}
}

// Agent-written text is data. Whatever it contains must not become markup.
func TestInboxEscapesAgentText(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	evil := `<script>alert(1)</script><img src=x onerror=alert(2)> "quoted" 'single' javascript:alert(3)`
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: evil, Options: []string{`<b>yes</b>`, `"><script>x</script>`}}, nil)
	var task api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: evil, AssignedTo: "worker"}, &task)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", task.ID), api.SubmitReq{Evidence: []string{evil}, Note: evil}, nil)

	for _, path := range []string{"/inbox", "/inbox/fragment"} {
		body := e.req("GET", path, nil, c, nil).body
		for _, bad := range []string{"<script>alert", "<img src=x", "<b>yes</b>", `"><script>x`, "onerror=alert(2)>"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains live markup %q", path, bad)
			}
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s does not show the text, escaped", path)
		}
	}
}

func TestAnswerFromTheWeb(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	var esc api.Escalation
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "Ship it?", Options: []string{"yes", "no"}}, &esc)

	post := func(answer string, form url.Values) webResp {
		if form == nil {
			form = url.Values{"csrf": {tok}, "answer": {answer}}
		}
		return e.req("POST", fmt.Sprintf("/inbox/escalations/%d/answer", esc.ID), form, c, nil)
	}
	if r := post("", url.Values{"answer": {"yes"}}); r.status != 403 { // no CSRF token
		t.Fatalf("without token: %d", r.status)
	}
	if r := post("maybe", nil); r.status != 400 || !strings.Contains(r.body, "must be one of") {
		t.Fatalf("not an option: %d %s", r.status, r.body)
	}
	if r := post("yes", nil); r.status != 303 || r.header.Get("Location") != "/inbox?done=answered" {
		t.Fatalf("answer: %d %s", r.status, r.body)
	}
	if r := post("no", nil); r.status != 409 || !strings.Contains(r.body, "already answered") {
		t.Fatalf("second answer: %d %s", r.status, r.body)
	}
	if r := e.req("GET", "/inbox?done=answered", nil, c, nil); !strings.Contains(r.body, "Answer sent") || !strings.Contains(r.body, "Nothing needs you.") {
		t.Fatalf("inbox after answer: %s", r.body)
	}
	// The lead gets it as a message from the signed-in human.
	var msgs []api.Message
	ag.lead("GET", "/v1/inbox", nil, &msgs)
	found := false
	for _, m := range msgs {
		found = found || (m.From == "human:afif" && strings.Contains(m.Body, "Answer to your question #1") && strings.HasSuffix(m.Body, ": yes"))
	}
	if !found {
		t.Fatalf("lead inbox: %+v", msgs)
	}
	if !e.audited("escalation.answer", "escalation:1") {
		t.Fatal("web answer is not audited")
	}
}

func TestReviewFromTheWeb(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	submit := func(title string) int64 {
		var task api.Task
		ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: title, AssignedTo: "worker"}, &task)
		ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil)
		ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", task.ID), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "done"}, nil)
		return task.ID
	}
	a, b := submit("one"), submit("two")
	if r := e.req("POST", fmt.Sprintf("/inbox/tasks/%d/accept", a), url.Values{"csrf": {tok}}, c, nil); r.status != 303 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if r := e.req("POST", fmt.Sprintf("/inbox/tasks/%d/reject", b), url.Values{"csrf": {tok}, "reason": {""}}, c, nil); r.status != 400 {
		t.Fatalf("reject without a reason: %d", r.status)
	}
	if r := e.req("POST", fmt.Sprintf("/inbox/tasks/%d/reject", b), url.Values{"csrf": {tok}, "reason": {"tests are missing"}}, c, nil); r.status != 303 {
		t.Fatalf("reject: %d", r.status)
	}
	if r := e.req("POST", fmt.Sprintf("/inbox/tasks/%d/accept", a), url.Values{"csrf": {tok}}, c, nil); r.status != 409 {
		t.Fatalf("accepting a done task: %d", r.status)
	}
	var ta, tb api.Task
	e.apiOK(e.admin, "", "GET", fmt.Sprintf("/v1/tasks/%d", a), nil, &ta)
	e.apiOK(e.admin, "", "GET", fmt.Sprintf("/v1/tasks/%d", b), nil, &tb)
	if ta.Status != api.StatusDone || tb.Status != api.StatusClaimed || tb.RejectReason != "tests are missing" {
		t.Fatalf("tasks: %+v %+v", ta, tb)
	}
}

func TestViewerSeesButCannotAct(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	e.owner()
	// A viewer account, as the owner's settings page will create them later.
	hash, _ := HashPassword(goodPassword)
	e.hub.db.Exec(`INSERT INTO human(name, token_hash, created_at, password_hash, role) VALUES ('vera', 'x', 1, ?, 'viewer')`, hash)
	c := e.login("vera", goodPassword).cookie
	var esc api.Escalation
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "Ship it?"}, &esc)

	r := e.req("GET", "/inbox", nil, c, nil)
	if !strings.Contains(r.body, "Ship it?") || strings.Contains(r.body, "<textarea") || strings.Contains(r.body, `action="/inbox/escalations`) {
		t.Fatalf("viewer inbox: %s", r.body)
	}
	tok := csrfOf(t, r.body)
	r = e.req("POST", fmt.Sprintf("/inbox/escalations/%d/answer", esc.ID), url.Values{"csrf": {tok}, "answer": {"yes"}}, c, nil)
	if r.status != 403 {
		t.Fatalf("viewer answer: %d %s", r.status, r.body)
	}
	if !e.audited("denied", fmt.Sprintf("POST /inbox/escalations/%d/answer", esc.ID)) {
		t.Fatal("the refused answer is not audited")
	}
	var got api.Escalation
	e.apiOK(e.admin, "", "GET", fmt.Sprintf("/v1/escalations/%d", esc.ID), nil, &got)
	if got.Answer != nil {
		t.Fatal("a viewer answered")
	}
}

// readEvents reads server-sent events from an open stream.
type sseReader struct {
	t  *testing.T
	ch chan string
}

func openStream(t *testing.T, e *webEnv, c *http.Cookie) (*sseReader, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+"/ui/stream", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r := &sseReader{t: t, ch: make(chan string, 32)}
	go func() {
		defer close(r.ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "event: ") {
				r.ch <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()
	return r, resp
}

func (r *sseReader) next(within time.Duration) string {
	select {
	case ev := <-r.ch:
		return ev
	case <-time.After(within):
		return ""
	}
}

func TestStreamSaysSomethingChanged(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()

	if resp, _ := http.Get(e.srv.URL + "/ui/stream"); resp.StatusCode != 401 {
		t.Fatalf("stream without a session: %d", resp.StatusCode)
	}
	s, resp := openStream(t, e, c)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-transform") {
		t.Fatalf("stream headers: %v", resp.Header)
	}
	if ev := s.next(3 * time.Second); ev != "resync" {
		t.Fatalf("first event %q, want resync", ev)
	}
	if ev := s.next(300 * time.Millisecond); ev != "" {
		t.Fatalf("event %q with nothing happening", ev)
	}
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "SECRET question text"}, nil)
	if ev := s.next(3 * time.Second); ev != "inbox-changed" {
		t.Fatalf("after an escalation: %q", ev)
	}
}

func TestStreamLimits(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, _ := e.owner()
	var open []*http.Response
	defer func() {
		for _, r := range open {
			r.Body.Close()
		}
	}()
	for i := 0; i < maxStreamsPerHuman; i++ {
		_, resp := openStream(t, e, c)
		open = append(open, resp)
		if resp.StatusCode != 200 {
			t.Fatalf("stream %d: %d", i, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/ui/stream", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("one stream too many: %d", resp.StatusCode)
	}
}

func TestFragmentNeedsASession(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.owner()
	if r := e.req("GET", "/inbox/fragment", nil, nil, nil); r.status != 303 || r.header.Get("Location") != "/login" {
		t.Fatalf("fragment without a session: %d", r.status)
	}
}

func TestSilentLeadNeedsYouAfterThirtyMinutes(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	var task api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "work"}, &task)

	if r := e.req("GET", "/inbox", nil, c, nil); strings.Contains(r.body, "is unknown while 1 task(s) are unfinished") {
		t.Fatal("a lead that registered a moment ago is reported silent")
	}
	e.clock.advance(31 * time.Minute)
	// A task assigned to nobody is not "work in flight"; claim it first.
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil)
	if r := e.req("GET", "/inbox", nil, c, nil); !strings.Contains(r.body, "The lead (lead) is unknown while 1 task(s) are unfinished") {
		t.Fatalf("a silent lead is not reported: %s", r.body)
	}
}

// Twenty people watching at once, one event, everybody gets the hint, and the
// hub (one database connection) still answers other requests meanwhile.
func TestManyStreamsOneEvent(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	e.owner()
	hash, _ := HashPassword(goodPassword)
	const n = 20
	var readers []*sseReader
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("viewer%02d", i)
		e.hub.db.Exec(`INSERT INTO human(name, token_hash, created_at, password_hash, role) VALUES (?, ?, 1, ?, 'viewer')`, name, name, hash)
		c := e.login(name, goodPassword).cookie
		if c == nil {
			t.Fatalf("login %s failed", name)
		}
		r, resp := openStream(t, e, c)
		defer resp.Body.Close()
		readers = append(readers, r)
	}
	for i, r := range readers {
		if ev := r.next(5 * time.Second); ev != "resync" {
			t.Fatalf("stream %d: first event %q", i, ev)
		}
	}
	// The API still answers while twenty streams wait.
	start := time.Now()
	ag.lead("POST", "/v1/escalations", api.AskReq{Question: "q"}, nil)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a request took %s with 20 streams open", d)
	}
	for i, r := range readers {
		if ev := r.next(5 * time.Second); ev != "inbox-changed" {
			t.Fatalf("stream %d: %q after the event", i, ev)
		}
	}
}
