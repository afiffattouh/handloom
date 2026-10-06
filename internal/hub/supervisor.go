package hub

import (
	"database/sql"
	"fmt"

	"handloom/internal/api"
	"handloom/internal/notify"
	"handloom/internal/store"
)

// The supervisor is the hub's own timer (Run calls Sweep): it turns "the
// link stopped vouching for this agent's terminal" into a state and, for a
// lead with work in flight, into something a human is told about. It decides
// nothing about the work: the task graph is durable, and a human (or the
// lead's replacement) picks it up with `job resume`.

// agentHeartbeat is the link vouching that an agent's terminal still exists.
// It renews the lease. An agent the hub had marked offline because the lease
// ran out comes back as "unknown" until its own hooks say what it is doing.
func agentHeartbeat(c *call) (any, error) {
	a, err := c.localAgent()
	if err != nil {
		return nil, err
	}
	if _, err := c.tx.Exec(`UPDATE agent SET lease_expires_at = ? WHERE id = ?`,
		store.Millis(c.now.Add(c.h.opt.AgentLease)), a.id); err != nil {
		return nil, err
	}
	if a.state == api.StateOffline {
		if err := c.setState(a, api.StateUnknown, "terminal_back"); err != nil {
			return nil, err
		}
	}
	return a.api(), nil
}

// expireAgentLeases marks agents whose lease ran out offline, once.
func (c *call) expireAgentLeases() error {
	rows, err := c.agents(`WHERE a.lease_expires_at IS NOT NULL AND a.lease_expires_at < ? AND a.state != 'offline'`, store.Millis(c.now))
	if err != nil {
		return err
	}
	for _, a := range rows {
		if _, err := c.tx.Exec(`UPDATE agent SET lease_expires_at = NULL WHERE id = ?`, a.id); err != nil {
			return err
		}
		if err := c.record(a.projectID, 0, "agent.lease_expired", "agent:"+a.name, map[string]any{"was": a.state}); err != nil {
			return err
		}
		if err := c.setState(a, api.StateOffline, "lease_expired"); err != nil {
			return err
		}
	}
	return nil
}

// leadWork counts the work that depends on a lead: for a job's lead, the
// job's tasks that are open, claimed or submitted; for the project lead, the
// claimed and submitted tasks that belong to no job.
func (c *call) leadWork(lead *agentRow) (int, error) {
	var n int
	var err error
	if lead.jobID.Valid {
		err = c.tx.QueryRow(`SELECT count(*) FROM task WHERE job_id = ? AND kind = 'task' AND status IN ('open', 'claimed', 'submitted')`, lead.jobID.Int64).Scan(&n)
	} else {
		err = c.tx.QueryRow(`SELECT count(*) FROM task WHERE project_id = ? AND job_id IS NULL AND kind = 'task' AND status IN ('claimed', 'submitted')`, lead.projectID).Scan(&n)
	}
	return n, err
}

// leadLost is called when an agent becomes offline. If it is a lead whose
// work is still in flight, the human is told: an event, the inbox, a push.
func (c *call) leadLost(a *agentRow) error {
	if a.role != api.RoleLead {
		return nil
	}
	n, err := c.leadWork(a)
	if err != nil || n == 0 {
		return err
	}
	payload := map[string]any{"lead": a.name, "unfinished": n}
	where := "the project"
	if a.jobID.Valid {
		payload["job"] = a.jobID.Int64
		where = fmt.Sprintf("job #%d", a.jobID.Int64)
	}
	if err := c.record(a.projectID, 0, "job.lead_lost", "agent:"+a.name, payload); err != nil {
		return err
	}
	c.tell(notify.Notification{Kind: notify.KindLeadLost, Title: "Handloom: a lead stopped",
		Text: fmt.Sprintf("The lead of %s went offline with %d task(s) unfinished. Open the inbox to resume it.", where, n)})
	return nil
}

// jobResume gives a job a new lead after the old one is gone. The task graph
// is the state; nothing else needs rebuilding. Only a human.
func jobResume(c *call) (any, error) {
	if err := c.humanOnly("resuming a job"); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.JobResumeReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.resumeJob(id, req.Lead)
}

