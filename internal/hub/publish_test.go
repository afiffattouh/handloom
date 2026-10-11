package hub

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
)

// mergedJob starts a repo job on d1 with one accepted task whose merge the device reported as done.
func (e *env) mergedJob(title string) api.Job {
	e.t.Helper()
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: title, Body: "Make the parser stricter.", Repo: "/srv/app", Device: "d1", Lead: "lead"}, &j)
	var task api.Task
	e.ok(e.lead(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "tighten it", AssignedTo: "worker"}, &task)
	e.ok(e.worker(), "POST", taskPath(task.ID, "claim"), nil, nil)
	e.ok(e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"secret evidence text"}, Note: "done"}, nil)
	e.ok(e.afif(), "POST", taskPath(task.ID, "accept"), nil, nil)
	var ms []api.PendingMerge
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/merges", nil, &ms)
	for _, m := range ms {
		if m.Job == j.ID {
			e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/merges/%d/report", m.ID), api.MergeReport{Status: "merged", Head: "abc"}, nil)
		}
	}
	return j
}

func TestPublishingALifecycleAndWhoMayAsk(t *testing.T) {
	e := newEnv(t)
	// A job whose work is not merged yet, and one that has no repository: refused in words.
	var early, plain api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Early", Repo: "/srv/early", Device: "d1"}, &early)
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "No repo", Lead: "lead"}, &plain)
	if msg := e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", early.ID), api.PublishReq{}); !strings.Contains(msg, "nothing to publish") {
		t.Fatalf("a job without merged work: %s", msg)
	}
	if msg := e.fail(400, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", plain.ID), api.PublishReq{}); !strings.Contains(msg, "git repository") {
		t.Fatalf("a job without a repository: %s", msg)
	}

	j := e.mergedJob("Stricter parser: don't accept `;`!")
	path := fmt.Sprintf("/v1/jobs/%d/publish", j.ID)

	// Viewer, agents, the admin token and an AI app may not ask.
	var viewer, op api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "vera"}, &viewer)
	e.hub.db.Exec(`UPDATE human SET role = 'viewer' WHERE name = 'vera'`)
	e.ok(e.afif(), "POST", "/v1/me/operator-token", nil, &op)
	for name, c := range map[string]caller{"viewer": {viewer.Token, ""}, "lead": e.lead(), "worker": e.worker(), "observer": e.observer(), "admin": {e.admin, ""}, "ai app": {op.Token, ""}} {
		if code, _ := e.do(c, "POST", path, api.PublishReq{}); code != 403 {
			t.Errorf("%s asked to publish: status %d, want 403", name, code)
		}
	}
	var none []api.Publish
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d/publishes", j.ID), nil, &none)
	if len(none) != 0 {
		t.Fatalf("a refused request left %d publishes", len(none))
	}
	// A bad remote name is refused.
	e.fail(400, e.afif(), "POST", path, api.PublishReq{Remote: "--upload-pack=x"})

	// A member may ask; the request waits for the device.
	var mem api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "mia"}, &mem)
	e.hub.db.Exec(`UPDATE human SET role = 'member' WHERE name = 'mia'`)
	var p api.Publish
	e.ok(caller{mem.Token, ""}, "POST", path, api.PublishReq{PR: true}, &p)
	if p.Status != "pending" || p.Remote != "origin" || !p.PR || p.RequestedBy == "" || p.Branch != fmt.Sprintf("handloom/job-%d-stricter-parser-don-t-accept", j.ID) {
		t.Fatalf("request: %+v", p)
	}
	if !e.audited("job.publish", fmt.Sprintf("job:%d", j.ID)) {
		t.Fatal("job.publish is not in the audit log")
	}
	if msg := e.fail(409, e.afif(), "POST", path, api.PublishReq{}); !strings.Contains(msg, "already waiting") {
		t.Fatalf("a second request while one waits: %s", msg)
	}

	// The device sees it with what it needs: the brief and the accepted tasks, never evidence.
	var pend []api.PendingPublish
	e.fail(403, e.afif(), "GET", "/v1/device/publishes", nil)
	e.ok(caller{e.d2, ""}, "GET", "/v1/device/publishes", nil, &pend)
	if len(pend) != 0 {
		t.Fatalf("another device sees the request: %+v", pend)
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/publishes", nil, &pend)
	if len(pend) != 1 || pend[0].Repo != "/srv/app" || pend[0].Body != "Make the parser stricter." || len(pend[0].Tasks) != 1 || pend[0].Tasks[0].Owner != "worker" || pend[0].Tasks[0].Title != "tighten it" {
		t.Fatalf("pending: %+v", pend)
	}
	if raw := fmt.Sprintf("%+v", pend); strings.Contains(raw, "secret evidence") {
		t.Fatal("evidence reached the publish request")
	}

	// Only the job's device reports, and only once.
	report := fmt.Sprintf("/v1/publishes/%d/report", p.ID)
	rep := api.PublishReport{Status: "done", URL: "https://example.test/pr/1", Behind: 3, Detail: "pushed"}
	e.fail(403, e.afif(), "POST", report, rep)
	e.fail(403, caller{e.d2, ""}, "POST", report, rep)
	e.fail(400, caller{e.d1, ""}, "POST", report, api.PublishReport{Status: "merged"})
	e.fail(404, caller{e.d1, ""}, "POST", "/v1/publishes/999/report", rep)
	e.ok(caller{e.d1, ""}, "POST", report, rep, nil)
	e.fail(409, caller{e.d1, ""}, "POST", report, rep)
	if !e.audited("job.published", fmt.Sprintf("job:%d", j.ID)) {
		t.Fatal("job.published is not in the audit log")
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/publishes", nil, &pend)
	if len(pend) != 0 {
		t.Fatalf("a reported publish is still pending: %+v", pend)
	}

	// Newest first; a failure is recorded and audited; an AI app and an agent may read the list.
	var p2 api.Publish
	e.ok(e.afif(), "POST", path, api.PublishReq{Remote: "upstream"}, &p2)
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/report", p2.ID), api.PublishReport{Status: "failed", Detail: "rejected"}, nil)
	if !e.audited("job.publish_failed", fmt.Sprintf("job:%d", j.ID)) {
		t.Fatal("job.publish_failed is not in the audit log")
	}
	var list []api.Publish
	for _, c := range []caller{e.afif(), {op.Token, ""}, e.worker()} {
		e.ok(c, "GET", fmt.Sprintf("/v1/jobs/%d/publishes", j.ID), nil, &list)
		if len(list) != 2 || list[0].ID != p2.ID || list[0].Status != "failed" || list[0].Detail != "rejected" || list[1].URL != "https://example.test/pr/1" || list[1].Behind != 3 {
			t.Fatalf("list: %+v", list)
		}
	}
}

func TestPublishBranchNames(t *testing.T) {
	for _, c := range []struct {
		id    int64
		title string
		want  string
	}{
		{3, "Fix the Parser", "handloom/job-3-fix-the-parser"},
		{4, "  --Weird__title!! ", "handloom/job-4-weird-title"},
		{5, "Überprüfung", "handloom/job-5-berpr-fung"},
		{6, "日本語", "handloom/job-6"},
		{7, strings.Repeat("a", 60), "handloom/job-7-" + strings.Repeat("a", 40)},
		{8, strings.Repeat("ab ", 20), "handloom/job-8-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-a"},
	} {
		if got := publishBranch(c.id, c.title); got != c.want {
			t.Errorf("%q: %q, want %q", c.title, got, c.want)
		}
	}
}

func TestOpenJobsOnTheSameRepositoryWarnEachOther(t *testing.T) {
	e := newEnv(t)
	var a, b, c, other, otherDev, closed api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "First", Repo: "/srv/app", Device: "d1", Lead: "lead"}, &a)
	if len(a.Overlaps) != 0 {
		t.Fatalf("the first job overlaps with %+v", a.Overlaps)
	}
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Other repository", Repo: "/srv/elsewhere", Device: "d1"}, &other)
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Other machine", Repo: "/srv/app", Device: "d2"}, &otherDev)
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Closed one", Repo: "/srv/app", Device: "d1"}, &closed)
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", closed.ID), api.JobCloseReq{Cancel: true}, nil)
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Second", Repo: "/srv/app", Device: "d1"}, &b)
	if len(b.Overlaps) != 1 || b.Overlaps[0].ID != a.ID || b.Overlaps[0].Title != "First" || b.Overlaps[0].Lead != "lead" {
		t.Fatalf("second job: %+v", b.Overlaps)
	}
	if got := api.OverlapText(b.Overlaps); !strings.Contains(got, "Also open on this repository: #1 First (lead lead).") || !strings.Contains(got, "will conflict when their branches are merged") {
		t.Fatalf("text: %q", got)
	}
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Third", Repo: "/srv/app", Device: "d1"}, &c)
	if len(c.Overlaps) != 2 {
		t.Fatalf("third job: %+v", c.Overlaps)
	}
	// The first job now sees both, on a plain read too; the others see nothing.
	var got api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", a.ID), nil, &got)
	if len(got.Overlaps) != 2 {
		t.Fatalf("first job later: %+v", got.Overlaps)
	}
	for _, id := range []int64{other.ID, otherDev.ID} {
		got = api.Job{}
		e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", id), nil, &got)
		if len(got.Overlaps) != 0 {
			t.Fatalf("job %d: %+v", id, got.Overlaps)
		}
	}
	// A finished job does not warn, and is not warned about.
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", b.ID), api.JobCloseReq{Cancel: true}, nil)
	got = api.Job{}
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", b.ID), nil, &got)
	if len(got.Overlaps) != 0 {
		t.Fatalf("closed job: %+v", got.Overlaps)
	}
	got = api.Job{}
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", c.ID), nil, &got)
	if len(got.Overlaps) != 1 || got.Overlaps[0].ID != a.ID {
		t.Fatalf("third job after the second closed: %+v", got.Overlaps)
	}
}

