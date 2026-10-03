// Package api holds the wire types of protocol handloom/1, shared by the hub,
// the link and the CLI.
package api

import (
	"encoding/json"
	"time"
)

const Version = "handloom/1"

// Header that carries the calling agent's name on device-authenticated requests.
const AgentHeader = "Handloom-Agent"

const DefaultProject = "default"

// Roles.
const (
	RoleLead     = "lead"
	RoleWorker   = "worker"
	RoleObserver = "observer"
)

// Agent states.
const (
	StateIdle    = "idle"
	StateWorking = "working"
	StateBlocked = "blocked"
	StateOffline = "offline"
	StateUnknown = "unknown"
)

// Task statuses.
const (
	StatusOpen      = "open"
	StatusClaimed   = "claimed"
	StatusSubmitted = "submitted"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// Wake methods reported by links.
const (
	WakeHook  = "hook"  // end-of-turn hook told the agent to continue
	WakeTmux  = "tmux"  // nudge typed through tmux
	WakeHerdr = "herdr" // nudge typed through herdr
	WakeNone  = "none"  // no method applied; see reason
)

type Error struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type Project struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Device struct {
	Name       string     `json:"name"`
	Joined     bool       `json:"joined"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type Agent struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Role         string    `json:"role"`
	Project      string    `json:"project"`
	Device       string    `json:"device"`
	State        string    `json:"state"`
	StateAt      time.Time `json:"state_at"`
	WakeTarget   string    `json:"wake_target,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// DeviceAgent is what a link sees for each agent on its own device.
type DeviceAgent struct {
	Agent
	Unread       int   `json:"unread"`
	Undelivered  int   `json:"undelivered"`
	LastUnreadID int64 `json:"last_unread_id"`
}

type Task struct {
	ID             int64      `json:"id"`
	Project        string     `json:"project"`
	Title          string     `json:"title"`
	Body           string     `json:"body,omitempty"`
	Status         string     `json:"status"`
	Owner          string     `json:"owner,omitempty"`
	AssignedTo     string     `json:"assigned_to,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	DependsOn      []int64    `json:"depends_on"`
	Evidence       []string   `json:"evidence"`
	Note           string     `json:"note,omitempty"`
	BlockedReason  string     `json:"blocked_reason,omitempty"`
	RejectReason   string     `json:"reject_reason,omitempty"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Message struct {
	ID          int64      `json:"id"`
	Project     string     `json:"project"`
	From        string     `json:"from"` // agent:<name> | human:<name> | hub
	To          string     `json:"to"`   // recipient agent name
	ToSpec      string     `json:"to_spec"`
	TaskID      *int64     `json:"task_id,omitempty"`
	Body        string     `json:"body"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	DeliveredBy string     `json:"delivered_by,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
}

type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Agent     string          `json:"agent,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type AuditRow struct {
	Seq       int64           `json:"seq"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Requests.

type NameReq struct {
	Name string `json:"name"`
}

type TokenResp struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

type JoinReq struct {
	JoinToken string `json:"join_token"`
}

type JoinResp struct {
	Device     string `json:"device"`
	Credential string `json:"credential"`
}

type RegisterReq struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Project    string `json:"project,omitempty"`
	WakeTarget string `json:"wake_target,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
}

type RoleReq struct {
	Role string `json:"role"`
}

type StateReq struct {
	State string `json:"state"`
}

type SendReq struct {
	To      string `json:"to"`
	Body    string `json:"body"`
	TaskID  *int64 `json:"task_id,omitempty"`
	Project string `json:"project,omitempty"` // humans only; agents use their own project
}

type SendResp struct {
	IDs        []int64  `json:"ids"`
	Recipients []string `json:"recipients"`
}

type DeliveredReq struct {
	Method string `json:"method"`
}

type WakeReq struct {
	Method string `json:"method"`
	Reason string `json:"reason,omitempty"` // set when Method is "none"
}

type WakeResp struct {
	Messages []int64 `json:"messages"`
}

type TurnEndResp struct {
	Block  bool `json:"block"`
	Unread int  `json:"unread"`
}

type TaskCreateReq struct {
	Title      string  `json:"title"`
	Body       string  `json:"body,omitempty"`
	AssignedTo string  `json:"assigned_to,omitempty"`
	DependsOn  []int64 `json:"depends_on,omitempty"`
	Project    string  `json:"project,omitempty"` // humans only
}

type AssignReq struct {
	Agent string `json:"agent"` // empty clears the assignment
}

type SubmitReq struct {
	Evidence []string `json:"evidence"`
	Note     string   `json:"note,omitempty"`
}

type ReasonReq struct {
	Reason string `json:"reason"`
}

type BlockReq struct {
	Reason string `json:"reason"` // empty clears the flag
}

type WhoAmI struct {
	Kind   string `json:"kind"` // admin | human | device
	Name   string `json:"name,omitempty"`
	Device string `json:"device,omitempty"`
	Agent  *Agent `json:"agent,omitempty"`
}
