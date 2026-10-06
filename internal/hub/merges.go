package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Accepted work in a repository job is merged, by the device that holds the
// repository, into the job's integration branch job/<id>/integration. The hub
// only keeps the list of what to merge and what came of it.

// queueMerge asks the job's device to merge an accepted task's branch. Tasks
// outside repo jobs, and tasks nobody owned, have nothing to merge.
func (c *call) queueMerge(t *taskRow) error {
	if !t.jobID.Valid || !t.owner.Valid {
		return nil
	}
	var repo string
	var dev sql.NullInt64
	if err := c.tx.QueryRow(`SELECT repo, device_id FROM task WHERE id = ?`, t.jobID.Int64).Scan(&repo, &dev); err != nil {
		return err
	}
	if repo == "" || !dev.Valid {
		return nil
	}
	a, err := c.agentByID(t.owner.Int64)
	if err != nil {
		return err
	}
	_, err = c.tx.Exec(`INSERT INTO merge(task_id, job_id, device_id, agent, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.id, t.jobID.Int64, dev.Int64, a.name, store.Millis(c.now))
	return err
}

// mergeOf is the latest merge of a task, if any.
func (c *call) mergeOf(taskID int64) (*api.TaskMerge, error) {
	var m api.TaskMerge
	var at int64
	err := c.tx.QueryRow(`SELECT id, status, detail, head, COALESCE(done_at, created_at) FROM merge WHERE task_id = ? ORDER BY id DESC LIMIT 1`, taskID).
		Scan(&m.ID, &m.Status, &m.Detail, &m.Head, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.At = store.Time(at)
	return &m, nil
}

// deviceMerges is what a link asks for: the merges waiting for its device, oldest first.
func deviceMerges(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list its merges")
	}
	rows, err := c.tx.Query(`SELECT m.id, m.task_id, m.job_id, m.agent, t.title, j.verify FROM merge m JOIN task t ON t.id = m.task_id JOIN task j ON j.id = m.job_id
		WHERE m.device_id = ? AND m.status = 'pending' ORDER BY m.id LIMIT 20`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.PendingMerge{}
	for rows.Next() {
		var m api.PendingMerge
		if err := rows.Scan(&m.ID, &m.Task, &m.Job, &m.Agent, &m.Title, &m.Verify); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// mergeReport is the device's account of a merge. Only its own device, and only once.
func mergeReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that merges reports on it")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.MergeReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Status != "merged" && req.Status != "conflict" && req.Status != "failed" {
		return nil, badRequest("status must be merged, conflict or failed")
	}
	var taskID, jobID, dev int64
	var agent, status string
	err = c.tx.QueryRow(`SELECT task_id, job_id, device_id, agent, status FROM merge WHERE id = ?`, id).Scan(&taskID, &jobID, &dev, &agent, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no merge %d", id)
	}
	if err != nil {
		return nil, err
	}
	if dev != c.p.deviceID {
		return nil, forbidden("merge %d belongs to another device", id)
	}
	if status != "pending" {
		return nil, conflict("merge %d is already %s", id, status)
	}
	if len(req.Detail) > 2000 {
		req.Detail = req.Detail[:2000]
	}
	if _, err := c.tx.Exec(`UPDATE merge SET status = ?, detail = ?, head = ?, done_at = ? WHERE id = ?`, req.Status, req.Detail, req.Head, store.Millis(c.now), id); err != nil {
		return nil, err
	}
	t, err := c.task(taskID)
	if err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.merge_"+req.Status, t.target(), map[string]any{"agent": agent, "head": req.Head}); err != nil {
		return nil, err
	}
	if req.Status == "merged" {
		return nil, nil
	}
	// The lead can fix it: the worker merges the integration branch into its own and resolves.
	a, err := c.agentByName(agent)
	if err != nil {
		return nil, err
	}
	var lead *agentRow
	if a != nil {
		if lead, err = c.leadOf(a); err != nil {
			return nil, err
		}
	}
	branch := fmt.Sprintf("job/%d/integration", jobID)
	body := fmt.Sprintf("Task #%d (%s) was accepted but its branch did not merge into %s (%s).\n\n%s\n\nCreate a task for %s: merge %s into its own branch, resolve the conflicts, run the check, commit and submit.",
		t.id, t.title, branch, req.Status, strings.TrimSpace(req.Detail), agent, branch)
	return nil, c.hubMessage(lead, &t.id, body)
}
