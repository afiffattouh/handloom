package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

type taskRow struct {
	id, projectID    int64
	title, body      string
	status           string
	owner, assigned  sql.NullInt64
	lease            sql.NullInt64
	deps             []int64
	evidence         []string
	note             string
	blocked, reject  string
	createdBy        string
	created, updated int64
	project          string
	ownerName        string
	assignedName     string
	kind             string
	jobID, parentID  sql.NullInt64
	confidential     bool
	repo, verify     string
	deviceID         sql.NullInt64
	jobVerify        string // the verify command of the job this task belongs to
	check            *api.TaskCheck
}

func (t *taskRow) api() api.Task {
	return api.Task{
		Kind: kindOrEmpty(t.kind), Job: nullInt(t.jobID),
		ID: t.id, Project: t.project, Title: t.title, Body: t.body, Status: t.status,
		Owner: t.ownerName, AssignedTo: t.assignedName, LeaseExpiresAt: store.TimePtr(t.lease),
		DependsOn: t.deps, Evidence: t.evidence, Note: t.note, BlockedReason: t.blocked,
		RejectReason: t.reject, CreatedBy: t.createdBy, Check: t.check,
		CreatedAt: store.Time(t.created), UpdatedAt: store.Time(t.updated),
	}
}

func kindOrEmpty(k string) string {
	if k == "task" {
		return ""
	}
	return k
}

func (t *taskRow) target() string { return fmt.Sprintf("task:%d", t.id) }

const taskSelect = `SELECT t.id, t.project_id, t.title, t.body, t.status, t.owner_agent_id, t.assigned_to,
	t.lease_expires_at, t.depends_on, t.evidence, t.note, t.blocked_reason, t.reject_reason, t.created_by,
	t.created_at, t.updated_at, p.name, COALESCE(o.name, ''), COALESCE(s.name, ''),
	t.kind, t.job_id, t.parent_id, t.confidential, t.repo, t.verify, t.device_id,
	COALESCE((SELECT j.verify FROM task j WHERE j.id = t.job_id), ''),
	ck.agent, ck.command, ck.exit_code, ck.timed_out, ck.tail, ck.sha256, ck.at, cd.name
	FROM task t LEFT JOIN task_check ck ON ck.task_id = t.id LEFT JOIN device cd ON cd.id = ck.device_id JOIN project p ON p.id = t.project_id
	LEFT JOIN agent o ON o.id = t.owner_agent_id LEFT JOIN agent s ON s.id = t.assigned_to `

func scanTask(s scanner) (*taskRow, error) {
	t := &taskRow{}
	var deps, evidence string
	var ckAgent, ckCmd, ckTail, ckSHA, ckDev sql.NullString
	var ckExit, ckTimed, ckAt sql.NullInt64
	err := s.Scan(&t.id, &t.projectID, &t.title, &t.body, &t.status, &t.owner, &t.assigned, &t.lease,
		&deps, &evidence, &t.note, &t.blocked, &t.reject, &t.createdBy, &t.created, &t.updated,
		&t.project, &t.ownerName, &t.assignedName, &t.kind, &t.jobID, &t.parentID, &t.confidential, &t.repo, &t.verify, &t.deviceID, &t.jobVerify,
		&ckAgent, &ckCmd, &ckExit, &ckTimed, &ckTail, &ckSHA, &ckAt, &ckDev)
	if err != nil {
		return nil, err
	}
	if ckAt.Valid {
		t.check = &api.TaskCheck{Agent: ckAgent.String, Device: ckDev.String, Command: ckCmd.String, ExitCode: int(ckExit.Int64),
			TimedOut: ckTimed.Int64 != 0, Tail: ckTail.String, SHA256: ckSHA.String, At: store.Time(ckAt.Int64)}
	}
	t.deps, t.evidence = []int64{}, []string{}
	json.Unmarshal([]byte(deps), &t.deps)
	json.Unmarshal([]byte(evidence), &t.evidence)
	return t, nil
}

