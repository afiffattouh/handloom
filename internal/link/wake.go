package link

import (
	"context"
	"time"

	"handloom/internal/api"
)

// memo is what the link remembers about one local agent between ladder runs.
type memo struct {
	lastNudge       time.Time
	firstNudge      time.Time // first nudge since the agent last read its inbox
	nudges          int       // nudges since the agent last read its inbox
	reportedUnknown bool
	failedFor       int64  // newest unread message id a failure was reported for
	state           string // agent state as last seen from the hub
	lastHeartbeat   time.Time
}

func (l *Link) memoFor(agent string) *memo {
	m := l.memo[agent]
	if m == nil {
		m = &memo{}
		l.memo[agent] = m
	}
	return m
}

type action int

const (
	doNothing action = iota
	doNudge
	doFail    // cannot wake: report to the lead
	doUnknown // nudged twice, no inbox call: mark unknown and report
)

type decision struct {
	action action
	reason string
}

// decide is the wake ladder for one agent (DESIGN.md section 9).
//
//  1. working: do nothing. The end-of-turn hook hands the mail over.
//  2. idle with a terminal: type the nudge, at most once per NudgeEvery.
//  3. headless resume: not in this version.
//  4. otherwise (blocked, offline, unknown, no terminal): report to the lead,
//     once per batch of mail.
func (l *Link) decide(a api.DeviceAgent, m *memo, now time.Time) decision {
	if a.Unread == 0 {
		return decision{}
	}
	switch a.State {
	case api.StateWorking:
		return decision{}
	case api.StateIdle:
		if a.WakeTarget == "" {
			return failOnce(a, m, "no_wake_target")
		}
		if m.nudges >= 2 {
			if !m.reportedUnknown && now.Sub(m.firstNudge) >= l.opt.UnknownAfter {
				return decision{doUnknown, "no_inbox_after_nudges"}
			}
			return decision{}
		}
		due := a.Undelivered > 0 || now.Sub(m.lastNudge) >= l.opt.Renudge
		if !due || now.Sub(m.lastNudge) < l.opt.NudgeEvery {
			return decision{} // nothing new, or rate limited: pending mail merges into the next nudge
		}
		return decision{doNudge, ""}
	default:
		// Never type into a blocked agent; offline and unknown agents have
		// nobody to read the nudge.
		return failOnce(a, m, "state_"+a.State)
	}
}

func failOnce(a api.DeviceAgent, m *memo, reason string) decision {
	if a.LastUnreadID <= m.failedFor {
		return decision{}
	}
	return decision{doFail, reason}
}

func (l *Link) runLadder(ctx context.Context) {
	var agents []api.DeviceAgent
	if err := l.hub.Do(ctx, "GET", "/v1/device/agents", nil, &agents); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("ladder: %v", err)
		}
		return
	}
	for _, a := range agents {
		l.step(ctx, a)
	}
}

func (l *Link) step(ctx context.Context, a api.DeviceAgent) {
	now := l.opt.Now()
	l.mu.Lock()
	m := l.memoFor(a.Name)
	m.state = a.State
	if a.Unread == 0 {
		m.nudges, m.reportedUnknown = 0, false
	}
	d := l.decide(a, m, now)
	l.mu.Unlock()

	switch d.action {
	case doNudge:
		drv, target, err := l.opt.Drivers(a.WakeTarget)
		if err == nil {
			err = drv.Nudge(ctx, target, NudgeLine(a.Unread))
		}
		if err != nil {
			l.opt.Log.Printf("nudge %s: %v", a.Name, err)
			l.mu.Lock()
			report := a.LastUnreadID > m.failedFor
			l.mu.Unlock()
			if report {
				l.fail(ctx, a, m, "driver_error: "+reasonText(err.Error()))
			}
			return
		}
		l.mu.Lock()
		if m.nudges == 0 {
			m.firstNudge = now
		}
		m.nudges++
		m.lastNudge = now
		l.mu.Unlock()
		l.opt.Log.Printf("nudged %s through %s (%d unread)", a.Name, drv.Name(), a.Unread)
		if err := l.hub.Do(ctx, "POST", "/v1/agents/"+a.Name+"/wake", api.WakeReq{Method: drv.Name()}, nil); err != nil {
			l.opt.Log.Printf("report wake %s: %v", a.Name, err)
		}
	case doFail:
		l.fail(ctx, a, m, d.reason)
	case doUnknown:
		l.mu.Lock()
		m.reportedUnknown = true
		m.failedFor = a.LastUnreadID // the lead hears about this batch once
		l.mu.Unlock()
		l.opt.Log.Printf("%s did not read its inbox after %d nudges: marking unknown", a.Name, m.nudges)
		if err := l.hub.Do(ctx, "POST", "/v1/agents/"+a.Name+"/wake", api.WakeReq{Method: api.WakeNone, Reason: d.reason}, nil); err != nil {
			l.opt.Log.Printf("report unknown %s: %v", a.Name, err)
		}
	}
}

func (l *Link) fail(ctx context.Context, a api.DeviceAgent, m *memo, reason string) {
	l.mu.Lock()
	m.failedFor = a.LastUnreadID
	l.mu.Unlock()
	l.opt.Log.Printf("cannot wake %s: %s", a.Name, reason)
	if err := l.hub.Do(ctx, "POST", "/v1/agents/"+a.Name+"/wake", api.WakeReq{Method: api.WakeNone, Reason: reason}, nil); err != nil {
		l.opt.Log.Printf("report failure %s: %v", a.Name, err)
	}
}
