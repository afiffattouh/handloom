// Package api holds the wire types of protocol handloom/1, shared by the hub,
// the link and the CLI.
package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"handloom/internal/profile"
)

const Version = "handloom/1"

// Header that carries the calling agent's name on device-authenticated requests.
const AgentHeader = "Handloom-Agent"

// RunTokenHeader carries an agent's run token (from its token file, through
// the link) next to the device credential the link adds.
const RunTokenHeader = "Handloom-Run-Token"

// AgentFrom returns the calling agent's name from a request.
func AgentFrom(h interface{ Get(string) string }) string { return h.Get(AgentHeader) }

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
	Owner      string     `json:"owner,omitempty"`  // the person the machine belongs to (D108); empty when it is shared by the owner
	Shared     bool       `json:"shared,omitempty"` // no person owns it: only owners may use it
}

// DeviceAssignReq hands a machine to a person (D108). Owners only.
type DeviceAssignReq struct {
	To string `json:"to"`
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
	Check          *TaskCheck `json:"check,omitempty"`   // the device's verification of the current submission
	Merge          *TaskMerge `json:"merge,omitempty"`   // what became of its branch after it was accepted
	Handoff        *Handoff   `json:"handoff,omitempty"` // the latest note left for whoever picks the task up
	Resume         string     `json:"resume,omitempty"`  // on a claim: this task was started before; what to do first
	BlockedReason  string     `json:"blocked_reason,omitempty"`
	RejectReason   string     `json:"reject_reason,omitempty"`
	Rejects        int        `json:"rejects,omitempty"` // how often its work was sent back
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
	Agent    string         `json:"agent"`
	Device   string         `json:"device"`
	Command  string         `json:"command"`
	ExitCode int            `json:"exit_code"`
	TimedOut bool           `json:"timed_out,omitempty"`
	Tail     string         `json:"tail,omitempty"`   // the last lines of the output
	SHA256   string         `json:"sha256,omitempty"` // of the full log, which stays on the device
	At       time.Time      `json:"at"`
	Evidence *CheckEvidence `json:"evidence,omitempty"` // what a project kit's check wrote, when the command was one
}

// CheckEvidence is the part of a project kit's summary.json that people read: what ran, what stopped it,
// and why, in words. A command that is not a kit leaves none.
type CheckEvidence struct {
	Result  string       `json:"result"` // pass, fail, config-error or blocked
	Reason  string       `json:"reason,omitempty"`
	Blocked string       `json:"blocked,omitempty"` // the agent's own note, when it said the task cannot be done as specified
	Mode    string       `json:"mode,omitempty"`
	Stages  []CheckStage `json:"stages,omitempty"`
	Refused []string     `json:"refused,omitempty"`
}

type CheckStage struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"` // pass, fail, skip, blocked
	Detail  string  `json:"detail,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
}

// Words says in words what the check found. A kit's exit 3 means the agent says the task cannot be done as
// specified (a person decides; retrying only pushes agents to fake a pass), and exit 2 means the project's
// own checks are broken, which is not the agent's work. Any other command gets only its exit code.
func (ck *TaskCheck) Words() string {
	ev := ck.Evidence
	switch {
	case ck.TimedOut:
		return "timed out"
	case ck.ExitCode == 0:
		return "passed"
	case ev != nil && ck.ExitCode == 3:
		w := "blocked: the agent says this cannot be done as specified, so a person decides"
		if n := strings.TrimSpace(ev.Blocked); n != "" {
			w += " (" + firstLine(n, 160) + ")"
		}
		return w
	case ev != nil && ck.ExitCode == 2:
		return "the project's own checks are broken (not the agent's work)"
	case ev != nil && ev.Reason != "":
		return "failed: " + ev.Reason
	}
	return fmt.Sprintf("failed, exit %d", ck.ExitCode)
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// TaskCheckReq is the link reporting a verify run.
type TaskCheckReq struct {
	Agent    string         `json:"agent"`
	Command  string         `json:"command"`
	ExitCode int            `json:"exit_code"`
	TimedOut bool           `json:"timed_out,omitempty"`
	Tail     string         `json:"tail,omitempty"`
	SHA256   string         `json:"sha256,omitempty"`
	Evidence *CheckEvidence `json:"evidence,omitempty"`
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
	Codebase     string    `json:"codebase,omitempty"` // the Handloom project this job works on (D104), if it was started from one
	Repo         string    `json:"repo,omitempty"`     // a git repository on Device; each agent of the job works in its own worktree of it
	Verify       string    `json:"verify,omitempty"`   // a command the device runs in an agent's worktree after it submits
	Device       string    `json:"device,omitempty"`
	Knowledge    string    `json:"knowledge,omitempty"` // a git repository on Device with what is known about the client; agents read it, and what they add is proposed on the branch job/<id> of it
	Base         string    `json:"base,omitempty"`      // the branch of Repo the job's worktrees start from (default: its current commit)
	Notes        int       `json:"notes,omitempty"`     // notes proposed so far on that branch
	NotesProblem string    `json:"notes_problem,omitempty"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	Tasks        JobCounts `json:"tasks"`
	Overlaps     []JobRef  `json:"overlaps,omitempty"`     // other open jobs on the same repository and device: a warning, never a block
	BaseState    string    `json:"base_state,omitempty"`   // how the start from a project's latest code went (D105): fresh | stale | diverged
	BaseDetail   string    `json:"base_detail,omitempty"`  // that, in words
	BaseCommit   string    `json:"base_commit,omitempty"`  // the short id of the base branch when the job started
	PullRequest  string    `json:"pull_request,omitempty"` // where the latest published pull request stands, in words (D105)
	PRMerged     bool      `json:"pr_merged,omitempty"`    // a pull request of this job was merged
	MergedInto   string    `json:"merged_into,omitempty"`  // the branch of a hosted project this job's work was merged into (D106)
	Progress     JobState  `json:"progress"`               // where the job stands, in words a person can act on
}

