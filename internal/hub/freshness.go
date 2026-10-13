package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// D105. Two things a job learns from the machine that holds its repository:
// how getting the project's latest code went before the job started (the
// base), and where a published pull request stands. The hub only records what
// the machine says; it decides nothing and starts or closes nothing because of it.

var commitRE = regexp.MustCompile(`^[0-9a-f]{4,40}$`)

// prPollWindow is how long after a publish its pull request is watched.
const prPollWindow = 14 * 24 * time.Hour

// baseView fills the job's base and pull-request fields.
func (c *call) baseView(j *api.Job) {
	c.tx.QueryRow(`SELECT base_state, base_detail, base_commit FROM task WHERE id = ?`, j.ID).Scan(&j.BaseState, &j.BaseDetail, &j.BaseCommit)
	var state, review, mergeable string
	if err := c.tx.QueryRow(`SELECT pr_state, pr_review, pr_mergeable FROM publish WHERE job_id = ? AND url != '' AND pr_state != '' ORDER BY id DESC LIMIT 1`, j.ID).Scan(&state, &review, &mergeable); err == nil {
		j.PullRequest = api.PRWords(state, review, mergeable)
	}
	var n int
	c.tx.QueryRow(`SELECT count(*) FROM publish WHERE job_id = ? AND pr_state = 'merged'`, j.ID).Scan(&n)
	j.PRMerged = n > 0
	c.tx.QueryRow(`SELECT merged_into FROM publish WHERE job_id = ? AND merged_into != '' ORDER BY id DESC LIMIT 1`, j.ID).Scan(&j.MergedInto)
}

// jobBase is the device's account of the start of a job from its project. Its own device, once.
func jobBase(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that starts a job reports how its base went")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.BaseReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	switch req.Status {
	case api.BaseFresh, api.BaseStale, api.BaseDiverged:
	default:
		return nil, badRequest("status must be fresh, stale or diverged")
	}
	if req.Commit != "" && !commitRE.MatchString(req.Commit) {
		return nil, badRequest("bad commit id")
	}
	if len(req.Detail) > 600 {
		req.Detail = req.Detail[:600]
	}
	t, err := c.task(id)
	if err != nil || t.kind != "job" {
		return nil, notFound("no job %d", id)
	}
	var cb sql.NullInt64
	var state string
	if err := c.tx.QueryRow(`SELECT codebase_id, base_state FROM task WHERE id = ?`, id).Scan(&cb, &state); err != nil {
		return nil, err
	}
	if !t.deviceID.Valid || t.deviceID.Int64 != c.p.deviceID {
		return nil, forbidden("job %d belongs to another device", id)
	}
	if !cb.Valid {
		return nil, badRequest("job %d does not belong to a project", id)
	}
	if state != "" {
		return nil, conflict("the base of job %d was already recorded", id)
	}
	if _, err := c.tx.Exec(`UPDATE task SET base_state = ?, base_detail = ?, base_commit = ? WHERE id = ?`, req.Status, req.Detail, req.Commit, id); err != nil {
		return nil, err
	}
	return nil, c.record(t.projectID, 0, "job.base", fmt.Sprintf("job:%d", id), map[string]any{"status": req.Status, "commit": req.Commit})
}

// devicePullRequests lists this device's published pull requests that are still worth looking at.
func devicePullRequests(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list the pull requests it watches")
	}
	rows, err := c.tx.Query(`SELECT id, job_id, url FROM publish WHERE device_id = ? AND status = 'done' AND url LIKE 'https://%'
		AND pr_state NOT IN ('merged', 'closed') AND done_at > ? ORDER BY id LIMIT 50`, c.p.deviceID, store.Millis(c.now.Add(-prPollWindow)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.PendingPR{}
	for rows.Next() {
		var p api.PendingPR
		if err := rows.Scan(&p.ID, &p.Job, &p.URL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// publishPR is the device's account of where a pull request stands. Only the device that published,
// only a publish that has a pull request. Only a change into merged or closed is audited.
func publishPR(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that published reports on the pull request")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.PRReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	switch req.State {
	case "open", "merged", "closed":
	default:
		return nil, badRequest("state must be open, merged or closed")
	}
	switch req.Review {
	case "", "approved", "changes_requested", "review_required":
	default:
		return nil, badRequest("review must be empty, approved, changes_requested or review_required")
	}
	if req.Mergeable == "" {
		req.Mergeable = "unknown"
	}
	switch req.Mergeable {
	case "true", "false", "unknown":
	default:
		return nil, badRequest("mergeable must be true, false or unknown")
	}
	var job, dev int64
	var status, url, old string
	err = c.tx.QueryRow(`SELECT job_id, device_id, status, url, pr_state FROM publish WHERE id = ?`, id).Scan(&job, &dev, &status, &url, &old)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no publish %d", id)
	}
	if err != nil {
		return nil, err
	}
	if dev != c.p.deviceID {
		return nil, forbidden("publish %d belongs to another device", id)
	}
	if status != "done" || !strings.HasPrefix(url, "https://") {
		return nil, conflict("publish %d has no pull request", id)
	}
	if old == req.State {
		if old == "merged" || old == "closed" {
			return nil, nil // said twice: nothing changes, nothing is audited again
		}
	} else if old == "merged" || old == "closed" {
		return nil, conflict("the pull request of publish %d is already %s", id, old)
	}
	if _, err := c.tx.Exec(`UPDATE publish SET pr_state = ?, pr_review = ?, pr_mergeable = ?, pr_checked_at = ? WHERE id = ?`,
		req.State, req.Review, req.Mergeable, store.Millis(c.now), id); err != nil {
		return nil, err
	}
	if old == req.State || req.State == "open" {
		return nil, nil
	}
	t, err := c.task(job)
	if err != nil {
		return nil, err
	}
	return nil, c.record(t.projectID, 0, "publish.pr_"+req.State, fmt.Sprintf("job:%d", job), map[string]any{"publish": id, "url": url})
}
