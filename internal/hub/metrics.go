package hub

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// The command center is computed from what the hub already records: the audit
// log, the task and agent tables, escalations and device checks. Nothing here
// invents a number: every figure says how many records it rests on, and the
// insights state only what those records show.

type MetricsRange struct {
	Key     string
	Span    time.Duration
	Buckets int
	Bucket  time.Duration
	Label   func(t time.Time) string
}

var metricsRanges = map[string]MetricsRange{
	"24h": {"24h", 24 * time.Hour, 24, time.Hour, func(t time.Time) string { return fmt.Sprintf("%02d", t.Hour()) }},
	"7d":  {"7d", 7 * 24 * time.Hour, 7, 24 * time.Hour, func(t time.Time) string { return t.Weekday().String()[:3] }},
	"30d": {"30d", 30 * 24 * time.Hour, 30, 24 * time.Hour, func(t time.Time) string { return fmt.Sprintf("%d", t.Day()) }},
}

// Metrics is the whole picture for one range.
type Metrics struct {
	Range    string        `json:"range"`
	Since    time.Time     `json:"since"`
	Needs    NeedsMetric   `json:"needs_you"`
	Agents   AgentsMetric  `json:"agents"`
	Tasks    TasksMetric   `json:"tasks"`
	Checks   RateMetric    `json:"checks"`
	TaskTime DurMetric     `json:"task_time"`
	Answer   DurMetric     `json:"answer_time"`
	Fleet    []FleetRow    `json:"fleet"`
	Buckets  []BucketStat  `json:"buckets"`
	Util     []UtilRow     `json:"utilization"`
	Profiles []ProfileStat `json:"profiles"`
	Insights []Insight     `json:"insights"`
}

type NeedsMetric struct {
	Count  int           `json:"count"`
	Oldest time.Duration `json:"oldest_ns"`
}

type AgentsMetric struct {
	Total, Working, Idle, Blocked, Offline, Other int
}

type TasksMetric struct {
	Claimed, Submitted, Open int
	AwaitingCheck            int
}

// RateMetric is a share with the number of records behind it, and the same for
// the period before, so a change can be shown.
type RateMetric struct {
	Pct, PriorPct float64 // -1 when there is nothing to compute from
	N, PriorN     int
}

type DurMetric struct {
	Value, Prior time.Duration // 0 when there is nothing to compute from
	N, PriorN    int
}

type FleetRow struct {
	Name, Kind, Role, Device, State string
	Job                             int64
	Task                            string // what it is working on, in words
	StateFor                        time.Duration
	LeaseLeft                       time.Duration
	LeasePct                        int
}

type BucketStat struct {
	Label                    string
	Accepted, Rejected       int
	Passed, Failed, TimedOut int
}

type UtilRow struct {
	Agent                       string
	Working, Idle, Blocked, Off int // percent of the range, which sum to 100 (or 0 with no data)
}

type ProfileStat struct {
	Name       string
	Tasks      int // accepted in the range
	FirstTime  int // of those, accepted without ever being sent back
	Rejected   int // sent back in the range
	ChecksN    int
	ChecksPass int
	Median     time.Duration
}

type Insight struct {
	Sev     string // bad | warn | info
	Title   string
	Detail  string
	Basis   string
	Actions []InsightAction
}

type InsightAction struct{ Label, Href string }

type arow struct {
	at      time.Time
	actor   string
	action  string
	target  string
	payload map[string]any
}