// Where a job stands. Worked out from the record each time; the first state that fits wins, in this order.
const (
	JobDone     = "done"
	JobNeedsYou = "needs_you"
	JobStuck    = "stuck"
	JobReady    = "ready"
	JobWorking  = "working"
	JobPlanning = "planning"
)

// JobState is one state with the reason for it and what a person can do about it.
type JobState struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Action string `json:"action,omitempty"`
}

// Base states of a job that works on a project (D105).
const (
	BaseFresh    = "fresh"
	BaseStale    = "stale"
	BaseDiverged = "diverged"
)

// BaseReport is the device's account of getting the latest code of a project's base branch before a job started.
type BaseReport struct {
	Status string `json:"status"` // fresh | stale | diverged
	Detail string `json:"detail,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// BaseLine is the one line that says how a job's start went, or "" when nothing was recorded.
func BaseLine(j Job) string {
	if j.BaseState == "" {
		return ""
	}
	return "Base: " + j.BaseState + ": " + j.BaseDetail
}

// PRWords says where a pull request stands, such as "open, approved, can merge", or "" when it has not been looked at.
func PRWords(state, review, mergeable string) string {
	switch state {
	case "merged":
		return "merged"
	case "closed":
		return "closed without being merged"
	case "open":
		s := "open"
		switch review {
		case "approved":
			s += ", approved"
		case "changes_requested":
			s += ", changes requested"
		case "review_required":
			s += ", waiting for a review"
		}
		switch mergeable {
		case "true":
			s += ", can merge"
		case "false":
			s += ", has conflicts"
		}
		return s
	}
	return ""
}

// JobRef names another job.
type JobRef struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Lead  string `json:"lead,omitempty"`
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
	Project      string `json:"project,omitempty"`  // the hub's tenancy project (not a Handloom project; see Codebase)
	Codebase     string `json:"codebase,omitempty"` // a Handloom project (D104): Repo and Base come from its ready clone on Device
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
	Verify    string    `json:"verify,omitempty"`     // the job's verify command, which the device runs after this agent submits
	Profile   string    `json:"profile,omitempty"`    // name@version it is pinned to
	FreshBase bool      `json:"fresh_base,omitempty"` // the job belongs to a project and its start has not been recorded: get the latest base first (D105)
	Status    string    `json:"status"`               // pending | launching | started | failed
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
	Evidence   []string `json:"evidence"`
	Note       string   `json:"note"`                   // what was done; required, it is the task's handoff note
	HowToCheck string   `json:"how_to_check,omitempty"` // how a reviewer or the next agent can verify it
}

// Lesson is a change to a skill that a finished job proposes. Nothing applies
// until a person accepts it, and then it becomes a new version of the profile.
type Lesson struct {
	ID         int64     `json:"id"`
	Job        int64     `json:"job,omitempty"`
	ProposedBy string    `json:"proposed_by"`
	Profile    string    `json:"profile"`
	Skill      string    `json:"skill"`
	Text       string    `json:"text"`
	Why        string    `json:"why"`
	Status     string    `json:"status"` // proposed | accepted | rejected
	DecidedBy  string    `json:"decided_by,omitempty"`
	Note       string    `json:"note,omitempty"`
	NewVersion int       `json:"new_version,omitempty"`
	Used       int       `json:"used"` // agents started from a version that has it
	At         time.Time `json:"at"`
}

type LessonReq struct {
	Profile string `json:"profile"`
	Skill   string `json:"skill"`
	Text    string `json:"text"`
	Why     string `json:"why"`
}

type LessonDecideReq struct {
	Reason string `json:"reason,omitempty"`
}

// SearchHit is one result of a search of what earlier jobs recorded.
type SearchHit struct {
	Kind    string    `json:"kind"` // job | task | handoff | answer
	Job     int64     `json:"job,omitempty"`
	Task    int64     `json:"task,omitempty"`
	Title   string    `json:"title"`
	Snippet string    `json:"snippet"`
	Who     string    `json:"who,omitempty"`
	At      time.Time `json:"at"`
	Score   int       `json:"score"`
}

// HandoffReq is what a task's owner writes for whoever picks the task up next.
type HandoffReq struct {
	Done   string `json:"done"`             // where the work got to
	Tried  string `json:"tried,omitempty"`  // what was tried, including what did not work
	Next   string `json:"next,omitempty"`   // the next step
	Verify string `json:"verify,omitempty"` // how to check the state of things
}

// Handoff is a stored note on a task.
type Handoff struct {
	ID     int64     `json:"id"`
	Author string    `json:"author"`
	Kind   string    `json:"kind"` // checkpoint | submit | release
	Done   string    `json:"done"`
	Tried  string    `json:"tried,omitempty"`
	Next   string    `json:"next,omitempty"`
	Verify string    `json:"verify,omitempty"`
	At     time.Time `json:"at"`
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

// PublishReq asks the job's device to push the job's integration branch to a shared remote (D103).
type PublishReq struct {
	Remote string `json:"remote,omitempty"` // a configured remote of the repository (default origin)
	PR     bool   `json:"pr,omitempty"`     // also open a pull request, when gh is installed
}

// Publish is one request to publish a job's work, and what came of it.
type Publish struct {
	ID           int64      `json:"id"`
	Job          int64      `json:"job"`
	RequestedBy  string     `json:"requested_by"`
	Remote       string     `json:"remote"`
	Branch       string     `json:"branch"` // the branch pushed to the remote
	PR           bool       `json:"pr"`
	Status       string     `json:"status"` // pending | done | failed
	Detail       string     `json:"detail,omitempty"`
	URL          string     `json:"url,omitempty"`          // the pull request, when one was opened
	Behind       int        `json:"behind,omitempty"`       // commits the base branch is behind on the remote
	PRState      string     `json:"pr_state,omitempty"`     // open | merged | closed, as the machine last saw it (D105)
	PRReview     string     `json:"pr_review,omitempty"`    // approved | changes_requested | review_required
	PRMergeable  string     `json:"pr_mergeable,omitempty"` // true | false | unknown
	PRWords      string     `json:"pr_words,omitempty"`     // the three above in words
	MergedInto   string     `json:"merged_into,omitempty"`  // hosted project: the branch this publish was merged into (D106)
	MergedAt     *time.Time `json:"merged_at,omitempty"`
	MergedCommit string     `json:"merged_commit,omitempty"`
	At           time.Time  `json:"at"`
	DoneAt       *time.Time `json:"done_at,omitempty"`
}

// PendingPublish is a publish a device has been asked to do, with what it needs.
type PendingPublish struct {
	ID     int64         `json:"id"`
	Job    int64         `json:"job"`
	Remote string        `json:"remote"`
	Branch string        `json:"branch"`
	PR     bool          `json:"pr"`
	Repo   string        `json:"repo"`
	Base   string        `json:"base,omitempty"`
	Hosted bool          `json:"hosted,omitempty"` // the project is held by the hub (D106): no pull request, the owner merges it
	Title  string        `json:"title"`
	Body   string        `json:"body,omitempty"`
	Tasks  []PublishTask `json:"tasks"` // accepted tasks and who did them: no evidence, no handoff notes
}

type PublishTask struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Owner string `json:"owner,omitempty"`
}

// PublishReport is the device's account of one publish.
type PublishReport struct {
	Status string `json:"status"` // done | failed
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
	Behind int    `json:"behind,omitempty"`
}

// PendingPR is a published pull request a device keeps an eye on.
type PendingPR struct {
	ID  int64  `json:"id"` // the publish
	Job int64  `json:"job"`
	URL string `json:"url"`
}

// PRReport is the device's account of where a pull request stands.
type PRReport struct {
	State     string `json:"state"`               // open | merged | closed
	Review    string `json:"review,omitempty"`    // approved | changes_requested | review_required
	Mergeable string `json:"mergeable,omitempty"` // true | false | unknown
}

// OverlapText is the warning shown when other open jobs work on the same repository and device.
func OverlapText(refs []JobRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = fmt.Sprintf("#%d %s", r.ID, r.Title)
		if r.Lead != "" {
			parts[i] += " (lead " + r.Lead + ")"
		}
	}
	return "Also open on this repository: " + strings.Join(parts, ", ") + ". Jobs that change the same files will conflict when their branches are merged."
}

// Codebase is a Handloom project (D104): a name and a shared git remote that any machine can get on demand.
// The hub keeps only this and where each machine's clone is; the code and the git login stay on the machines.
type Codebase struct {
	Name      string          `json:"name"`
	Remote    string          `json:"remote"`               // empty while a repository the hub was asked to create is not made yet
	Hosted    bool            `json:"hosted,omitempty"`     // the hub holds the code (D106)
	SizeBytes int64           `json:"size_bytes,omitempty"` // hosted: disk used by the repository
	Base      string          `json:"base,omitempty"`
	Check     string          `json:"check,omitempty"` // the command that says the project's work is done; jobs from it use it unless told otherwise
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	Clones    []CodebaseClone `json:"clones"`
}

// CodebaseClone is one machine's copy: where it is, and how getting it went.
type CodebaseClone struct {
	Device string    `json:"device"`
	Path   string    `json:"path,omitempty"`
	Status string    `json:"status"` // pending | ready | failed
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// CodebaseReq attaches a repository that already exists.
type CodebaseReq struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	Base   string `json:"base,omitempty"`
}

// CodebaseCheckReq sets (or, empty, clears) a project's check command.
type CodebaseCheckReq struct {
	Check string `json:"check"`
}

// CodebaseHostReq makes a project the hub holds itself (D106): no address, no GitHub, no login.
type CodebaseHostReq struct {
	Name string `json:"name"`
}

// CodebaseMergeReq asks the hub to merge a job's published branch into the base branch of a hosted project.
type CodebaseMergeReq struct {
	Job    int64  `json:"job"`
	Branch string `json:"branch,omitempty"` // default: the job's latest published branch
}

// CodebaseMerge is the answer to a merge.
type CodebaseMerge struct {
	Project string `json:"project"`
	Job     int64  `json:"job"`
	Branch  string `json:"branch"`
	Into    string `json:"into"`
	Commit  string `json:"commit"`
	How     string `json:"how"` // fast-forward | merge commit | already merged
}

// CodebaseCreateReq asks a machine to create a repository under its own login.
type CodebaseCreateReq struct {
	Name    string `json:"name"`
	Owner   string `json:"owner,omitempty"`   // a user or organisation; empty: the account the machine is logged in as
	Private *bool  `json:"private,omitempty"` // default true
	Base    string `json:"base,omitempty"`
	Device  string `json:"device,omitempty"`
}

// CodebaseCloneReq asks a machine to get its own copy.
type CodebaseCloneReq struct {
	Device string `json:"device,omitempty"`
}

// CodebaseRequest is one thing a machine was asked to do for a project, or, for a clone that is already
// there, the answer with its path (Status ready, no ID).
type CodebaseRequest struct {
	ID          int64      `json:"id,omitempty"`
	Codebase    string     `json:"project"`
	Device      string     `json:"device"`
	Kind        string     `json:"kind"`   // clone | create
	Status      string     `json:"status"` // pending | done | failed | ready
	Path        string     `json:"path,omitempty"`
	Detail      string     `json:"detail,omitempty"`
	RequestedBy string     `json:"requested_by,omitempty"`
	At          time.Time  `json:"at"`
	DoneAt      *time.Time `json:"done_at,omitempty"`
}

// PendingCodebaseRequest is what a device is asked to do.
type PendingCodebaseRequest struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // clone | create
	Remote  string `json:"remote,omitempty"`
	Base    string `json:"base,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Private bool   `json:"private"`
	Hosted  bool   `json:"hosted,omitempty"` // the remote is on the hub (D106): the link sets up its credential helper
}

// CodebaseReport is the device's account of one request.
type CodebaseReport struct {
	Status string `json:"status"` // done | failed
	Detail string `json:"detail,omitempty"`
	Path   string `json:"path,omitempty"`
	Remote string `json:"remote,omitempty"` // for a create: the address of the repository that was made
	Base   string `json:"base,omitempty"`   // the repository's default branch
}