func (c *call) task(id int64) (*taskRow, error) {
	t, err := scanTask(c.tx.QueryRow(taskSelect+`WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no task %d", id)
	}
	return t, err
}

func (c *call) tasks(where string, args ...any) ([]*taskRow, error) {
	rows, err := c.tx.Query(taskSelect+where+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*taskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// pathTask loads the task in the path and checks it is in the caller's project.
func (c *call) pathTask() (*taskRow, error) {
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	t, err := c.task(id)
	if err != nil {
		return nil, err
	}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if a.projectID != t.projectID {
			return nil, notFound("no task %d", id)
		}
	}
	return t, nil
}

func (c *call) touch(t *taskRow, set string, args ...any) error {
	args = append(args, store.Millis(c.now), t.id)
	_, err := c.tx.Exec(`UPDATE task SET `+set+`, updated_at = ? WHERE id = ?`, args...)
	return err
}

// reload returns the task as it is after the change, for the response.
func (c *call) reload(t *taskRow) (any, error) {
	t, err := c.task(t.id)
	if err != nil {
		return nil, err
	}
	return t.api(), nil
}

// unfinishedDeps lists dependencies that are not done.
func (c *call) unfinishedDeps(t *taskRow) ([]int64, error) {
	var open []int64
	for _, dep := range t.deps {
		var status string
		if err := c.tx.QueryRow(`SELECT status FROM task WHERE id = ?`, dep).Scan(&status); err != nil {
			return nil, err
		}
		if status != api.StatusDone {
			open = append(open, dep)
		}
	}
	return open, nil
}

// expireLeases returns claimed tasks with an expired lease to open and tells
// the lead. The old owner is no longer the owner, so its next call on the
// task is rejected.
func (c *call) expireLeases() error {
	expired, err := c.tasks(`WHERE t.status = 'claimed' AND t.lease_expires_at IS NOT NULL AND t.lease_expires_at <= ?`,
		store.Millis(c.now))
	if err != nil {
		return err
	}
	for _, t := range expired {
		if err := c.touch(t, `status = 'open', owner_agent_id = NULL, lease_expires_at = NULL, blocked_reason = ''`); err != nil {
			return err
		}
		if err := c.markClaimable(t); err != nil {
			return err
		}
		payload := map[string]any{"owner": t.ownerName}
		b := marshal(payload)
		// The hub is the actor here, whoever's request triggered the check.
		if _, err := c.tx.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES ('hub', 'task.lease_expired', ?, ?, ?)`,
			t.target(), string(b), store.Millis(c.now)); err != nil {
			return err
		}
		if err := c.emit(t.projectID, 0, "task.lease_expired", map[string]any{"target": t.target(), "detail": payload}); err != nil {
			return err
		}
		lead, err := c.leadFor(t.projectID, t.jobID)
		if err != nil {
			return err
		}
		if lead != nil {
			body := fmt.Sprintf("Task #%d (%s): the lease held by %s expired. The task is open again.", t.id, t.title, t.ownerName)
			if _, err := c.insertMessage("hub", lead, lead.name, &t.id, body); err != nil {
				return err
			}
		}
	}
	return nil
}

// markClaimable starts the clock for "assigned but never claimed": the task
// is open, assigned, and nothing holds it back.
func (c *call) markClaimable(t *taskRow) error {
	_, err := c.tx.Exec(`UPDATE task SET claimable_at = CASE WHEN assigned_to IS NULL THEN NULL ELSE ? END, unclaimed_notified = 0 WHERE id = ?`,
		store.Millis(c.now), t.id)
	return err
}