func (c *call) auditRows(since time.Time, actions ...string) ([]arow, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(actions)), ",")
	args := []any{store.Millis(since)}
	for _, a := range actions {
		args = append(args, a)
	}
	rows, err := c.tx.Query(`SELECT actor, action, target, payload, created_at FROM audit WHERE created_at >= ? AND action IN (`+marks+`) ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arow
	for rows.Next() {
		var r arow
		var p string
		var at int64
		if err := rows.Scan(&r.actor, &r.action, &r.target, &p, &at); err != nil {
			return nil, err
		}
		r.at = store.Time(at)
		json.Unmarshal([]byte(p), &r.payload)
		out = append(out, r)
	}
	return out, rows.Err()
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func num(m map[string]any, k string) int    { f, _ := m[k].(float64); return int(f) }
func flag(m map[string]any, k string) bool  { b, _ := m[k].(bool); return b }

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

func rate(pass, n int) float64 {
	if n == 0 {
		return -1
	}
	return float64(pass) * 100 / float64(n)
}

// computeMetrics reads the record. It reads only: the transaction is the caller's.
func (c *call) computeMetrics(key string) (*Metrics, error) {
	rg, ok := metricsRanges[key]
	if !ok {
		rg = metricsRanges["7d"]
	}
	now := c.now
	since := now.Add(-rg.Span)
	priorSince := since.Add(-rg.Span)
	m := &Metrics{Range: rg.Key, Since: since}

	projectScope := ""
	var pargs []any
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		projectScope, pargs = ` AND a.project_id = ?`, []any{a.projectID}
	}

	// ---- agents and the fleet ----
	agents, err := c.agents(`WHERE 1 = 1`+projectScope, pargs...)
	if err != nil {
		return nil, err
	}
	running, err := c.tasks(`WHERE t.status IN ('claimed', 'submitted') AND t.kind = 'task'`)
	if err != nil {
		return nil, err
	}
	taskOf := map[int64]*taskRow{}
	for _, t := range running {
		if t.owner.Valid {
			taskOf[t.owner.Int64] = t
		}
		switch t.status {
		case api.StatusClaimed:
			m.Tasks.Claimed++
		case api.StatusSubmitted:
			m.Tasks.Submitted++
			if t.jobVerify != "" && t.check == nil {
				m.Tasks.AwaitingCheck++
			}
		}
	}
	c.tx.QueryRow(`SELECT count(*) FROM task WHERE kind = 'task' AND status = 'open'`).Scan(&m.Tasks.Open)
	for _, a := range agents {
		m.Agents.Total++
		switch a.state {
		case api.StateWorking:
			m.Agents.Working++
		case api.StateIdle:
			m.Agents.Idle++
		case api.StateBlocked:
			m.Agents.Blocked++
		case api.StateOffline:
			m.Agents.Offline++
		default:
			m.Agents.Other++
		}
		row := FleetRow{Name: a.name, Kind: a.kind, Role: a.role, Device: a.device, State: a.state, StateFor: now.Sub(store.Time(a.stateAt))}
		if a.jobID.Valid {
			row.Job = a.jobID.Int64
		}
		if t := taskOf[a.id]; t != nil {
			row.Task = fmt.Sprintf("#%d %s", t.id, t.title)
		} else if a.jobID.Valid {
			row.Task = fmt.Sprintf("job #%d", a.jobID.Int64)
		}
		if a.lease.Valid {
			left := store.Time(a.lease.Int64).Sub(now)
			if left < 0 {
				left = 0
			}
			row.LeaseLeft = left
			row.LeasePct = int(left * 100 / c.h.opt.AgentLease)
			if row.LeasePct > 100 {
				row.LeasePct = 100
			}
		}
		m.Fleet = append(m.Fleet, row)
	}
	sort.SliceStable(m.Fleet, func(i, j int) bool {
		if m.Fleet[i].Device != m.Fleet[j].Device {
			return m.Fleet[i].Device < m.Fleet[j].Device
		}
		if (m.Fleet[i].Role == api.RoleLead) != (m.Fleet[j].Role == api.RoleLead) {
			return m.Fleet[i].Role == api.RoleLead
		}
		return m.Fleet[i].Name < m.Fleet[j].Name
	})

	// ---- needs you ----
	d, err := c.digest()
	if err != nil {
		return nil, err
	}
	m.Needs.Count = len(d.NeedsYou)
	for _, it := range d.NeedsYou {
		if age := now.Sub(it.At); age > m.Needs.Oldest {
			m.Needs.Oldest = age
		}
	}

	// ---- audit-derived figures ----
	rows, err := c.auditRows(priorSince.Add(-30*24*time.Hour), "task.claim", "task.accept", "task.reject", "task.verified", "agent.state")
	if err != nil {
		return nil, err
	}
	lastClaim := map[string]time.Time{}
	rejectsBefore := map[string]int{} // sent back at least once, up to the time of reading each accept
	profileOf, err := c.profileByAgent()
	if err != nil {
		return nil, err
	}
	buckets := make([]BucketStat, rg.Buckets)
	bucketStart := now.Add(-time.Duration(rg.Buckets) * rg.Bucket)
	for i := range buckets {
		buckets[i].Label = rg.Label(bucketStart.Add(time.Duration(i)*rg.Bucket + rg.Bucket/2).UTC())
	}
	at := func(t time.Time) int {
		if t.Before(bucketStart) || t.After(now) {
			return -1
		}
		i := int(t.Sub(bucketStart) / rg.Bucket)
		if i >= rg.Buckets {
			i = rg.Buckets - 1
		}
		return i
	}
	profiles := map[string]*ProfileStat{}
	prof := func(agent string) *ProfileStat {
		n := profileOf[agent]
		if n == "" {
			n = "(no profile)"
		}
		p := profiles[n]
		if p == nil {
			p = &ProfileStat{Name: n}
			profiles[n] = p
		}
		return p
	}
	var curTimes, priorTimes []time.Duration
	var profTimes = map[string][]time.Duration{}
	var curPass, curN, priorPass, priorN int
	for _, r := range rows {
		switch r.action {
		case "task.claim":
			lastClaim[r.target] = r.at
		case "task.reject":
			if !r.at.Before(since) {
				if i := at(r.at); i >= 0 {
					buckets[i].Rejected++
				}
				prof(str(r.payload, "owner")).Rejected++
			}
			rejectsBefore[r.target]++
		case "task.accept":
			first := rejectsBefore[r.target] == 0
			var dur time.Duration
			cl, known := lastClaim[r.target]
			if known {
				dur = r.at.Sub(cl)
			}
			switch {
			case !r.at.Before(since):
				if i := at(r.at); i >= 0 {
					buckets[i].Accepted++
				}
				p := prof(str(r.payload, "owner"))
				p.Tasks++
				if first {
					p.FirstTime++
				}
				if known {
					curTimes = append(curTimes, dur)
					profTimes[p.Name] = append(profTimes[p.Name], dur)
				}
			case !r.at.Before(priorSince):
				if known {
					priorTimes = append(priorTimes, dur)
				}
			}
		case "task.verified":
			ok := num(r.payload, "exit_code") == 0 && !flag(r.payload, "timed_out")
			switch {
			case !r.at.Before(since):
				curN++
				if ok {
					curPass++
				}
				i := at(r.at)
				if i >= 0 {
					switch {
					case flag(r.payload, "timed_out"):
						buckets[i].TimedOut++
					case ok:
						buckets[i].Passed++
					default:
						buckets[i].Failed++
					}
				}
				p := prof(str(r.payload, "agent"))
				p.ChecksN++
				if ok {
					p.ChecksPass++
				}
			case !r.at.Before(priorSince):
				priorN++
				if ok {
					priorPass++
				}
			}
		}
	}
	m.Buckets = buckets
	m.Checks = RateMetric{Pct: rate(curPass, curN), PriorPct: rate(priorPass, priorN), N: curN, PriorN: priorN}
	m.TaskTime = DurMetric{Value: median(curTimes), Prior: median(priorTimes), N: len(curTimes), PriorN: len(priorTimes)}
	for n, p := range profiles {
		p.Median = median(profTimes[n])
		m.Profiles = append(m.Profiles, *p)
	}
	sort.Slice(m.Profiles, func(i, j int) bool {
		return m.Profiles[i].Tasks+m.Profiles[i].Rejected > m.Profiles[j].Tasks+m.Profiles[j].Rejected
	})

	// ---- how long you take to answer ----
	var ans, priorAns []time.Duration
	arows, err := c.tx.Query(`SELECT created_at, answered_at FROM escalation WHERE answered_at IS NOT NULL AND answered_at >= ?`, store.Millis(priorSince))
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var cr, an int64
		if err := arows.Scan(&cr, &an); err != nil {
			arows.Close()
			return nil, err
		}
		dur := store.Time(an).Sub(store.Time(cr))
		if !store.Time(an).Before(since) {
			ans = append(ans, dur)
		} else {
			priorAns = append(priorAns, dur)
		}
	}
	arows.Close()
	m.Answer = DurMetric{Value: median(ans), Prior: median(priorAns), N: len(ans), PriorN: len(priorAns)}

	// ---- where agents spend their time ----
	m.Util = utilisation(rows, agents, since, now)

	// ---- insights ----
	if m.Insights, err = c.insights(m, agents, taskOf); err != nil {
		return nil, err
	}
	return m, nil
}

// profileByAgent maps an agent's name to the profile it was started from.
func (c *call) profileByAgent() (map[string]string, error) {
	rows, err := c.tx.Query(`SELECT name, profile_name, profile_version FROM spawn WHERE profile_name != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, p string
		var v int
		if err := rows.Scan(&n, &p, &v); err != nil {
			return nil, err
		}
		out[n] = fmt.Sprintf("%s@%d", p, v)
	}
	return out, rows.Err()
}

