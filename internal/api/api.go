// Package api holds the wire types of protocol handloom/1, shared by the hub,
// the link and the CLI.
package api

import (
	"encoding/json"
	"time"

	"handloom/internal/profile"
)

const Version = "handloom/1"

// Header that carries the calling agent's name on device-authenticated requests.
const AgentHeader = "Handloom-Agent"

// LegacyAgentHeader is what the product sent under its old name. Hubs accept
// it and clients send it too, so a new binary and an old one still talk.
const LegacyAgentHeader = "Handloom-Agent"

// RunTokenHeader carries an agent's run token (from its token file, through
// the link) next to the device credential the link adds.
const RunTokenHeader = "Handloom-Run-Token"

// AgentFrom returns the calling agent's name from a request, new header first.
func AgentFrom(h interface{ Get(string) string }) string {
	if v := h.Get(AgentHeader); v != "" {
		return v
	}
	return h.Get(LegacyAgentHeader)
}

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
	WakeHook     = "hook"     // end-of-turn hook told the agent to continue
	WakeTmux     = "tmux"     // nudge typed through tmux
	WakeHerdr    = "herdr"    // nudge typed through herdr
	WakeHeadless = "headless" // one headless turn started on the agent's session
	WakeNone     = "none"     // no method applied; see reason
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
	Name         string     `json:"name"`
	Kind         string     `json:"kind"`
	Role         string     `json:"role"`
	Project      string     `json:"project"`
	Device       string     `json:"device"`
	State        string     `json:"state"`
	StateAt      time.Time  `json:"state_at"`
	WakeTarget   string     `json:"wake_target,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	Dir          string     `json:"dir,omitempty"`              // the agent's working directory on its device
	Job          *int64     `json:"job,omitempty"`              // the job this agent belongs to; jobs have their own lead
	LeaseUntil   *time.Time `json:"lease_expires_at,omitempty"` // an agent with a terminal is alive until then; its link renews it
	RunToken     string     `json:"run_token,omitempty"`        // only in the answer to a register that asked to rotate it; shown once
	RegisteredAt time.Time  `json:"registered_at"`
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
	Kind           string     `json:"kind,omitempty"` // "job" for a job's root task
	Job            *int64     `json:"job,omitempty"`  // the job this task belongs to
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
	Check          *TaskCheck `json:"check,omitempty"` // the device's verification of the current submission
	Merge          *TaskMerge `json:"merge,omitempty"` // what became of its branch after it was accepted
	BlockedReason  string     `json:"blocked_reason,omitempty"`
	RejectReason   string     `json:"reject_reason,omitempty"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TaskMerge is what became of an accepted task's branch: the device merges it
// into the job's integration branch (job/<id>/integration) and checks the result.
type TaskMerge struct {
	ID     int64     `json:"id"`
	Status string    `json:"status"` // pending | merged | conflict | failed
	Detail string    `json:"detail,omitempty"`
	Head   string    `json:"head,omitempty"` // the integration branch's commit after a merge
	At     time.Time `json:"at"`
}

// PendingMerge is one merge a device has been asked to do.
type PendingMerge struct {
	ID     int64  `json:"id"`
	Task   int64  `json:"task"`
	Job    int64  `json:"job"`
	Agent  string `json:"agent"`
	Title  string `json:"title"`
	Verify string `json:"verify,omitempty"`
}

// MergeReport is the device's account of a merge.
type MergeReport struct {
	Status string `json:"status"` // merged | conflict | failed
	Detail string `json:"detail,omitempty"`
	Head   string `json:"head,omitempty"`
}

// TaskCheck is what a device found when it ran the job's verify command in an
// agent's worktree after the agent submitted. It is the device's word, not the
// agent's.
type TaskCheck struct {
	Agent    string    `json:"agent"`
	Device   string    `json:"device"`
	Command  string    `json:"command"`
	ExitCode int       `json:"exit_code"`
	TimedOut bool      `json:"timed_out,omitempty"`
	Tail     string    `json:"tail,omitempty"`   // the last lines of the output
	SHA256   string    `json:"sha256,omitempty"` // of the full log, which stays on the device
	At       time.Time `json:"at"`
}