// reportUnclaimed tells the lead, once per task, when an assigned task that
// could be claimed has been left alone for too long. Leases only cover
// claimed tasks; this covers an assignee that never started.
func (c *call) reportUnclaimed() error {
	stale, err := c.tasks(`WHERE t.status = 'open' AND t.assigned_to IS NOT NULL AND t.unclaimed_notified = 0
		AND t.claimable_at IS NOT NULL AND t.claimable_at <= ?`, store.Millis(c.now.Add(-c.h.opt.Unclaimed)))
	if err != nil {
		return err
	}
	for _, t := range stale {
		if _, err := c.tx.Exec(`UPDATE task SET unclaimed_notified = 1 WHERE id = ?`, t.id); err != nil {
			return err
		}
		assignee, err := c.agentByID(t.assigned.Int64)
		if err != nil {
			return err
		}
		payload := map[string]any{"assigned_to": assignee.name, "state": assignee.state}
		if _, err := c.tx.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES ('hub', 'task.unclaimed', ?, ?, ?)`,
			t.target(), string(marshal(payload)), store.Millis(c.now)); err != nil {
			return err
		}
		if err := c.emit(t.projectID, 0, "task.unclaimed", map[string]any{"target": t.target(), "detail": payload}); err != nil {
			return err
		}
		lead, err := c.leadFor(t.projectID, t.jobID)
		if err != nil {
			return err
		}
		if lead != nil && lead.id != assignee.id {
			body := fmt.Sprintf("Task #%d (%s) is assigned to %s and has not been claimed for %s. %s is %s. Message it, reassign the task, or cancel it.",
				t.id, t.title, assignee.name, c.h.opt.Unclaimed, assignee.name, assignee.state)
			if _, err := c.insertMessage("hub", lead, lead.name, &t.id, body); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *call) leaseUntil() int64 { return store.Millis(c.now.Add(c.h.opt.Lease)) }

// ---- manage: create, assign, accept, reject, cancel (lead, human) ----

func taskCreate(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	var req api.TaskCreateReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		return nil, badRequest("title is required")
	}
	projectID, _, err := c.project(req.Project)
	if err != nil {
		return nil, err
	}
	// The job: a lead's tasks go into its own job; a human names one (or none).
	var job sql.NullInt64
	var jobConfidential bool
	switch {
	case c.p.kind == kindDevice:
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if req.Job != 0 {
			return nil, forbidden("an agent cannot choose the job; tasks go into the job its lead leads")
		}
		job = a.jobID
	case req.Job != 0:
		job = sql.NullInt64{Int64: req.Job, Valid: true}
	}
	if job.Valid {
		j, err := c.task(job.Int64)
		if err != nil || j.kind != "job" || j.projectID != projectID {
			return nil, notFound("no job %d", job.Int64)
		}
		if j.status != api.StatusOpen {
			return nil, conflict("job %d is %s", j.id, j.status)
		}
		jobConfidential = j.confidential
	}
	var assignee *agentRow
	if req.AssignedTo != "" {
		if assignee, err = c.assignee(projectID, req.AssignedTo); err != nil {
			return nil, err
		}
	}
	deps := []int64{}
	for _, dep := range req.DependsOn {
		d, err := c.task(dep)
		if err != nil {
			return nil, err
		}
		if d.projectID != projectID || d.kind == "job" {
			return nil, notFound("no task %d", dep)
		}
		if d.jobID != job {
			return nil, conflict("task %d belongs to another job; a task depends only on tasks of its own job", dep)
		}
		if d.status == api.StatusCancelled {
			return nil, conflict("task %d is cancelled and cannot be a dependency", dep)
		}
		deps = append(deps, dep)
	}
	depsJSON, _ := json.Marshal(deps)
	var assigned any
	if assignee != nil {
		assigned = assignee.id
	}
	ms := store.Millis(c.now)
	var parent any
	if job.Valid {
		parent = job.Int64
	}
	res, err := c.tx.Exec(`INSERT INTO task(project_id, title, body, status, assigned_to, depends_on, created_by, created_at, updated_at, job_id, parent_id, confidential)
		VALUES (?, ?, ?, 'open', ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, req.Title, req.Body, assigned, string(depsJSON), c.p.actor(), ms, ms,
		nullAny(job), parent, jobConfidential)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	t, err := c.task(id)
	if err != nil {
		return nil, err
	}
	if err := c.record(projectID, 0, "task.create", t.target(),
		map[string]any{"title": t.title, "assigned_to": t.assignedName, "depends_on": deps}); err != nil {
		return nil, err
	}
	if err := c.notifyAssigned(t, assignee); err != nil {
		return nil, err
	}
	return t.api(), nil
}

func nullAny(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

func (c *call) assignee(projectID int64, name string) (*agentRow, error) {
	a, err := c.agentByName(name)
	if err != nil {
		return nil, err
	}
	if a == nil || a.projectID != projectID {
		return nil, notFound("no agent %q in this project", name)
	}
	if !Allowed(ActTaskWork, a.role) {
		return nil, conflict("agent %s is an %s and cannot work on tasks", a.name, a.role)
	}
	return a, nil
}

func (c *call) notifyAssigned(t *taskRow, assignee *agentRow) error {
	if assignee == nil {
		return nil
	}
	open, err := c.unfinishedDeps(t)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Task #%d is assigned to you: %s. ", t.id, t.title)
	if len(open) > 0 {
		body += fmt.Sprintf("It depends on %s, not done yet. You will be told when you can claim it.", taskRefs(open))
	} else {
		body += fmt.Sprintf("Read it with `handloom task show %d`, then claim it with `handloom task claim %d`.", t.id, t.id)
		if _, err := c.tx.Exec(`UPDATE task SET claimable_at = ?, unclaimed_notified = 0 WHERE id = ?`, store.Millis(c.now), t.id); err != nil {
			return err
		}
	}
	return c.hubMessage(assignee, &t.id, body)
}

func taskRefs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(s, ", ")
}

func taskAssign(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	var req api.AssignReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if t.status != api.StatusOpen {
		return nil, conflict("task %d is %s; only an open task can be assigned", t.id, t.status)
	}
	var assignee *agentRow
	var assigned any
	if req.Agent != "" {
		if assignee, err = c.assignee(t.projectID, req.Agent); err != nil {
			return nil, err
		}
		assigned = assignee.id
	}
	if err := c.touch(t, `assigned_to = ?, claimable_at = NULL, unclaimed_notified = 0`, assigned); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.assign", t.target(), map[string]any{"assigned_to": req.Agent}); err != nil {
		return nil, err
	}
	if err := c.notifyAssigned(t, assignee); err != nil {
		return nil, err
	}
	return c.reload(t)
}

