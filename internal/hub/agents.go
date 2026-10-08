package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

type agentRow struct {
	id, projectID, deviceID int64
	name, kind, role        string
	wakeTarget, sessionID   string
	dir                     string
	state                   string
	stateAt, hookMsgID      int64
	registeredAt            int64
	project, device         string
	jobID                   sql.NullInt64
	runTokenHash            string
	lease                   sql.NullInt64
}

func (a *agentRow) api() api.Agent {
	return api.Agent{
		Name: a.name, Kind: a.kind, Role: a.role, Project: a.project, Device: a.device,
		State: a.state, StateAt: store.Time(a.stateAt), WakeTarget: a.wakeTarget,
		SessionID: a.sessionID, Dir: a.dir, RegisteredAt: store.Time(a.registeredAt),
		Job: nullInt(a.jobID), LeaseUntil: store.TimePtr(a.lease),
	}
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

const agentSelect = `SELECT a.id, a.project_id, a.device_id, a.name, a.kind, a.role, a.wake_target,
	a.session_id, a.state, a.state_at, a.hook_msg_id, a.registered_at, p.name, d.name, a.dir, a.job_id, COALESCE(a.run_token_hash, ''), a.lease_expires_at
	FROM agent a JOIN project p ON p.id = a.project_id JOIN device d ON d.id = a.device_id `

type scanner interface{ Scan(...any) error }

func scanAgent(s scanner) (*agentRow, error) {
	a := &agentRow{}
	err := s.Scan(&a.id, &a.projectID, &a.deviceID, &a.name, &a.kind, &a.role, &a.wakeTarget,
		&a.sessionID, &a.state, &a.stateAt, &a.hookMsgID, &a.registeredAt, &a.project, &a.device, &a.dir, &a.jobID, &a.runTokenHash, &a.lease)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (c *call) agentByName(name string) (*agentRow, error) {
	return scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.name = ?`, name))
}

func (c *call) agentByID(id int64) (*agentRow, error) {
	return scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.id = ?`, id))
}

func (c *call) agents(where string, args ...any) ([]*agentRow, error) {
	rows, err := c.tx.Query(agentSelect+where+` ORDER BY a.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*agentRow
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// lead is the project-level lead: the one that belongs to no job.
func (c *call) lead(projectID int64) (*agentRow, error) {
	return scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.project_id = ? AND a.role = 'lead' AND a.job_id IS NULL`, projectID))
}

