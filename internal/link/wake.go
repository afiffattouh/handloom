package link

import (
	"context"
	"net/url"
	"time"

	"handloom/internal/api"
	"handloom/internal/drivers"
)

// memo is what the link remembers about one local agent between ladder runs.
type memo struct {
	lastNudge       time.Time
	firstNudge      time.Time // first nudge since the agent last read its inbox
	nudges          int       // nudges since the agent last read its inbox
	reportedUnknown bool
	failedReason    string // wake failure already reported to the lead; cleared when the state changes
	state           string // agent state as last seen from the hub
	headlessRunning bool   // a headless turn started by this link is still running
	lastHeartbeat   time.Time
	lastLive        time.Time // when this agent's terminal was last reported alive
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
//  3. no terminal but a saved session (wake target "headless"): run one
//     headless turn on it. Such an agent is offline between turns.
//  4. otherwise (blocked, offline, unknown, no terminal): report to the lead,
//     once until the agent's state changes.
func (l *Link) decide(a api.DeviceAgent, m *memo, now time.Time) decision {
	if a.Unread == 0 {
		return decision{}
	}
	headless := a.WakeTarget == HeadlessTarget
	switch {
	case a.State == api.StateWorking:
		return decision{}
	case a.State == api.StateIdle, a.State == api.StateOffline && headless:
		if a.WakeTarget == "" {
			return failOnce(a, m, "no_wake_target")
		}
		if headless && m.headlessRunning {
			return decision{} // its end-of-turn hook hands over new mail
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
		// nobody to read the nudge. A prompt that is answered (or denied
		// by policy) within the grace period is not worth a report.
		if a.State == api.StateBlocked && now.Sub(a.StateAt) < l.opt.BlockedGrace {
			return decision{}
		}
		return failOnce(a, m, "state_"+a.State)
	}
}

func failOnce(a api.DeviceAgent, m *memo, reason string) decision {
	if m.failedReason == reason {
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

// vouch tells the hub that an agent's terminal still exists, so the hub can
// tell "quiet" from "gone". Agents without a terminal (headless, none) are
// resumable and have nothing to vouch for.
func (l *Link) vouch(ctx context.Context, a api.DeviceAgent, now time.Time) {
	if a.WakeTarget == "" || a.WakeTarget == HeadlessTarget {
		return
	}
	l.mu.Lock()
	due := now.Sub(l.memoFor(a.Name).lastLive) >= l.opt.LiveEvery
	l.mu.Unlock()
	if !due {
		return
	}
	drv, target, err := l.opt.Drivers(a.WakeTarget)
	if err != nil || !drv.Alive(ctx, target) {
		return
	}
	if err := l.hub.Do(ctx, "POST", "/v1/agents/"+url.PathEscape(a.Name)+"/heartbeat", nil, nil); err != nil {
		l.opt.Log.Printf("heartbeat %s: %v", a.Name, err)
		return
	}
	l.mu.Lock()
	l.memoFor(a.Name).lastLive = now
	l.mu.Unlock()
}

func (l *Link) step(ctx context.Context, a api.DeviceAgent) {
	now := l.opt.Now()
	l.vouch(ctx, a, now)
	l.mu.Lock()
	m := l.memoFor(a.Name)
	if m.state != a.State {
		m.state, m.failedReason = a.State, ""
	}
	if a.Unread == 0 {
		m.nudges, m.reportedUnknown = 0, false
	}
	d := l.decide(a, m, now)
	l.mu.Unlock()

	switch d.action {
	case doNudge:
		method := api.WakeHeadless
		var err error
		if a.WakeTarget == HeadlessTarget {
			err = l.startHeadless(ctx, a, m, NudgeLine(a.Unread))
		} else {
			var drv drivers.Driver
			var target string
			if drv, target, err = l.opt.Drivers(a.WakeTarget); err == nil {
				method = drv.Name()
				err = drv.Nudge(ctx, target, NudgeLine(a.Unread))
			}
		}
		if err != nil {
			l.opt.Log.Printf("nudge %s: %v", a.Name, err)
			reason := "driver_error: " + reasonText(err.Error())
			l.mu.Lock()
			report := m.failedReason != reason
			l.mu.Unlock()
			if report {
				l.fail(ctx, a, m, reason)
			}
			return
		}
		l.mu.Lock()
		if m.nudges == 0 {
			m.firstNudge = now
		}
		m.nudges++
		m.lastNudge = now
		m.failedReason = ""
		l.mu.Unlock()
		l.opt.Log.Printf("woke %s through %s (%d unread)", a.Name, method, a.Unread)
		if err := l.hub.Do(ctx, "POST", "/v1/agents/"+a.Name+"/wake", api.WakeReq{Method: method}, nil); err != nil {
			l.opt.Log.Printf("report wake %s: %v", a.Name, err)
		}
	case doFail:
		l.fail(ctx, a, m, d.reason)
	case doUnknown:
		l.mu.Lock()
		m.reportedUnknown = true
		// The hub sets the state to unknown and tells the lead; do not report that again.
		m.state, m.failedReason = api.StateUnknown, "state_"+api.StateUnknown
		l.mu.Unlock()
		l.opt.Log.Printf("%s did not read its inbox after %d nudges: marking unknown", a.Name, m.nudges)
		if err := l.hub.Do(ctx, "POST", "/v1/agents/"+a.Name+"/wake", api.WakeReq{Method: api.WakeNone, Reason: d.reason}, nil); err != nil {
			l.opt.Log.Printf("report unknown %s: %v", a.Name, err)
		}
	}
}

func (l *Link) fail(ctx context.Context, a api.DeviceAgent, m *memo, reason string) {
	l.mu.Lock()
	m.failedReason = reason
	l.mu.Unlock()
	l.opt.Log.Printf("cannot wake %s: %s", a.Name, reason)
	if err := l.hub.Do(ctx, "POST", "/v1/agents/"+a.Name+"/wake", api.WakeReq{Method: api.WakeNone, Reason: reason}, nil); err != nil {
		l.opt.Log.Printf("report failure %s: %v", a.Name, err)
	}
}
