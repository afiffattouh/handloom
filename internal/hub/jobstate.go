package hub

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Where a job stands, in a few words a person can act on. It is worked out from what the hub already
// records each time a job is shown; nothing stores it. The order matters: the first line that fits wins.

// leadIdleGrace is how long a lead may sit idle with work waiting before that counts as stuck; a lead
// that has only just made its tasks is not.
const leadIdleGrace = 2 * time.Minute

type waitingTask struct {
	id        int64
	assignee  string // "" when nobody
	state     string // the assignee's state
	elsewhere int64  // the other job the assignee works in, 0 if none
}

type jobFacts struct {
	status      string
	counts      api.JobCounts
	lead        string
	leadState   string
	leadSince   time.Time
	questions   int
	starting    int           // agents being started
	liveWorkers int           // workers that are not offline
	waiting     []waitingTask // open tasks nobody has claimed
	failedStart string        // the latest failed start, in words, when nothing is running
	solo        bool          // no lead: a person made the steps and one helper does them
	now         time.Time
}

func jobProgress(f jobFacts) api.JobState {
	total := f.counts.Open + f.counts.Claimed + f.counts.Submitted + f.counts.Done
	switch {
	case f.status == api.StatusDone:
		return api.JobState{State: api.JobDone, Reason: "The job is finished."}
	case f.status != api.StatusOpen:
		return api.JobState{State: api.JobDone, Reason: "The job was cancelled."}
	case f.questions > 0:
		return api.JobState{State: api.JobNeedsYou, Reason: plural1(f.questions, "An agent has asked you a question", "Agents have asked you questions"),
			Action: "Answer in your inbox; the job waits for you."}
	}
	if f.lead == "" && f.starting == 0 && !f.solo {
		return stuck("No lead is running this job.", "Give it a lead: handloom job resume, or start one from the job's page.")
	}
	if f.lead != "" && f.leadState == api.StateOffline {
		return stuck(f.lead+", the lead, has stopped.", "Hand the job to another lead: handloom job resume.")
	}
	if f.lead != "" && f.leadState == api.StateUnknown && f.counts.Submitted > 0 && f.now.Sub(f.leadSince) >= leadIdleGrace {
		return stuck(fmt.Sprintf("%s, the lead, is not answering, so %s waiting to be checked.", f.lead, plural1(f.counts.Submitted, "1 step is", fmt.Sprintf("%d steps are", f.counts.Submitted))),
			"Nudge the lead (type `handloom inbox` in its terminal), or check the work yourself.")
	}
	if f.failedStart != "" && f.liveWorkers == 0 && f.starting == 0 {
		return stuck(f.failedStart, "Fix what it says and start the worker again, or ask the lead to.")
	}
	for _, w := range f.waiting {
		switch {
		case w.elsewhere != 0:
			return stuck(fmt.Sprintf("Step #%d is given to %s, who works on job %d and will never see it.", w.id, w.assignee, w.elsewhere),
				fmt.Sprintf("Start a worker for this job and give step #%d to it.", w.id))
		case w.assignee != "" && w.state == api.StateOffline:
			return stuck(fmt.Sprintf("Step #%d waits for %s, who is offline.", w.id, w.assignee),
				fmt.Sprintf("Start %s again, or give step #%d to another worker.", w.assignee, w.id))
		}
	}
	if f.solo && f.counts.Submitted > 0 {
		return api.JobState{State: api.JobNeedsYou, Reason: "The helper has finished and its work is waiting for you.", Action: "Look at what changed, then use it or send it back."}
	}
	if f.solo && f.counts.Open+f.counts.Claimed > 0 && f.liveWorkers == 0 && f.starting == 0 {
		return stuck("The helper is not running.", "Give the step to another profile from the job page, or stop the job.")
	}
	if f.solo && f.counts.Done < total {
		if f.liveWorkers == 0 {
			return api.JobState{State: api.JobWorking, Reason: "Starting the helper."}
		}
		return api.JobState{State: api.JobWorking, Reason: "A helper is working on it."}
	}
	idle := f.leadState == api.StateIdle && f.now.Sub(f.leadSince) >= leadIdleGrace
	if f.counts.Open > 0 && f.liveWorkers == 0 && f.starting == 0 && idle {
		return stuck(plural1(f.counts.Open, "1 step is waiting and nobody is working on it.", fmt.Sprintf("%d steps are waiting and nobody is working on them.", f.counts.Open)),
			"Message the lead to start workers, or start one yourself.")
	}
	if total > 0 && f.counts.Done == total {
		return api.JobState{State: api.JobReady, Reason: plural1(total, "The step is done and checked.", fmt.Sprintf("All %d steps are done and checked.", total)),
			Action: "Look at the result, then use it or close the job."}
	}
	if total == 0 {
		if f.lead == "" {
			return api.JobState{State: api.JobPlanning, Reason: "Starting the lead."}
		}
		return api.JobState{State: api.JobPlanning, Reason: f.lead + " is planning the steps."}
	}
	reason := fmt.Sprintf("%d of %d steps done", f.counts.Done, total)
	if f.counts.Submitted > 0 {
		reason += fmt.Sprintf("; %d waiting to be checked", f.counts.Submitted)
	}
	return api.JobState{State: api.JobWorking, Reason: reason + "."}
}