// leadFor is who judges work in a job: the job's own lead, or the project
// lead when the job has none (and for tasks that belong to no job).
func (c *call) leadFor(projectID int64, job sql.NullInt64) (*agentRow, error) {
	if job.Valid {
		a, err := scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.project_id = ? AND a.role = 'lead' AND a.job_id = ?`, projectID, job.Int64))
		if err != nil || a != nil {
			return a, err
		}
	}
	return c.lead(projectID)
}

// leadOf is the lead an agent reports to: that of its own job, else that of
// the job of the task it is working on, else the project lead.
func (c *call) leadOf(a *agentRow) (*agentRow, error) {
	job := a.jobID
	if !job.Valid {
		// The job of the task it is working on, else of its latest task in a job that is still open.
		err := c.tx.QueryRow(`SELECT t.job_id FROM task t JOIN task j ON j.id = t.job_id
			WHERE t.owner_agent_id = ? AND t.job_id IS NOT NULL AND j.status = 'open'
			ORDER BY (t.status IN ('claimed', 'submitted')) DESC, t.updated_at DESC LIMIT 1`, a.id).Scan(&job)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return c.leadFor(a.projectID, job)
}

// localAgent returns the agent named in the path, which must live on the
// calling device. The link and the adapter hooks use these endpoints.
func (c *call) localAgent() (*agentRow, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the agent's own device may do this")
	}
	name := c.r.PathValue("name")
	a, err := c.agentByName(name)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not registered on device %s", name, c.p.name)
	}
	// Hooks speak for the agent and carry its token; the link's own calls
	// do not, and are not asked to.
	if c.p.runToken != "" {
		if err := c.verifyRun(a, false); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func whoami(c *call) (any, error) {
	out := api.WhoAmI{Kind: c.p.kind, Name: c.p.name}
	if c.p.kind == kindHuman {
		out.Role = c.p.role
	}
	if c.p.kind == kindDevice {
		out.Device, out.Name = c.p.name, ""
		if c.p.agentName != "" {
			a, err := c.agent()
			if err != nil {
				return nil, err
			}
			v := a.api()
			out.Agent = &v
			out.Via = c.p.via
		}
	}
	return out, nil
}

// agentRegister creates or refreshes an agent on the calling device. The role
// is never taken from the request: new agents are workers until a human or
// the admin says otherwise.
func agentRegister(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("agents register through their device's link")
	}
	var req api.RegisterReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if err := checkName("agent", req.Name); err != nil {
		return nil, err
	}
	a, err := c.agentByName(req.Name)
	if err != nil {
		return nil, err
	}
	if a != nil {
		if a.deviceID != c.p.deviceID {
			return nil, conflict("agent name %q is taken by another device", req.Name)
		}
		if req.Project != "" && req.Project != a.project {
			return nil, conflict("agent %q is already in project %s", req.Name, a.project)
		}
		// A request that carries a token must carry the right one. One that
		// carries none is the old, device-asserted kind: a hub that requires
		// tokens refuses it, except for the registration that mints one.
		// (A rotation is authorised by the device, as it always has been.)
		if !req.RotateToken && (c.p.runToken != "" || c.h.opt.RequireRunToken) {
			if err := c.verifyRun(a, true); err != nil {
				return nil, err
			}
		}
		kind := a.kind
		if req.Kind != "" {
			kind = req.Kind
		}
		// An empty wake target or session id keeps the recorded one, so a
		// re-register from a hook without terminal variables loses nothing.
		wake, session, dir := a.wakeTarget, a.sessionID, a.dir
		if req.WakeTarget != "" {
			wake = req.WakeTarget
		}
		if req.SessionID != "" {
			session = req.SessionID
		}
		if req.Dir != "" {
			dir = req.Dir
		}
		if _, err := c.tx.Exec(`UPDATE agent SET kind = ?, wake_target = ?, session_id = ?, dir = ? WHERE id = ?`,
			kind, wake, session, dir, a.id); err != nil {
			return nil, err
		}
		a.kind, a.wakeTarget, a.sessionID, a.dir = kind, wake, session, dir
		c.p.agent = a
		if err := c.record(a.projectID, 0, "agent.register", "agent:"+a.name,
			map[string]any{"kind": kind, "wake_target": wake, "session_id": session, "again": true, "rotate_token": req.RotateToken}); err != nil {
			return nil, err
		}
		out := a.api()
		if req.RotateToken {
			if out.RunToken, err = c.mintRunToken(a); err != nil {
				return nil, err
			}
			if err := c.audit("agent.token", "agent:"+a.name, map[string]any{"again": true}); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	if req.Kind == "" {
		return nil, badRequest("kind is required")
	}
	if req.Project == "" {
		req.Project = api.DefaultProject
	}
	var projectID int64
	if err := c.tx.QueryRow(`SELECT id FROM project WHERE name = ?`, req.Project).Scan(&projectID); err != nil {
		return nil, notFound("no project %q", req.Project)
	}
	ms := store.Millis(c.now)
	res, err := c.tx.Exec(`INSERT INTO agent(project_id, device_id, name, kind, role, wake_target, session_id, dir, state, state_at, registered_at)
		VALUES (?, ?, ?, ?, 'worker', ?, ?, ?, 'unknown', ?, ?)`,
		projectID, c.p.deviceID, req.Name, req.Kind, req.WakeTarget, req.SessionID, req.Dir, ms, ms)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	a, err = c.agentByID(id)
	if err != nil {
		return nil, err
	}
	c.p.agent = a
	if err := c.record(projectID, 0, "agent.register", "agent:"+a.name,
		map[string]any{"kind": a.kind, "role": a.role, "device": a.device, "wake_target": a.wakeTarget}); err != nil {
		return nil, err
	}
	out := a.api()
	if req.RotateToken {
		if out.RunToken, err = c.mintRunToken(a); err != nil {
			return nil, err
		}
		if err := c.audit("agent.token", "agent:"+a.name, map[string]any{"again": false}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func agentList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	projectID, _, err := c.project(c.r.URL.Query().Get("project"))
	if err != nil {
		return nil, err
	}
	rows, err := c.agents(`WHERE a.project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Agent, 0, len(rows))
	for _, a := range rows {
		out = append(out, a.api())
	}
	return out, nil
}

