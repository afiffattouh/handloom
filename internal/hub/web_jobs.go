package hub

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// ---- jobs ----

type jobsView struct {
	Jobs     []jobRow
	CanStart bool
}

type jobRow struct {
	api.Job
	Counts string
	Ago    string
}

func (q *webReq) jobsPage(status int, errMsg string) error {
	list, err := jobList(q.c)
	if err != nil {
		return err
	}
	jobs := list.([]api.Job)
	v := &jobsView{CanStart: q.human.Role != store.RoleViewer}
	for i := len(jobs) - 1; i >= 0; i-- {
		j := jobs[i]
		n := j.Tasks
		v.Jobs = append(v.Jobs, jobRow{Job: j, Ago: ago(q.now, j.CreatedAt),
			Counts: fmt.Sprintf("%d open, %d claimed, %d submitted, %d done", n.Open, n.Claimed, n.Submitted, n.Done)})
	}
	q.page(status, "jobs", pageData{Title: "Jobs", Error: errMsg, Extra: v})
	return nil
}

func (h *Hub) webJobs(q *webReq) error { return q.jobsPage(200, "") }

type jobFormView struct {
	Profiles  []api.ProfileInfo
	Leads     []api.ProfileInfo // profiles that look like leads, first
	Others    []api.ProfileInfo
	Devices   []string
	Repos     []string // folders earlier jobs used, to pick from
	Form      map[string]string
	AllowConf bool
}

func (q *webReq) jobForm(status int, errMsg string, form map[string]string) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot start jobs.")
	}
	pl, err := profileList(q.c)
	if err != nil {
		return err
	}
	v := &jobFormView{Profiles: pl.([]api.ProfileInfo), Form: form, AllowConf: q.c.h.opt.AllowConfidential}
	for _, p := range v.Profiles {
		if strings.HasPrefix(p.Name, "lead") {
			v.Leads = append(v.Leads, p)
		} else {
			v.Others = append(v.Others, p)
		}
	}
	if rows, err := q.c.tx.Query(`SELECT DISTINCT repo FROM task WHERE kind = 'job' AND repo != '' ORDER BY id DESC LIMIT 8`); err == nil {
		for rows.Next() {
			var r string
			if rows.Scan(&r) == nil {
				v.Repos = append(v.Repos, r)
			}
		}
		rows.Close()
	}
	ds, err := store.ListDevices(q.c.tx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.Joined && !d.Revoked.Valid {
			v.Devices = append(v.Devices, d.Name)
		}
	}
	// Fill in what can be known: the only machine, the lead profile when there is one obvious choice.
	if form["device"] == "" && len(v.Devices) == 1 {
		form["device"] = v.Devices[0]
	}
	if form["lead_profile"] == "" {
		switch {
		case len(v.Leads) > 0:
			form["lead_profile"] = v.Leads[0].Name
			for _, p := range v.Leads {
				if p.Name == "lead" {
					form["lead_profile"] = "lead"
				}
			}
		case len(v.Profiles) == 1:
			form["lead_profile"] = v.Profiles[0].Name
		}
	}
	q.page(status, "jobform", pageData{Title: "New job", Error: errMsg, Extra: v, Narrow: true})
	return nil
}

func (h *Hub) webJobNewForm(q *webReq) error { return q.jobForm(200, "", map[string]string{}) }

func (h *Hub) webJobCreate(q *webReq) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot start jobs.")
	}
	f := q.r.PostForm
	req := api.JobNewReq{Title: strings.TrimSpace(f.Get("title")), Body: strings.TrimSpace(strings.ReplaceAll(f.Get("body"), "\r\n", "\n")),
		Repo: strings.TrimSpace(f.Get("repo")), Verify: strings.TrimSpace(f.Get("verify")), Knowledge: strings.TrimSpace(f.Get("knowledge")), Base: strings.TrimSpace(f.Get("base")), Device: f.Get("device"),
		LeadProfile: f.Get("lead_profile"), Confidential: f.Get("confidential") == "1"}
	res, err := q.c.createJob(req)
	if err != nil {
		var ae *apiError
		status, msg := 500, "Something went wrong."
		if errors.As(err, &ae) {
			status, msg = ae.status, ae.msg
		}
		// The failed statements are abandoned with the transaction; show the form again with what was typed.
		q.c.tx.Rollback()
		tx, terr := q.c.h.db.Begin()
		if terr != nil {
			return terr
		}
		defer tx.Rollback()
		q.c.tx = tx
		q.jobForm(status, msg, f1(f))
		return errHandled
	}
	job := res.(api.Job)
	http.Redirect(q.w, q.r, fmt.Sprintf("/jobs/%d?done=started", job.ID), http.StatusSeeOther)
	return nil
}

// ---- one job ----

type taskView struct {
	api.Task
	Ago     string
	CheckOK bool
	Words   string
}

type jobView2 struct {
	Job      api.Job
	Body     string
	Team     []agentView
	Tasks    []taskView
	Activity []activityView
	Attach   []attachView
	Agents   []string // agents that could take over as lead
	CanAct   bool
	Open     bool
	Ready    bool // every task is done: the job can be closed
}

type attachView struct {
	Name string
	Cmd  string
}

