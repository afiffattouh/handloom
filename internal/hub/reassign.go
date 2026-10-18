package hub

import (
	"fmt"
	"regexp"
	"strings"

	"handloom/internal/api"
)

// Redirecting work: a person takes a task from its helper and gives it to a new helper started from another
// profile. The new helper starts fresh from the task and a note; the old one is told to stop. Nothing the old
// helper did is merged unless it was already accepted.

func taskReassign(c *call) (any, error) {
	if c.p.kind == kindDevice {
		return nil, forbidden("redirecting work is for a person, not an agent")
	}
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	var req api.ReassignReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.reassignTask(t, req)
}

var nameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func (c *call) reassignTask(t *taskRow, req api.ReassignReq) (any, error) {
	req.Profile, req.Note = strings.TrimSpace(req.Profile), strings.TrimSpace(req.Note)
	if req.Profile == "" {
		return nil, badRequest("say which profile the new helper should start from")
	}
	if len(req.Note) > 2000 {
		return nil, badRequest("the note is too long")
	}
	if t.kind == "job" || !t.jobID.Valid {
		return nil, badRequest("only a task of a job can be given to another helper")
	}
	switch t.status {
	case api.StatusOpen, api.StatusClaimed, api.StatusSubmitted:
	default:
		return nil, conflict("task %d is %s; only unfinished work can be given to another helper", t.id, t.status)
	}
	// A name nobody has: the profile and the task, then a number if that is taken.
	base := nameChars.ReplaceAllString(strings.ToLower(strings.SplitN(req.Profile, "@", 2)[0]), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "helper"
	}
	name := fmt.Sprintf("%s-t%d", base, t.id)
	for i := 2; ; i++ {
		if a, err := c.agentByName(name); err != nil {
			return nil, err
		} else if a == nil {
			break
		}
		name = fmt.Sprintf("%s-t%d-%d", base, t.id, i)
	}
	// Start the new helper first: a profile that does not exist, or a job that is full, stops here and changes nothing.
	if _, err := c.spawnFromRequest(api.SpawnReq{Name: name, Profile: req.Profile, Job: t.jobID.Int64, Role: api.RoleWorker}); err != nil {
		return nil, err
	}
	old := t.ownerName
	if t.owner.Valid {
		if err := c.notifyOwner(t, fmt.Sprintf("Task #%d (%s) was given to another helper by a person. Stop working on it; do not submit it.", t.id, t.title)); err != nil {
			return nil, err
		}
	}
	if err := c.touch(t, `status = 'open', owner_agent_id = NULL, lease_expires_at = NULL, blocked_reason = '', assigned_to = NULL, claimable_at = NULL, assign_name = ?`, name); err != nil {
		return nil, err
	}
	for _, tbl := range []string{"task_check", "task_result"} {
		if _, err := c.tx.Exec(`DELETE FROM `+tbl+` WHERE task_id = ?`, t.id); err != nil {
			return nil, err
		}
	}
	done := "A person gave this task to a new helper, " + name + ", started from the profile " + req.Profile + "."
	if old != "" {
		done = fmt.Sprintf("A person took this task from %s and gave it to a new helper, %s (profile %s). Start from the task description; %s's branch is not used.", old, name, req.Profile, old)
	}
	next := "Do the task as written."
	if req.Note != "" {
		next = req.Note
	}
	if err := c.addHandoff(t.id, "reassign", api.HandoffReq{Done: done, Next: next}); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.reassign", t.target(), map[string]any{"from": old, "to": name, "profile": req.Profile}); err != nil {
		return nil, err
	}
	if lead, err := c.leadFor(t.projectID, t.jobID); err != nil {
		return nil, err
	} else if lead != nil && lead.name != old {
		if err := c.hubMessage(lead, &t.id, fmt.Sprintf("Task #%d (%s) was given by a person to a new helper, %s. Do not assign it to anyone else.", t.id, t.title, name)); err != nil {
			return nil, err
		}
	}
	return c.reload(t)
}

// takePendingAssignments gives a helper that has just registered the tasks a person gave it by name.
func (c *call) takePendingAssignments(a *agentRow) error {
	pending, err := c.tasks(`WHERE t.assign_name = ? AND t.status = 'open'`, a.name)
	if err != nil {
		return err
	}
	for _, t := range pending {
		if _, err := c.tx.Exec(`UPDATE task SET assigned_to = ?, assign_name = '' WHERE id = ?`, a.id, t.id); err != nil {
			return err
		}
		if err := c.notifyAssigned(t, a); err != nil {
			return err
		}
	}
	return nil
}
