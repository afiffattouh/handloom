// Package hub is the handloom server: HTTP API, scope enforcement, leases,
// events and the audit log. It keeps records and delivers messages; it never
// plans or decides.
package hub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

type Options struct {
	Lease     time.Duration    // task lease; default 15 minutes
	Sweep     time.Duration    // how often expired leases are collected; default 5 seconds
	Unclaimed time.Duration    // tell the lead when an assigned task stays unclaimed this long; default 10 minutes
	MsgRate   int              // messages per minute per sender; default 60
	Now       func() time.Time // clock, replaceable in tests
	Log       *log.Logger
}

type Hub struct {
	db  *sql.DB
	opt Options

	mu     sync.Mutex
	notify chan struct{} // closed and replaced whenever events are written
	rate   map[string]*bucket
}

func New(db *sql.DB, opt Options) *Hub {
	if opt.Lease <= 0 {
		opt.Lease = 15 * time.Minute
	}
	if opt.Sweep <= 0 {
		opt.Sweep = 5 * time.Second
	}
	if opt.Unclaimed <= 0 {
		opt.Unclaimed = 10 * time.Minute
	}
	if opt.MsgRate <= 0 {
		opt.MsgRate = 60
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = log.New(io.Discard, "", 0)
	}
	return &Hub{db: db, opt: opt, notify: make(chan struct{}), rate: map[string]*bucket{}}
}

// Run collects expired leases until ctx is done.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(h.opt.Sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := h.Sweep(); err != nil {
				h.opt.Log.Printf("sweep: %v", err)
			}
		}
	}
}

// Sweep returns tasks with an expired lease to open.
func (h *Hub) Sweep() error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c := &call{h: h, tx: tx, p: &principal{kind: kindHub}, now: h.opt.Now()}
	if err := c.expireLeases(); err != nil {
		return err
	}
	if err := c.reportUnclaimed(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if c.events {
		h.wake()
	}
	return nil
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "protocol": api.Version})
	})
	v1 := func(pattern string, fn func(*call) (any, error)) {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" /v1"+path, h.handle(fn))
	}

	mux.HandleFunc("POST /v1/devices/join", h.handleJoin)
	mux.HandleFunc("GET /v1/events", h.handleEvents)

	v1("GET /whoami", whoami)
	v1("POST /admin/devices", adminDeviceAdd)
	v1("GET /admin/devices", adminDeviceList)
	v1("POST /admin/devices/{name}/revoke", adminDeviceRevoke)
	v1("POST /admin/humans", adminHumanAdd)
	v1("POST /admin/projects", adminProjectAdd)
	v1("GET /projects", projectList)
	v1("GET /audit", auditList)

	v1("POST /agents", agentRegister)
	v1("GET /agents", agentList)
	v1("POST /agents/{name}/role", agentRole)
	v1("POST /agents/{name}/state", agentState)
	v1("POST /agents/{name}/turn-end", agentTurnEnd)
	v1("POST /agents/{name}/wake", agentWake)
	v1("GET /device/agents", deviceAgents)

	v1("POST /messages", messageSend)
	v1("GET /inbox", inbox)
	v1("POST /messages/{id}/delivered", messageDelivered)

	v1("POST /tasks", taskCreate)
	v1("GET /tasks", taskList)
	v1("GET /tasks/{id}", taskGet)
	v1("POST /tasks/{id}/assign", taskAssign)
	v1("POST /tasks/{id}/claim", taskClaim)
	v1("POST /tasks/{id}/heartbeat", taskHeartbeat)
	v1("POST /tasks/{id}/release", taskRelease)
	v1("POST /tasks/{id}/block", taskBlock)
	v1("POST /tasks/{id}/submit", taskSubmit)
	v1("POST /tasks/{id}/accept", taskAccept)
	v1("POST /tasks/{id}/reject", taskReject)
	v1("POST /tasks/{id}/cancel", taskCancel)

	v1("POST /escalations", escalationOpen)
	v1("GET /escalations", escalationList)
	v1("GET /escalations/{id}", escalationGet)
	v1("POST /escalations/{id}/answer", escalationAnswer)
	return mux
}

// ---- errors ----

type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &apiError{400, "bad_request", fmt.Sprintf(format, args...)}
}
func unauthorized(format string, args ...any) error {
	return &apiError{401, "unauthorized", fmt.Sprintf(format, args...)}
}
func notFound(format string, args ...any) error {
	return &apiError{404, "not_found", fmt.Sprintf(format, args...)}
}
func conflict(format string, args ...any) error {
	return &apiError{409, "conflict", fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Handloom-Protocol", api.Version)
	w.WriteHeader(status)
	if v == nil {
		v = map[string]any{"ok": true}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func (h *Hub) writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, api.Error{Error: ae.msg, Code: ae.code})
		return
	}
	h.opt.Log.Printf("internal error: %v", err)
	writeJSON(w, 500, api.Error{Error: "internal error", Code: "internal"})
}

