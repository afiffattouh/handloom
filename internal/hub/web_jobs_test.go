package hub

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestStartAJobFromTheWeb(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents() // a joined device d1
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	c, tok := e.owner()

	if r := e.req("GET", "/jobs", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "No jobs yet") || !strings.Contains(r.body, "New job") {
		t.Fatalf("empty list: %d", r.status)
	}
	form := e.req("GET", "/jobs/new", nil, c, nil)
	for _, want := range []string{`name="title"`, `name="repo"`, `name="verify"`, `<option value="d1"`, `<option value="boss"`} {
		if !strings.Contains(form.body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	if strings.Contains(form.body, `name="confidential"`) {
		t.Error("the confidential box is offered on a hub that does not allow it")
	}

	post := func(extra url.Values) webResp {
		f := url.Values{"title": {"Fix the parser"}, "body": {"app.go is broken\nDone when ./check.sh passes."}, "device": {"d1"},
			"repo": {"/srv/app"}, "verify": {"./check.sh"}, "lead_profile": {"boss"}, "csrf": {tok}}
		for k, v := range extra {
			f[k] = v
		}
		return e.req("POST", "/jobs", f, c, nil)
	}
	r := post(url.Values{"repo": {"relative/path"}, "title": {"Keep my words"}})
	if r.status != 400 || !strings.Contains(r.body, "absolute path") || !strings.Contains(r.body, "Keep my words") || !strings.Contains(r.body, `value="boss" selected`) {
		t.Fatalf("a bad repo: %d %s", r.status, r.body)
	}
	if post(url.Values{"lead_profile": {"nobody"}}).status != 404 {
		t.Fatal("an unknown profile")
	}
	var js []api.Job
	e.apiOK(e.admin, "", "GET", "/v1/jobs", nil, &js)
	if len(js) != 0 {
		t.Fatalf("a refused form left jobs behind: %+v", js)
	}

	ok := post(nil)
	if ok.status != 303 || ok.header.Get("Location") != "/jobs/1?done=started" {
		t.Fatalf("start: %d %s", ok.status, ok.body)
	}
	var sp []api.Spawn
	e.apiOK(e.admin, "", "GET", "/v1/spawns", nil, &sp)
	if len(sp) != 1 || sp[0].Role != "lead" || sp[0].Profile != "boss@1" || sp[0].Repo != "/srv/app" || sp[0].Verify != "./check.sh" {
		t.Fatalf("spawns: %+v", sp)
	}
	page := e.req("GET", "/jobs/1?done=started", nil, c, nil)
	for _, want := range []string{"#1 Fix the parser", "Job started", "/srv/app on d1", "./check.sh", "app.go is broken", "No tasks yet"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if r := e.req("GET", "/jobs", nil, c, nil); !strings.Contains(r.body, "Fix the parser") || !strings.Contains(r.body, "repo on d1") {
		t.Fatalf("list: %s", r.body)
	}

	// A viewer reads and cannot start or change anything.
	vc, vtok := e.member("vera", "viewer")
	if r := e.req("GET", "/jobs/1", nil, vc, nil); r.status != 200 || strings.Contains(r.body, "Manage") {
		t.Fatalf("a viewer's job page: %d", r.status)
	}
	if r := e.req("GET", "/jobs/new", nil, vc, nil); r.status != 403 {
		t.Fatalf("a viewer opens the form: %d", r.status)
	}
	f := url.Values{"title": {"x"}, "csrf": {vtok}}
	if r := e.req("POST", "/jobs", f, vc, nil); r.status != 403 {
		t.Fatalf("a viewer starts a job: %d", r.status)
	}
	if r := e.req("POST", "/jobs/1/close", url.Values{"csrf": {vtok}}, vc, nil); r.status != 403 {
		t.Fatalf("a viewer closes a job: %d", r.status)
	}
	if r := e.req("GET", "/jobs/99", nil, c, nil); r.status != 404 {
		t.Fatalf("unknown job: %d", r.status)
	}
}

// humanAPI returns a token with which jobs can be created through the API (the admin token cannot).
func (e *webEnv) humanAPI() string {
	e.t.Helper()
	var tok api.TokenResp
	e.apiOK(e.admin, "", "POST", "/v1/admin/humans", api.NameReq{Name: "apiuser"}, &tok)
	return tok.Token
}

func TestTheJobPageShowsTasksChecksAndTheTeam(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)
	evil := `<script>alert(1)</script>`
	var t1, t2 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "first " + evil, AssignedTo: "worker"}, &t1)
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "second", DependsOn: []int64{t1.ID}}, &t2)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc " + evil}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 2, Tail: "FAIL " + evil}, nil)

	page := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	for _, want := range []string{"first", "second", "after #", "checked by d1: failed, exit 2", "check-result bad", "FAIL", "lead", "Recent activity", "created"} {
		if !strings.Contains(page, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>alert") {
		t.Fatal("task text became live markup")
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("the task text is not shown, escaped")
	}
	if strings.Contains(page, "Every task is done") {
		t.Fatal("a job with open tasks is offered for closing")
	}

	// Everything done: the job asks to be closed, in the job page and in the inbox.
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/reject", t1.ID), api.ReasonReq{Reason: "fix the test"}, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:def"}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t2.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t2.ID), api.SubmitReq{Evidence: []string{"commit:ghi"}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t2.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t2.ID), nil, nil)
	page = e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	if !strings.Contains(page, "Every task is done") || !strings.Contains(page, "Close the job as done") {
		t.Fatalf("a finished job is not offered for closing")
	}
	inbox := e.req("GET", "/inbox", nil, c, nil).body
	if !strings.Contains(inbox, "Job #1 Fix") || !strings.Contains(inbox, "Open the job") {
		t.Fatalf("the inbox does not ask about the finished job: %s", inbox)
	}
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/close", j.ID), url.Values{"csrf": {tok}}, c, nil); r.status != 303 || r.header.Get("Location") != fmt.Sprintf("/jobs/%d?done=closed", j.ID) {
		t.Fatalf("close: %d %s", r.status, r.body)
	}
	if body := e.req("GET", "/inbox", nil, c, nil).body; strings.Contains(body, "Open the job") {
		t.Fatal("a closed job is still in the inbox")
	}
	var got api.Job
	e.apiOK(e.admin, "", "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	if got.Status != "done" {
		t.Fatalf("job: %+v", got)
	}
}