// agentRole sets a role. Only a human or the admin may; agents never can.
func agentRole(c *call) (any, error) {
	if c.p.kind != kindAdmin && c.p.kind != kindHuman {
		return nil, forbidden("roles are set by a human or the admin, not by agents")
	}
	var req api.RoleReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	switch req.Role {
	case api.RoleLead, api.RoleWorker, api.RoleObserver:
	default:
		return nil, badRequest("role must be lead, worker or observer")
	}
	a, err := c.agentByName(c.r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, notFound("no agent %q", c.r.PathValue("name"))
	}
	if req.Role == api.RoleLead {
		if err := c.checkLeadFree(a, a.jobID); err != nil {
			return nil, err
		}
	}
	if _, err := c.tx.Exec(`UPDATE agent SET role = ? WHERE id = ?`, req.Role, a.id); err != nil {
		return nil, err
	}
	old := a.role
	a.role = req.Role
	if err := c.record(a.projectID, 0, "agent.role", "agent:"+a.name, map[string]any{"role": req.Role, "was": old}); err != nil {
		return nil, err
	}
	return a.api(), nil
}

// checkLeadFree fails when someone else already leads that job (or the project, without a job).
// mintRunToken gives the agent a new run token, replacing any old one. Only
// its hash is stored; the token is returned once.
func (c *call) mintRunToken(a *agentRow) (string, error) {
	tok := store.NewToken(store.PrefixRun)
	if _, err := c.tx.Exec(`UPDATE agent SET run_token_hash = ?, run_token_at = ? WHERE id = ?`,
		store.HashToken(tok), store.Millis(c.now), a.id); err != nil {
		return "", err
	}
	a.runTokenHash = store.HashToken(tok)
	return tok, nil
}

func (c *call) checkLeadFree(a *agentRow, job sql.NullInt64) error {
	var cur *agentRow
	var err error
	if job.Valid {
		cur, err = scanAgent(c.tx.QueryRow(agentSelect+`WHERE a.project_id = ? AND a.role = 'lead' AND a.job_id = ?`, a.projectID, job.Int64))
	} else {
		cur, err = c.lead(a.projectID)
	}
	if err != nil {
		return err
	}
	if cur != nil && cur.id != a.id {
		where := "project " + a.project
		if job.Valid {
			where = fmt.Sprintf("job %d", job.Int64)
		}
		return conflict("%s already has a lead (%s); change that agent's role first", where, cur.name)
	}
	return nil
}

func (c *call) setState(a *agentRow, state string, why string) error {
	if _, err := c.tx.Exec(`UPDATE agent SET state = ?, state_at = ? WHERE id = ?`, state, store.Millis(c.now), a.id); err != nil {
		return err
	}
	if a.state == state {
		return nil
	}
	payload := map[string]any{"state": state, "was": a.state}
	if why != "" {
		payload["reason"] = why
	}
	a.state = state
	if err := c.record(a.projectID, 0, "agent.state", "agent:"+a.name, payload); err != nil {
		return err
	}
	if state == api.StateOffline {
		return c.leadLost(a)
	}
	return nil
}

// agentState is called by adapter hooks through the link.
func agentState(c *call) (any, error) {
	a, err := c.localAgent()
	if err != nil {
		return nil, err
	}
	var req api.StateReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	switch req.State {
	case api.StateIdle, api.StateWorking, api.StateBlocked, api.StateOffline:
	default:
		return nil, badRequest("state must be idle, working, blocked or offline")
	}
	if err := c.setState(a, req.State, ""); err != nil {
		return nil, err
	}
	return a.api(), nil
}

// agentTurnEnd is the end-of-turn hook (wake ladder step 1). If mail arrived
// that the hook has not handed over yet, the agent is told to continue and
// stays "working". Otherwise it becomes idle. hook_msg_id makes sure one
// batch of mail blocks the stop at most once.
func agentTurnEnd(c *call) (any, error) {
	a, err := c.localAgent()
	if err != nil {
		return nil, err
	}
	var unread int
	if err := c.tx.QueryRow(`SELECT count(*) FROM message WHERE to_agent_id = ? AND read_at IS NULL`, a.id).Scan(&unread); err != nil {
		return nil, err
	}
	ids, err := c.messageIDs(`to_agent_id = ? AND read_at IS NULL AND id > ?`, a.id, a.hookMsgID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		if err := c.setState(a, api.StateIdle, ""); err != nil {
			return nil, err
		}
		return api.TurnEndResp{Block: false, Unread: unread}, nil
	}
	newest := ids[len(ids)-1]
	if _, err := c.tx.Exec(`UPDATE agent SET hook_msg_id = ? WHERE id = ?`, newest, a.id); err != nil {
		return nil, err
	}
	if err := c.markDelivered(a, api.WakeHook); err != nil {
		return nil, err
	}
	if err := c.setState(a, api.StateWorking, ""); err != nil {
		return nil, err
	}
	if err := c.record(a.projectID, 0, "wake", "agent:"+a.name,
		map[string]any{"method": api.WakeHook, "messages": ids}); err != nil {
		return nil, err
	}
	return api.TurnEndResp{Block: true, Unread: unread}, nil
}

