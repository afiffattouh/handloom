package hub

import (
	"database/sql"
	"encoding/json"
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
	"escalation.open": "asked", "escalation.answer": "answered", "agent.register": "joined the project",
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
			if t.jobVerify != "" {
				switch {
				case t.check == nil:
					item.Detail += " — verification pending on the device"
				default:
					item.Detail += " — checked by " + t.check.Device + ": " + checkWords(t.check)
				}
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

	jscope, jex := scope("j")
	ex = jex
	// Jobs whose every task is done wait for a human to look and close them.
	jrows, err := c.tx.Query(`SELECT j.id, j.title, j.updated_at, (SELECT max(t.updated_at) FROM task t WHERE t.job_id = j.id)
		FROM task j WHERE j.kind = 'job' AND j.status = 'open' `+jscope+`
		AND EXISTS (SELECT 1 FROM task t WHERE t.job_id = j.id AND t.kind = 'task' AND t.status = 'done')
		AND NOT EXISTS (SELECT 1 FROM task t WHERE t.job_id = j.id AND t.kind = 'task' AND t.status IN ('open', 'claimed', 'submitted'))`, ex...)
	if err != nil {
		return nil, err
	}
	var ready []api.DigestItem
	for jrows.Next() {
		var id, upd int64
		var title string
		var last sql.NullInt64
		if err := jrows.Scan(&id, &title, &upd, &last); err != nil {
			jrows.Close()
			return nil, err
		}
		at := store.Time(upd)
		if last.Valid {
			at = store.Time(last.Int64)
		}
		ready = append(ready, api.DigestItem{Kind: "job-done", ID: id, Title: title, Detail: "Every task is done. Look over the result, then close the job.", At: at})
	}
	jrows.Close()
	if len(ready) > 0 {
		d.ToReview = append(ready, d.ToReview...)
	}

	// Accepted work that could not be merged waits for somebody to sort out.
	mrows, err := c.tx.Query(`SELECT m.task_id, t.title, m.status, m.detail, m.done_at FROM merge m JOIN task t ON t.id = m.task_id JOIN task j ON j.id = m.job_id
		WHERE j.status = 'open' AND m.status IN ('conflict', 'failed') `+jscope+`
		AND m.id = (SELECT max(m2.id) FROM merge m2 WHERE m2.task_id = m.task_id)`, ex...)
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var id int64
		var title, status, detail string
		var at sql.NullInt64
		if err := mrows.Scan(&id, &title, &status, &detail, &at); err != nil {
			mrows.Close()
			return nil, err
		}
		d.NeedsYou = append(d.NeedsYou, api.DigestItem{Kind: "merge-" + status, ID: id, Title: title,
			Detail: "Accepted, but its branch did not merge into the integration branch: " + detail, At: store.Time(at.Int64)})
	}
	mrows.Close()

	// Proposed notes that could not be put on the job's branch.
	krows, err := c.tx.Query(`SELECT k.job_id, j.title, k.agent, k.status, k.detail FROM kcollect k JOIN task j ON j.id = k.job_id
		WHERE j.status = 'open' AND k.status IN ('conflict', 'failed') `+jscope+`
		AND k.id = (SELECT max(k2.id) FROM kcollect k2 WHERE k2.job_id = k.job_id AND k2.agent = k.agent)`, jex...)
	if err != nil {
		return nil, err
	}
	for krows.Next() {
		var id int64
		var title, agent, status, detail string
		if err := krows.Scan(&id, &title, &agent, &status, &detail); err != nil {
			krows.Close()
			return nil, err
		}
		d.NeedsYou = append(d.NeedsYou, api.DigestItem{Kind: "notes-" + status, ID: id, Title: title,
			Detail: agent + "'s proposed notes could not be put on the knowledge branch: " + detail, At: c.now})
	}
	krows.Close()

	// Agents, and a silent lead.
	extra, ex = scope("a")
	agents, err := c.agents(`WHERE 1 = 1 `+extra, ex...)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		d.Agents = append(d.Agents, a.api())
		if a.role != api.RoleLead {
			continue
		}
		// A lead that is offline, or has been unknown for a while, while its
		// work is unfinished: somebody has to resume it.
		silent := a.state == api.StateOffline ||
			(a.state == api.StateUnknown && c.now.Sub(store.Time(a.stateAt)) > leadSilence)
		if !silent {
			continue
		}
		n, err := c.leadWork(a)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		item := api.DigestItem{Kind: "lead-silent", Who: a.name, At: store.Time(a.stateAt),
			Title: fmt.Sprintf("The lead (%s) is %s while %d task(s) are unfinished", a.name, a.state, n)}
		if a.jobID.Valid {
			item.ID = a.jobID.Int64
			item.Title = fmt.Sprintf("The lead of job #%d (%s) is %s while %d task(s) are unfinished", a.jobID.Int64, a.name, a.state, n)
			item.Detail = fmt.Sprintf("Resume it: handloom job resume %d --lead <agent>", a.jobID.Int64)
		}
		d.NeedsYou = append(d.NeedsYou, item)
	}

	// Recent activity from the audit log (humans and the admin only: the log spans projects).
	if c.p.kind == kindDevice {
		c.tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM event`).Scan(&d.Seq)
		return d, nil
	}
	arows, err := c.tx.Query(`SELECT actor, action, target, payload, created_at FROM audit ORDER BY seq DESC LIMIT 400`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() && len(d.Activity) < 15 {
		var actor, action, target, payload string
		var at int64
		if err := arows.Scan(&actor, &action, &target, &payload, &at); err != nil {
			return nil, err
		}
		if text, ok := activityText(actor, action, target, payload); ok {
			d.Activity = append(d.Activity, api.Activity{At: store.Time(at), Text: text})
			continue
		}
		verb, ok := activityVerbs[action]
		if !ok {
			continue
		}
		d.Activity = append(d.Activity, api.Activity{At: store.Time(at), Text: strings.TrimSpace(fmt.Sprintf("%s %s %s", who(actor), verb, what(target)))})
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

// who and what turn audit identifiers into words for people.
func who(actor string) string {
	for _, p := range []string{"agent:", "human:"} {
		if strings.HasPrefix(actor, p) {
			return strings.TrimPrefix(actor, p)
		}
	}
	return actor
}

func what(target string) string {
	switch {
	case strings.HasPrefix(target, "task:"):
		return "task #" + strings.TrimPrefix(target, "task:")
	case strings.HasPrefix(target, "escalation:"):
		return "question #" + strings.TrimPrefix(target, "escalation:")
	case strings.HasPrefix(target, "agent:"):
		return "" // "researcher registered" reads better than "registered agent:researcher"
	}
	return target
}

// activityText words the events that need their details: what a device found,
// which state an agent entered, what was asked for. It reports false for
// everything the plain verb table covers.
func activityText(actor, action, target, payload string) (string, bool) {
	var p map[string]any
	json.Unmarshal([]byte(payload), &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	name := strings.TrimPrefix(target, "agent:")
	taskNo := strings.TrimPrefix(target, "task:")
	switch action {
	case "job.new":
		return fmt.Sprintf("%s started job %s: %s", who(actor), strings.TrimPrefix(target, "job:"), str("title")), true
	case "job.close":
		return fmt.Sprintf("job %s was closed (%s)", strings.TrimPrefix(target, "job:"), str("status")), true
	case "job.lead_lost":
		return fmt.Sprintf("the lead of job %s stopped responding", strings.TrimPrefix(target, "job:")), true
	case "spawn.request":
		return fmt.Sprintf("%s asked for the agent %s on %s", who(actor), str("name"), str("device")), true
	case "spawn.failed":
		return fmt.Sprintf("the agent %s could not be started: %s", str("name"), str("error")), true
	case "agent.state":
		switch str("state") {
		case "working":
			return name + " started working", true
		case "blocked":
			return name + " is blocked", true
		case "offline":
			return name + " went offline", true
		case "idle":
			return name + " finished and is idle", true
		}
		return "", false
	case "task.verified":
		res := "passed"
		if n, _ := p["exit_code"].(float64); n != 0 {
			res = fmt.Sprintf("failed (exit %d)", int(n))
		}
		if b, _ := p["timed_out"].(bool); b {
			res = "timed out"
		}
		return fmt.Sprintf("the device checked task #%s: %s", taskNo, res), true
	case "task.merge_merged":
		return fmt.Sprintf("task #%s was merged into the integration branch", taskNo), true
	case "task.merge_conflict":
		return fmt.Sprintf("task #%s conflicts with the integration branch", taskNo), true
	case "task.merge_failed":
		return fmt.Sprintf("task #%s could not be merged", taskNo), true
	case "task.scope_refused":
		return fmt.Sprintf("a submit of task #%s was refused: it changed files outside its scope", taskNo), true
	case "job.notes_done":
		if n, _ := p["notes"].(float64); n > 0 {
			return fmt.Sprintf("%s's %d proposed note(s) were gathered for review", str("agent"), int(n)), true
		}
	}
	return "", false
}
