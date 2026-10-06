package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

const (
	maxQuestion     = 4 << 10
	maxOptions      = 10
	maxOptionLen    = 200
	maxAnswerLength = 4 << 10
)

type escalationRow struct {
	id         int64
	projectID  int64
	project    string
	fromID     int64
	from       string
	taskID     sql.NullInt64
	question   string
	options    []string
	answer     sql.NullString
	answeredBy string
	createdAt  int64
	answeredAt sql.NullInt64
}

const escalationSelect = `SELECT e.id, e.project_id, p.name, e.from_agent_id, a.name, e.task_id, e.question,
	e.options, e.answer, e.answered_by, e.created_at, e.answered_at
	FROM escalation e JOIN project p ON p.id = e.project_id JOIN agent a ON a.id = e.from_agent_id `

func scanEscalation(s scanner) (*escalationRow, error) {
	e := &escalationRow{}
	var opts string
	err := s.Scan(&e.id, &e.projectID, &e.project, &e.fromID, &e.from, &e.taskID, &e.question,
		&opts, &e.answer, &e.answeredBy, &e.createdAt, &e.answeredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(opts), &e.options); err != nil || e.options == nil {
		e.options = []string{}
	}
	return e, nil
}

func (e *escalationRow) api() api.Escalation {
	out := api.Escalation{
		ID: e.id, Project: e.project, From: e.from, Question: e.question, Options: e.options,
		AnsweredBy: e.answeredBy, CreatedAt: store.Time(e.createdAt), AnsweredAt: store.TimePtr(e.answeredAt),
	}
	if e.taskID.Valid {
		id := e.taskID.Int64
		out.TaskID = &id
	}
	if e.answer.Valid {
		a := e.answer.String
		out.Answer = &a
	}
	return out
}

func (e *escalationRow) target() string { return fmt.Sprintf("escalation:%d", e.id) }

func (c *call) escalation(id int64) (*escalationRow, error) {
	e, err := scanEscalation(c.tx.QueryRow(escalationSelect+`WHERE e.id = ?`, id))
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, notFound("no escalation %d", id)
	}
	// Agents see only their own project's escalations; humans and the admin see all.
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if a.projectID != e.projectID {
			return nil, notFound("no escalation %d", id)
		}
	}
	return e, nil
}

// escalationOpen: the lead asks the human a question. The caller does not
// wait here; `handloom ask` polls GET /escalations/{id}.
func escalationOpen(c *call) (any, error) {
	if err := c.allow(ActEscalationOpen); err != nil {
		return nil, err
	}
	var req api.AskReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		return nil, badRequest("the question is empty")
	}
	if len(req.Question) > maxQuestion {
		return nil, badRequest("the question is larger than %d bytes", maxQuestion)
	}
	if len(req.Options) > maxOptions {
		return nil, badRequest("at most %d options", maxOptions)
	}
	seen := map[string]bool{}
	for i, o := range req.Options {
		o = strings.TrimSpace(o)
		if o == "" || len(o) > maxOptionLen {
			return nil, badRequest("an option must have 1 to %d characters", maxOptionLen)
		}
		if seen[o] {
			return nil, badRequest("option %q is listed twice", o)
		}
		seen[o] = true
		req.Options[i] = o
	}
	if req.Options == nil {
		req.Options = []string{}
	}
	a, err := c.agent()
	if err != nil {
		return nil, err
	}
	if req.TaskID != nil {
		t, err := c.task(*req.TaskID)
		if err != nil {
			return nil, err
		}
		if t.projectID != a.projectID {
			return nil, notFound("no task %d", *req.TaskID)
		}
	}
	if !c.h.rateOK("ask:"+a.name, c.now) {
		return nil, &apiError{429, "rate_limited", "too many questions; slow down"}
	}
	opts, _ := json.Marshal(req.Options)
	res, err := c.tx.Exec(`INSERT INTO escalation(project_id, from_agent_id, task_id, question, options, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, a.projectID, a.id, req.TaskID, req.Question, string(opts), store.Millis(c.now))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	e, err := c.escalation(id)
	if err != nil {
		return nil, err
	}
	if err := c.record(a.projectID, 0, "escalation.open", e.target(), map[string]any{"from": a.name, "task_id": req.TaskID}); err != nil {
		return nil, err
	}
	return e.api(), nil
}

func escalationGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	e, err := c.escalation(id)
	if err != nil {
		return nil, err
	}
	return e.api(), nil
}

// escalationList returns open escalations by default; ?status=answered or
// ?status=all widens it.
func escalationList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	where, args := ``, []any{}
	switch s := c.r.URL.Query().Get("status"); s {
	case "", "open":
		where = `WHERE e.answered_at IS NULL `
	case "answered":
		where = `WHERE e.answered_at IS NOT NULL `
	case "all":
	default:
		return nil, badRequest("bad status %q: use open, answered or all", s)
	}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if where == "" {
			where = `WHERE e.project_id = ? `
		} else {
			where += `AND e.project_id = ? `
		}
		args = append(args, a.projectID)
	}
	rows, err := c.tx.Query(escalationSelect+where+`ORDER BY e.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Escalation{}
	for rows.Next() {
		e, err := scanEscalation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e.api())
	}
	return out, rows.Err()
}

// escalationAnswer: only a human. The answer reaches the asking agent as a
// message whose sender is "human:<name>", taken from the credentials; no
// agent can produce that sender.
func escalationAnswer(c *call) (any, error) {
	if err := c.allow(ActEscalationAnswer); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.AnswerReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req.Answer = strings.TrimSpace(req.Answer)
	if req.Answer == "" {
		return nil, badRequest("the answer is empty")
	}
	if len(req.Answer) > maxAnswerLength {
		return nil, badRequest("the answer is larger than %d bytes", maxAnswerLength)
	}
	e, err := c.escalation(id)
	if err != nil {
		return nil, err
	}
	if e.answeredAt.Valid {
		return nil, conflict("escalation %d is already answered", e.id)
	}
	if len(e.options) > 0 {
		ok := false
		for _, o := range e.options {
			ok = ok || o == req.Answer
		}
		if !ok {
			return nil, badRequest("the answer must be one of: %s", strings.Join(e.options, ", "))
		}
	}
	by := c.p.actor() // human:<name>
	if _, err := c.tx.Exec(`UPDATE escalation SET answer = ?, answered_by = ?, answered_at = ? WHERE id = ?`,
		req.Answer, by, store.Millis(c.now), e.id); err != nil {
		return nil, err
	}
	asker, err := c.agentByID(e.fromID)
	if err != nil {
		return nil, err
	}
	var taskID *int64
	if e.taskID.Valid {
		t := e.taskID.Int64
		taskID = &t
	}
	body := fmt.Sprintf("Answer to your question #%d (%q): %s", e.id, clip(e.question, 120), req.Answer)
	if _, err := c.insertMessage(by, asker, asker.name, taskID, body); err != nil {
		return nil, err
	}
	if err := c.record(e.projectID, 0, "escalation.answer", e.target(), map[string]any{"by": by}); err != nil {
		return nil, err
	}
	e, err = c.escalation(id)
	if err != nil {
		return nil, err
	}
	return e.api(), nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