// agentWake is the link's report of one wake attempt: a nudge it typed, or a
// failure (method "none") that the lead must hear about.
func agentWake(c *call) (any, error) {
	a, err := c.localAgent()
	if err != nil {
		return nil, err
	}
	var req api.WakeReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	ids, err := c.messageIDs(`to_agent_id = ? AND read_at IS NULL`, a.id)
	if err != nil {
		return nil, err
	}
	switch req.Method {
	case api.WakeTmux, api.WakeHerdr, api.WakeHeadless:
		if err := c.markDelivered(a, req.Method); err != nil {
			return nil, err
		}
		if err := c.record(a.projectID, 0, "wake", "agent:"+a.name,
			map[string]any{"method": req.Method, "messages": ids}); err != nil {
			return nil, err
		}
	case api.WakeNone:
		if req.Reason == "" {
			return nil, badRequest("reason is required when method is none")
		}
		if strings.HasPrefix(req.Reason, "no_inbox_after_nudges") {
			if err := c.setState(a, api.StateUnknown, req.Reason); err != nil {
				return nil, err
			}
		}
		if err := c.record(a.projectID, 0, "wake.failed", "agent:"+a.name,
			map[string]any{"reason": req.Reason, "messages": ids}); err != nil {
			return nil, err
		}
		lead, err := c.leadOf(a)
		if err != nil {
			return nil, err
		}
		if lead != nil && lead.id != a.id {
			body := "Agent " + a.name + " cannot be woken (" + req.Reason + "). It has " +
				strconv.Itoa(len(ids)) + " unread message(s)."
			if err := c.hubMessage(lead, nil, body); err != nil {
				return nil, err
			}
		}
	default:
		return nil, badRequest("method must be tmux, herdr, headless or none")
	}
	return api.WakeResp{Messages: ids}, nil
}

// deviceAgents lists the calling device's agents with their mail counters.
// The link runs the wake ladder from this.
func deviceAgents(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list its agents")
	}
	rows, err := c.agents(`WHERE a.device_id = ?`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	out := make([]api.DeviceAgent, 0, len(rows))
	for _, a := range rows {
		da := api.DeviceAgent{Agent: a.api()}
		err := c.tx.QueryRow(`SELECT count(*), count(*) FILTER (WHERE delivered_at IS NULL), COALESCE(max(id), 0)
			FROM message WHERE to_agent_id = ? AND read_at IS NULL`, a.id).
			Scan(&da.Unread, &da.Undelivered, &da.LastUnreadID)
		if err != nil {
			return nil, err
		}
		out = append(out, da)
	}
	return out, nil
}

// ---- administration ----

func (c *call) adminOnly() error {
	if c.p.kind != kindAdmin {
		return forbidden("this needs the admin token")
	}
	return nil
}

func adminDeviceAdd(c *call) (any, error) {
	if err := c.adminOnly(); err != nil {
		return nil, err
	}
	var req api.NameReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if err := checkName("device", req.Name); err != nil {
		return nil, err
	}
	tok, err := c.addDevice(req.Name)
	if err != nil {
		return nil, err
	}
	return api.TokenResp{Name: req.Name, Token: tok}, nil
}

// addDevice creates a device and returns its one-time join token, which
// expires after Options.JoinTTL. Callers check the caller's authority.
func (c *call) addDevice(name string) (string, error) {
	tok := store.NewToken(store.PrefixJoin)
	if _, err := c.tx.Exec(`INSERT INTO device(name, join_hash, join_expires_at, created_at) VALUES (?, ?, ?, ?)`,
		name, store.HashToken(tok), store.Millis(c.now.Add(c.h.opt.JoinTTL)), store.Millis(c.now)); err != nil {
		return "", conflict("device %q already exists", name)
	}
	return tok, c.audit("device.add", "device:"+name, nil)
}

func (c *call) revokeDevice(name string) error {
	res, err := c.tx.Exec(`UPDATE device SET revoked_at = ?, join_hash = NULL WHERE name = ? AND revoked_at IS NULL`,
		store.Millis(c.now), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("no active device %q", name)
	}
	return c.audit("device.revoke", "device:"+name, nil)
}

