package link

import (
	"fmt"

	"handloom/internal/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheKitsSummaryIsKeptAndOtherCommandsLeaveNone(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "runs", "r1")
	os.MkdirAll(run, 0o755)
	summary := `{"schema":1,"mode":"submit","result":"blocked","reason":"the agent wrote BLOCKED.md","blocked":"cannot do it","stages":[{"name":"lint","status":"pass","seconds":1.5}]}`
	os.WriteFile(filepath.Join(run, "summary.json"), []byte(summary), 0o644)

	ev := kitEvidence(dir, "PASS lint\nBLOCKED: the agent wrote BLOCKED.md\nevidence: runs/r1/summary.json\n")
	if ev == nil || ev.Result != "blocked" || ev.Blocked != "cannot do it" || len(ev.Stages) != 1 || ev.Stages[0].Name != "lint" {
		t.Fatalf("evidence: %+v", ev)
	}
	if kitEvidence(dir, "just some output\n") != nil {
		t.Fatal("a command that is not a kit has no evidence")
	}
	for _, bad := range []string{"evidence: ../../etc/summary.json", "evidence: /etc/summary.json", "evidence: runs/missing/summary.json"} {
		if kitEvidence(dir, bad) != nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	// A path that leaves the worktree through a link is refused too.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "summary.json"), []byte(summary), 0o644)
	os.Symlink(outside, filepath.Join(dir, "runs", "escape"))
	if kitEvidence(dir, "evidence: runs/escape/summary.json") != nil {
		t.Fatal("a link out of the worktree must not be followed")
	}
	// A different schema is not ours to read.
	os.WriteFile(filepath.Join(run, "summary.json"), []byte(`{"schema":2,"result":"pass"}`), 0o644)
	if kitEvidence(dir, "evidence: runs/r1/summary.json") != nil {
		t.Fatal("unknown schema")
	}
}

func TestTheCheckIsToldWhatItIsCheckingAgainst(t *testing.T) {
	l := &Link{}
	l.opt.VerifyTimeout = time.Minute
	t.Setenv("HANDLOOM_TOKEN", "secret")
	res := l.execVerify(t.TempDir(), `echo "$HANDLOOM_MODE $HANDLOOM_BASE [$HANDLOOM_TOKEN]"`, "env-test", "submit", "task/x-base")
	if !strings.Contains(res.tail, "submit task/x-base []") {
		t.Fatalf("tail: %q", res.tail)
	}
	res = l.execVerify(t.TempDir(), `echo "[$HANDLOOM_BASE]"`, "env-test2", "merge", "")
	if !strings.Contains(res.tail, "[]") {
		t.Fatalf("tail: %q", res.tail)
	}
}

func TestASealedKitTasksSpecReachesTheCheck(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "specs"), 0o755)
	os.WriteFile(filepath.Join(dir, "specs", "parser-fix.md"), []byte("# x"), 0o644)
	for base, want := range map[string]string{"task/parser-fix-base": "specs/parser-fix.md", "main": "", "task/missing-base": "", "task/../x-base": "", "task/parser-fix": ""} {
		if got := sealedSpec(dir, base); got != want {
			t.Errorf("sealedSpec(%q) = %q, want %q", base, got, want)
		}
	}
	l := &Link{}
	l.opt.VerifyTimeout = time.Minute
	res := l.execVerify(dir, `echo "[$HANDLOOM_SPEC]"`, "spec-test", "submit", "task/parser-fix-base")
	if !strings.Contains(res.tail, "[specs/parser-fix.md]") {
		t.Fatalf("tail: %q", res.tail)
	}
}

func TestWhatASubmittedTaskChangedReachesTheHub(t *testing.T) {
	f := newFixture(t)
	ac, dir, task := f.scopedAgent(nil)
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src\n\nfunc A() {}\n"), 0o644) // modified
	os.WriteFile(filepath.Join(dir, "site.html"), []byte("<html><head><title>x</title></head><body>hi</body></html>\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("adapter file"), 0o644) // not the agent's work
	if err := f.submit(ac, task); err != nil {
		t.Fatal(err)
	}
	var got api.TaskResult
	for i := 0; i < 300; i++ {
		if err := f.human.Get(fmt.Sprintf("/v1/tasks/%d/result", task.ID), &got); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Agent != "alpha" || len(got.Files) != 2 {
		t.Fatalf("result: %+v", got)
	}
	byPath := map[string]api.ResultFile{}
	for _, fl := range got.Files {
		byPath[fl.Path] = fl
	}
	if byPath["src/a.go"].Status != "modified" || byPath["src/a.go"].Added < 1 || byPath["site.html"].Status != "added" {
		t.Fatalf("files: %+v", got.Files)
	}
	if !strings.Contains(got.Diff, "+func A() {}") || !strings.Contains(got.Diff, "+++ b/site.html") || strings.Contains(got.Diff, "adapter file") {
		t.Fatalf("diff:\n%s", got.Diff)
	}
	if len(got.Pages) != 1 || got.Pages[0] != "site.html" {
		t.Fatalf("pages: %v", got.Pages)
	}
}
