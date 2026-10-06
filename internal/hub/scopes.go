package hub

import (
	"fmt"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Action is one row of the scope table in DESIGN.md section 6.
type Action string

const (
	ActRead             Action = "read"              // read board and messages
	ActSend             Action = "send"              // send messages
	ActTaskManage       Action = "task.manage"       // create, assign, accept, reject, cancel tasks
	ActTaskWork         Action = "task.work"         // claim, heartbeat, submit, release own task
	ActEscalationOpen   Action = "escalation.open"   // ask_human
	ActEscalationAnswer Action = "escalation.answer" // answer escalation
	ActSpawn            Action = "spawn"             // ask for an agent to be started
)

const (
	subjectHuman  = "human"        // owner and member
	subjectViewer = "human:viewer" // reads only
)

// scopeTable is DESIGN.md section 6, cell for cell. A missing entry means "no".
// The hub checks it on every request; nothing the client sends can change it.
var scopeTable = map[Action]map[string]bool{
	ActRead:             {api.RoleLead: true, api.RoleWorker: true, api.RoleObserver: true, subjectHuman: true, subjectViewer: true},
	ActSend:             {api.RoleLead: true, api.RoleWorker: true, subjectHuman: true},
	ActTaskManage:       {api.RoleLead: true, subjectHuman: true},
	ActTaskWork:         {api.RoleLead: true, api.RoleWorker: true},
	ActEscalationOpen:   {api.RoleLead: true},
	ActEscalationAnswer: {subjectHuman: true},
	ActSpawn:            {api.RoleLead: true, subjectHuman: true},
}

// Allowed reports whether subject (a role, or "human") may perform action.
func Allowed(action Action, subject string) bool {
	return scopeTable[action][subject]
}

// allow enforces the scope table for the caller. The admin token is for
// administration only: it may read, and nothing else in the table.
func (c *call) allow(action Action) error {
	switch c.p.kind {
	case kindAdmin:
		if action == ActRead {
			return nil
		}
		return forbidden("the admin token may not %s; use a human token", action)
	case kindHuman:
		subject := subjectHuman
		if c.p.role == store.RoleViewer {
			subject = subjectViewer
		}
		if Allowed(action, subject) {
			return nil
		}
		return forbidden("a %s may not %s", c.p.role, action)
	case kindDevice:
		a, err := c.agent()
		if err != nil {
			return err
		}
		if Allowed(action, a.role) {
			return nil
		}
		return forbidden("role %s may not %s", a.role, action)
	}
	return forbidden("unknown caller")
}

func forbidden(format string, args ...any) error {
	return &apiError{status: 403, code: "forbidden", msg: fmt.Sprintf(format, args...)}
}