func (c *call) resumeJob(id int64, lead string) (any, error) {
	req := api.JobResumeReq{Lead: lead}
	j, err := c.job(id)
	if err != nil {
		return nil, err
	}
	if j.status != api.StatusOpen {
		return nil, conflict("job %d is %s", j.id, j.status)
	}
	next, err := c.agentByName(req.Lead)
	if err != nil {
		return nil, err
	}
	if next == nil || next.projectID != j.projectID {
		return nil, notFound("no agent %q in this project", req.Lead)
	}
	old, err := scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.project_id = ? AND a.role = 'lead' AND a.job_id = ?`, j.projectID, j.id))
	if err != nil {
		return nil, err
	}
	if old != nil && old.id == next.id {
		return nil, conflict("%s already leads job %d", next.name, j.id)
	}
	if next.role == api.RoleLead {
		return nil, conflict("%s already leads %s; give it a worker's role first", next.name, leadScope(next))
	}
	forwarded := 0
	if old != nil {
		// The old lead steps down to a worker without a job; its unread mail goes to the new lead.
		if err := c.putInJob(old, sql.NullInt64{}, api.RoleWorker); err != nil {
			return nil, err
		}
		if forwarded, err = c.forwardUnread(old, next); err != nil {
			return nil, err
		}
	}
	if err := c.putInJob(next, sql.NullInt64{Int64: j.id, Valid: true}, api.RoleLead); err != nil {
		return nil, err
	}
	counts, err := c.jobCounts(j.id)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf("You are resuming job #%d (%s). Its tasks: %d open, %d claimed, %d submitted, %d done. Read the board with `handloom task list --job %d` and `handloom task show <id>`, review anything submitted, and carry on.",
		j.id, j.title, counts.Open, counts.Claimed, counts.Submitted, counts.Done, j.id)
	if forwarded > 0 {
		body += fmt.Sprintf(" %d unread message(s) of the previous lead follow.", forwarded)
	}
	if err := c.hubMessage(next, &j.id, body); err != nil {
		return nil, err
	}
	oldName := ""
	if old != nil {
		oldName = old.name
	}
	if err := c.record(j.projectID, 0, "job.resume", fmt.Sprintf("job:%d", j.id), map[string]any{"lead": next.name, "was": oldName, "forwarded": forwarded}); err != nil {
		return nil, err
	}
	return c.jobView(j)
}

func leadScope(a *agentRow) string {
	if a.jobID.Valid {
		return fmt.Sprintf("job %d", a.jobID.Int64)
	}
	return "project " + a.project
}

// forwardUnread copies the unread messages of one agent to another, marked as
// forwarded, and marks them read for the first (which is no longer listening).
func (c *call) forwardUnread(from, to *agentRow) (int, error) {
	rows, err := c.tx.Query(`SELECT id, from_id, task_id, body FROM message WHERE to_agent_id = ? AND read_at IS NULL ORDER BY id`, from.id)
	if err != nil {
		return 0, err
	}
	type m struct {
		id   int64
		from string
		task sql.NullInt64
		body string
	}
	var ms []m
	for rows.Next() {
		var x m
		if err := rows.Scan(&x.id, &x.from, &x.task, &x.body); err != nil {
			rows.Close()
			return 0, err
		}
		ms = append(ms, x)
	}
	rows.Close()
	for _, x := range ms {
		body := fmt.Sprintf("[forwarded from %s, sent by %s] %s", from.name, x.from, x.body)
		if _, err := c.insertMessage("hub", to, to.name, nullInt(x.task), body); err != nil {
			return 0, err
		}
	}
	if _, err := c.tx.Exec(`UPDATE message SET read_at = ?, delivered_at = COALESCE(delivered_at, ?), delivered_by = CASE WHEN delivered_at IS NULL THEN 'forwarded' ELSE delivered_by END
		WHERE to_agent_id = ? AND read_at IS NULL`, store.Millis(c.now), store.Millis(c.now), from.id); err != nil {
		return 0, err
	}
	return len(ms), nil
}
