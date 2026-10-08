package hub

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// One agent: its state, what it is doing, how it has done, and (for people
// allowed to) its terminal.

type agentPageView struct {
	Name, Kind, Role, Device, State, Profile string
	Job                                      int64
	StateFor                                 string
	Badge, Dot                               string
	TaskID                                   int64
	Task                                     string
	LeasePct                                 int
	HasLease                                 bool
	LeaseNote                                string
	Accepted, Rejected                       int
	Checks                                   string
	ChecksNote                               string
	Idle                                     string
	Usage, UsageNote                         string
	Timeline                                 template.HTML
	Recent                                   []agentTask
	Attach                                   string
	CanWatch                                 bool
	CanRemove                                bool
	WatchWhy                                 string
}

type agentTask struct {
	ID            int64
	Title, Status string
	Badge         string
	Ago           string
}

func (h *Hub) webAgent(q *webReq) error {
	a, err := q.c.agentByName(q.r.PathValue("name"))
	if err != nil {
		return err
	}
	if a == nil {
		q.page(404, "message", pageData{Title: "Not found", Error: "No such agent."})
		return errHandled
	}
	now := q.now
	v := &agentPageView{Name: a.name, Kind: a.kind, Role: a.role, Device: a.device, State: a.state, StateFor: dur(now.Sub(store.Time(a.stateAt)))}
	if a.jobID.Valid {
		v.Job = a.jobID.Int64
	}
	switch a.state {
	case api.StateWorking:
		v.Badge = "success"
	case api.StateBlocked:
		v.Badge = "warning"
	case api.StateOffline:
		v.Badge, v.Dot = "destructive", " ring"
	case api.StateIdle:
	default:
		v.Badge, v.Dot = "outline", " ring"
	}
	if p, err := q.c.profileByAgent(); err == nil {
		v.Profile = p[a.name]
	}
	if a.lease.Valid {
		v.HasLease = true
		left := store.Time(a.lease.Int64).Sub(now)
		if left < 0 {
			left = 0
		}
		v.LeasePct = int(left * 100 / q.c.h.opt.AgentLease)
		if v.LeasePct > 100 {
			v.LeasePct = 100
		}
		v.LeaseNote = "renewed while its terminal is seen"
	} else {
		v.LeaseNote = "no lease: it has no terminal the link watches"
	}

	tasks, err := q.c.tasks(`WHERE o.name = ? AND t.kind = 'task'`, a.name)
	if err != nil {
		return err
	}
	for i := len(tasks) - 1; i >= 0; i-- {
		t := tasks[i]
		if t.status == api.StatusClaimed && v.TaskID == 0 {
			v.TaskID, v.Task = t.id, t.title
		}
		if len(v.Recent) < 8 {
			b := ""
			switch t.status {
			case api.StatusDone:
				b = "success"
			case api.StatusClaimed:
				b = "warning"
			case api.StatusSubmitted:
				b = "info"
			}
			v.Recent = append(v.Recent, agentTask{t.id, t.title, t.status, b, ago(now, store.Time(t.updated))})
		}
	}

	rows, err := q.c.auditRows(now.Add(-7*24*time.Hour), "task.accept", "task.reject", "task.verified", "agent.state")
	if err != nil {
		return err
	}
	var pass, n int
	for _, r := range rows {
		switch r.action {
		case "task.accept":
			if str(r.payload, "owner") == a.name {
				v.Accepted++
			}
		case "task.reject":
			if str(r.payload, "owner") == a.name {
				v.Rejected++
			}
		case "task.verified":
			if str(r.payload, "agent") == a.name {
				n++
				if num(r.payload, "exit_code") == 0 && !flag(r.payload, "timed_out") {
					pass++
				}
			}
		}
	}
	v.Checks, v.ChecksNote = "–", "no device checks in the last 7 days"
	if n > 0 {
		v.Checks, v.ChecksNote = fmt.Sprintf("%d%%", pass*100/n), fmt.Sprintf("%d of %d checks, last 7 days", pass, n)
	}
	since := now.Add(-24 * time.Hour)
	segs := stateSegments(stateEvents(rows)[a.name], a, since, now)
	v.Idle = "–"
	if len(segs) > 0 {
		span := segs[len(segs)-1].To.Sub(segs[0].From)
		var idle time.Duration
		for _, s := range segs {
			if s.State == api.StateIdle {
				idle += s.To.Sub(s.From)
			}
		}
		if span > 0 {
			v.Idle = fmt.Sprintf("%d%%", int(idle*100/span))
		}
	}
	v.Timeline = timelineSVG(segs, since, now)

	v.Usage, v.UsageNote = "–", "no usage reported yet"
	if sp, err := q.c.spendBetween(now.Add(-7*24*time.Hour), now); err == nil {
		for _, as := range sp.PerAgent {
			if as.Agent == a.name {
				v.Usage, v.UsageNote = tokensWords(as.Tokens), "tokens, last 7 days"
				if as.Priced {
					v.UsageNote = fmt.Sprintf("tokens, last 7 days · about $%.2f", as.Cost)
				} else if !sp.HasPrice {
					v.UsageNote = "tokens, last 7 days · no prices entered"
				}
			}
		}
	}
	var started int
	q.c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE name = ? AND status = 'started'`, a.name).Scan(&started)
	if started > 0 {
		v.Attach = "tmux -L handloom attach -t handloom:" + a.name
	}
	v.CanRemove = q.human.Role != store.RoleViewer
	v.CanWatch, v.WatchWhy = q.watchAllowed(a)
	if v.CanWatch && a.wakeTarget == "" {
		v.CanWatch, v.WatchWhy = false, "This agent has no terminal the machine's link can read."
	}
	q.page(http.StatusOK, "agent", pageData{Title: a.name, Extra: v})
	return nil
}

// timelineSVG draws 24 hours of state as one strip.
func timelineSVG(segs []segment, since, now time.Time) template.HTML {
	var b strings.Builder
	b.WriteString(`<svg class="chart" viewBox="0 0 300 40" role="img" aria-label="State over the last 24 hours">`)
	span := now.Sub(since).Seconds()
	for _, s := range segs {
		cls := map[string]string{api.StateWorking: "c-primary", api.StateIdle: "c-idle", api.StateBlocked: "c-warn"}[s.State]
		if cls == "" {
			continue // offline or unknown: left blank
		}
		x := 2 + s.From.Sub(since).Seconds()/span*296
		w := s.To.Sub(s.From).Seconds() / span * 296
		if w < 1 {
			w = 1
		}
		fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="4" width="%.1f" height="18" rx="2"/>`, cls, x, w)
	}
	for i := 0; i <= 4; i++ {
		t := since.Add(time.Duration(i) * 6 * time.Hour).UTC()
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case 4:
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text x="%d" y="37" text-anchor="%s">%02d:00</text>`, 2+i*74, anchor, t.Hour())
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
