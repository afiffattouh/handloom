package hub

import (
	"fmt"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// leadSilence is how long a lead may be offline or unknown, with work open,
// before the human is told (DESIGN.md section 11).
const leadSilence = 30 * time.Minute

// activityVerbs are the audit actions worth showing a human, and how to say them.
var activityVerbs = map[string]string{
	"task.create": "created", "task.claim": "claimed", "task.submit": "submitted", "task.accept": "accepted",
	"task.reject": "rejected", "task.block": "marked blocked", "task.cancel": "cancelled", "task.release": "released",
	"escalation.open": "asked a question", "escalation.answer": "answered", "agent.register": "registered",
}

// digest builds the human's view of the project(s). Agents see only their own project.
func (c *call) digest() (*api.Digest, error) {
	d := &api.Digest{NeedsYou: []api.DigestItem{}, ToReview: []api.DigestItem{}, Running: []api.DigestItem{},
		Agents: []api.Agent{}, Activity: []api.Activity{}}
	where, args := ``, []any{}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		where, args = `WHERE project_id = ? `, []any{a.projectID}
	}
	scope := func(alias string) (string, []any) {
		if where == "" {
			return "", nil
		}
		return `AND ` + alias + `.project_id = ? `, args
	}

	// Open questions.
	extra, ex := scope("e")
	rows, err := c.tx.Query(escalationSelect+`WHERE e.answered_at IS NULL `+extra+`ORDER BY e.id`, ex...)
	if err != nil {
		return nil, err
	}
	var escs []*escalationRow
	for rows.Next() {
		e, err := scanEscalation(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		escs = append(escs, e)
	}
	rows.Close()
	for _, e := range escs {
		d.NeedsYou = append(d.NeedsYou, api.DigestItem{Kind: "escalation", ID: e.id, Title: e.question, Who: e.from,
			Options: e.options, At: store.Time(e.createdAt)})
	}

	// Tasks.
	extra, ex = scope("t")
	tasks, err := c.tasks(`WHERE t.status IN ('claimed', 'submitted') `+extra, ex...)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		item := api.DigestItem{ID: t.id, Title: t.title, Who: t.ownerName, At: store.Time(t.updated)}
		switch {
		case t.status == api.StatusSubmitted:
			item.Kind, item.Detail = "submitted", strings.Join(t.evidence, "; ")
			if t.note != "" {
				item.Detail += " — " + t.note
			}
			d.ToReview = append(d.ToReview, item)
		case t.blocked != "":
			item.Kind, item.Detail = "blocked", t.blocked
			d.NeedsYou = append(d.NeedsYou, item)
		default:
			item.Kind = "running"
			d.Running = append(d.Running, item)
		}
	}

	// Agents, and a silent lead.
	extra, ex = scope("a")
	agents, err := c.agents(`WHERE 1 = 1 `+extra, ex...)
	if err != nil {
		return nil, err
	}
	busy := len(tasks) > 0
	for _, a := range agents {
		d.Agents = append(d.Agents, a.api())
		silent := c.now.Sub(store.Time(a.stateAt)) > leadSilence
		if a.role == api.RoleLead && busy && silent && (a.state == api.StateOffline || a.state == api.StateUnknown) {
			d.NeedsYou = append(d.NeedsYou, api.DigestItem{Kind: "lead-silent", Title: fmt.Sprintf("The lead (%s) is %s while work is open", a.name, a.state),
				Who: a.name, At: store.Time(a.stateAt)})
		}
	}

	// Recent activity from the audit log (humans and the admin only: the log spans projects).
	if c.p.kind == kindDevice {
		c.tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM event`).Scan(&d.Seq)
		return d, nil
	}
	arows, err := c.tx.Query(`SELECT actor, action, target, created_at FROM audit ORDER BY seq DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() && len(d.Activity) < 15 {
		var actor, action, target string
		var at int64
		if err := arows.Scan(&actor, &action, &target, &at); err != nil {
			return nil, err
		}
		verb, ok := activityVerbs[action]
		if !ok {
			continue
		}
		d.Activity = append(d.Activity, api.Activity{At: store.Time(at), Text: fmt.Sprintf("%s %s %s", actor, verb, target)})
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}
	c.tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM event`).Scan(&d.Seq)
	return d, nil
}

func digestGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	return c.digest()
}