func taskAccept(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	if t.status != api.StatusSubmitted {
		return nil, conflict("task %d is %s; only a submitted task can be accepted", t.id, t.status)
	}
	// In a job that has a verify command, an agent (the lead) accepts only
	// work the device has checked and found good. A human may still decide otherwise.
	if t.jobVerify != "" && c.p.kind == kindDevice {
		switch {
		case t.check == nil:
			return nil, conflict("task %d has not been verified yet: the device is still running %q. Try again in a moment.", t.id, t.jobVerify)
		case t.check.ExitCode != 0 || t.check.TimedOut:
			return nil, conflict("task %d failed verification (%q: %s). Reject it with the reason so its owner can fix it.", t.id, t.jobVerify, checkWords(t.check))
		}
	}
	if err := c.touch(t, `status = 'done'`); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.accept", t.target(), map[string]any{"owner": t.ownerName}); err != nil {
		return nil, err
	}
	if err := c.notifyOwner(t, fmt.Sprintf("Task #%d (%s) was accepted. It is done.", t.id, t.title)); err != nil {
		return nil, err
	}
	if err := c.queueMerge(t); err != nil {
		return nil, err
	}
	// Tell assignees of tasks that this one was holding back.
	waiting, err := c.tasks(`WHERE t.project_id = ? AND t.status = 'open' AND t.assigned_to IS NOT NULL`, t.projectID)
	if err != nil {
		return nil, err
	}
	for _, w := range waiting {
		if !containsID(w.deps, t.id) {
			continue
		}
		open, err := c.unfinishedDeps(w)
		if err != nil {
			return nil, err
		}
		if len(open) > 0 {
			continue
		}
		a, err := c.agentByID(w.assigned.Int64)
		if err != nil {
			return nil, err
		}
		if err := c.markClaimable(w); err != nil {
			return nil, err
		}
		body := fmt.Sprintf("Task #%d (%s) can be claimed now: its dependencies are done. Claim it with `handloom task claim %d`.", w.id, w.title, w.id)
		if err := c.hubMessage(a, &w.id, body); err != nil {
			return nil, err
		}
	}
	return c.reload(t)
}

func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func (c *call) notifyOwner(t *taskRow, body string) error {
	if !t.owner.Valid {
		return nil
	}
	owner, err := c.agentByID(t.owner.Int64)
	if err != nil {
		return err
	}
	return c.hubMessage(owner, &t.id, body)
}

func taskReject(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	var req api.ReasonReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.rejectTask(t, req.Reason)
}

