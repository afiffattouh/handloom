package hub

import (
	"database/sql"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

const maxMessageBody = 16 << 10

func (c *call) messageIDs(where string, args ...any) ([]int64, error) {
	rows, err := c.tx.Query(`SELECT id FROM message WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// markDelivered stamps every unread, undelivered message of an agent.
func (c *call) markDelivered(a *agentRow, method string) error {
	_, err := c.tx.Exec(`UPDATE message SET delivered_at = ?, delivered_by = ?
		WHERE to_agent_id = ? AND read_at IS NULL AND delivered_at IS NULL`, store.Millis(c.now), method, a.id)
	return err
}

// insertMessage stores one message for one recipient and emits the event
// that makes the recipient's link run the wake ladder.
func (c *call) insertMessage(from string, to *agentRow, toSpec string, taskID *int64, body string) (int64, error) {
	res, err := c.tx.Exec(`INSERT INTO message(project_id, from_id, to_agent_id, to_spec, task_id, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, to.projectID, from, to.id, toSpec, taskID, body, store.Millis(c.now))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	payload := map[string]any{"id": id, "from": from}
	if taskID != nil {
		payload["task_id"] = *taskID
	}
	return id, c.emit(to.projectID, to.id, "message.new", payload)
}

// hubMessage is a notice from the hub itself (task assigned, submitted,
// lease expired, ...). The caller is not notified about its own action.
func (c *call) hubMessage(to *agentRow, taskID *int64, body string) error {
	if to == nil || (c.p.agent != nil && c.p.agent.id == to.id) {
		return nil
	}
	_, err := c.insertMessage("hub", to, to.name, taskID, body)
	return err
}

// messageSend: the sender is always derived from the credentials. An agent
// cannot produce a "human:" sender, whatever it puts in the request.
func messageSend(c *call) (any, error) {
	if err := c.allow(ActSend); err != nil {
		return nil, err
	}
	var req api.SendReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" {
		return nil, badRequest("message body is empty")
	}
	if len(req.Body) > maxMessageBody {
		return nil, badRequest("message body is larger than %d bytes", maxMessageBody)
	}
	projectID, _, err := c.project(req.Project)
	if err != nil {
		return nil, err
	}
	from := c.p.actor()
	if !c.h.rateOK(from, c.now) {
		return nil, &apiError{429, "rate_limited", "too many messages; slow down"}
	}
	if req.TaskID != nil {
		t, err := c.task(*req.TaskID)
		if err != nil {
			return nil, err
		}
		if t.projectID != projectID {
			return nil, notFound("no task %d", *req.TaskID)
		}
	}

	var to []*agentRow
	switch {
	case strings.HasPrefix(req.To, "role:"):
		role := strings.TrimPrefix(req.To, "role:")
		to, err = c.agents(`WHERE a.project_id = ? AND a.role = ?`, projectID, role)
		if err != nil {
			return nil, err
		}
		if len(to) == 0 {
			return nil, notFound("no agent with role %s in this project", role)
		}
	case strings.HasPrefix(req.To, "task:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(req.To, "task:"), 10, 64)
		if err != nil {
			return nil, badRequest("bad recipient %q", req.To)
		}
		t, err := c.task(id)
		if err != nil {
			return nil, err
		}
		if t.projectID != projectID {
			return nil, notFound("no task %d", id)
		}
		if req.TaskID == nil {
			req.TaskID = &id
		}
		// Owner plus lead.
		if t.owner.Valid {
			a, err := c.agentByID(t.owner.Int64)
			if err != nil {
				return nil, err
			}
			to = append(to, a)
		}
		lead, err := c.lead(projectID)
		if err != nil {
			return nil, err
		}
		if lead != nil && (len(to) == 0 || to[0].id != lead.id) {
			to = append(to, lead)
		}
	default:
		a, err := c.agentByName(req.To)
		if err != nil {
			return nil, err
		}
		if a == nil || a.projectID != projectID {
			return nil, notFound("no agent %q in this project", req.To)
		}
		to = []*agentRow{a}
	}

	resp := api.SendResp{IDs: []int64{}, Recipients: []string{}}
	for _, a := range to {
		if c.p.agent != nil && a.id == c.p.agent.id && len(to) > 1 {
			continue // do not echo a group message back to its sender
		}
		id, err := c.insertMessage(from, a, req.To, req.TaskID, req.Body)
		if err != nil {
			return nil, err
		}
		resp.IDs = append(resp.IDs, id)
		resp.Recipients = append(resp.Recipients, a.name)
	}
	if len(resp.IDs) == 0 {
		return nil, badRequest("no recipients for %q", req.To)
	}
	if err := c.audit("message.send", "message:"+joinIDs(resp.IDs),
		map[string]any{"to": req.To, "recipients": resp.Recipients, "task_id": req.TaskID}); err != nil {
		return nil, err
	}
	return resp, nil
}

func joinIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ",")
}

