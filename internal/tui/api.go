package tui

import (
	"fmt"
	"net/url"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

// API is everything the console asks of the hub. The real one wraps the same
// client the command line uses; tests supply a fake.
type API interface {
	Whoami() (api.WhoAmI, error)
	// People is owner-only; the console uses it to learn the signed-in role.
	People() ([]api.Person, error)
	Metrics(rng string) (Metrics, error)
	Digest() (api.Digest, error)
	Jobs() ([]api.Job, error)
	Job(id int64) (api.Job, error)
	JobTasks(id int64) ([]api.Task, error)
	Task(id int64) (api.Task, error)
	Screen(agent string) (api.Screen, error)
	Profiles() ([]api.ProfileInfo, error)
	Profile(name string) (api.ProfileFull, error)
	Starters() ([]api.StarterInfo, error)
	Starter(name string) (api.StarterFull, error)
	Devices() ([]api.Device, error)

	Accept(task int64) error
	Reject(task int64, reason string) error
	Answer(escalation int64, text string) error
	NewJob(req api.JobNewReq) (api.Job, error)
	CloseJob(id int64, cancel bool) error
}

// Metrics mirrors GET /v1/metrics. The hub tags only its outer fields, so the
// inner ones keep Go's names; decoding ignores case.
type Metrics struct {
	Range string `json:"range"`
	Needs struct {
		Count  int           `json:"count"`
		Oldest time.Duration `json:"oldest_ns"`
	} `json:"needs_you"`
	Agents struct {
		Total, Working, Idle, Blocked, Offline, Other int
	} `json:"agents"`
	Tasks struct {
		Claimed, Submitted, Open, AwaitingCheck int
	} `json:"tasks"`
	Checks   Rate `json:"checks"`
	TaskTime Dur  `json:"task_time"`
	Answer   Dur  `json:"answer_time"`
	Fleet    []FleetRow
	Buckets  []Bucket
	Util     []UtilRow `json:"utilization"`
	Insights []Insight
	Spend    *Spend `json:"usage"`
}

// Rate is a share with the number of records behind it; Pct is -1 when there are none.
type Rate struct {
	Pct, PriorPct float64
	N, PriorN     int
}

type Dur struct {
	Value, Prior time.Duration
	N, PriorN    int
}

type FleetRow struct {
	Name, Kind, Role, Device, State string
	Job                             int64
	Task                            string
	StateFor                        time.Duration
}

type Bucket struct {
	Label                    string
	Accepted, Rejected       int
	Passed, Failed, TimedOut int
}

type UtilRow struct {
	Agent                       string
	Working, Idle, Blocked, Off int
}

type Insight struct {
	Sev, Title, Detail, Basis string
}

type Spend struct {
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost_usd"`
	Priced   int64   `json:"priced_tokens"`
	HasPrice bool    `json:"has_price"`
}

// HTTP is the API over a hub connection.
type HTTP struct{ c *client.Client }

func NewHTTP(c *client.Client) *HTTP { return &HTTP{c} }

func get[T any](h *HTTP, path string) (T, error) {
	var v T
	err := h.c.Get(path, &v)
	return v, err
}

func (h *HTTP) Whoami() (api.WhoAmI, error)    { return get[api.WhoAmI](h, "/v1/whoami") }
func (h *HTTP) People() ([]api.Person, error)  { return get[[]api.Person](h, "/v1/people") }
func (h *HTTP) Digest() (api.Digest, error)    { return get[api.Digest](h, "/v1/digest") }
func (h *HTTP) Jobs() ([]api.Job, error)       { return get[[]api.Job](h, "/v1/jobs") }
func (h *HTTP) Devices() ([]api.Device, error) { return get[[]api.Device](h, "/v1/admin/devices") }
func (h *HTTP) Profiles() ([]api.ProfileInfo, error) {
	return get[[]api.ProfileInfo](h, "/v1/profiles")
}
func (h *HTTP) Starters() ([]api.StarterInfo, error) {
	return get[[]api.StarterInfo](h, "/v1/starters")
}

func (h *HTTP) Metrics(rng string) (Metrics, error) {
	return get[Metrics](h, "/v1/metrics?range="+url.QueryEscape(rng))
}
func (h *HTTP) Job(id int64) (api.Job, error) { return get[api.Job](h, fmt.Sprintf("/v1/jobs/%d", id)) }
func (h *HTTP) JobTasks(id int64) ([]api.Task, error) {
	return get[[]api.Task](h, fmt.Sprintf("/v1/tasks?job=%d", id))
}
func (h *HTTP) Task(id int64) (api.Task, error) {
	return get[api.Task](h, fmt.Sprintf("/v1/tasks/%d", id))
}
func (h *HTTP) Screen(agent string) (api.Screen, error) {
	return get[api.Screen](h, "/v1/agents/"+url.PathEscape(agent)+"/screen")
}
func (h *HTTP) Profile(name string) (api.ProfileFull, error) {
	return get[api.ProfileFull](h, "/v1/profiles/"+url.PathEscape(name))
}
func (h *HTTP) Starter(name string) (api.StarterFull, error) {
	return get[api.StarterFull](h, "/v1/starters/"+url.PathEscape(name))
}

func (h *HTTP) Accept(task int64) error {
	return h.c.Post(fmt.Sprintf("/v1/tasks/%d/accept", task), nil, nil)
}
func (h *HTTP) Reject(task int64, reason string) error {
	return h.c.Post(fmt.Sprintf("/v1/tasks/%d/reject", task), api.ReasonReq{Reason: reason}, nil)
}
func (h *HTTP) Answer(id int64, text string) error {
	return h.c.Post(fmt.Sprintf("/v1/escalations/%d/answer", id), api.AnswerReq{Answer: text}, nil)
}
func (h *HTTP) NewJob(req api.JobNewReq) (api.Job, error) {
	var j api.Job
	err := h.c.Post("/v1/jobs", req, &j)
	return j, err
}
func (h *HTTP) CloseJob(id int64, cancel bool) error {
	return h.c.Post(fmt.Sprintf("/v1/jobs/%d/close", id), api.JobCloseReq{Cancel: cancel}, nil)
}