func TestResumeAndCancelFromTheJobPage(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	e.apiOK(ag.device, "", "POST", "/v1/agents", api.RegisterReq{Name: "spare", Kind: "shell"}, nil)
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead"}, &j)
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "work"}, nil)

	page := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	if !strings.Contains(page, `<option value="spare">`) || !strings.Contains(page, "Cancel the job") {
		t.Fatalf("manage section: %s", page)
	}
	path := fmt.Sprintf("/jobs/%d/resume", j.ID)
	if r := e.req("POST", path, url.Values{"csrf": {tok}, "lead": {"nobody"}}, c, nil); r.status != 404 || !strings.Contains(r.body, "no agent") {
		t.Fatalf("resume with a bad agent: %d", r.status)
	}
	if r := e.req("POST", path, url.Values{"csrf": {tok}, "lead": {"spare"}}, c, nil); r.status != 303 {
		t.Fatalf("resume: %d %s", r.status, r.body)
	}
	var got api.Job
	e.apiOK(e.admin, "", "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	if got.Lead != "spare" {
		t.Fatalf("lead after resume: %+v", got)
	}
	// Cancelling needs the box; an unfinished task blocks a plain close.
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/close", j.ID), url.Values{"csrf": {tok}}, c, nil); r.status != 409 || !strings.Contains(r.body, "unfinished") {
		t.Fatalf("closing with work left: %d", r.status)
	}
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/close", j.ID), url.Values{"csrf": {tok}, "cancel": {"1"}}, c, nil); r.status != 303 {
		t.Fatalf("cancel: %d", r.status)
	}
	e.apiOK(e.admin, "", "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &got)
	if got.Status != "cancelled" || got.Tasks.Cancelled != 1 {
		t.Fatalf("job: %+v", got)
	}
}

// The digest is for clients that read JSON: lists are lists, never null.
func TestTheDigestHasNoNullLists(t *testing.T) {
	e := newWebEnv(t, Options{})
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/digest", nil)
	req.Header.Set("Authorization", "Bearer "+e.admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "null") {
		t.Fatalf("null in the digest: %s", raw)
	}
}
