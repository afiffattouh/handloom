package hub

import (
	"encoding/json"
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// A job may carry a verify command. The device of a repo job runs it in the
// agent's worktree after the agent submits and tells the hub what happened.
// That is a check by the machine that holds the files, not the agent's claim:
// the lead sees it next to the agent's evidence, and an agent (the lead)
// cannot accept work that failed it.

func checkWords(ck *api.TaskCheck) string { return ck.Words() }

// cleanEvidence keeps what a person reads from a kit's summary and bounds it: the device is trusted
// to run the command, not to send an unbounded body.
func cleanEvidence(ev *api.CheckEvidence) *api.CheckEvidence {
	if ev == nil {
		return nil
	}
	cut := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	out := &api.CheckEvidence{Result: cut(ev.Result, 20), Reason: cut(ev.Reason, 500), Blocked: cut(ev.Blocked, 2000), Mode: cut(ev.Mode, 20)}
	for i, s := range ev.Stages {
		if i >= 40 {
			break
		}
		out.Stages = append(out.Stages, api.CheckStage{Name: cut(s.Name, 60), Status: cut(s.Status, 20), Detail: cut(s.Detail, 200), Seconds: s.Seconds})
	}
	for i, r := range ev.Refused {
		if i >= 20 {
			break
		}
		out.Refused = append(out.Refused, cut(r, 300))
	}
	return out
}

// taskVerify is the link reporting a verify run. Only the device the agent
// lives on, only for a task the agent owns and has submitted, and only in a
// job that has a verify command.
func taskVerify(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device reports a verification")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.TaskCheckReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	a, err := c.agentByName(req.Agent)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not on this device", req.Agent)
	}
	t, err := c.task(id)
	if err != nil {
		return nil, err
	}
	if t.projectID != a.projectID || !t.owner.Valid || t.owner.Int64 != a.id {
		return nil, forbidden("task %d is not %s's", id, a.name)
	}
	if t.status != api.StatusSubmitted {
		return nil, conflict("task %d is %s; only a submitted task is verified", id, t.status)
	}
	if t.jobVerify == "" || req.Command != t.jobVerify {
		return nil, conflict("task %d's job has no such verify command", id)
	}
	if len(req.Tail) > 4000 {
		req.Tail = req.Tail[len(req.Tail)-4000:]
	}
	timedOut := 0
	if req.TimedOut {
		timedOut = 1
	}
	ev := cleanEvidence(req.Evidence)
	evJSON := ""
	if ev != nil {
		b, _ := json.Marshal(ev)
		evJSON = string(b)
	}
	if _, err := c.tx.Exec(`INSERT INTO task_check(task_id, agent, device_id, command, exit_code, timed_out, tail, sha256, at, evidence) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET agent = excluded.agent, device_id = excluded.device_id, command = excluded.command, exit_code = excluded.exit_code,
		timed_out = excluded.timed_out, tail = excluded.tail, sha256 = excluded.sha256, at = excluded.at, evidence = excluded.evidence`,
		id, a.name, c.p.deviceID, req.Command, req.ExitCode, timedOut, req.Tail, req.SHA256, store.Millis(c.now), evJSON); err != nil {
		return nil, err
	}
	words := (&api.TaskCheck{ExitCode: req.ExitCode, TimedOut: req.TimedOut, Evidence: ev}).Words()
	if err := c.record(t.projectID, a.id, "task.verified", t.target(), map[string]any{"agent": a.name, "exit_code": req.ExitCode, "timed_out": req.TimedOut, "sha256": req.SHA256}); err != nil {
		return nil, err
	}
	// The lead hears about it when there is something to act on: the check is in, or it failed.
	lead, err := c.leadOf(a)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf("Task #%d (%s): the device ran %q on %s's work: %s.", t.id, t.title, req.Command, a.name, words)
	if ev != nil && req.ExitCode == 3 && !req.TimedOut {
		body += " Do not send it back to be retried: change the task, or cancel it, or ask the person who owns the project."
	} else if ev != nil && req.ExitCode == 2 && !req.TimedOut {
		body += " That is a problem with the project's checks, not with this work: a person has to fix them."
	} else if req.ExitCode != 0 || req.TimedOut {
		body += " It cannot be accepted as it is: reject it with the reason, or read the end of the output with `handloom task show " + fmt.Sprint(t.id) + "`."
		if tail := strings.TrimSpace(req.Tail); tail != "" {
			body += "\n\n" + lastLines(tail, 12)
		}
	}
	if lead != nil && lead.id != a.id {
		return nil, c.hubMessage(lead, &t.id, body)
	}
	return nil, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