// ---- callers ----

const (
	kindAdmin  = "admin"
	kindHuman  = "human"
	kindDevice = "device"
	kindHub    = "hub" // the hub itself, for sweeps
)

type principal struct {
	kind      string
	name      string // human or device name
	deviceID  int64
	agentName string    // from the Handloom-Agent header; checked in call.agent
	agent     *agentRow // resolved lazily
}

func (p *principal) actor() string {
	switch {
	case p == nil:
		return "anonymous"
	case p.agent != nil:
		return "agent:" + p.agent.name
	case p.kind == kindAdmin, p.kind == kindHub:
		return p.kind
	default:
		return p.kind + ":" + p.name
	}
}

// call is one request: one transaction, one caller.
type call struct {
	h      *Hub
	tx     *sql.Tx
	p      *principal
	r      *http.Request
	now    time.Time
	events bool // events were written; wake long-pollers after commit
}

func (h *Hub) handle(fn func(*call) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		c := &call{h: h, tx: tx, r: r, now: h.opt.Now()}
		var out any
		c.p, err = authenticate(tx, r, c.now)
		if err == nil {
			out, err = fn(c)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			tx.Rollback()
			var ae *apiError
			if errors.As(err, &ae) && ae.status == 403 {
				h.auditDenied(c.p, r, ae)
			}
			h.writeErr(w, err)
			return
		}
		if c.events {
			h.wake()
		}
		writeJSON(w, 200, out)
	}
}

// auditDenied records a rejected action. It runs in its own transaction
// because the request's transaction was rolled back.
func (h *Hub) auditDenied(p *principal, r *http.Request, ae *apiError) {
	payload, _ := json.Marshal(map[string]string{"reason": ae.msg})
	_, err := h.db.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES (?, 'denied', ?, ?, ?)`,
		p.actor(), r.Method+" "+r.URL.Path, string(payload), store.Millis(h.opt.Now()))
	if err != nil {
		h.opt.Log.Printf("audit denied: %v", err)
	}
}

func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

func authenticate(tx *sql.Tx, r *http.Request, now time.Time) (*principal, error) {
	tok := bearer(r)
	if tok == "" {
		return nil, unauthorized("missing bearer token")
	}
	hash := store.HashToken(tok)
	switch {
	case strings.HasPrefix(tok, store.PrefixAdmin):
		var want string
		if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'admin_hash'`).Scan(&want); err != nil {
			return nil, unauthorized("invalid token")
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(want)) != 1 {
			return nil, unauthorized("invalid token")
		}
		return &principal{kind: kindAdmin}, nil
	case strings.HasPrefix(tok, store.PrefixHuman):
		p := &principal{kind: kindHuman}
		if err := tx.QueryRow(`SELECT name FROM human WHERE token_hash = ?`, hash).Scan(&p.name); err != nil {
			return nil, unauthorized("invalid token")
		}
		return p, nil
	case strings.HasPrefix(tok, store.PrefixDevice):
		p := &principal{kind: kindDevice, agentName: r.Header.Get(api.AgentHeader)}
		var revoked, seen sql.NullInt64
		err := tx.QueryRow(`SELECT id, name, revoked_at, last_seen_at FROM device WHERE credential_hash = ?`, hash).
			Scan(&p.deviceID, &p.name, &revoked, &seen)
		if err != nil {
			return nil, unauthorized("invalid token")
		}
		if revoked.Valid {
			return nil, unauthorized("device %s is revoked", p.name)
		}
		if !seen.Valid || now.UnixMilli()-seen.Int64 > 10_000 {
			if _, err := tx.Exec(`UPDATE device SET last_seen_at = ? WHERE id = ?`, store.Millis(now), p.deviceID); err != nil {
				return nil, err
			}
		}
		return p, nil
	}
	return nil, unauthorized("invalid token")
}

// agent returns the calling agent. The name comes from the Handloom-Agent header
// and must belong to the calling device.
func (c *call) agent() (*agentRow, error) {
	if c.p.agent != nil {
		return c.p.agent, nil
	}
	if c.p.kind != kindDevice {
		return nil, forbidden("this action needs an agent identity")
	}
	if c.p.agentName == "" {
		return nil, forbidden("no agent identity: set HANDLOOM_AGENT or run `handloom register`")
	}
	a, err := c.agentByName(c.p.agentName)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not registered on device %s", c.p.agentName, c.p.name)
	}
	c.p.agent = a
	return a, nil
}

