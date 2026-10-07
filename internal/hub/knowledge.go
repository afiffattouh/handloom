package hub

import (
	"database/sql"
	"errors"
	"fmt"

	"handloom/internal/api"
	"handloom/internal/store"
)

// A job may name a knowledge repository: what the team knows about a client,
// as markdown in a git repository on the job's device. Every agent of the job
// gets a read-only-by-convention checkout of it at .handloom/knowledge; what
// an agent adds there is a proposal. The device gathers each agent's proposals
// onto the branch job/<id> of that repository. The hub keeps only counts: the
// notes themselves never reach it, and only a human merges the branch.

// queueCollect asks the job's device to gather an agent's proposed notes. It
// does nothing for jobs without a knowledge repository, and does not pile up
// requests for an agent that already has one waiting.
func (c *call) queueCollect(jobID int64, agent string) error {
	var knowledge string
	var dev sql.NullInt64
	if err := c.tx.QueryRow(`SELECT knowledge, device_id FROM task WHERE id = ?`, jobID).Scan(&knowledge, &dev); err != nil {
		return err
	}
	if knowledge == "" || !dev.Valid || agent == "" {
		return nil
	}
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM kcollect WHERE job_id = ? AND agent = ? AND status = 'pending'`, jobID, agent).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := c.tx.Exec(`INSERT INTO kcollect(job_id, device_id, agent, created_at) VALUES (?, ?, ?, ?)`, jobID, dev.Int64, agent, store.Millis(c.now))
	return err
}

// queueCollectAfterAccept gathers the notes of the accepted task's owner and
// of the job's lead, so a human who reviews a finished job finds them.
func (c *call) queueCollectAfterAccept(t *taskRow, ownerName string) error {
	if !t.jobID.Valid {
		return nil
	}
	if err := c.queueCollect(t.jobID.Int64, ownerName); err != nil {
		return err
	}
	var lead string
	err := c.tx.QueryRow(`SELECT name FROM agent WHERE job_id = ? AND role = 'lead'`, t.jobID.Int64).Scan(&lead)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.queueCollect(t.jobID.Int64, lead)
}

// queueCollectAll gathers everybody's, when a job closes.
func (c *call) queueCollectAll(jobID int64) error {
	rows, err := c.tx.Query(`SELECT name FROM agent WHERE job_id = ?`, jobID)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if err := c.queueCollect(jobID, n); err != nil {
			return err
		}
	}
	return nil
}

// deviceCollects is what a link asks for: the gatherings waiting for its device.
func deviceCollects(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list its note collections")
	}
	rows, err := c.tx.Query(`SELECT id, job_id, agent FROM kcollect WHERE device_id = ? AND status = 'pending' ORDER BY id LIMIT 20`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.PendingCollect{}
	for rows.Next() {
		var p api.PendingCollect
		if err := rows.Scan(&p.ID, &p.Job, &p.Agent); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// collectReport is the device's account of one gathering.
func collectReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that gathers notes reports on it")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.CollectReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Status != "done" && req.Status != "conflict" && req.Status != "failed" {
		return nil, badRequest("status must be done, conflict or failed")
	}
	if req.Notes < 0 || req.Notes > 10000 {
		return nil, badRequest("bad note count")
	}
	var job, dev int64
	var agent, status string
	err = c.tx.QueryRow(`SELECT job_id, device_id, agent, status FROM kcollect WHERE id = ?`, id).Scan(&job, &dev, &agent, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no collection %d", id)
	}
	if err != nil {
		return nil, err
	}
	if dev != c.p.deviceID {
		return nil, forbidden("collection %d belongs to another device", id)
	}
	if status != "pending" {
		return nil, conflict("collection %d is already %s", id, status)
	}
	if len(req.Detail) > 1000 {
		req.Detail = req.Detail[:1000]
	}
	if _, err := c.tx.Exec(`UPDATE kcollect SET status = ?, notes = ?, detail = ?, done_at = ? WHERE id = ?`, req.Status, req.Notes, req.Detail, store.Millis(c.now), id); err != nil {
		return nil, err
	}
	j, err := c.task(job)
	if err != nil {
		return nil, err
	}
	return nil, c.record(j.projectID, 0, "job.notes_"+req.Status, fmt.Sprintf("job:%d", job), map[string]any{"agent": agent, "notes": req.Notes})
}