func stuck(reason, action string) api.JobState {
	return api.JobState{State: api.JobStuck, Reason: reason, Action: action}
}

func plural1(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// progress gathers the facts for jobProgress.
func (c *call) progress(t *taskRow, lead *agentRow, counts api.JobCounts) (api.JobState, error) {
	f := jobFacts{status: t.status, counts: counts, now: c.now}
	if lead != nil {
		f.lead, f.leadState, f.leadSince = lead.name, lead.state, store.Time(lead.stateAt)
	}
	if t.status == api.StatusOpen {
		if err := c.tx.QueryRow(`SELECT count(*) FROM escalation e JOIN agent a ON a.id = e.from_agent_id
			WHERE a.job_id = ? AND e.answered_at IS NULL`, t.id).Scan(&f.questions); err != nil {
			return api.JobState{}, err
		}
		if err := c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE job_id = ? AND status IN ('pending', 'launching')`, t.id).Scan(&f.starting); err != nil {
			return api.JobState{}, err
		}
		if err := c.tx.QueryRow(`SELECT count(*) FROM agent WHERE job_id = ? AND role != 'lead' AND state != 'offline'`, t.id).Scan(&f.liveWorkers); err != nil {
			return api.JobState{}, err
		}
		if lead == nil { // a job a person made the steps of, with no lead
			var n int
			if err := c.tx.QueryRow(`SELECT count(*) FROM task WHERE job_id = ? AND kind != 'job' AND created_by NOT LIKE 'agent:%'`, t.id).Scan(&n); err != nil {
				return api.JobState{}, err
			}
			f.solo = n > 0
		}
		rows, err := c.tx.Query(`SELECT t.id, COALESCE(a.name, ''), COALESCE(a.state, ''), a.job_id
			FROM task t LEFT JOIN agent a ON a.id = t.assigned_to
			WHERE t.job_id = ? AND t.status = 'open' AND t.kind != 'job' AND t.assigned_to IS NOT NULL ORDER BY t.id`, t.id)
		if err != nil {
			return api.JobState{}, err
		}
		for rows.Next() {
			var w waitingTask
			var aj sql.NullInt64
			if err := rows.Scan(&w.id, &w.assignee, &w.state, &aj); err != nil {
				rows.Close()
				return api.JobState{}, err
			}
			if aj.Valid && aj.Int64 != t.id {
				w.elsewhere = aj.Int64
			}
			f.waiting = append(f.waiting, w)
		}
		rows.Close()
		var name, errText string
		if err := c.tx.QueryRow(`SELECT name, error FROM spawn WHERE job_id = ? AND status = 'failed' ORDER BY id DESC LIMIT 1`, t.id).Scan(&name, &errText); err == nil {
			f.failedStart = fmt.Sprintf("Starting %s failed: %s", name, strings.TrimSpace(errText))
		}
	}
	return jobProgress(f), nil
}