func (q *webReq) jobPage(status int, errMsg string, id int64) error {
	t, err := q.c.job(id)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			q.page(ae.status, "message", pageData{Title: "Not found", Error: ae.msg})
			return errHandled
		}
		return err
	}
	jv, err := q.c.jobView(t)
	if err != nil {
		return err
	}
	v := &jobView2{Job: jv, Body: t.body, CanAct: q.human.Role != store.RoleViewer, Open: t.status == api.StatusOpen}
	v.Ready = v.Open && jv.Tasks.Done > 0 && jv.Tasks.Open+jv.Tasks.Claimed+jv.Tasks.Submitted == 0
	agents, err := q.c.agents(`WHERE a.job_id = ?`, id)
	if err != nil {
		return err
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].role == api.RoleLead && agents[j].role != api.RoleLead })
	for _, a := range agents {
		v.Team = append(v.Team, agentView{a.api(), stateGlyph(a.state), ago(q.now, store.Time(a.stateAt))})
	}
	// Agents that are not in a job can be asked to take over as lead.
	free, err := q.c.agents(`WHERE a.job_id IS NULL AND a.role != 'lead'`)
	if err != nil {
		return err
	}
	for _, a := range free {
		v.Agents = append(v.Agents, a.name)
	}
	tasks, err := q.c.tasks(`WHERE t.job_id = ? AND t.kind = 'task'`, id)
	if err != nil {
		return err
	}
	var targets []string
	for _, tk := range tasks {
		tv := taskView{Task: tk.api(), Ago: ago(q.now, store.Time(tk.updated))}
		if tk.check != nil {
			tv.CheckOK = tk.check.ExitCode == 0 && !tk.check.TimedOut
			tv.Words = checkWords(tk.check)
		}
		if tv.Merge, err = q.c.mergeOf(tk.id); err != nil {
			return err
		}
		if tv.Handoff, err = q.c.latestHandoff(tk.id); err != nil {
			return err
		}
		v.Tasks = append(v.Tasks, tv)
		targets = append(targets, tk.target())
	}
	targets = append(targets, fmt.Sprintf("job:%d", id))
	for _, a := range agents {
		targets = append(targets, "agent:"+a.name)
	}
	if len(targets) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(targets)), ",")
		args := make([]any, len(targets))
		for i, x := range targets {
			args[i] = x
		}
		rows, err := q.c.tx.Query(`SELECT actor, action, target, created_at FROM audit WHERE target IN (`+marks+`) ORDER BY seq DESC LIMIT 60`, args...)
		if err != nil {
			return err
		}
		for rows.Next() && len(v.Activity) < 15 {
			var actor, action, target string
			var at int64
			if err := rows.Scan(&actor, &action, &target, &at); err != nil {
				rows.Close()
				return err
			}
			verb, ok := activityVerbs[action]
			if !ok {
				continue
			}
			v.Activity = append(v.Activity, activityView{ago(q.now, store.Time(at)), strings.TrimSpace(fmt.Sprintf("%s %s %s", who(actor), verb, what(target)))})
		}
		rows.Close()
	}
	// How to look over an agent's shoulder: its terminal is a tmux window the link made.
	sp, err := spawnList(q.c)
	if err != nil {
		return err
	}
	for _, s := range sp.([]api.Spawn) {
		if s.Job != nil && *s.Job == id && s.Status == api.SpawnStarted {
			v.Attach = append(v.Attach, attachView{s.Name, "tmux -L handloom attach -t handloom:" + s.Name})
		}
	}
	q.page(status, "job", pageData{Title: fmt.Sprintf("Job #%d", id), Error: errMsg, Extra: v,
		Notice: map[string]string{"started": "Job started. Its lead is starting; this page updates as work arrives.", "closed": "Job closed.", "resumed": "Job resumed with the new lead."}[q.r.URL.Query().Get("done")]})
	return nil
}

func (h *Hub) webJob(q *webReq) error {
	id, err := q.c.pathID()
	if err != nil {
		q.page(404, "message", pageData{Title: "Not found", Error: "No such job."})
		return errHandled
	}
	return q.jobPage(200, "", id)
}

func (q *webReq) jobAction(redirect string, fn func(id int64) error) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot change jobs.")
	}
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		q.page(404, "message", pageData{Title: "Not found", Error: "No such job."})
		return errHandled
	}
	if err := fn(id); err != nil {
		var ae *apiError
		status, msg := 500, "Something went wrong."
		if errors.As(err, &ae) {
			status, msg = ae.status, ae.msg
		}
		q.c.tx.Rollback()
		tx, terr := q.c.h.db.Begin()
		if terr != nil {
			return terr
		}
		defer tx.Rollback()
		q.c.tx = tx
		q.jobPage(status, msg, id)
		return errHandled
	}
	http.Redirect(q.w, q.r, fmt.Sprintf("/jobs/%d?done=%s", id, redirect), http.StatusSeeOther)
	return nil
}

func (h *Hub) webJobClose(q *webReq) error {
	return q.jobAction("closed", func(id int64) error {
		_, err := q.c.closeJob(id, q.r.PostForm.Get("cancel") == "1")
		return err
	})
}

func (h *Hub) webJobResume(q *webReq) error {
	return q.jobAction("resumed", func(id int64) error {
		_, err := q.c.resumeJob(id, q.r.PostForm.Get("lead"))
		return err
	})
}
