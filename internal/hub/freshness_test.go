package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

// projectJob starts a job from a project that machine d1 has a ready copy of.
func (e *env) projectJob(title string) api.Job {
	e.t.Helper()
	var cbs []api.Codebase
	e.ok(e.afif(), "GET", "/v1/codebases", nil, &cbs)
	if len(cbs) == 0 {
		e.ok(e.afif(), "POST", "/v1/codebases", api.CodebaseReq{Name: "billing", Remote: "https://github.com/acme/billing", Base: "main"}, nil)
		var q api.CodebaseRequest
		e.ok(e.afif(), "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
		e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/project-requests/%d/report", q.ID), api.CodebaseReport{Status: "done", Path: "/home/x/projects/billing"}, nil)
	}
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: title, Codebase: "billing", Device: "d1"}, &j)
	return j
}

func TestTheDeviceReportsHowTheBaseWent(t *testing.T) {
	e := newEnv(t)
	j := e.projectJob("Add greeting")
	var plain api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Plain", Repo: "/srv/app", Device: "d1"}, &plain)

	// The link is told to look at the base only for a job of a project that has no record yet.
	e.spawn(e.afif(), api.SpawnReq{Name: "lead-a", Kind: "claude", Device: "d1", Job: j.ID, Role: "lead"})
	e.spawn(e.afif(), api.SpawnReq{Name: "lead-b", Kind: "claude", Device: "d1", Job: plain.ID, Role: "lead"})
	var sp []api.Spawn
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/spawns", nil, &sp)
	got := map[string]bool{}
	for _, s := range sp {
		got[s.Name] = s.FreshBase
	}
	if !got["lead-a"] || got["lead-b"] {
		t.Fatalf("fresh_base by spawn: %v", got)
	}

	path := fmt.Sprintf("/v1/jobs/%d/base", j.ID)
	rep := api.BaseReport{Status: "fresh", Commit: "abc1234", Detail: "main moved forward 2 commits to abc1234"}
	e.fail(403, e.afif(), "POST", path, rep)
	e.fail(403, caller{e.d2, ""}, "POST", path, rep)
	e.fail(400, caller{e.d1, ""}, "POST", path, api.BaseReport{Status: "great"})
	e.fail(400, caller{e.d1, ""}, "POST", path, api.BaseReport{Status: "fresh", Commit: "not a commit"})
	e.fail(400, caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/jobs/%d/base", plain.ID), rep) // not a job of a project
	e.fail(404, caller{e.d1, ""}, "POST", "/v1/jobs/999/base", rep)
	e.ok(caller{e.d1, ""}, "POST", path, rep, nil)
	e.fail(409, caller{e.d1, ""}, "POST", path, rep)
	if !e.audited("job.base", fmt.Sprintf("job:%d", j.ID)) {
		t.Fatal("job.base is not in the audit log")
	}
	var back api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &back)
	if back.BaseState != "fresh" || back.BaseCommit != "abc1234" || !strings.Contains(back.BaseDetail, "moved forward 2 commits") {
		t.Fatalf("job: %+v", back)
	}
	sp = nil
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/spawns", nil, &sp)
	for _, s := range sp {
		if s.FreshBase {
			t.Fatalf("a job whose base was recorded is asked again: %+v", s)
		}
	}
}

