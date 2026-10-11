package link

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/api"
)

// baseFixture is a project whose copy on this machine is a clone of a bare remote, a second clone
// ("mate") that moves the remote, and a link that can start a job's lead.
type baseFixture struct {
	*projectFixture
	clone, mate string
	tl          *tmuxLog
	pushed      int
}

func newBaseFixture(t *testing.T) *baseFixture {
	t.Helper()
	pf := newProjectFixture(t)
	fakeBin(t, nil)
	pf.attach("shared", pf.remote)
	st := pf.clone("shared")
	if st.Status != "ready" {
		t.Fatalf("clone: %+v", st)
	}
	bf := &baseFixture{projectFixture: pf, clone: st.Path, tl: &tmuxLog{}}
	pf.spawnLink(bf.tl)
	bf.mate = filepath.Join(t.TempDir(), "mate")
	gitOut(t, t.TempDir(), "clone", "-q", pf.bare, bf.mate)
	for _, d := range []string{bf.clone, bf.mate} {
		gitOut(t, d, "config", "user.email", "t@example.test")
		gitOut(t, d, "config", "user.name", "T")
	}
	return bf
}

// advanceRemote makes n commits in the teammate's clone and pushes them.
func (bf *baseFixture) advanceRemote(n int) {
	bf.t.Helper()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("remote%d.txt", bf.pushed)
		bf.pushed++
		if err := os.WriteFile(filepath.Join(bf.mate, name), []byte("x\n"), 0o644); err != nil {
			bf.t.Fatal(err)
		}
		gitOut(bf.t, bf.mate, "add", ".")
		gitOut(bf.t, bf.mate, "commit", "-q", "-m", "remote "+name)
	}
	gitOut(bf.t, bf.mate, "push", "-q", "origin", "HEAD")
}

// start asks for a job of the project and for its lead, and lets the link start it; it returns the job as the hub shows it.
func (bf *baseFixture) start(title string) api.Job {
	bf.t.Helper()
	var j api.Job
	bf.must(bf.human.Post("/v1/jobs", api.JobNewReq{Title: title, Codebase: "shared", Device: "dev"}, &j))
	bf.spawn("lead-"+fmt.Sprint(j.ID), j.ID)
	return bf.job(j.ID)
}

func (bf *baseFixture) spawn(name string, job int64) {
	bf.t.Helper()
	s := bf.requestSpawn(name, job)
	bf.link.runSpawns(context.Background())
	if bf.spawnStatus(s.ID).Status != "launching" {
		bf.t.Fatalf("the agent %s was not started: %+v", name, bf.spawnStatus(s.ID))
	}
}

func (bf *baseFixture) job(id int64) api.Job {
	bf.t.Helper()
	var j api.Job
	bf.must(bf.human.Get(fmt.Sprintf("/v1/jobs/%d", id), &j))
	return j
}

func (bf *baseFixture) head(dir string) string { return gitOut(bf.t, dir, "rev-parse", "HEAD") }

func TestAJobStartsFromTheLatestBaseWhenItIsAPlainFastForward(t *testing.T) {
	bf := newBaseFixture(t)
	before := bf.head(bf.clone)
	bf.advanceRemote(2)
	want := gitOut(t, bf.mate, "rev-parse", "HEAD")
	j := bf.start("Add greeting")
	if j.BaseState != "fresh" || !strings.Contains(j.BaseDetail, bf.branch+" moved forward 2 commits to "+short(want)) || j.BaseCommit != short(want) {
		t.Fatalf("job: %+v", j)
	}
	if got := bf.head(bf.clone); got != want || got == before {
		t.Fatalf("the copy is at %s, want %s (was %s)", got, want, before)
	}
	// The agent's worktree starts from the new commit, and the working folder shows the new files.
	wt := filepath.Join(bf.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), fmt.Sprintf("lead-%d", j.ID))
	if _, err := os.Stat(filepath.Join(wt, "remote1.txt")); err != nil {
		t.Fatalf("the worktree lacks the new commits: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bf.clone, "remote1.txt")); err != nil {
		t.Fatalf("the copy's folder was not brought forward: %v", err)
	}
	if !bf.audited("job.base", j.ID) {
		t.Fatal("job.base is not in the audit log")
	}
	// A second agent of the same job does not look again, and the record stays as it was.
	bf.advanceRemote(1)
	bf.spawn("second", j.ID)
	if again := bf.job(j.ID); again.BaseDetail != j.BaseDetail {
		t.Fatalf("the base was recorded again: %+v", again)
	}
	if bf.head(bf.clone) != want {
		t.Fatal("a second agent moved the base in the middle of the job")
	}
}

func (bf *baseFixture) audited(action string, job int64) bool {
	for _, r := range bf.audit(action) {
		if r.Target == fmt.Sprintf("job:%d", job) {
			return true
		}
	}
	return false
}

func TestAnUpToDateBaseIsFreshWithNothingMoved(t *testing.T) {
	bf := newBaseFixture(t)
	j := bf.start("Nothing new")
	if j.BaseState != "fresh" || !strings.Contains(j.BaseDetail, "already current at "+short(bf.head(bf.clone))) {
		t.Fatalf("job: %+v", j)
	}
}

