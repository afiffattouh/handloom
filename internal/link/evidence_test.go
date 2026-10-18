package link

import (
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