// utilisation turns the agent.state events into the share of the range each
// agent spent working, idle, blocked and off (offline or unknown).
func utilisation(rows []arow, agents []*agentRow, since, now time.Time) []UtilRow {
	type seg struct {
		state string
		from  time.Time
	}
	events := map[string][]arow{}
	for _, r := range rows {
		if r.action == "agent.state" {
			events[strings.TrimPrefix(r.target, "agent:")] = append(events[strings.TrimPrefix(r.target, "agent:")], r)
		}
	}
	var out []UtilRow
	for _, a := range agents {
		ev := events[a.name]
		// The state at the start of the range: the last event before it, or the first event's "was".
		state := ""
		var inRange []arow
		for _, e := range ev {
			if e.at.Before(since) {
				state = str(e.payload, "state")
			} else {
				inRange = append(inRange, e)
			}
		}
		if state == "" {
			if len(inRange) > 0 {
				state = str(inRange[0].payload, "was")
			} else {
				state = a.state
			}
		}
		from := since
		if reg := store.Time(a.registeredAt); reg.After(from) {
			from = reg
		}
		if !from.Before(now) {
			continue
		}
		var tot = map[string]time.Duration{}
		cur := state
		last := from
		for _, e := range inRange {
			if e.at.After(last) {
				tot[cur] += e.at.Sub(last)
				last = e.at
			}
			cur = str(e.payload, "state")
		}
		tot[cur] += now.Sub(last)
		span := now.Sub(from)
		if span <= 0 {
			continue
		}
		pct := func(d time.Duration) int { return int(d * 100 / span) }
		u := UtilRow{Agent: a.name, Working: pct(tot[api.StateWorking]), Idle: pct(tot[api.StateIdle]), Blocked: pct(tot[api.StateBlocked])}
		u.Off = 100 - u.Working - u.Idle - u.Blocked
		if u.Off < 0 {
			u.Off = 0
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Working > out[j].Working })
	return out
}