func TestThePullRequestOfAPublishIsReportedByItsDevice(t *testing.T) {
	e := newEnv(t)
	j := e.mergedJob("Stricter parser")
	path := fmt.Sprintf("/v1/jobs/%d/publish", j.ID)
	var nourl, p api.Publish
	e.ok(e.afif(), "POST", path, api.PublishReq{}, &nourl)
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/report", nourl.ID), api.PublishReport{Status: "done", Detail: "pushed"}, nil)
	e.ok(e.afif(), "POST", path, api.PublishReq{PR: true}, &p)
	prPath := fmt.Sprintf("/v1/publishes/%d/pr", p.ID)
	open := api.PRReport{State: "open", Review: "approved", Mergeable: "true"}

	// Not done yet, or no pull request: nothing to report on.
	e.fail(409, caller{e.d1, ""}, "POST", prPath, open)
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/report", p.ID), api.PublishReport{Status: "done", URL: "https://github.com/acme/app/pull/7", Detail: "pushed"}, nil)
	e.fail(409, caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/pr", nourl.ID), open)

	// Device-only, its own publishes only, and only known words.
	e.fail(403, e.afif(), "POST", prPath, open)
	e.fail(403, caller{e.admin, ""}, "POST", prPath, open)
	e.fail(403, caller{e.d2, ""}, "POST", prPath, open)
	e.fail(404, caller{e.d1, ""}, "POST", "/v1/publishes/999/pr", open)
	e.fail(400, caller{e.d1, ""}, "POST", prPath, api.PRReport{State: "draft"})
	e.fail(400, caller{e.d1, ""}, "POST", prPath, api.PRReport{State: "open", Review: "loved"})
	e.fail(400, caller{e.d1, ""}, "POST", prPath, api.PRReport{State: "open", Mergeable: "maybe"})

	// The device watches it until it is merged or closed.
	var watch []api.PendingPR
	e.fail(403, e.afif(), "GET", "/v1/device/pull-requests", nil)
	e.ok(caller{e.d2, ""}, "GET", "/v1/device/pull-requests", nil, &watch)
	if len(watch) != 0 {
		t.Fatalf("another device watches it: %+v", watch)
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/pull-requests", nil, &watch)
	if len(watch) != 1 || watch[0].ID != p.ID || watch[0].URL != "https://github.com/acme/app/pull/7" || watch[0].Job != j.ID {
		t.Fatalf("watch: %+v", watch)
	}

	e.ok(caller{e.d1, ""}, "POST", prPath, open, nil)
	var back api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &back)
	if back.PullRequest != "open, approved, can merge" || back.PRMerged {
		t.Fatalf("open: %+v", back)
	}
	e.ok(caller{e.d1, ""}, "POST", prPath, api.PRReport{State: "open", Review: "changes_requested", Mergeable: "false"}, nil)
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &back)
	if back.PullRequest != "open, changes requested, has conflicts" {
		t.Fatalf("open again: %q", back.PullRequest)
	}
	count := func(action string) int {
		n := 0
		for _, r := range e.audit() {
			if r.Action == action {
				n++
			}
		}
		return n
	}
	if count("publish.pr_merged")+count("publish.pr_closed") != 0 {
		t.Fatal("a pull request that is only open was audited as finished")
	}

	// Merged: audited once however often the machine says so; nothing else happens.
	e.ok(caller{e.d1, ""}, "POST", prPath, api.PRReport{State: "merged", Review: "approved", Mergeable: "unknown"}, nil)
	e.ok(caller{e.d1, ""}, "POST", prPath, api.PRReport{State: "merged"}, nil)
	if count("publish.pr_merged") != 1 || !e.audited("publish.pr_merged", fmt.Sprintf("job:%d", j.ID)) {
		t.Fatalf("pr_merged audited %d times", count("publish.pr_merged"))
	}
	e.fail(409, caller{e.d1, ""}, "POST", prPath, open) // a finished pull request does not come back
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &back)
	if back.PullRequest != "merged" || !back.PRMerged || back.Status != api.StatusOpen {
		t.Fatalf("merged: %+v", back)
	}
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/pull-requests", nil, &watch)
	if len(watch) != 0 {
		t.Fatalf("a merged pull request is still watched: %+v", watch)
	}
	var d api.Digest
	e.ok(e.afif(), "GET", "/v1/digest", nil, &d)
	found := false
	for _, a := range d.Activity {
		found = found || strings.Contains(a.Text, fmt.Sprintf("pull request merged for job %d", j.ID))
	}
	if !found {
		t.Fatalf("the activity feed does not say the pull request was merged: %+v", d.Activity)
	}
	var pubs []api.Publish
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d/publishes", j.ID), nil, &pubs)
	if pubs[0].PRState != "merged" || pubs[0].PRWords != "merged" {
		t.Fatalf("publish: %+v", pubs[0])
	}
}

