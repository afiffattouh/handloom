package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// A finished job's work sits in the branch job/<id>/integration of the clone
// on the job's device. Publishing asks that device to push the branch to the
// project's shared remote under a name of its own, never forced, and, when
// asked, to open a pull request. A person asks; nothing publishes by itself.
// The hub keeps the request and what came of it.

var remoteRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// publishBranch is the branch a job is pushed to: handloom/job-<id>-<slug of the title>.
func publishBranch(id int64, title string) string {
	var b strings.Builder
	dash := true // no leading dash
	for _, r := range strings.ToLower(title) {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > 40 {
		slug = slug[:40]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return fmt.Sprintf("handloom/job-%d", id)
	}
	return fmt.Sprintf("handloom/job-%d-%s", id, slug)
}

func jobPublish(c *call) (any, error) {
	if err := c.allow(ActPublish); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.PublishReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.publishJob(id, req)
}

// publishJob is the work for the API and the web UI; the caller checked the scope.
func (c *call) publishJob(id int64, req api.PublishReq) (api.Publish, error) {
	t, err := c.job(id)
	if err != nil {
		return api.Publish{}, err
	}
	if t.repo == "" || !t.deviceID.Valid {
		return api.Publish{}, badRequest("job %d does not work in a git repository on a device, so there is no branch to publish", id)
	}
	if t.status == api.StatusCancelled {
		return api.Publish{}, conflict("job %d was cancelled", id)
	}
	req.Remote = strings.TrimSpace(req.Remote)
	if req.Remote == "" {
		req.Remote = "origin"
	}
	if !remoteRE.MatchString(req.Remote) {
		return api.Publish{}, badRequest("%q is not a remote name: use the name of a remote of the repository, such as origin", req.Remote)
	}
	var merged int
	if err := c.tx.QueryRow(`SELECT count(*) FROM merge WHERE job_id = ? AND status = 'merged'`, id).Scan(&merged); err != nil {
		return api.Publish{}, err
	}
	if merged == 0 {
		return api.Publish{}, conflict("job %d has no accepted and merged work yet, so there is nothing to publish; accept a finished task first and wait for its merge", id)
	}
	var waiting int
	if err := c.tx.QueryRow(`SELECT count(*) FROM publish WHERE job_id = ? AND status = 'pending'`, id).Scan(&waiting); err != nil {
		return api.Publish{}, err
	}
	if waiting > 0 {
		return api.Publish{}, conflict("a publish of job %d is already waiting for its device", id)
	}
	branch := publishBranch(id, t.title)
	res, err := c.tx.Exec(`INSERT INTO publish(job_id, device_id, requested_by, remote, branch, open_pr, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, t.deviceID.Int64, c.p.actor(), req.Remote, branch, req.PR, store.Millis(c.now))
	if err != nil {
		return api.Publish{}, err
	}
	pid, _ := res.LastInsertId()
	if err := c.record(t.projectID, 0, "job.publish", fmt.Sprintf("job:%d", id), map[string]any{"remote": req.Remote, "branch": branch, "pr": req.PR}); err != nil {
		return api.Publish{}, err
	}
	return c.onePublish(pid)
}

const publishSelect = `SELECT id, job_id, requested_by, remote, branch, open_pr, status, detail, url, behind, created_at, done_at FROM publish `

func scanPublish(s scanner) (api.Publish, error) {
	var p api.Publish
	var at int64
	var done sql.NullInt64
	if err := s.Scan(&p.ID, &p.Job, &p.RequestedBy, &p.Remote, &p.Branch, &p.PR, &p.Status, &p.Detail, &p.URL, &p.Behind, &at, &done); err != nil {
		return p, err
	}
	p.At = store.Time(at)
	if done.Valid {
		t := store.Time(done.Int64)
		p.DoneAt = &t
	}
	return p, nil
}

func (c *call) onePublish(id int64) (api.Publish, error) {
	p, err := scanPublish(c.tx.QueryRow(publishSelect+`WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, notFound("no publish %d", id)
	}
	return p, err
}

// publishes lists a job's publish requests, newest first.
func (c *call) publishes(jobID int64) ([]api.Publish, error) {
	rows, err := c.tx.Query(publishSelect+`WHERE job_id = ? ORDER BY id DESC LIMIT 50`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Publish{}
	for rows.Next() {
		p, err := scanPublish(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func jobPublishes(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	if _, err := c.job(id); err != nil {
		return nil, err
	}
	return c.publishes(id)
}

// devicePublishes is what a link asks for: the publishes waiting for its device.
func devicePublishes(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list its publishes")
	}
	rows, err := c.tx.Query(`SELECT p.id, p.job_id, p.remote, p.branch, p.open_pr, j.repo, j.base, j.title, j.body FROM publish p JOIN task j ON j.id = p.job_id
		WHERE p.device_id = ? AND p.status = 'pending' ORDER BY p.id LIMIT 5`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	out := []api.PendingPublish{}
	for rows.Next() {
		var p api.PendingPublish
		if err := rows.Scan(&p.ID, &p.Job, &p.Remote, &p.Branch, &p.PR, &p.Repo, &p.Base, &p.Title, &p.Body); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	for i := range out {
		trows, err := c.tx.Query(`SELECT t.id, t.title, COALESCE(a.name, '') FROM task t LEFT JOIN agent a ON a.id = t.owner_agent_id
			WHERE t.job_id = ? AND t.kind = 'task' AND t.status = 'done' ORDER BY t.id`, out[i].Job)
		if err != nil {
			return nil, err
		}
		out[i].Tasks = []api.PublishTask{}
		for trows.Next() {
			var pt api.PublishTask
			if err := trows.Scan(&pt.ID, &pt.Title, &pt.Owner); err != nil {
				trows.Close()
				return nil, err
			}
			out[i].Tasks = append(out[i].Tasks, pt)
		}
		trows.Close()
	}
	return out, nil
}

// publishReport is the device's account of a publish. Only its own device, and only once.
func publishReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that publishes reports on it")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.PublishReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Status != "done" && req.Status != "failed" {
		return nil, badRequest("status must be done or failed")
	}
	if req.Behind < 0 || req.Behind > 1000000 {
		return nil, badRequest("bad count of commits")
	}
	var job, dev int64
	var status, branch string
	err = c.tx.QueryRow(`SELECT job_id, device_id, status, branch FROM publish WHERE id = ?`, id).Scan(&job, &dev, &status, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no publish %d", id)
	}
	if err != nil {
		return nil, err
	}
	if dev != c.p.deviceID {
		return nil, forbidden("publish %d belongs to another device", id)
	}
	if status != "pending" {
		return nil, conflict("publish %d is already %s", id, status)
	}
	if len(req.Detail) > 2000 {
		req.Detail = req.Detail[:2000]
	}
	if len(req.URL) > 500 {
		req.URL = ""
	}
	if _, err := c.tx.Exec(`UPDATE publish SET status = ?, detail = ?, url = ?, behind = ?, done_at = ? WHERE id = ?`,
		req.Status, req.Detail, req.URL, req.Behind, store.Millis(c.now), id); err != nil {
		return nil, err
	}
	t, err := c.task(job)
	if err != nil {
		return nil, err
	}
	action := "job.published"
	if req.Status == "failed" {
		action = "job.publish_failed"
	}
	return nil, c.record(t.projectID, 0, action, fmt.Sprintf("job:%d", job), map[string]any{"branch": branch, "url": req.URL, "behind": req.Behind})
}

// overlaps lists the other open jobs on the same repository and device as t.
func (c *call) overlaps(t *taskRow) ([]api.JobRef, error) {
	if t.kind != "job" || t.repo == "" || !t.deviceID.Valid || t.status != api.StatusOpen {
		return nil, nil
	}
	rows, err := c.tx.Query(`SELECT j.id, j.title, COALESCE((SELECT a.name FROM agent a WHERE a.job_id = j.id AND a.role = 'lead' LIMIT 1), '')
		FROM task j WHERE j.kind = 'job' AND j.status = 'open' AND j.repo = ? AND j.device_id = ? AND j.id != ? ORDER BY j.id`, t.repo, t.deviceID.Int64, t.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.JobRef
	for rows.Next() {
		var r api.JobRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Lead); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