const messageSelect = `SELECT m.id, p.name, m.from_id, a.name, m.to_spec, m.task_id, m.body, m.created_at,
	m.delivered_at, m.delivered_by, m.read_at
	FROM message m JOIN project p ON p.id = m.project_id JOIN agent a ON a.id = m.to_agent_id `

func (c *call) messages(where string, args ...any) ([]api.Message, error) {
	rows, err := c.tx.Query(messageSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Message{}
	for rows.Next() {
		var m api.Message
		var task, delivered, read sql.NullInt64
		var created int64
		if err := rows.Scan(&m.ID, &m.Project, &m.From, &m.To, &m.ToSpec, &task, &m.Body, &created,
			&delivered, &m.DeliveredBy, &read); err != nil {
			return nil, err
		}
		if task.Valid {
			m.TaskID = &task.Int64
		}
		m.CreatedAt, m.DeliveredAt, m.ReadAt = store.Time(created), store.TimePtr(delivered), store.TimePtr(read)
		out = append(out, m)
	}
	return out, rows.Err()
}

// inbox returns the calling agent's unread messages and marks them read.
// With ?all=1 it returns the full history and marks nothing.
func inbox(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	a, err := c.agent()
	if err != nil {
		return nil, err
	}
	if c.r.URL.Query().Get("all") != "" {
		return c.messages(`WHERE m.to_agent_id = ? ORDER BY m.id`, a.id)
	}
	msgs, err := c.messages(`WHERE m.to_agent_id = ? AND m.read_at IS NULL ORDER BY m.id`, a.id)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return msgs, nil
	}
	ms := store.Millis(c.now)
	if _, err := c.tx.Exec(`UPDATE message SET read_at = ?,
		delivered_at = COALESCE(delivered_at, ?),
		delivered_by = CASE WHEN delivered_at IS NULL THEN 'inbox' ELSE delivered_by END
		WHERE to_agent_id = ? AND read_at IS NULL`, ms, ms, a.id); err != nil {
		return nil, err
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if err := c.record(a.projectID, 0, "inbox.read", "agent:"+a.name, map[string]any{"messages": ids}); err != nil {
		return nil, err
	}
	return msgs, nil
}

// messageDelivered lets a link report delivery of a single message.
func messageDelivered(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a link may report delivery")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.DeliveredReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Method == "" {
		return nil, badRequest("method is required")
	}
	var deviceID int64
	var name string
	err = c.tx.QueryRow(`SELECT a.device_id, a.name FROM message m JOIN agent a ON a.id = m.to_agent_id WHERE m.id = ?`, id).
		Scan(&deviceID, &name)
	if err != nil || deviceID != c.p.deviceID {
		return nil, forbidden("message %d is not for an agent on device %s", id, c.p.name)
	}
	if _, err := c.tx.Exec(`UPDATE message SET delivered_at = ?, delivered_by = ? WHERE id = ? AND delivered_at IS NULL`,
		store.Millis(c.now), req.Method, id); err != nil {
		return nil, err
	}
	return nil, c.audit("message.delivered", "message:"+strconv.FormatInt(id, 10), map[string]any{"method": req.Method, "agent": name})
}