// project resolves the project for this call: the agent's own, or the named
// one (default "default") for humans and the admin.
func (c *call) project(name string) (int64, string, error) {
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return 0, "", err
		}
		if name != "" && name != a.project {
			return 0, "", forbidden("agent %s belongs to project %s", a.name, a.project)
		}
		return a.projectID, a.project, nil
	}
	if name == "" {
		name = api.DefaultProject
	}
	var id int64
	if err := c.tx.QueryRow(`SELECT id FROM project WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, "", notFound("no project %q", name)
	}
	return id, name, nil
}

// ---- events and audit ----

// marshal encodes a payload as it should read in the log: "->" stays "->".
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimSpace(buf.Bytes())
}

func (c *call) emit(projectID, agentID int64, typ string, payload any) error {
	b := marshal(payload)
	var pid, aid any
	if projectID != 0 {
		pid = projectID
	}
	if agentID != 0 {
		aid = agentID
	}
	_, err := c.tx.Exec(`INSERT INTO event(project_id, agent_id, type, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		pid, aid, typ, string(b), store.Millis(c.now))
	c.events = true
	return err
}

func (c *call) audit(action, target string, payload any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	b := marshal(payload)
	_, err := c.tx.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.p.actor(), action, target, string(b), store.Millis(c.now))
	return err
}

// record writes the audit row and the event for one change.
func (c *call) record(projectID, agentID int64, action, target string, payload any) error {
	if err := c.audit(action, target, payload); err != nil {
		return err
	}
	return c.emit(projectID, agentID, action, map[string]any{"target": target, "actor": c.p.actor(), "detail": payload})
}

func (h *Hub) wake() {
	h.mu.Lock()
	close(h.notify)
	h.notify = make(chan struct{})
	h.mu.Unlock()
}

func (h *Hub) waitChan() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notify
}

// handleEvents is the long-poll: events for agents on the calling device.
func (h *Hub) handleEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	if wait < 0 || wait > 60 {
		wait = 60
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		ch := h.waitChan()
		events, cursor, err := h.deviceEvents(r, after)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		remaining := time.Until(deadline)
		if len(events) > 0 || remaining <= 0 {
			writeJSON(w, 200, map[string]any{"events": events, "cursor": cursor})
			return
		}
		t := time.NewTimer(remaining)
		select {
		case <-ch:
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return
		}
		t.Stop()
	}
}

func (h *Hub) deviceEvents(r *http.Request, after int64) ([]api.Event, int64, error) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	p, err := authenticate(tx, r, h.opt.Now())
	if err != nil {
		return nil, 0, err
	}
	if p.kind != kindDevice {
		return nil, 0, forbidden("only a device may poll events")
	}
	rows, err := tx.Query(`SELECT e.seq, e.type, a.name, e.payload, e.created_at
		FROM event e JOIN agent a ON a.id = e.agent_id
		WHERE a.device_id = ? AND e.seq > ? ORDER BY e.seq LIMIT 200`, p.deviceID, after)
	if err != nil {
		return nil, 0, err
	}
	events := []api.Event{}
	cursor := after
	for rows.Next() {
		var e api.Event
		var payload string
		var at int64
		if err := rows.Scan(&e.Seq, &e.Type, &e.Agent, &payload, &at); err != nil {
			rows.Close()
			return nil, 0, err
		}
		e.Payload, e.CreatedAt = json.RawMessage(payload), store.Time(at)
		events = append(events, e)
		cursor = e.Seq
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return events, cursor, tx.Commit()
}

// ---- request helpers ----

const maxBody = 256 << 10

// decode reads a JSON body. Unknown fields are rejected, so a client cannot
// smuggle in fields such as "from" or "role".
func (c *call) decode(v any) error {
	dec := json.NewDecoder(io.LimitReader(c.r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body
		}
		return badRequest("bad request body: %v", err)
	}
	return nil
}

func (c *call) pathID() (int64, error) {
	id, err := strconv.ParseInt(c.r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("bad id %q", c.r.PathValue("id"))
	}
	return id, nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func checkName(kind, name string) error {
	if !nameRE.MatchString(name) {
		return badRequest("bad %s name %q: use letters, digits, '.', '_' or '-', at most 64 characters", kind, name)
	}
	return nil
}

// ---- rate limit ----

type bucket struct {
	tokens float64
	at     time.Time
}

// rateOK is a token bucket: MsgRate messages per minute per sender.
func (h *Hub) rateOK(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	max := float64(h.opt.MsgRate)
	b := h.rate[key]
	if b == nil {
		b = &bucket{tokens: max, at: now}
		h.rate[key] = b
	}
	b.tokens += now.Sub(b.at).Minutes() * max
	if b.tokens > max {
		b.tokens = max
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