func human(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// insights are rules over the record. Each one states what the record shows
// and what it is based on, and nothing about why or what would happen.
func (c *call) insights(m *Metrics, agents []*agentRow, taskOf map[int64]*taskRow) ([]Insight, error) {
	now := c.now
	var out []Insight
	byName := map[string]*agentRow{}
	for _, a := range agents {
		byName[a.name] = a
	}
	// A job's lead that is gone while the job is open.
	for _, a := range agents {
		if a.role != api.RoleLead || !a.jobID.Valid || (a.state != api.StateOffline && a.state != api.StateUnknown) || now.Sub(store.Time(a.stateAt)) < 10*time.Minute {
			continue
		}
		var n int
		var status string
		c.tx.QueryRow(`SELECT status FROM task WHERE id = ?`, a.jobID.Int64).Scan(&status)
		c.tx.QueryRow(`SELECT count(*) FROM task WHERE job_id = ? AND kind = 'task' AND status IN ('open', 'claimed', 'submitted')`, a.jobID.Int64).Scan(&n)
		if status != api.StatusOpen || n == 0 {
			continue
		}
		out = append(out, Insight{Sev: "bad", Title: fmt.Sprintf("%s, the lead of job #%d, has been %s for %s", a.name, a.jobID.Int64, a.state, human(now.Sub(store.Time(a.stateAt)))),
			Detail: fmt.Sprintf("The job has %d unfinished task(s).", n), Basis: "agent state history, tasks of the job",
			Actions: []InsightAction{{"Open the job", fmt.Sprintf("/jobs/%d", a.jobID.Int64)}}})
	}
	// Questions waiting for a person.
	rows, err := c.tx.Query(`SELECT e.id, a.name, e.created_at FROM escalation e JOIN agent a ON a.id = e.from_agent_id WHERE e.answered_at IS NULL ORDER BY e.created_at`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, cr int64
		var who string
		if err := rows.Scan(&id, &who, &cr); err != nil {
			rows.Close()
			return nil, err
		}
		if age := now.Sub(store.Time(cr)); age >= 30*time.Minute {
			out = append(out, Insight{Sev: "warn", Title: fmt.Sprintf("A question from %s has waited %s for an answer", who, human(age)),
				Basis: fmt.Sprintf("question #%d, asked %s", id, store.Time(cr).UTC().Format("15:04 UTC")), Actions: []InsightAction{{"Answer", "/inbox"}}})
		}
	}
	rows.Close()
	// Agents blocked for a long time, and idle ones with nothing to do.
	for _, a := range agents {
		age := now.Sub(store.Time(a.stateAt))
		switch {
		case a.state == api.StateBlocked && age >= 20*time.Minute:
			out = append(out, Insight{Sev: "warn", Title: fmt.Sprintf("%s has been blocked for %s", a.name, human(age)), Basis: "agent state history",
				Actions: []InsightAction{{"Open the agent", "/agents/" + a.name}}})
		case a.state == api.StateIdle && a.role != api.RoleLead && age >= 30*time.Minute && taskOf[a.id] == nil:
			out = append(out, Insight{Sev: "info", Title: fmt.Sprintf("%s has been idle for %s with no task", a.name, human(age)), Basis: "agent state history, task board",
				Actions: []InsightAction{{"Open the agent", "/agents/" + a.name}}})
		}
	}
	// Submitted work whose check failed and that nobody has sent back.
	trows, err := c.tasks(`WHERE t.status = 'submitted' AND t.kind = 'task'`)
	if err != nil {
		return nil, err
	}
	for _, t := range trows {
		if t.check != nil && (t.check.ExitCode != 0 || t.check.TimedOut) && now.Sub(t.check.At) >= 10*time.Minute {
			out = append(out, Insight{Sev: "warn", Title: fmt.Sprintf("Task #%d failed its check %s ago and is still waiting", t.id, human(now.Sub(t.check.At))),
				Detail: t.title, Basis: "device check of the submission", Actions: []InsightAction{{"Open the job", fmt.Sprintf("/jobs/%d", t.jobID.Int64)}}})
		}
	}
	// Profiles that are sent back more often than the rest, with enough tasks to say so.
	var tot, rej int
	for _, p := range m.Profiles {
		tot += p.Tasks + p.Rejected
		rej += p.Rejected
	}
	for _, p := range m.Profiles {
		n := p.Tasks + p.Rejected
		if n < 5 || tot-n < 5 {
			continue
		}
		mine, others := float64(p.Rejected)/float64(n), float64(rej-p.Rejected)/float64(tot-n)
		if mine >= 0.3 && mine >= others+0.2 {
			small := ""
			if n < 10 {
				small = ", small sample"
			}
			out = append(out, Insight{Sev: "warn", Title: fmt.Sprintf("Work from %s is sent back more often than the rest", p.Name),
				Detail: fmt.Sprintf("%d of %d tasks sent back (%d%%), against %d%% for the others.", p.Rejected, n, int(mine*100), int(others*100)),
				Basis:  fmt.Sprintf("accepted and rejected tasks, last %s%s", m.Range, small), Actions: []InsightAction{{"Open the profile", "/profiles/" + strings.SplitN(p.Name, "@", 2)[0]}}})
		}
	}
	rank := map[string]int{"bad": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Sev] < rank[out[j].Sev] })
	if len(out) > 8 {
		out = out[:8]
	}
	return out, nil
}

// metricsGet is GET /v1/metrics?range=24h|7d|30d.
func metricsGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	return c.computeMetrics(c.r.URL.Query().Get("range"))
}

var _ = sql.ErrNoRows
