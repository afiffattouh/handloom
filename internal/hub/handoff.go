package hub

import (
	"database/sql"
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Handoff notes. The unit of durability is the task, the git branch and the
// evidence; an agent's own context is not. So whoever owns a task leaves a short
// note when they stop, release or submit (and may leave one at any time), and the
// next owner reads it first. The notes are append-only; the latest is the current one.

const maxHandoffField = 4000

// cleanHandoff trims a note and checks it says where the work got to.
func cleanHandoff(h api.HandoffReq, need string) (api.HandoffReq, error) {
	h.Done, h.Tried, h.Next, h.Verify = strings.TrimSpace(h.Done), strings.TrimSpace(h.Tried), strings.TrimSpace(h.Next), strings.TrimSpace(h.Verify)
	for _, f := range []string{h.Done, h.Tried, h.Next, h.Verify} {
		if len(f) > maxHandoffField {
			return h, badRequest("a handoff note field is longer than %d characters: say less, and put detail in a file in the repository", maxHandoffField)
		}
	}
	if h.Done == "" {
		return h, badRequest("%s", need)
	}
	return h, nil
}

func (c *call) addHandoff(taskID int64, kind string, h api.HandoffReq) error {
	_, err := c.tx.Exec(`INSERT INTO handoff(task_id, author, kind, done, tried, next, verify, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, c.p.actor(), kind, h.Done, h.Tried, h.Next, h.Verify, store.Millis(c.now))
	return err
}

// latestHandoff is the current note on a task, or nil.
func (c *call) latestHandoff(taskID int64) (*api.Handoff, error) {
	var h api.Handoff
	var at int64
	err := c.tx.QueryRow(`SELECT id, author, kind, done, tried, next, verify, created_at FROM handoff WHERE task_id = ? ORDER BY id DESC LIMIT 1`, taskID).
		Scan(&h.ID, &h.Author, &h.Kind, &h.Done, &h.Tried, &h.Next, &h.Verify, &at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.At = store.Time(at)
	return &h, nil
}

// handoffLine is a note in one line, for a message to the lead.
func handoffLine(h *api.Handoff) string {
	if h == nil {
		return "No handoff note was ever written for it, so the next owner starts from the task and the git history only."
	}
	s := fmt.Sprintf("The last handoff note (by %s, %s) says: %s", h.Author, h.Kind, h.Done)
	if h.Next != "" {
		s += " Next: " + h.Next
	}
	return s
}

// taskHandoff is a checkpoint: the owner records progress without stopping.
func taskHandoff(c *call) (any, error) {
	t, _, err := c.ownTask()
	if err != nil {
		return nil, err
	}
	var req api.HandoffReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req, err = cleanHandoff(req, "a handoff note needs at least where the work got to (done)")
	if err != nil {
		return nil, err
	}
	if err := c.addHandoff(t.id, "checkpoint", req); err != nil {
		return nil, err
	}
	if err := c.record(t.projectID, 0, "task.handoff", t.target(), map[string]any{"kind": "checkpoint"}); err != nil {
		return nil, err
	}
	// a checkpoint is a sign of life too
	if err := c.touch(t, `lease_expires_at = ?`, c.leaseUntil()); err != nil {
		return nil, err
	}
	return c.reloadWithHandoff(t.id)
}

// reloadWithHandoff is the task as the API returns it, with its latest handoff note.
func (c *call) reloadWithHandoff(id int64) (api.Task, error) {
	t, err := c.task(id)
	if err != nil {
		return api.Task{}, err
	}
	out := t.api()
	out.Handoff, err = c.latestHandoff(id)
	return out, err
}

// resumeNotice says, on a claim, whether the task was started before and what to do first.
func (c *call) resumeNotice(t *taskRow, h *api.Handoff) (string, error) {
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM audit WHERE action = 'task.claim' AND target = ?`, t.target()).Scan(&n); err != nil {
		return "", err
	}
	if n == 0 && h == nil { // nobody has claimed it before
		return "", nil
	}
	s := "This task was started before. Do not trust that the last step finished: check the git history and the state of the work, and read the handoff note, before you continue. Do not repeat an action that may already have happened."
	if h == nil {
		s += " There is no handoff note."
	}
	return s, nil
}