// TaskCheckReq is the link reporting a verify run.
type TaskCheckReq struct {
	Agent    string `json:"agent"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Tail     string `json:"tail,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
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

// Escalation is a question from the lead to the human. Only a human can answer it.
type Escalation struct {
	ID         int64      `json:"id"`
	Project    string     `json:"project"`
	From       string     `json:"from"` // the asking agent
	TaskID     *int64     `json:"task_id,omitempty"`
	Question   string     `json:"question"`
	Options    []string   `json:"options"`
	Answer     *string    `json:"answer,omitempty"`
	AnsweredBy string     `json:"answered_by,omitempty"` // human:<name>
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
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
	Via       string          `json:"via,omitempty"` // how the actor was authenticated
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
	Dir        string `json:"dir,omitempty"`
	// RotateToken asks for a new run token, which replaces the old one and is
	// returned once in the answer. Adapter installs and `run` ask.
	RotateToken bool `json:"rotate_token,omitempty"`
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

// Job is a unit of work: a root task with its own lead, and tasks under it.
type Job struct {
	ID           int64     `json:"id"`
	Project      string    `json:"project"`
	Title        string    `json:"title"`
	Body         string    `json:"body,omitempty"`
	Status       string    `json:"status"` // open | done | cancelled
	Lead         string    `json:"lead,omitempty"`
	Confidential bool      `json:"confidential,omitempty"`
	Repo         string    `json:"repo,omitempty"`   // a git repository on Device; each agent of the job works in its own worktree of it
	Verify       string    `json:"verify,omitempty"` // a command the device runs in an agent's worktree after it submits
	Device       string    `json:"device,omitempty"`
	Knowledge    string    `json:"knowledge,omitempty"` // a git repository on Device with what is known about the client; agents read it, and what they add is proposed on the branch job/<id> of it
	Base         string    `json:"base,omitempty"`      // the branch of Repo the job's worktrees start from (default: its current commit)
	Notes        int       `json:"notes,omitempty"`     // notes proposed so far on that branch
	NotesProblem string    `json:"notes_problem,omitempty"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	Tasks        JobCounts `json:"tasks"`
}

type JobCounts struct {
	Open      int `json:"open"`
	Claimed   int `json:"claimed"`
	Submitted int `json:"submitted"`
	Done      int `json:"done"`
	Cancelled int `json:"cancelled"`
}

type JobNewReq struct {
	Title        string `json:"title"`
	Body         string `json:"body,omitempty"`
	Lead         string `json:"lead,omitempty"` // an existing agent that becomes the job's lead
	Confidential bool   `json:"confidential,omitempty"`
	Project      string `json:"project,omitempty"`
	Repo         string `json:"repo,omitempty"`
	Verify       string `json:"verify,omitempty"`
	Knowledge    string `json:"knowledge,omitempty"`    // a git repository on the device with what is known about the client
	Base         string `json:"base,omitempty"`         // a branch of Repo to start the worktrees from
	Device       string `json:"device,omitempty"`       // where the repo is, and where the lead starts
	LeadProfile  string `json:"lead_profile,omitempty"` // start the job's lead from this profile (instead of naming an existing agent)
	LeadName     string `json:"lead_name,omitempty"`    // default lead-<job id>
}

type JobCloseReq struct {
	Cancel bool `json:"cancel,omitempty"` // cancel the job and its unfinished tasks instead of finishing it
}

type JobResumeReq struct {
	Lead string `json:"lead"` // the agent that takes over as the job's lead
}

// Spawn is a request to start an agent on a device.
type Spawn struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Model     string    `json:"model,omitempty"`
	Device    string    `json:"device"`
	Project   string    `json:"project"`
	Job       *int64    `json:"job,omitempty"`
	Role      string    `json:"role,omitempty"` // worker, or lead for the job's own lead
	Repo      string    `json:"repo,omitempty"` // the repository its work directory is a worktree of
	Knowledge string    `json:"knowledge,omitempty"`
	Base      string    `json:"base,omitempty"`
	Verify    string    `json:"verify,omitempty"`  // the job's verify command, which the device runs after this agent submits
	Profile   string    `json:"profile,omitempty"` // name@version it is pinned to
	Status    string    `json:"status"`            // pending | launching | started | failed
	Pane      string    `json:"pane,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type SpawnReq struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Profile string `json:"profile,omitempty"` // name or name@version; what the agent may do and know
	Role    string `json:"role,omitempty"`    // worker (default) or lead: humans only, for a job's own lead
	Model   string `json:"model,omitempty"`
	Device  string `json:"device,omitempty"` // humans; a lead's agents start on the lead's own device
	Job     int64  `json:"job,omitempty"`    // humans; a lead's agents join the lead's own job
	Project string `json:"project,omitempty"`
}

// SpawnReport is the link telling the hub how a start went.
type SpawnReport struct {
	Status string `json:"status"` // launching | started | failed
	Pane   string `json:"pane,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Spawn statuses.
const (
	SpawnPending   = "pending"
	SpawnLaunching = "launching"
	SpawnStarted   = "started"
	SpawnFailed    = "failed"
)

// ProfileInfo is a profile in a list.
type ProfileInfo struct {
	Name        string    `json:"name"`
	Version     int       `json:"version"`
	Hash        string    `json:"hash"`
	Description string    `json:"description,omitempty"`
	Kind        string    `json:"kind"`
	Runtime     string    `json:"runtime"`
	Tools       []string  `json:"tools"`
	Skills      []string  `json:"skills,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProfileFull is one version of a profile with everything in it.
type ProfileFull struct {
	ProfileInfo
	Spec    profile.Spec `json:"spec"`
	Adapted []string     `json:"adapted,omitempty"`
}

type ProfileReq struct {
	Name  string       `json:"name"`
	Spec  profile.Spec `json:"spec"`
	Adapt bool         `json:"adapt,omitempty"` // fit the spec to its CLI instead of refusing; the answer says what changed
}

// ScopeRefusal is a link telling the hub it refused an agent's submit because
// the agent had changed files its profile does not allow.
type ScopeRefusal struct {
	Agent string   `json:"agent"`
	Task  int64    `json:"task"`
	Paths []string `json:"paths"`
}

type AgentJobReq struct {
	Job int64 `json:"job"` // 0 removes the agent from its job
}

type TaskCreateReq struct {
	Job        int64   `json:"job,omitempty"` // humans only; a lead's tasks go into its own job
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

// DigestItem is one line of the digest: something that needs a human, waits
// for review, or is running.
type DigestItem struct {
	Kind    string    `json:"kind"`         // escalation | blocked | lead-silent | submitted | running
	ID      int64     `json:"id,omitempty"` // escalation or task number
	Title   string    `json:"title"`
	Detail  string    `json:"detail,omitempty"`
	Who     string    `json:"who,omitempty"`
	Options []string  `json:"options,omitempty"`
	At      time.Time `json:"at"`
}

type Activity struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Digest is what the inbox, the phone and the TUI show. One query, so the
// surfaces cannot disagree.
type Digest struct {
	NeedsYou []DigestItem `json:"needs_you"`
	ToReview []DigestItem `json:"to_review"`
	Running  []DigestItem `json:"running"`
	Agents   []Agent      `json:"agents"`
	Activity []Activity   `json:"activity"`
	Seq      int64        `json:"seq"` // newest event; clients refetch when it moves
}

type AskReq struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	TaskID   *int64   `json:"task_id,omitempty"`
}

type AnswerReq struct {
	Answer string `json:"answer"`
}

type WhoAmI struct {
	Via    string `json:"via,omitempty"` // how the hub knows the agent: run-token | device-asserted
	Kind   string `json:"kind"`          // admin | human | device
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"` // humans: owner | member | viewer
	Device string `json:"device,omitempty"`
	Agent  *Agent `json:"agent,omitempty"`
}

// TailReq is a device posting the end of an agent's terminal.
type TailReq struct {
	Text string `json:"text"`
}

// Price is what a model costs, in US dollars per million tokens.
type Price struct {
	Model      string  `json:"model"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Person is somebody who can sign in to the hub.
type Person struct {
	Name     string `json:"name"`
	Role     string `json:"role"` // owner | member | viewer
	WebLogin bool   `json:"web_login"`
}

type PersonReq struct {
	Name string `json:"name,omitempty"`
	Role string `json:"role,omitempty"`
}

type PersonResp struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	InviteURL string `json:"invite_url"` // shown once; works once, for a limited time
}

// Notifications are the owner's phone-push settings (the access token is only ever set in the hub's environment).
type Notifications struct {
	NtfyURL         string `json:"ntfy_url"`
	NtfyTopic       string `json:"ntfy_topic"`
	FromEnvironment bool   `json:"from_environment,omitempty"`
}

// Screen is the end of an agent's terminal, as last read by its machine.
type Screen struct {
	Text       string `json:"text,omitempty"`
	AgeSeconds int    `json:"age_seconds,omitempty"`
	Pending    bool   `json:"pending,omitempty"` // asked for; the machine has not answered yet
}

// StarterInfo is one entry of the starter library, and whether this hub already has a profile of that name.
type StarterInfo struct {
	Name             string   `json:"name"`
	Title            string   `json:"title"`
	Group            string   `json:"group"`
	Summary          string   `json:"summary"`
	Core             bool     `json:"core,omitempty"`
	Skills           []string `json:"skills,omitempty"`
	Tools            []string `json:"tools"`
	RecommendRuntime string   `json:"recommend_runtime,omitempty"`
	Note             string   `json:"note,omitempty"`
	Added            bool     `json:"added,omitempty"`
}

// StarterFull is a starter with everything in it. Its spec has no CLI, model or runtime yet.
type StarterFull struct {
	StarterInfo
	Spec        profile.Spec `json:"spec"`
	WhatItCanDo string       `json:"what_it_can_do"`
}

// StarterAddReq adds a starter to this hub's profiles. Kind and Runtime are required.
type StarterAddReq struct {
	Name    string `json:"name,omitempty"` // default: the starter's own name
	Kind    string `json:"kind"`
	Runtime string `json:"runtime"`
	Model   string `json:"model,omitempty"`
	Adapt   bool   `json:"adapt,omitempty"` // leave out what the CLI cannot do instead of refusing
}

type StarterAddResp struct {
	Profile  ProfileInfo `json:"profile"`
	Adapted  []string    `json:"adapted,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
}

// UsageReq is a device reporting what an agent has used so far, per model, as running totals.
type UsageReq struct {
	Agent  string       `json:"agent"`
	Models []UsageModel `json:"models"`
}

type UsageModel struct {
	Model      string `json:"model"`
	Input      int64  `json:"input"` // tokens not served from a cache
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cache_read"`
	CacheWrite int64  `json:"cache_write"`
}

// PendingCollect is one gathering of an agent's proposed notes a device has been asked to do.
type PendingCollect struct {
	ID    int64  `json:"id"`
	Job   int64  `json:"job"`
	Agent string `json:"agent"`
}

// CollectReport is the device's account of one: how many notes it put on the job's branch.
type CollectReport struct {
	Status string `json:"status"` // done | conflict | failed
	Notes  int    `json:"notes"`
	Detail string `json:"detail,omitempty"`
}