func TestACopyWithCommitsOfItsOwnIsLeftAlone(t *testing.T) {
	bf := newBaseFixture(t)
	if err := os.WriteFile(filepath.Join(bf.clone, "mine.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, bf.clone, "add", ".")
	gitOut(t, bf.clone, "commit", "-q", "-m", "local only")
	mine := bf.head(bf.clone)
	bf.advanceRemote(1)
	j := bf.start("Diverged")
	if j.BaseState != "diverged" || !strings.Contains(j.BaseDetail, "this machine's copy of "+bf.branch+" has commits that are not on origin; the job starts from it as it is") {
		t.Fatalf("job: %+v", j)
	}
	if bf.head(bf.clone) != mine {
		t.Fatal("local history was rewritten")
	}
	if _, err := os.Stat(filepath.Join(bf.clone, "remote0.txt")); err == nil {
		t.Fatal("the remote's commit was merged into the diverged copy")
	}
	if gitOut(t, bf.clone, "log", "--format=%s", "-1") != "local only" {
		t.Fatal("the local commit is gone")
	}
}

func TestADirtyWorkingTreeIsLeftAlone(t *testing.T) {
	bf := newBaseFixture(t)
	file := filepath.Join(bf.clone, "a.txt")
	if err := os.WriteFile(file, []byte("my unfinished edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := bf.head(bf.clone)
	bf.advanceRemote(1)
	j := bf.start("Dirty")
	if j.BaseState != "stale" || !strings.Contains(j.BaseDetail, "uncommitted changes") || !strings.Contains(j.BaseDetail, "starts from this machine's copy") {
		t.Fatalf("job: %+v", j)
	}
	if bf.head(bf.clone) != before {
		t.Fatal("the branch moved under uncommitted changes")
	}
	if b, _ := os.ReadFile(file); string(b) != "my unfinished edit\n" {
		t.Fatalf("the edit was touched: %q", b)
	}
}

func TestABranchThatIsNotCheckedOutIsMovedWithoutTouchingTheFolder(t *testing.T) {
	bf := newBaseFixture(t)
	gitOut(t, bf.clone, "checkout", "-q", "-b", "elsewhere")
	if err := os.WriteFile(filepath.Join(bf.clone, "a.txt"), []byte("edit on another branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bf.advanceRemote(1)
	want := gitOut(t, bf.mate, "rev-parse", "HEAD")
	j := bf.start("Elsewhere")
	if j.BaseState != "fresh" {
		t.Fatalf("job: %+v", j)
	}
	if got := gitOut(t, bf.clone, "rev-parse", "refs/heads/"+bf.branch); got != want {
		t.Fatalf("the base branch is at %s, want %s", got, want)
	}
	if got := gitOut(t, bf.clone, "symbolic-ref", "--short", "HEAD"); got != "elsewhere" {
		t.Fatalf("the checked out branch changed to %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(bf.clone, "a.txt")); string(b) != "edit on another branch\n" {
		t.Fatal("the working folder was touched")
	}
}

func TestAFailedFetchMakesTheBaseStaleAndTheJobStillStarts(t *testing.T) {
	bf := newBaseFixture(t)
	before := bf.head(bf.clone)
	gitOut(t, bf.clone, "remote", "set-url", "origin", "file://"+filepath.Join(t.TempDir(), "gone.git"))
	j := bf.start("Offline")
	if j.BaseState != "stale" || !strings.Contains(j.BaseDetail, "could not get the latest from origin") || !strings.Contains(j.BaseDetail, "starts from this machine's copy of "+bf.branch+" as it is") {
		t.Fatalf("job: %+v", j)
	}
	if bf.head(bf.clone) != before {
		t.Fatal("the copy moved although nothing could be fetched")
	}
	wt := filepath.Join(bf.link.opt.WorkRoot, fmt.Sprintf("job-%d", j.ID), fmt.Sprintf("lead-%d", j.ID))
	if _, err := os.Stat(filepath.Join(wt, "a.txt")); err != nil {
		t.Fatalf("the job did not start from the copy: %v", err)
	}
	joined := strings.Join(bf.tl.all(), "\n")
	if !strings.Contains(joined, "new-session") && !strings.Contains(joined, "new-window") {
		t.Fatalf("no window was opened: %s", joined)
	}
}

func TestAJobWithoutAProjectIsNotTouched(t *testing.T) {
	f := newFixture(t)
	tl := &tmuxLog{}
	f.spawnLink(tl)
	repo := gitRepo(t)
	// An origin that cannot be reached: if the link looked at it, the record would say so.
	gitOut(t, repo, "remote", "add", "origin", "file://"+filepath.Join(t.TempDir(), "gone.git"))
	j := f.repoJob(repo)
	s := f.requestSpawn("alpha", j.ID)
	f.link.runSpawns(context.Background())
	if f.spawnStatus(s.ID).Status != "launching" {
		t.Fatalf("spawn: %+v", f.spawnStatus(s.ID))
	}
	var got api.Job
	f.must(f.human.Get(fmt.Sprintf("/v1/jobs/%d", j.ID), &got))
	if got.BaseState != "" || got.BaseDetail != "" {
		t.Fatalf("a job without a project has a base record: %+v", got)
	}
	if len(f.audit("job.base")) != 0 {
		t.Fatal("job.base was audited for a job without a project")
	}
}

func TestAnAgentCannotReportABaseThroughTheSocket(t *testing.T) {
	bf := newBaseFixture(t)
	var j api.Job
	bf.must(bf.human.Post("/v1/jobs", api.JobNewReq{Title: "Mine", Codebase: "shared", Device: "dev"}, &j))
	if err := bf.lead.Post(fmt.Sprintf("/v1/jobs/%d/base", j.ID), api.BaseReport{Status: "fresh"}, nil); err == nil {
		t.Fatal("an agent reported a base through the link socket")
	}
	if err := bf.lead.Post("/v1/publishes/1/pr", api.PRReport{State: "merged"}, nil); err == nil {
		t.Fatal("an agent reported a pull request through the link socket")
	}
	if err := bf.lead.Get("/v1/device/pull-requests", nil); err == nil {
		t.Fatal("an agent listed the pull requests through the link socket")
	}
}
