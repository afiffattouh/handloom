package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestAKitsExitThreeIsAQuestionForAPersonNotAFailureToRetry(t *testing.T) {
	e := newEnv(t)
	_, task := e.verifiedSetup()
	e.ok(e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"file:x"}, Note: "done"}, nil)
	ev := &api.CheckEvidence{Result: "blocked", Reason: "the agent wrote BLOCKED.md", Blocked: "The spec asks for X but the tests need Y.\nMore detail."}
	e.ok(caller{e.d1, ""}, "POST", fmt.Sprintf("/v1/tasks/%d/verify", task.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 3, Evidence: ev}, nil)

	got := e.task(task.ID)
	if got.Check == nil || got.Check.Evidence == nil || got.Check.Evidence.Blocked == "" {
		t.Fatalf("evidence is not kept with the check: %+v", got.Check)
	}
	words := got.Check.Words()
	if !strings.Contains(words, "a person decides") || !strings.Contains(words, "The spec asks for X") || strings.Contains(words, "More detail") {
		t.Fatalf("words: %q", words)
	}
	body := e.inboxBodies(e.lead())
	if !strings.Contains(body, "Do not send it back to be retried") {
		t.Fatalf("the lead is not told what to do:\n%s", body)
	}
	e.fail(409, e.lead(), "POST", taskPath(task.ID, "accept"), nil)
}

func TestAKitsExitTwoIsTheProjectsProblem(t *testing.T) {
	ck := &api.TaskCheck{ExitCode: 2, Evidence: &api.CheckEvidence{Result: "config-error"}}
	if w := ck.Words(); !strings.Contains(w, "not the agent's work") {
		t.Fatalf("words: %q", w)
	}
	// A command that is not a kit keeps its plain words: its exit codes mean nothing in particular.
	for _, code := range []int{2, 3} {
		if w := (&api.TaskCheck{ExitCode: code}).Words(); w != fmt.Sprintf("failed, exit %d", code) {
			t.Fatalf("plain exit %d: %q", code, w)
		}
	}
	if w := (&api.TaskCheck{ExitCode: 1, Evidence: &api.CheckEvidence{Result: "fail", Reason: "stage lint failed"}}).Words(); w != "failed: stage lint failed" {
		t.Fatalf("words: %q", w)
	}
}

func TestEvidenceIsBounded(t *testing.T) {
	ev := &api.CheckEvidence{Result: "fail", Blocked: strings.Repeat("x", 9000)}
	for i := 0; i < 100; i++ {
		ev.Stages = append(ev.Stages, api.CheckStage{Name: "s"})
		ev.Refused = append(ev.Refused, "r")
	}
	got := cleanEvidence(ev)
	if len(got.Blocked) != 2000 || len(got.Stages) != 40 || len(got.Refused) != 20 {
		t.Fatalf("not bounded: %d %d %d", len(got.Blocked), len(got.Stages), len(got.Refused))
	}
}

func TestATaskSentBackTooOftenStopsAndAPersonIsAsked(t *testing.T) {
	e := newEnv(t)
	_, task := e.verifiedSetup()
	for i := 1; i <= 3; i++ {
		e.ok(e.worker(), "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"file:x"}, Note: "done"}, nil)
		got := e.act(e.lead(), task.ID, "reject", api.ReasonReq{Reason: fmt.Sprintf("not yet %d", i)})
		want := "claimed"
		if i == 3 {
			want = "cancelled"
		}
		if got.Status != want {
			t.Fatalf("after reject %d the task is %s, want %s", i, got.Status, want)
		}
	}
	var escs []api.Escalation
	e.ok(e.afif(), "GET", "/v1/escalations?status=open", nil, &escs)
	if len(escs) != 1 || !strings.Contains(escs[0].Question, "sent back 3 times") || len(escs[0].Options) != 3 {
		t.Fatalf("the person is not asked: %+v", escs)
	}
	var capped bool
	for _, r := range e.audit() {
		capped = capped || r.Action == "task.capped"
	}
	if !capped {
		t.Fatal("the stop is not in the audit log")
	}
}
