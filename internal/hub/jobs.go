package hub

import (
	"database/sql"
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// A job is a root task of kind "job" with its own lead. Tasks made for it
// carry its id (job_id). Jobs are started and closed by humans; the lead
// plans the tasks, workers do them, and the lead's accept/reject works as
// before. Agents and tasks without a job keep the old single-lead behaviour.

func (c *call) jobCounts(jobID int64) (api.JobCounts, error) {
	var n api.JobCounts
	rows, err := c.tx.Query(`SELECT status, count(*) FROM task WHERE job_id = ? GROUP BY status`, jobID)
	if err != nil {
		return n, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return n, err
		}
		switch status {
		case api.StatusOpen:
			n.Open = count
		case api.StatusClaimed:
			n.Claimed = count
		case api.StatusSubmitted:
			n.Submitted = count
		case api.StatusDone:
			n.Done = count
		case api.StatusCancelled:
			n.Cancelled = count
		}
	}
	return n, rows.Err()
}

func (c *call) jobView(t *taskRow) (api.Job, error) {
	lead, err := scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.project_id = ? AND a.role = 'lead' AND a.job_id = ?`, t.projectID, t.id))
	if err != nil {
		return api.Job{}, err
	}
	counts, err := c.jobCounts(t.id)
	if err != nil {
		return api.Job{}, err
	}
	j := api.Job{ID: t.id, Project: t.project, Title: t.title, Body: t.body, Status: t.status,
		Confidential: t.confidential, CreatedBy: t.createdBy, CreatedAt: store.Time(t.created), Tasks: counts}
	if lead != nil {
		j.Lead = lead.name
	}
	return j, nil
}

func (c *call) job(id int64) (*taskRow, error) {
	t, err := c.task(id)
	if err != nil || t.kind != "job" {
		return nil, notFound("no job %d", id)
	}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if a.projectID != t.projectID {
			return nil, notFound("no job %d", id)
		}
	}
	return t, nil
}

// humanOnly refuses agents: starting and closing jobs, and moving agents
// between jobs, are decisions for a person (or the admin).
func (c *call) humanOnly(what string) error {
	if c.p.kind == kindDevice {
		return forbidden("%s is for a human, not an agent", what)
	}
	return c.allow(ActTaskManage)
}

func jobNew(c *call) (any, error) {
	if err := c.humanOnly("starting a job"); err != nil {
		return nil, err
	}
	var req api.JobNewReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		return nil, badRequest("a job needs a title")
	}
	if req.Confidential && !c.h.opt.AllowConfidential {
		return nil, badRequest("this hub does not host confidential jobs: set HANDLOOM_ALLOW_CONFIDENTIAL=1 on a private hub that you trust with that material")
	}
	projectID, _, err := c.project(req.Project)
	if err != nil {
		return nil, err
	}
	var lead *agentRow
	if req.Lead != "" {
		if lead, err = c.agentByName(req.Lead); err != nil {
			return nil, err
		}
		if lead == nil || lead.projectID != projectID {
			return nil, notFound("no agent %q in this project", req.Lead)
		}
	}
	ms := store.Millis(c.now)
	res, err := c.tx.Exec(`INSERT INTO task(project_id, title, body, status, kind, depends_on, created_by, created_at, updated_at, confidential)
		VALUES (?, ?, ?, 'open', 'job', '[]', ?, ?, ?, ?)`, projectID, req.Title, req.Body, c.p.actor(), ms, ms, req.Confidential)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := c.record(projectID, 0, "job.new", fmt.Sprintf("job:%d", id), map[string]any{"title": req.Title, "lead": req.Lead, "confidential": req.Confidential}); err != nil {
		return nil, err
	}
	if lead != nil {
		if err := c.putInJob(lead, sql.NullInt64{Int64: id, Valid: true}, api.RoleLead); err != nil {
			return nil, err
		}
	}
	t, err := c.task(id)
	if err != nil {
		return nil, err
	}
	return c.jobView(t)
}

// putInJob moves an agent into a job (or out of it) with the given role, and
// tells it. A job has one lead.
func (c *call) putInJob(a *agentRow, job sql.NullInt64, role string) error {
	if role == api.RoleLead {
		if err := c.checkLeadFree(a, job); err != nil {
			return err
		}
	}
	if _, err := c.tx.Exec(`UPDATE agent SET job_id = ?, role = ? WHERE id = ?`, nullAny(job), role, a.id); err != nil {
		return err
	}
	old := a.role
	a.role, a.jobID = role, job
	if err := c.record(a.projectID, 0, "agent.job", "agent:"+a.name, map[string]any{"job": nullInt(job), "role": role, "was": old}); err != nil {
		return err
	}
	if job.Valid && role == api.RoleLead {
		t, err := c.task(job.Int64)
		if err != nil {
			return err
		}
		body := fmt.Sprintf("You are the lead of job #%d: %s. Plan it as tasks with `handloom task create`, assign them, and accept or reject what comes back. Use `handloom ask` for decisions only the human can make.", t.id, t.title)
		if t.body != "" {
			body += "\n\n" + t.body
		}
		return c.hubMessage(a, &t.id, body)
	}
	return nil
}

func jobList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	projectID, _, err := c.project(c.r.URL.Query().Get("project"))
	if err != nil {
		return nil, err
	}
	rows, err := c.tasks(`WHERE t.project_id = ? AND t.kind = 'job'`, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Job, 0, len(rows))
	for _, t := range rows {
		j, err := c.jobView(t)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func jobGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	t, err := c.job(id)
	if err != nil {
		return nil, err
	}
	return c.jobView(t)
}

// jobClose finishes a job, or cancels it with its unfinished tasks. Only a human.
func jobClose(c *call) (any, error) {
	if err := c.humanOnly("closing a job"); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.JobCloseReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	t, err := c.job(id)
	if err != nil {
		return nil, err
	}
	if t.status != api.StatusOpen {
		return nil, conflict("job %d is already %s", t.id, t.status)
	}
	status := api.StatusDone
	if req.Cancel {
		status = api.StatusCancelled
		if _, err := c.tx.Exec(`UPDATE task SET status = 'cancelled', updated_at = ? WHERE job_id = ? AND status IN ('open', 'claimed', 'submitted')`,
			store.Millis(c.now), t.id); err != nil {
			return nil, err
		}
	} else if n, err := c.openTasks(t.id); err != nil {
		return nil, err
	} else if n > 0 {
		return nil, conflict("job %d still has %d unfinished task(s); finish or cancel them, or close with --cancel", t.id, n)
	}
	if err := c.touch(t, `status = ?`, status); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "job.close", fmt.Sprintf("job:%d", t.id), map[string]any{"status": status}); err != nil {
		return nil, err
	}
	t, err = c.task(t.id)
	if err != nil {
		return nil, err
	}
	return c.jobView(t)
}

func (c *call) openTasks(jobID int64) (int, error) {
	var n int
	err := c.tx.QueryRow(`SELECT count(*) FROM task WHERE job_id = ? AND status IN ('open', 'claimed', 'submitted')`, jobID).Scan(&n)
	return n, err
}

// agentJob moves an agent into a job as a worker, or out of it (job 0). Making
// it the lead is `job new --lead` or `job resume`; this keeps the role.
func agentJob(c *call) (any, error) {
	if err := c.humanOnly("moving an agent between jobs"); err != nil {
		return nil, err
	}
	var req api.AgentJobReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	a, err := c.agentByName(c.r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, notFound("no agent %q", c.r.PathValue("name"))
	}
	var job sql.NullInt64
	if req.Job != 0 {
		j, err := c.job(req.Job)
		if err != nil || j.projectID != a.projectID {
			return nil, notFound("no job %d", req.Job)
		}
		if j.status != api.StatusOpen {
			return nil, conflict("job %d is %s", j.id, j.status)
		}
		job = sql.NullInt64{Int64: req.Job, Valid: true}
	}
	if err := c.putInJob(a, job, a.role); err != nil {
		return nil, err
	}
	return a.api(), nil
}