func TestAClosedPullRequestIsAuditedAndAnOldOneIsNotWatched(t *testing.T) {
	e := newEnv(t)
	j := e.mergedJob("Old news")
	var p api.Publish
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/publish", j.ID), api.PublishReq{PR: true}, &p)
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/report", p.ID), api.PublishReport{Status: "done", URL: "https://github.com/acme/app/pull/9"}, nil)
	var watch []api.PendingPR
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/pull-requests", nil, &watch)
	if len(watch) != 1 {
		t.Fatalf("watch: %+v", watch)
	}
	e.clock.advance(14*24*time.Hour + time.Hour)
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/pull-requests", nil, &watch)
	if len(watch) != 0 {
		t.Fatalf("a pull request older than 14 days is still watched: %+v", watch)
	}
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/publishes/%d/pr", p.ID), api.PRReport{State: "closed"}, nil)
	if !e.audited("publish.pr_closed", fmt.Sprintf("job:%d", j.ID)) || e.audited("publish.pr_merged", "") {
		t.Fatal("closing was not audited as closed")
	}
	var back api.Job
	e.ok(e.afif(), "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, &back)
	if back.PullRequest != "closed without being merged" || back.PRMerged {
		t.Fatalf("job: %+v", back)
	}
}

func TestTheJobPagesShowTheBaseAndThePullRequest(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	hum := e.humanAPI()
	e.apiOK(hum, "", "POST", "/v1/codebases", api.CodebaseReq{Name: "billing", Remote: "https://github.com/acme/billing", Base: "main"}, nil)
	var q api.CodebaseRequest
	e.apiOK(hum, "", "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: "d1"}, &q)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/project-requests/%d/report", q.ID), api.CodebaseReport{Status: "done", Path: "/home/x/projects/billing"}, nil)
	var j api.Job
	e.apiOK(hum, "", "POST", "/v1/jobs", api.JobNewReq{Title: "Stricter parser", Lead: "lead", Codebase: "billing", Device: "d1"}, &j)
	page := func(path string) string { return e.req("GET", path, nil, c, nil).body }
	jp := fmt.Sprintf("/jobs/%d", j.ID)
	if strings.Contains(page(jp), "Base:") {
		t.Fatal("a base line before anything was recorded")
	}
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/jobs/%d/base", j.ID), api.BaseReport{Status: "diverged", Commit: "abc1234",
		Detail: "this machine's copy of main has commits that are not on origin; the job starts from it as it is <b>"}, nil)
	p := page(jp)
	for _, want := range []string{"Base: diverged.", "has commits that are not on origin", "sev-warn", "&lt;b&gt;"} {
		if !strings.Contains(p, want) {
			t.Errorf("job page lacks %q", want)
		}
	}
	if strings.Contains(p, "<b>") {
		t.Fatal("the base detail became markup")
	}

	// A merged job with a pull request.
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "first", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "done"}, nil)
	e.apiOK(hum, "", "POST", fmt.Sprintf("/v1/tasks/%d/accept", t1.ID), nil, nil)
	var ms []api.PendingMerge
	e.apiOK(ag.device, "", "GET", "/v1/device/merges", nil, &ms)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/merges/%d/report", ms[0].ID), api.MergeReport{Status: "merged"}, nil)
	var pub api.Publish
	e.apiOK(hum, "", "POST", fmt.Sprintf("/v1/jobs/%d/publish", j.ID), api.PublishReq{PR: true}, &pub)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/publishes/%d/report", pub.ID), api.PublishReport{Status: "done", URL: "https://github.com/acme/app/pull/7"}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/publishes/%d/pr", pub.ID), api.PRReport{State: "open", Review: "approved", Mergeable: "true"}, nil)
	if p := page(jp); !strings.Contains(p, "open, approved, can merge") || strings.Contains(page("/jobs"), "pull request merged") {
		t.Fatalf("open pull request: job page %v", strings.Contains(p, "open, approved, can merge"))
	}
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/publishes/%d/pr", pub.ID), api.PRReport{State: "merged"}, nil)
	p = page(jp)
	if !strings.Contains(p, `<span class="badge success">merged</span>`) || !strings.Contains(p, "pull request merged for job") {
		t.Fatalf("merged pull request: job page lacks the badge or the activity line")
	}
	if !strings.Contains(page("/jobs"), "pull request merged") {
		t.Fatal("the jobs list has no marker for a merged pull request")
	}
}