func adminDeviceList(c *call) (any, error) {
	if err := c.adminOnly(); err != nil {
		return nil, err
	}
	rows, err := c.tx.Query(`SELECT name, credential_hash IS NOT NULL, last_seen_at, revoked_at FROM device ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Device{}
	for rows.Next() {
		var d api.Device
		var seen, revoked sql.NullInt64
		if err := rows.Scan(&d.Name, &d.Joined, &seen, &revoked); err != nil {
			return nil, err
		}
		d.LastSeenAt, d.RevokedAt = store.TimePtr(seen), store.TimePtr(revoked)
		out = append(out, d)
	}
	return out, rows.Err()
}

func adminDeviceRevoke(c *call) (any, error) {
	if err := c.adminOnly(); err != nil {
		return nil, err
	}
	return nil, c.revokeDevice(c.r.PathValue("name"))
}

func adminHumanAdd(c *call) (any, error) {
	if err := c.adminOnly(); err != nil {
		return nil, err
	}
	var req api.NameReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if err := checkName("human", req.Name); err != nil {
		return nil, err
	}
	tok := store.NewToken(store.PrefixHuman)
	if _, err := c.tx.Exec(`INSERT INTO human(name, token_hash, created_at) VALUES (?, ?, ?)`,
		req.Name, store.HashToken(tok), store.Millis(c.now)); err != nil {
		return nil, conflict("human %q already exists", req.Name)
	}
	if err := c.audit("human.add", "human:"+req.Name, nil); err != nil {
		return nil, err
	}
	return api.TokenResp{Name: req.Name, Token: tok}, nil
}

func adminProjectAdd(c *call) (any, error) {
	if err := c.adminOnly(); err != nil {
		return nil, err
	}
	var req api.NameReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if err := checkName("project", req.Name); err != nil {
		return nil, err
	}
	if _, err := c.tx.Exec(`INSERT INTO project(name, created_at) VALUES (?, ?)`, req.Name, store.Millis(c.now)); err != nil {
		return nil, conflict("project %q already exists", req.Name)
	}
	if err := c.audit("project.add", "project:"+req.Name, nil); err != nil {
		return nil, err
	}
	return api.Project{Name: req.Name, CreatedAt: c.now.UTC()}, nil
}

func projectList(c *call) (any, error) {
	if c.p.kind == kindDevice {
		return nil, forbidden("agents see only their own project")
	}
	rows, err := c.tx.Query(`SELECT name, created_at FROM project ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Project{}
	for rows.Next() {
		var p api.Project
		var at int64
		if err := rows.Scan(&p.Name, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = store.Time(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

func auditList(c *call) (any, error) {
	if c.p.kind != kindAdmin && c.p.kind != kindHuman {
		return nil, forbidden("the audit log is for humans and the admin")
	}
	q := c.r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := c.tx.Query(`SELECT seq, actor, action, target, payload, created_at, via FROM audit WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AuditRow{}
	for rows.Next() {
		var a api.AuditRow
		var payload string
		var at int64
		if err := rows.Scan(&a.Seq, &a.Actor, &a.Action, &a.Target, &payload, &at, &a.Via); err != nil {
			return nil, err
		}
		a.Payload, a.CreatedAt = []byte(payload), store.Time(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// handleJoin exchanges a one-time join token for the device credential.
func (h *Hub) handleJoin(w http.ResponseWriter, r *http.Request) {
	// The endpoint is unauthenticated: limit by address before any lookup.
	if !h.allowRate("join:ip:"+h.clientIP(r), 10, 10, h.opt.Now()) {
		h.writeErr(w, &apiError{429, "rate_limited", "too many join attempts; wait a minute"})
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	defer tx.Rollback()
	c := &call{h: h, tx: tx, r: r, now: h.opt.Now(), p: &principal{kind: kindDevice}}
	var req api.JoinReq
	if err := c.decode(&req); err != nil {
		h.writeErr(w, err)
		return
	}
	var id int64
	err = tx.QueryRow(`SELECT id, name FROM device WHERE join_hash = ? AND revoked_at IS NULL
		AND (join_expires_at IS NULL OR join_expires_at > ?)`,
		store.HashToken(req.JoinToken), store.Millis(c.now)).Scan(&id, &c.p.name)
	if err != nil || !strings.HasPrefix(req.JoinToken, store.PrefixJoin) {
		h.writeErr(w, unauthorized("invalid, used or expired join token"))
		return
	}
	cred := store.NewToken(store.PrefixDevice)
	if _, err := tx.Exec(`UPDATE device SET join_hash = NULL, credential_hash = ?, last_seen_at = ? WHERE id = ?`,
		store.HashToken(cred), store.Millis(c.now), id); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := c.audit("device.join", "device:"+c.p.name, nil); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, 200, api.JoinResp{Device: c.p.name, Credential: cred})
}
