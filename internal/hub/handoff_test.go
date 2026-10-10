package hub

import (
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

func TestAFirstClaimHasNoResumeNoticeAndACheckpointIsStored(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "first", AssignedTo: "worker"})
	got := e.act(e.worker(), task.ID, "claim", nil)
	if got.Resume != "" || got.Handoff != nil {
		t.Fatalf("a first claim: %+v", got)
	}
	var cp api.Task
	e.ok(e.worker(), "POST", taskPath(task.ID, "handoff"), api.HandoffReq{Done: "parser written", Tried: "regex (too slow)", Next: "tests", Verify: "go test ./parser"}, &cp)
	if cp.Handoff == nil || cp.Handoff.Done != "parser written" || cp.Handoff.Kind != "checkpoint" || cp.Handoff.Verify != "go test ./parser" {
		t.Fatalf("checkpoint: %+v", cp.Handoff)
	}
	if got := e.task(task.ID); got.Handoff == nil || got.Handoff.Tried != "regex (too slow)" {
		t.Fatalf("task show: %+v", got.Handoff)
	}
	e.fail(400, e.worker(), "POST", taskPath(task.ID, "handoff"), api.HandoffReq{Next: "no done"})
	e.fail(400, e.worker(), "POST", taskPath(task.ID, "handoff"), api.HandoffReq{Done: strings.Repeat("x", 4001)})
	e.fail(403, e.afif(), "POST", taskPath(task.ID, "handoff"), api.HandoffReq{Done: "a person is not the owner"})
	e.fail(403, e.observer(), "POST", taskPath(task.ID, "handoff"), api.HandoffReq{Done: "an observer may not"})
}

func TestAReleaseNeedsANoteAndTheNextAgentOnAnotherMachineGetsIt(t *testing.T) {
	e := newEnv(t)
	task := e.create(e.lead(), api.TaskCreateReq{Title: "moves machines"})
	e.act(e.worker(), task.ID, "claim", nil)
	msg := e.fail(400, e.worker(), "POST", taskPath(task.ID, "release"), nil)
	if !strings.Contains(msg, "handoff note") {
		t.Fatalf("the refusal should say what to do: %s", msg)
	}
	if got := e.task(task.ID); got.Status != "claimed" {
		t.Fatalf("a refused release changed the task: %+v", got)
	}
	e.act(e.worker(), task.ID, "release", api.HandoffReq{Done: "schema migrated, data not yet", Tried: "ALTER in one go: timed out", Next: "backfill in batches of 1000", Verify: "select count(*) where new_col is null"})
	told := false
	for _, m := range e.inbox(e.lead()) {
		told = told || (strings.Contains(m.Body, "released") && strings.Contains(m.Body, "schema migrated") && strings.Contains(m.Body, "backfill in batches"))
	}
	if !told {
		t.Fatal("the lead's message should carry the note")
	}
	// another agent, on another device, takes over from the note alone
	took := e.act(e.worker2(), task.ID, "claim", nil)
	if took.Handoff == nil || took.Handoff.Next != "backfill in batches of 1000" || took.Handoff.Author == "" || took.Handoff.Kind != "release" {
		t.Fatalf("the new owner did not get the note: %+v", took.Handoff)
	}
	if !strings.Contains(took.Resume, "started before") || !strings.Contains(took.Resume, "check the git history") {
		t.Fatalf("the new owner was not told to check first: %q", took.Resume)
	}
}

func TestALostAgentsLastNoteIsInTheLeadsMessageAndWithoutOneItSaysSo(t *testing.T) {
	e := newEnv(t)
	with := e.claimed(e.worker(), "has a note")
	e.ok(e.worker(), "POST", taskPath(with.ID, "handoff"), api.HandoffReq{Done: "half done", Next: "finish the loop"}, nil)
	without := e.claimed(e.worker2(), "has none")
	e.clock.advance(20 * time.Minute)
	e.sweep()
	var sawNote, sawNone bool
	for _, m := range e.inbox(e.lead()) {
		if m.TaskID != nil && *m.TaskID == with.ID && strings.Contains(m.Body, "half done") && strings.Contains(m.Body, "finish the loop") {
			sawNote = true
		}
		if m.TaskID != nil && *m.TaskID == without.ID && strings.Contains(m.Body, "No handoff note was ever written") {
			sawNone = true
		}
	}
	if !sawNote || !sawNone {
		t.Fatalf("lease-expiry messages: note shown %v, 'none' shown %v", sawNote, sawNone)
	}
	// whoever claims it next is told to check first, and gets the note
	e.act(e.lead(), with.ID, "assign", api.AssignReq{Agent: "worker2"})
	got := e.act(e.worker2(), with.ID, "claim", nil)
	if got.Handoff == nil || got.Handoff.Done != "half done" || got.Resume == "" {
		t.Fatalf("takeover after a lost agent: %+v / %q", got.Handoff, got.Resume)
	}
	// no note at all: still told it was started before
	e.act(e.lead(), without.ID, "assign", api.AssignReq{Agent: "worker"})
	again := e.act(e.worker(), without.ID, "claim", nil)
	if again.Handoff != nil || !strings.Contains(again.Resume, "no handoff note") {
		t.Fatalf("takeover without a note: %+v / %q", again.Handoff, again.Resume)
	}
}

func TestASubmitNeedsANoteAndKeepsHowToCheck(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "to submit")
	e.fail(400, e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"commit:abc"}})
	e.fail(400, e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "   "})
	got := e.act(e.worker(), task.ID, "submit", api.SubmitReq{Evidence: []string{"commit:abc"}, Note: "added the export", HowToCheck: "run handloom export --csv"})
	_ = got
	shown := e.task(task.ID)
	if shown.Handoff == nil || shown.Handoff.Kind != "submit" || shown.Handoff.Done != "added the export" || shown.Handoff.Verify != "run handloom export --csv" || shown.Note != "added the export" {
		t.Fatalf("submitted task: %+v", shown)
	}
}

func TestHandoffNotesCannotBeChangedOrDeleted(t *testing.T) {
	e := newEnv(t)
	task := e.claimed(e.worker(), "history")
	e.ok(e.worker(), "POST", taskPath(task.ID, "handoff"), api.HandoffReq{Done: "one"}, nil)
	if _, err := e.hub.db.Exec(`UPDATE handoff SET done = 'rewritten'`); err == nil {
		t.Fatal("a handoff note was rewritten")
	}
	if _, err := e.hub.db.Exec(`DELETE FROM handoff`); err == nil {
		t.Fatal("a handoff note was deleted")
	}
}