// rejectTask does the work for the API and the web UI; the caller has checked the scope.
func (c *call) rejectTask(t *taskRow, reason string) (any, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, badRequest("a reason is required to reject a task")
	}
	if t.status != api.StatusSubmitted {
		return nil, conflict("task %d is %s; only a submitted task can be rejected", t.id, t.status)
	}
	// Back to claimed, with a fresh lease for the owner. The device's check was of the rejected work.
	if err := c.touch(t, `status = 'claimed', reject_reason = ?, lease_expires_at = ?`, reason, c.leaseUntil()); err != nil {
		return nil, err
	}
	if _, err := c.tx.Exec(`DELETE FROM task_check WHERE task_id = ?`, t.id); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.reject", t.target(), map[string]any{"owner": t.ownerName, "reason": reason}); err != nil {
		return nil, err
	}
	if err := c.notifyOwner(t, fmt.Sprintf("Task #%d (%s) was rejected: %s. It is yours again; fix it and submit again.", t.id, t.title, reason)); err != nil {
		return nil, err
	}
	return c.reload(t)
}

func taskCancel(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	if t.status == api.StatusDone || t.status == api.StatusCancelled {
		return nil, conflict("task %d is already %s", t.id, t.status)
	}
	if err := c.touch(t, `status = 'cancelled', lease_expires_at = NULL`); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.cancel", t.target(), map[string]any{"owner": t.ownerName, "was": t.status}); err != nil {
		return nil, err
	}
	if err := c.notifyOwner(t, fmt.Sprintf("Task #%d (%s) was cancelled. Stop working on it.", t.id, t.title)); err != nil {
		return nil, err
	}
	return c.reload(t)
}

// ---- work: claim, heartbeat, release, block, submit (lead, worker) ----

// ownTask loads the task and checks the caller owns it and it is claimed.
func (c *call) ownTask() (*taskRow, *agentRow, error) {
	if err := c.allow(ActTaskWork); err != nil {
		return nil, nil, err
	}
	if err := c.expireLeases(); err != nil {
		return nil, nil, err
	}
	a, err := c.agent()
	if err != nil {
		return nil, nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, nil, err
	}
	if !t.owner.Valid || t.owner.Int64 != a.id {
		return nil, nil, forbidden("task %d is not yours (status %s, owner %s)", t.id, t.status, orNone(t.ownerName))
	}
	if t.status != api.StatusClaimed {
		return nil, nil, conflict("task %d is %s, not claimed", t.id, t.status)
	}
	return t, a, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func taskClaim(c *call) (any, error) {
	if err := c.allow(ActTaskWork); err != nil {
		return nil, err
	}
	if err := c.expireLeases(); err != nil {
		return nil, err
	}
	a, err := c.agent()
	if err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	if t.kind == "job" {
		return nil, conflict("#%d is a job, not a task: nobody claims it; its lead creates tasks under it", t.id)
	}
	if t.status != api.StatusOpen {
		return nil, conflict("task %d is %s (owner %s); only an open task can be claimed", t.id, t.status, orNone(t.ownerName))
	}
	if t.assigned.Valid && t.assigned.Int64 != a.id {
		return nil, forbidden("task %d is assigned to %s", t.id, t.assignedName)
	}
	open, err := c.unfinishedDeps(t)
	if err != nil {
		return nil, err
	}
	if len(open) > 0 {
		return nil, conflict("task %d depends on %s, not done yet", t.id, taskRefs(open))
	}
	if err := c.touch(t, `status = 'claimed', owner_agent_id = ?, lease_expires_at = ?, blocked_reason = ''`, a.id, c.leaseUntil()); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.claim", t.target(), map[string]any{"owner": a.name}); err != nil {
		return nil, err
	}
	return c.reload(t)
}

func taskHeartbeat(c *call) (any, error) {
	t, _, err := c.ownTask()
	if err != nil {
		return nil, err
	}
	if err := c.touch(t, `lease_expires_at = ?`, c.leaseUntil()); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.heartbeat", t.target(), nil); err != nil {
		return nil, err
	}
	return c.reload(t)
}