func TestThePublishCardOnTheJobPage(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	api_ := e.humanAPI()
	var j api.Job
	e.apiOK(api_, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Stricter parser", Body: "Be strict.", Lead: "lead", Repo: "/srv/app", Device: "d1"}, &j)
	e.apiOK(api_, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Parallel work", Repo: "/srv/app", Device: "d1"}, nil)
	page := func() string { return e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, c, nil).body }

	// Nothing merged: a card that says why there is no button.
	if p := page(); !strings.Contains(p, "Nothing to publish yet") || strings.Contains(p, "Push branch") {
		t.Fatalf("before any merge: %s", p)
	}
	if p := page(); !strings.Contains(p, "Also open on this repository: #2 Parallel work") {
		t.Fatalf("the overlap warning is missing from the job page")
	}
	post := e.req("POST", fmt.Sprintf("/jobs/%d/publish", j.ID), url.Values{"csrf": {tok}}, c, nil)
	if post.status != 409 || !strings.Contains(post.body, "nothing to publish") {
		t.Fatalf("publishing without merged work: %d", post.status)
	}

	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "first", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "done"}, nil)
	e.apiOK(api_, "", "POST", fmt.Sprintf("/v1/tasks/%d/accept", t1.ID), nil, nil)
	var ms []api.PendingMerge
	e.apiOK(ag.device, "", "GET", "/v1/device/merges", nil, &ms)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/merges/%d/report", ms[0].ID), api.MergeReport{Status: "merged"}, nil)

	p := page()
	for _, want := range []string{"Push branch", "Push and open pull request", fmt.Sprintf(`action="/jobs/%d/publish"`, j.ID), `name="pr"`, `name="csrf"`} {
		if !strings.Contains(p, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if strings.Contains(p, "<script") && strings.Contains(p, "onclick") {
		t.Error("inline script on the job page")
	}
	// A request without the token is refused; with it, it redirects with a notice.
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/publish", j.ID), url.Values{}, c, nil); r.status != 403 {
		t.Fatalf("no CSRF token: %d", r.status)
	}
	r := e.req("POST", fmt.Sprintf("/jobs/%d/publish", j.ID), url.Values{"csrf": {tok}, "pr": {"1"}}, c, nil)
	if r.status != 303 || r.header.Get("Location") != fmt.Sprintf("/jobs/%d?done=published", j.ID) {
		t.Fatalf("publish: %d %s", r.status, r.body)
	}
	if p := e.req("GET", r.header.Get("Location"), nil, c, nil).body; !strings.Contains(p, "Publishing asked for") || !strings.Contains(p, "Waiting") || !strings.Contains(p, "with a pull request") {
		t.Fatalf("after asking: %s", p)
	}
	var pend []api.PendingPublish
	e.apiOK(ag.device, "", "GET", "/v1/device/publishes", nil, &pend)
	if len(pend) != 1 || !pend[0].PR {
		t.Fatalf("pending: %+v", pend)
	}
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/publishes/%d/report", pend[0].ID), api.PublishReport{Status: "done", URL: "https://github.com/acme/app/pull/7", Behind: 2, Detail: "pushed"}, nil)
	p = page()
	for _, want := range []string{"Pushed", pend[0].Branch, `href="https://github.com/acme/app/pull/7"`, "The base moved: 2 new commits on origin"} {
		if !strings.Contains(p, want) {
			t.Errorf("after the report the page lacks %q", want)
		}
	}
	// A link that is not https is text, never a link.
	r = e.req("POST", fmt.Sprintf("/jobs/%d/publish", j.ID), url.Values{"csrf": {tok}}, c, nil)
	e.apiOK(ag.device, "", "GET", "/v1/device/publishes", nil, &pend)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/publishes/%d/report", pend[0].ID), api.PublishReport{Status: "failed", URL: "javascript:alert(1)", Detail: "the push was refused <b>x</b>"}, nil)
	p = page()
	if strings.Contains(p, `href="javascript:`) || !strings.Contains(p, "javascript:alert(1)") || strings.Contains(p, "<b>x</b>") || !strings.Contains(p, "Failed") {
		t.Fatalf("failed publish: %s", p)
	}

	// A viewer sees the history and no buttons, and cannot post.
	vc, vtok := e.member("vera", "viewer")
	if vp := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, vc, nil).body; strings.Contains(vp, "Push branch") || !strings.Contains(vp, "Pushed") {
		t.Fatalf("a viewer's job page: %s", vp)
	}
	if r := e.req("POST", fmt.Sprintf("/jobs/%d/publish", j.ID), url.Values{"csrf": {vtok}}, vc, nil); r.status != 403 {
		t.Fatalf("a viewer publishes: %d", r.status)
	}
	// The overlap notice appears in the redirect after starting a job.
	if n := e.req("GET", "/jobs/2?done=started", nil, c, nil).body; !strings.Contains(n, "Job started.") || !strings.Contains(n, "#1 Stricter parser") {
		t.Fatalf("start notice: %s", n)
	}
}
