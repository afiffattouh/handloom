package hub

import (
	"encoding/json"
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
	for _, want := range []string{"Fix the parser", "Job started", "/srv/app", "./check.sh", "app.go is broken", "No tasks yet"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if r := e.req("GET", "/jobs", nil, c, nil); !strings.Contains(r.body, "Fix the parser") || !strings.Contains(r.body, "d1") {
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
	// The test machines are added with the admin token, so they are shared by the owner (D108): give them to this member.
	if _, err := e.hub.db.Exec(`UPDATE device SET owner_id = (SELECT id FROM human WHERE name = 'apiuser') WHERE owner_id IS NULL`); err != nil {
		e.t.Fatal(err)
	}
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
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc " + evil}, Note: "done"}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 2, Tail: "FAIL " + evil}, nil)

	page := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	for _, want := range []string{"first", "second", "after #", "failed, exit 2", "badge destructive", "FAIL", "lead", "Recent activity", "created"} {
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
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:def"}, Note: "done"}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t2.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t2.ID), api.SubmitReq{Evidence: []string{"commit:ghi"}, Note: "done"}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t2.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t2.ID), nil, nil)
	page = e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	if !strings.Contains(page, "Every task is done") || !strings.Contains(page, "Finish the job") {
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

func TestOnlyTheDeviceListsAndReportsMerges(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	code := func(tok, method, path string, body any) int {
		b, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, tok := range []string{e.admin, e.humanAPI()} {
		if c := code(tok, "GET", "/v1/device/merges", nil); c != 403 {
			t.Fatalf("a non-device lists merges: %d", c)
		}
		if c := code(tok, "POST", "/v1/merges/1/report", api.MergeReport{Status: "merged"}); c != 403 {
			t.Fatalf("a non-device reports a merge: %d", c)
		}
	}
}

func TestTheInboxShowsWhatIsLeftToSetUp(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, _ := e.owner()
	body := e.req("GET", "/inbox", nil, c, nil).body
	for _, want := range []string{"Get started", "Join a computer", "/devices#add", "Add profiles", "Start your first job"} {
		if !strings.Contains(body, want) {
			t.Errorf("a new hub's inbox lacks %q", want)
		}
	}
	e.agents() // a device joins
	body = e.req("GET", "/inbox", nil, c, nil).body
	if !strings.Contains(body, "Get started") || strings.Contains(body, "Add a device") || !strings.Contains(body, "starter library") {
		t.Fatalf("after a device joined: %s", body)
	}
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "x", Lead: "lead"}, nil)
	if body = e.req("GET", "/inbox", nil, c, nil).body; strings.Contains(body, "Get started") {
		t.Fatal("the checklist stays after everything is done")
	}
}

func TestAViewerDoesNotSeeTheSetupChecklist(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.owner()
	vc, _ := e.member("vera", "viewer")
	if body := e.req("GET", "/inbox", nil, vc, nil).body; strings.Contains(body, "Get started") {
		t.Fatal("a viewer is told to set things up")
	}
}

func TestTheJobFormTakesAKnowledgeRepositoryAndABase(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	c, tok := e.owner()
	form := e.req("GET", "/jobs/new", nil, c, nil)
	if !strings.Contains(form.body, `name="knowledge"`) || !strings.Contains(form.body, `name="base"`) {
		t.Fatal("the form lacks the knowledge and base fields")
	}
	post := func(k, b string) webResp {
		return e.req("POST", "/jobs", url.Values{"title": {"x"}, "device": {"d1"}, "repo": {"/srv/app"}, "knowledge": {k}, "base": {b}, "lead_profile": {"boss"}, "csrf": {tok}}, c, nil)
	}
	if r := post("clients/acme", ""); r.status != 400 || !strings.Contains(r.body, "knowledge repository must be an absolute path") {
		t.Fatalf("relative knowledge path: %d", r.status)
	}
	if r := post("", "--upload-pack=x"); r.status != 400 || !strings.Contains(r.body, "base must be a branch name") {
		t.Fatalf("a base that looks like a git option: %d", r.status)
	}
	if r := post("/srv/acme", "main"); r.status != 303 {
		t.Fatalf("valid: %d %s", r.status, r.body)
	}
	page := e.req("GET", "/jobs/1", nil, c, nil).body
	if !strings.Contains(page, "/srv/acme") || !strings.Contains(page, "branch <code>main</code>") || !strings.Contains(page, "you merge them yourself") {
		t.Fatalf("job page: %s", page)
	}
}

func TestTheJobPageShowsEachTasksLatestHandoffNote(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	var j api.Job
	person := e.humanAPI()
	e.apiOK(person, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Notes", Lead: "lead"}, &j)
	var task api.Task
	e.apiOK(person, "", "POST", "/v1/tasks", api.TaskCreateReq{Title: "migrate", Job: j.ID, AssignedTo: "worker"}, &task)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", task.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/handoff", task.ID), api.HandoffReq{Done: "tables created", Next: "backfill in batches", Verify: "select count(*)"}, nil)
	page := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	for _, want := range []string{"Handoff", "tables created", "backfill in batches", "select count(*)"} {
		if !strings.Contains(page, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
}

func TestTheJobPageShowsWhatAKitsCheckLookedAtAndTheOverviewCanStopAJob(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Site", Lead: "lead", Repo: "/srv/app", Verify: "./check", Device: "d1"}, &j)
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "fix links", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "done"}, nil)
	ev := &api.CheckEvidence{Result: "blocked", Reason: "the agent wrote BLOCKED.md", Blocked: "needs <b>a decision</b>",
		Stages: []api.CheckStage{{Name: "web-links", Status: "pass"}, {Name: "web-images", Status: "fail", Detail: "2 missing alt"}}}
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check", ExitCode: 3, Evidence: ev}, nil)

	page := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	for _, want := range []string{"a person decides", "What the project's check looked at", "web-images", "2 missing alt", "needs &lt;b&gt;a decision&lt;/b&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if strings.Contains(page, "<b>a decision</b>") {
		t.Error("the agent's note is not escaped")
	}
	ov := e.req("GET", "/command", nil, c, nil).body
	if !strings.Contains(ov, fmt.Sprintf(`action="/jobs/%d/close"`, j.ID)) || !strings.Contains(ov, "Stop job #") {
		t.Error("the Overview has no Stop for an open job")
	}
}

func TestOneSentenceAskUsesTheProjectsMachineAndCheck(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	_ = ag
	hum := e.humanAPI()
	e.apiOK(hum, "", "POST", "/v1/codebases", api.CodebaseReq{Name: "site", Remote: "git@github.com:acme/site.git"}, nil)
	e.apiOK(hum, "", "POST", "/v1/codebases/site/check", api.CodebaseCheckReq{Check: "./check"}, nil)
	var cb api.Codebase
	e.apiOK(hum, "", "GET", "/v1/codebases/site", nil, &cb)
	if cb.Check != "./check" {
		t.Fatalf("check not kept: %+v", cb)
	}
	// A multi-line command is refused.
	bad, _ := json.Marshal(api.CodebaseCheckReq{Check: "a\nb"})
	rq, _ := http.NewRequest("POST", e.srv.URL+"/v1/codebases/site/check", strings.NewReader(string(bad)))
	rq.Header.Set("Authorization", "Bearer "+hum)
	if resp, err := http.DefaultClient.Do(rq); err != nil || resp.StatusCode != 400 {
		t.Fatalf("a multi-line check: %v %v", err, resp)
	}
	// A machine's copy is ready, so the project is on offer.
	if _, err := e.hub.db.Exec(`INSERT INTO codebase_clone(codebase_id, device_id, path, status, created_at, updated_at) SELECT cb.id, d.id, '/srv/site', 'ready', 1, 1 FROM codebase cb, device d WHERE cb.name = 'site' AND d.name = 'd1'`); err != nil {
		t.Fatal(err)
	}
	page := e.req("GET", "/jobs/new", nil, c, nil).body
	if !strings.Contains(page, "Just ask") || !strings.Contains(page, "site on d1") {
		t.Fatalf("the quick ask is not offered:\n%s", page)
	}
	r := e.req("POST", "/jobs/ask", url.Values{"csrf": {tok}, "ask": {"Change the opening hours on the contact page. Use 9 to 5."}, "project": {"site|d1"}, "team": {"1"}, "steps_first": {"1"}}, c, nil)
	if r.status != 303 {
		t.Fatalf("ask: %d %s", r.status, r.body)
	}
	var jobs []api.Job
	e.apiOK(hum, "", "GET", "/v1/jobs", nil, &jobs)
	if len(jobs) != 1 || jobs[0].Verify != "./check" || jobs[0].Title != "Change the opening hours on the contact page" || !strings.Contains(jobs[0].Body, "wait for the answer") {
		t.Fatalf("job: %+v", jobs)
	}
}

// Without "have a lead plan it", an ask is one job, one task and one worker: no lead is started.
func TestAskWithoutALeadStartsOneWorkerOnOneTask(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	c, tok := e.owner()
	hum := e.humanAPI()
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	e.apiOK(hum, "", "POST", "/v1/codebases", api.CodebaseReq{Name: "site", Remote: "git@github.com:acme/site.git"}, nil)
	e.apiOK(hum, "", "POST", "/v1/codebases/site/check", api.CodebaseCheckReq{Check: "./check"}, nil)
	if _, err := e.hub.db.Exec(`INSERT INTO codebase_clone(codebase_id, device_id, path, status, created_at, updated_at) SELECT cb.id, d.id, '/srv/site', 'ready', 1, 1 FROM codebase cb, device d WHERE cb.name = 'site' AND d.name = 'd1'`); err != nil {
		t.Fatal(err)
	}
	page := e.req("GET", "/jobs/new", nil, c, nil).body
	if !strings.Contains(page, `name="team"`) || !strings.Contains(page, "Have a lead plan it") {
		t.Fatal("the form does not offer a lead as the option")
	}
	r := e.req("POST", "/jobs/ask", url.Values{"csrf": {tok}, "ask": {"Add an About page. Two sentences."}, "project": {"site|d1"}}, c, nil)
	if r.status != 303 {
		t.Fatalf("ask: %d %s", r.status, r.body)
	}
	var jobs []api.Job
	e.apiOK(hum, "", "GET", "/v1/jobs", nil, &jobs)
	if len(jobs) != 1 || jobs[0].Verify != "./check" || jobs[0].Title != "Add an About page" {
		t.Fatalf("job: %+v", jobs)
	}
	var tasks []api.Task
	e.apiOK(hum, "", "GET", fmt.Sprintf("/v1/tasks?job=%d", jobs[0].ID), nil, &tasks)
	if len(tasks) != 1 || tasks[0].Title != "Add an About page" || !strings.Contains(tasks[0].Body, "Do not invent facts") || tasks[0].Status != api.StatusOpen {
		t.Fatalf("tasks: %+v", tasks)
	}
	var sp []api.Spawn
	e.apiOK(hum, "", "GET", "/v1/spawns", nil, &sp)
	if len(sp) != 1 || sp[0].Role != api.RoleWorker || !strings.HasPrefix(sp[0].Profile, "boss") || sp[0].Job == nil || *sp[0].Job != jobs[0].ID {
		t.Fatalf("spawns: %+v", sp)
	}
	if body := e.req("GET", fmt.Sprintf("/jobs/%d", jobs[0].ID), nil, c, nil).body; !strings.Contains(body, "Add an About page") {
		t.Fatalf("the job page: %.400s", body)
	}
}

func TestASealedKitTaskNamedOnTheFormSetsTheBaseAndTheCheck(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	c, tok := e.owner()
	hum := e.humanAPI()
	bad := e.req("POST", "/jobs", url.Values{"csrf": {tok}, "title": {"x"}, "repo": {"/srv/app"}, "device": {"d1"}, "kit_task": {"../evil"}}, c, nil)
	if bad.status != 400 {
		t.Fatalf("a bad name: %d", bad.status)
	}
	r := e.req("POST", "/jobs", url.Values{"csrf": {tok}, "title": {"Fix the parser"}, "repo": {"/srv/app"}, "device": {"d1"}, "kit_task": {"parser-fix"}}, c, nil)
	if r.status != 303 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var jobs []api.Job
	e.apiOK(hum, "", "GET", "/v1/jobs", nil, &jobs)
	if len(jobs) != 1 || jobs[0].Base != "task/parser-fix-base" || jobs[0].Verify != "./check" {
		t.Fatalf("job: %+v", jobs)
	}
}

func (e *webEnv) apiTask(token string, id int64) api.Task {
	var tk api.Task
	e.apiOK(token, "", "GET", fmt.Sprintf("/v1/tasks/%d", id), nil, &tk)
	return tk
}

func TestTheJobPageShowsWhatChangedAndLetsAPersonActOnIt(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	hum := e.humanAPI()
	var j api.Job
	e.apiOK(hum, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Site", Lead: "lead", Repo: "/srv/app", Device: "d1"}, &j)
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "bakery pages", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "done"}, nil)
	page := `<html><head><title>Home</title></head><body><script>alert(1)</script><a href="contact.html">Contact</a></body></html>`
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/result", t1.ID), api.TaskResultReq{Agent: "worker",
		Files: []api.ResultFile{{Path: "index.html", Status: "added", Added: 5}, {Path: "old.txt", Status: "deleted", Deleted: 2}},
		Diff:  "diff --git a/index.html b/index.html\n--- /dev/null\n+++ b/index.html\n@@ -0,0 +1 @@\n+<h1>Hello <b>x</b></h1>\n-old\n",
		Pages: map[string]string{"index.html": page, "../evil.html": "x", "notes.txt": "x"}}, nil)

	body := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body
	for _, want := range []string{"What changed", "index.html", "old.txt", "Open index.html", "See every change", `class="dl add"`, `class="dl del"`, "&lt;h1&gt;Hello", "Use this result", "Send back with a note", "Stop this task"} {
		if !strings.Contains(body, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if strings.Contains(body, "<h1>Hello") || strings.Contains(body, "evil.html") {
		t.Errorf("not escaped / bad path kept: %v %v", strings.Contains(body, "<h1>Hello"), strings.Contains(body, "evil.html"))
	}
	// The page opens under a policy that gives it nothing, with links between pages working.
	pg := e.req("GET", fmt.Sprintf("/jobs/%d/tasks/%d/page/index.html", j.ID, t1.ID), nil, c, nil)
	csp := pg.header.Get("Content-Security-Policy")
	if pg.status != 200 || !strings.HasPrefix(csp, "sandbox;") || strings.Contains(csp, "script-src") || !strings.Contains(pg.body, `<base href="/jobs/`) || !strings.Contains(pg.body, "Contact") {
		t.Fatalf("page: %d %q\n%s", pg.status, csp, pg.body)
	}
	if r := e.req("GET", fmt.Sprintf("/jobs/%d/tasks/%d/page/notes.txt", j.ID, t1.ID), nil, c, nil); r.status != 404 {
		t.Errorf("a file that is not a page: %d", r.status)
	}
	if r := e.req("GET", fmt.Sprintf("/jobs/%d/tasks/%d/page/index.html", j.ID+5, t1.ID), nil, c, nil); r.status != 404 {
		t.Errorf("a task of another job: %d", r.status)
	}

	// Send back with a note, from the job page; the result of the rejected work is gone.
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/tasks/%d/reject", j.ID, t1.ID), url.Values{"csrf": {tok}, "reason": {"remove the phone number"}}, c, nil); r.status != 303 {
		t.Fatalf("send back: %d %s", r.status, r.body)
	}
	if got := e.apiTask(hum, t1.ID); got.Status != "claimed" || got.RejectReason != "remove the phone number" || got.Rejects != 1 {
		t.Fatalf("after send back: %+v", got)
	}
	if body := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body; strings.Contains(body, "See every change") {
		t.Error("the result of rejected work is still shown")
	}
	// Submit again, use it from the job page.
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:def"}, Note: "fixed"}, nil)
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/tasks/%d/accept", j.ID, t1.ID), url.Values{"csrf": {tok}}, c, nil); r.status != 303 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if got := e.apiTask(hum, t1.ID); got.Status != "done" {
		t.Fatalf("after accept: %+v", got)
	}
	// A task that is another job's cannot be reached through this job.
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/tasks/%d/cancel", j.ID+5, t1.ID), url.Values{"csrf": {tok}}, c, nil); r.status == 303 {
		t.Error("a task was stopped through the wrong job")
	}
}