func taskRelease(c *call) (any, error) {
	t, a, err := c.ownTask()
	if err != nil {
		return nil, err
	}
	if err := c.touch(t, `status = 'open', owner_agent_id = NULL, lease_expires_at = NULL, blocked_reason = ''`); err != nil {
		return nil, err
	}
	if err := c.markClaimable(t); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.release", t.target(), map[string]any{"owner": a.name}); err != nil {
		return nil, err
	}
	lead, err := c.leadFor(t.projectID, t.jobID)
	if err != nil {
		return nil, err
	}
	if err := c.hubMessage(lead, &t.id, fmt.Sprintf("Task #%d (%s) was released by %s. It is open again.", t.id, t.title, a.name)); err != nil {
		return nil, err
	}
	return c.reload(t)
}

// taskBlock sets or clears the blocked flag on a claimed task.
func taskBlock(c *call) (any, error) {
	t, a, err := c.ownTask()
	if err != nil {
		return nil, err
	}
	var req api.BlockReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if err := c.touch(t, `blocked_reason = ?`, req.Reason); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.block", t.target(), map[string]any{"owner": a.name, "reason": req.Reason}); err != nil {
		return nil, err
	}
	if req.Reason != "" {
		lead, err := c.leadFor(t.projectID, t.jobID)
		if err != nil {
			return nil, err
		}
		if err := c.hubMessage(lead, &t.id, fmt.Sprintf("Task #%d (%s) is blocked, says %s: %s", t.id, t.title, a.name, req.Reason)); err != nil {
			return nil, err
		}
	}
	return c.reload(t)
}

// taskSubmit needs evidence. The hub stores it; the lead judges it.
func taskSubmit(c *call) (any, error) {
	t, a, err := c.ownTask()
	if err != nil {
		return nil, err
	}
	var req api.SubmitReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	evidence := []string{}
	for _, e := range req.Evidence {
		if e = strings.TrimSpace(e); e != "" {
			evidence = append(evidence, e)
		}
	}
	if len(evidence) == 0 {
		return nil, badRequest("evidence is required: commit:<sha>, pr:<url>, file:<path>, test:<command> -> <result>, or free text")
	}
	evJSON, _ := json.Marshal(evidence)
	if err := c.touch(t, `status = 'submitted', evidence = ?, note = ?, lease_expires_at = NULL, blocked_reason = '', reject_reason = ''`,
		string(evJSON), req.Note); err != nil {
		return nil, err
	}
	if _, err := c.tx.Exec(`DELETE FROM task_check WHERE task_id = ?`, t.id); err != nil { // a new submission is checked afresh
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.submit", t.target(),
		map[string]any{"owner": a.name, "evidence": evidence, "note": req.Note}); err != nil {
		return nil, err
	}
	lead, err := c.leadFor(t.projectID, t.jobID)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf("Task #%d (%s) was submitted by %s. Review the evidence with `handloom task show %d`, then `handloom task accept %d` or `handloom task reject %d --reason \"...\"`.",
		t.id, t.title, a.name, t.id, t.id, t.id)
	if err := c.hubMessage(lead, &t.id, body); err != nil {
		return nil, err
	}
	return c.reload(t)
}

// ---- read ----

func taskGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	out := t.api()
	if out.Merge, err = c.mergeOf(t.id); err != nil {
		return nil, err
	}
	return out, nil
}

func taskList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	q := c.r.URL.Query()
	projectID, _, err := c.project(q.Get("project"))
	if err != nil {
		return nil, err
	}
	where, args := `WHERE t.project_id = ?`, []any{projectID}
	if s := q.Get("status"); s != "" {
		where, args = where+` AND t.status = ?`, append(args, s)
	}
	if o := q.Get("owner"); o != "" {
		where, args = where+` AND o.name = ?`, append(args, o)
	}
	if j := q.Get("job"); j != "" {
		id, err := strconv.ParseInt(j, 10, 64)
		if err != nil {
			return nil, badRequest("bad job %q", j)
		}
		where, args = where+` AND t.job_id = ?`, append(args, id)
	}
	if q.Get("jobs") == "" { // job roots are listed by `job list`, not on the task board
		where += ` AND t.kind = 'task'`
	}
	rows, err := c.tasks(where, args...)
	if err != nil {
		return nil, err
	}
	out := make([]api.Task, 0, len(rows))
	for _, t := range rows {
		out = append(out, t.api())
	}
	return out, nil
}
