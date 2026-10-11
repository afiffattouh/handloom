package link

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

const testPR = "https://github.com/acme/app/pull/7"

// prFixture is a job whose publish opened a pull request, and a fake gh that answers `pr view`
// from a file and records every call.
type prFixture struct {
	*publishFixture
	dir     string
	publish api.Publish
}

func newPRFixture(t *testing.T) *prFixture {
	t.Helper()
	pf := newPublishFixture(t, true)
	pf.link.opt.PRPoll = 2 * time.Minute
	fakeGH(t, testPR, false) // `pr create` prints the link (the same script also answers the first look, which makes no sense to the link)
	p := pf.publish(api.PublishReq{PR: true})
	if p.URL != testPR {
		t.Fatalf("publish: %+v", p)
	}
	x := &prFixture{publishFixture: pf, dir: t.TempDir(), publish: p}
	script := fmt.Sprintf("echo \"$@\" >> %s/calls\ncat %s/answer\n", x.dir, x.dir)
	fakeBin(t, map[string]string{"gh": script})
	x.answer(`{"state":"OPEN","mergedAt":null,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","title":"Fix it"}`)
	// the publish's own ladder already looked once, at this very moment
	pf.advance(3 * time.Minute)
	return x
}

func (x *prFixture) answer(json string) {
	x.t.Helper()
	if err := os.WriteFile(filepath.Join(x.dir, "answer"), []byte(json+"\n"), 0o644); err != nil {
		x.t.Fatal(err)
	}
}

func (x *prFixture) calls() []string {
	b, _ := os.ReadFile(filepath.Join(x.dir, "calls"))
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (x *prFixture) jobNow() api.Job {
	x.t.Helper()
	var j api.Job
	x.must(x.human.Get(fmt.Sprintf("/v1/jobs/%d", x.job), &j))
	return j
}

func TestThePullRequestIsLookedAtThroughGhAndReported(t *testing.T) {
	x := newPRFixture(t)
	x.ladder()
	calls := x.calls()
	if len(calls) != 1 || calls[0] != "pr view "+testPR+" --json state,mergedAt,mergeable,reviewDecision,title" {
		t.Fatalf("gh calls: %q", calls)
	}
	if j := x.jobNow(); j.PullRequest != "open, approved, can merge" || j.PRMerged {
		t.Fatalf("job: %+v", j)
	}
	if len(x.audit("publish.pr_merged")) != 0 {
		t.Fatal("an open pull request was audited as merged")
	}
}

func TestThePullRequestIsNotLookedAtMoreOftenThanTheInterval(t *testing.T) {
	x := newPRFixture(t)
	x.ladder()
	x.ladder()
	x.advance(119 * time.Second)
	x.ladder()
	if n := len(x.calls()); n != 1 {
		t.Fatalf("gh was called %d times inside the interval", n)
	}
	x.advance(2 * time.Second)
	x.answer(`{"state":"OPEN","mergedAt":null,"mergeable":"CONFLICTING","reviewDecision":"CHANGES_REQUESTED","title":"Fix it"}`)
	x.ladder()
	if n := len(x.calls()); n != 2 {
		t.Fatalf("gh was called %d times after the interval, want 2", n)
	}
	if j := x.jobNow(); j.PullRequest != "open, changes requested, has conflicts" {
		t.Fatalf("job: %+v", j)
	}
}

func TestAMergedPullRequestIsReportedOnceAndNotLookedAtAgain(t *testing.T) {
	x := newPRFixture(t)
	x.ladder()
	x.advance(3 * time.Minute)
	x.answer(`{"state":"MERGED","mergedAt":"2026-10-03T12:04:00Z","mergeable":"UNKNOWN","reviewDecision":"APPROVED","title":"Fix it"}`)
	x.ladder()
	if j := x.jobNow(); j.PullRequest != "merged" || !j.PRMerged || j.Status != api.StatusOpen {
		t.Fatalf("job: %+v", j)
	}
	if len(x.audit("publish.pr_merged")) != 1 {
		t.Fatalf("pr_merged audited %d times", len(x.audit("publish.pr_merged")))
	}
	n := len(x.calls())
	x.advance(time.Hour)
	x.ladder()
	x.ladder()
	if len(x.calls()) != n {
		t.Fatal("gh was asked about a merged pull request again")
	}
	if len(x.audit("publish.pr_merged")) != 1 {
		t.Fatal("the merge was audited again")
	}
}

func TestAClosedPullRequestStopsTheLooking(t *testing.T) {
	x := newPRFixture(t)
	x.answer(`{"state":"CLOSED","mergedAt":null,"mergeable":"UNKNOWN","reviewDecision":"","title":"Fix it"}`)
	x.ladder()
	if j := x.jobNow(); j.PullRequest != "closed without being merged" || j.PRMerged {
		t.Fatalf("job: %+v", j)
	}
	x.advance(time.Hour)
	x.ladder()
	if n := len(x.calls()); n != 1 {
		t.Fatalf("gh calls after close: %d", n)
	}
	if len(x.audit("publish.pr_closed")) != 1 {
		t.Fatal("pr_closed is not in the audit log")
	}
}

func TestWithoutGhThePullRequestIsSimplyNotLookedAt(t *testing.T) {
	x := newPRFixture(t)
	fakeBin(t, nil) // no gh on this machine
	x.ladder()
	j := x.jobNow()
	if j.PullRequest != "" || j.PRMerged {
		t.Fatalf("job: %+v", j)
	}
	var pubs []api.Publish
	x.must(x.human.Get(fmt.Sprintf("/v1/jobs/%d/publishes", x.job), &pubs))
	if pubs[0].URL != testPR || pubs[0].Status != "done" {
		t.Fatalf("the publish lost its link: %+v", pubs[0])
	}
}

func TestAFailingOrConfusedGhChangesNothing(t *testing.T) {
	x := newPRFixture(t)
	fakeBin(t, map[string]string{"gh": "echo 'HTTP 502' >&2\nexit 1\n"})
	x.ladder()
	fakeBin(t, map[string]string{"gh": "echo 'not json'\n"})
	x.advance(3 * time.Minute)
	x.ladder()
	fakeBin(t, map[string]string{"gh": "echo '{\"state\":\"SOMETHING\"}'\n"})
	x.advance(3 * time.Minute)
	x.ladder()
	if j := x.jobNow(); j.PullRequest != "" {
		t.Fatalf("job: %+v", j)
	}
}

func TestThePollIntervalCanBeSetFromTheEnvironment(t *testing.T) {
	t.Setenv("HANDLOOM_PR_POLL", "3s")
	if l := New(Options{}); l.opt.PRPoll != 3*time.Second {
		t.Fatalf("interval: %v", l.opt.PRPoll)
	}
	t.Setenv("HANDLOOM_PR_POLL", "nonsense")
	if l := New(Options{}); l.opt.PRPoll != 2*time.Minute {
		t.Fatalf("interval: %v", l.opt.PRPoll)
	}
}