func TestATaskCanBeGivenToANewHelperFromAnotherProfile(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	hum := e.humanAPI()
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	var j api.Job
	e.apiOK(hum, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Site", Lead: "lead", Repo: "/srv/app", Device: "d1"}, &j)
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "bakery pages", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "done"}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/result", t1.ID), api.TaskResultReq{Agent: "worker", Files: []api.ResultFile{{Path: "a.txt", Status: "added"}}, Diff: "+x\n"}, nil)

	if body := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body; !strings.Contains(body, "Try again with another profile") || !strings.Contains(body, `<option value="boss"`) {
		t.Fatal("the job page does not offer another profile")
	}
	// A profile that does not exist changes nothing.
	r := e.req("POST", fmt.Sprintf("/jobs/%d/tasks/%d/reassign", j.ID, t1.ID), url.Values{"csrf": {tok}, "profile": {"nope"}}, c, nil)
	if r.status == 303 || e.apiTask(hum, t1.ID).Status != "submitted" {
		t.Fatalf("a missing profile: %d", r.status)
	}
	r = e.req("POST", fmt.Sprintf("/jobs/%d/tasks/%d/reassign", j.ID, t1.ID), url.Values{"csrf": {tok}, "profile": {"boss"}, "note": {"use two files only"}}, c, nil)
	if r.status != 303 {
		t.Fatalf("reassign: %d %s", r.status, r.body)
	}
	got := e.apiTask(hum, t1.ID)
	if got.Status != "open" || got.Owner != "" || got.Check != nil {
		t.Fatalf("after: %+v", got)
	}
	rq, _ := http.NewRequest("GET", e.srv.URL+fmt.Sprintf("/v1/tasks/%d/result", t1.ID), nil)
	rq.Header.Set("Authorization", "Bearer "+hum)
	if resp, err := http.DefaultClient.Do(rq); err != nil || resp.StatusCode != 404 {
		t.Fatalf("the old result is still there: %v %v", err, resp)
	}
	var msgs []api.Message
	ag.worker("GET", "/v1/inbox", nil, &msgs)
	told := false
	for _, m := range msgs {
		told = told || strings.Contains(m.Body, "given to another helper")
	}
	if !told {
		t.Error("the old helper is not told to stop")
	}
	// The new helper is waiting to be started, named for the profile and the task.
	var spawns []api.Spawn
	e.apiOK(hum, "", "GET", "/v1/spawns", nil, &spawns)
	found := false
	for _, s := range spawns {
		found = found || (s.Name == fmt.Sprintf("boss-t%d", t1.ID) && strings.HasPrefix(s.Profile, "boss"))
	}
	if !found {
		t.Fatalf("no spawn for the new helper: %+v", spawns)
	}
	// When it registers, the task is its.
	e.apiOK(ag.device, "", "POST", "/v1/agents", api.RegisterReq{Name: fmt.Sprintf("boss-t%d", t1.ID), Kind: "claude"}, nil)
	if got := e.apiTask(hum, t1.ID); got.AssignedTo != fmt.Sprintf("boss-t%d", t1.ID) {
		t.Fatalf("not assigned on registration: %+v", got)
	}
	// The link nudges a helper that has unread mail: the new helper must have its task in its inbox.
	var da []api.DeviceAgent
	e.apiOK(ag.device, "", "GET", "/v1/device/agents", nil, &da)
	for _, a := range da {
		if a.Name == fmt.Sprintf("boss-t%d", t1.ID) && a.Unread == 0 {
			t.Fatalf("the new helper has no unread message, so nothing wakes it: %+v", a)
		}
	}
}
