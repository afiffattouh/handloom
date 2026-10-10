package hub

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
)

func searchFor(e *env, c caller, q string) []api.SearchHit {
	e.t.Helper()
	var hits []api.SearchHit
	e.ok(c, "GET", "/v1/search?q="+url.QueryEscape(q), nil, &hits)
	return hits
}

func hasJob(hits []api.SearchHit, job int64) bool {
	for _, h := range hits {
		if h.Job == job {
			return true
		}
	}
	return false
}

func TestSearchFindsWhatEarlierJobsRecordedAndOnlyWhatTheCallerMaySee(t *testing.T) {
	e := newEnv(t)
	setKnowledge := func(job int64, path string, conf bool) {
		if _, err := e.hub.db.Exec(`UPDATE task SET knowledge = ?, confidential = ? WHERE id = ?`, path, conf, job); err != nil {
			t.Fatal(err)
		}
	}
	// job 1: an earlier job for Acme, with accepted work, a handoff note and an answered question
	var j1 api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Invoice numbers for Acme", Body: "Acme's accounting system rejects other prefixes."}, &j1)
	setKnowledge(j1.ID, "/k/acme", false)
	t1 := e.create(e.afif(), api.TaskCreateReq{Title: "Format the numbers", Job: j1.ID, AssignedTo: "worker"})
	e.act(e.worker(), t1.ID, "claim", nil)
	e.ok(e.worker(), "POST", taskPath(t1.ID, "handoff"), api.HandoffReq{Done: "sequence stored in a table", Next: "use the ACM prefix"}, nil)
	e.act(e.worker(), t1.ID, "submit", api.SubmitReq{Evidence: []string{"test:./check.sh -> ok"}, Note: "numbers now print ACM-0001 style"})
	e.act(e.afif(), t1.ID, "accept", nil)
	var q api.Escalation
	e.ok(e.lead(), "POST", "/v1/escalations", api.AskReq{Question: "Should Acme invoices restart numbering each year?", Options: []string{"yes", "no"}, TaskID: &t1.ID}, &q)
	e.ok(e.afif(), "POST", "/v1/escalations/"+fmt.Sprint(q.ID)+"/answer", api.AnswerReq{Answer: "no"}, nil)
	// job 2: the current job, same client; its lead is the agent that searches
	var j2 api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Add credit notes for Acme", Lead: "lead"}, &j2)
	setKnowledge(j2.ID, "/k/acme", false)
	// job 3: another client whose text also says ACM; job 4: Acme again but confidential
	var j3, j4 api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Other client invoices", Body: "their prefix ACM is unrelated"}, &j3)
	setKnowledge(j3.ID, "/k/other", false)
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Acme contract terms", Body: "invoice prefix and penalty terms, secret"}, &j4)
	setKnowledge(j4.ID, "/k/acme", true)

	// the lead of job 2 finds job 1's results in every form, and nothing else
	hits := searchFor(e, e.lead(), "ACM prefix")
	if !hasJob(hits, j1.ID) {
		t.Fatalf("the earlier job was not found: %+v", hits)
	}
	if hasJob(hits, j3.ID) {
		t.Fatalf("another client's job leaked into an agent's search: %+v", hits)
	}
	if hasJob(hits, j4.ID) {
		t.Fatalf("a confidential job leaked into a non-confidential agent's search: %+v", hits)
	}
	kinds := map[string]bool{}
	for _, h := range hits {
		kinds[h.Kind] = true
		if !strings.Contains(strings.ToLower(h.Snippet+h.Title), "acm") && !strings.Contains(strings.ToLower(h.Snippet), "prefix") {
			t.Errorf("a hit has no match in it: %+v", h)
		}
	}
	for _, want := range []string{"job", "task", "handoff"} {
		if !kinds[want] {
			t.Errorf("no %s among the hits: %v", want, kinds)
		}
	}
	// accepted work and the answered question are found too
	if a := searchFor(e, e.lead(), "restart numbering"); len(a) == 0 || a[0].Kind != "answer" || !strings.Contains(a[0].Snippet, "A: no") {
		t.Fatalf("the answered question: %+v", a)
	}
	// a person sees everything
	all := searchFor(e, e.afif(), "ACM prefix")
	for _, j := range []int64{j1.ID, j3.ID, j4.ID} {
		if !hasJob(all, j) {
			t.Errorf("a person did not see job %d: %+v", j, all)
		}
	}
	// an agent in a job with no knowledge repository or repo sees only its own job
	var j5 api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Alone", Lead: "observer"}, &j5)
	if own := searchFor(e, e.observer(), "ACM prefix"); len(own) != 0 {
		t.Fatalf("an agent with nothing shared saw other jobs: %+v", own)
	}
	// an empty or all-stop-word query is refused in words
	e.fail(400, e.lead(), "GET", "/v1/search?q=the+and", nil)
	// an agent that belongs to no job sees nothing
	if none := searchFor(e, e.worker2(), "ACM prefix"); len(none) != 0 {
		t.Fatalf("an agent outside any job saw %+v", none)
	}
}

func TestTheSearchPageFindsEarlierJobsAndSaysWhereNotesAreSearched(t *testing.T) {
	e := newWebEnv(t, Options{})
	c, _ := e.owner()
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Invoice numbers", Body: "Acme wants the ACM prefix <b>bold</b>"}, nil)
	page := e.req("GET", "/search?q=ACM+prefix", nil, c, nil)
	for _, want := range []string{"1 result", "Invoice numbers", "ACM prefix", "handloom search --notes", `href="/jobs/1"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("search page lacks %q", want)
		}
	}
	if strings.Contains(page.body, "<b>bold</b>") {
		t.Error("a snippet was not escaped")
	}
	if none := e.req("GET", "/search?q=zebra", nil, c, nil); !strings.Contains(none.body, "Nothing recorded on the hub matches") {
		t.Fatal("a miss is not explained")
	}
	if blank := e.req("GET", "/search", nil, c, nil); blank.status != 200 || strings.Contains(blank.body, "result") {
		t.Fatalf("an empty search page: %d", blank.status)
	}
	// the box is in the top bar of every page, and a viewer can search too
	if !strings.Contains(e.req("GET", "/jobs", nil, c, nil).body, `action="/search"`) {
		t.Error("no search box in the top bar")
	}
	vc, _ := e.member("vi", "viewer")
	if r := e.req("GET", "/search?q=prefix", nil, vc, nil); r.status != 200 || !strings.Contains(r.body, "Invoice numbers") {
		t.Fatalf("a viewer's search: %d", r.status)
	}
	if r := e.req("GET", "/search?q=prefix", nil, nil, nil); r.status == 200 {
		t.Fatal("search is open to anyone")
	}
}
