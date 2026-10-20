package hub

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
	Projects  []projectOption // projects with a ready copy: one entry per machine that has it
	NotReady  []string        // projects no machine has a copy of yet
	Quick     bool            // the one-sentence ask can be offered: there is a project to ask about
}

type projectOption struct{ Value, Label string }

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
	ds, err := store.ListDevices(q.c.tx, q.c.deviceScope())
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.Joined && !d.Revoked.Valid {
			v.Devices = append(v.Devices, d.Name)
		}
	}
	// Projects: only a machine that has its copy ready can start a job from it.
	cbs, err := q.c.codebases()
	if err != nil {
		return err
	}
	for _, cb := range cbs {
		ready := 0
		for _, k := range cb.Clones {
			if k.Status == "ready" {
				v.Projects = append(v.Projects, projectOption{Value: cb.Name + "|" + k.Device, Label: cb.Name + " on " + k.Device})
				ready++
			}
		}
		if ready == 0 {
			v.NotReady = append(v.NotReady, cb.Name)
		}
	}
	v.Quick = len(v.Projects) > 0
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

// webJobAsk is the one-sentence ask: a sentence, a project and nothing else. The machine is the one that has
// the project, the check is the project's own, the lead is the usual one, and the lead is told to keep the
// plan small. "Get steps first" makes the lead show its plan and wait before any worker starts.
func (h *Hub) webJobAsk(q *webReq) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot start jobs.")
	}
	f := q.r.PostForm
	ask := strings.TrimSpace(strings.ReplaceAll(f.Get("ask"), "\r\n", "\n"))
	if ask == "" || f.Get("project") == "" {
		return q.jobForm(400, "Say what you want done, and pick the project.", map[string]string{"ask": ask})
	}
	title := ask
	if i := strings.IndexAny(title, ".\n"); i > 8 {
		title = title[:i]
	}
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80]) + "..."
	}
	if f.Get("team") != "1" {
		return h.webJobSolo(q, title, ask)
	}
	how := "How to run this: it is a small ask in the owner's own words. Keep the plan small: as few tasks as will do, one worker if one is enough."
	if f.Get("steps_first") == "1" {
		how += " Before you start any worker, send your plan (the tasks, in plain words) to the owner with `handloom ask` and wait for the answer."
	}
	lead := f.Get("lead_profile")
	if lead == "" {
		pl, err := profileList(q.c)
		if err != nil {
			return err
		}
		for _, p := range pl.([]api.ProfileInfo) {
			if p.Name == "lead" || (lead == "" && strings.HasPrefix(p.Name, "lead")) {
				lead = p.Name
			}
		}
	}
	f.Set("title", title)
	f.Set("body", ask+"\n\n---\n"+how)
	f.Set("lead_profile", lead)
	f.Set("verify", "") // the project's own check
	f.Set("repo", "")
	return h.webJobCreate(q)
}

// webJobSolo starts a job with no lead: one task from the owner's sentence and one helper, started from a
// profile for doing work. The owner accepts or sends back the result on the job page.
func (h *Hub) webJobSolo(q *webReq, title, ask string) error {
	f := q.r.PostForm
	worker := f.Get("worker_profile")
	if worker == "" {
		pl, err := profileList(q.c)
		if err != nil {
			return err
		}
		for _, p := range pl.([]api.ProfileInfo) {
			if p.Name == "coder" || (worker == "" && !strings.HasPrefix(p.Name, "lead")) {
				worker = p.Name
			}
		}
	}
	if worker == "" {
		return q.jobForm(400, "There is no profile to start a helper from yet. Make one on the Profiles page, or ask for a lead to plan it.", map[string]string{"ask": ask})
	}
	name, dev, _ := strings.Cut(f.Get("project"), "|")
	res, err := q.c.createJob(api.JobNewReq{Title: title, Body: ask, Codebase: name, Device: dev, WorkerProfile: worker})
	if err != nil {
		return q.jobFormFailed(err, f)
	}
	http.Redirect(q.w, q.r, fmt.Sprintf("/jobs/%d?done=started", res.(api.Job).ID), http.StatusSeeOther)
	return nil
}

func (h *Hub) webJobNewForm(q *webReq) error { return q.jobForm(200, "", map[string]string{}) }

var kitSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,60}$`)

func (h *Hub) webJobCreate(q *webReq) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot start jobs.")
	}
	f := q.r.PostForm
	req := api.JobNewReq{Title: strings.TrimSpace(f.Get("title")), Body: strings.TrimSpace(strings.ReplaceAll(f.Get("body"), "\r\n", "\n")),
		Repo: strings.TrimSpace(f.Get("repo")), Verify: strings.TrimSpace(f.Get("verify")), Knowledge: strings.TrimSpace(f.Get("knowledge")), Base: strings.TrimSpace(f.Get("base")), Device: f.Get("device"),
		LeadProfile: f.Get("lead_profile"), Confidential: f.Get("confidential") == "1"}
	if slug := strings.TrimSpace(f.Get("kit_task")); slug != "" { // a task sealed with the kit: its commit is the base, the kit is the check
		if !kitSlug.MatchString(slug) {
			return q.jobForm(400, "A sealed kit task is named with lower-case letters, digits and dashes, such as parser-fix.", f1(f))
		}
		if req.Base == "" {
			req.Base = "task/" + slug + "-base"
		}
		if req.Verify == "" {
			req.Verify = "./check"
		}
	}
	if pr := f.Get("project"); pr != "" { // "name|machine": a project chosen, the folder field is ignored
		name, dev, _ := strings.Cut(pr, "|")
		req.Codebase, req.Device, req.Repo = name, dev, ""
	}
	res, err := q.c.createJob(req)
	if err != nil {
		return q.jobFormFailed(err, f)
	}
	job := res.(api.Job)
	http.Redirect(q.w, q.r, fmt.Sprintf("/jobs/%d?done=started", job.ID), http.StatusSeeOther)
	return nil
}

// jobFormFailed shows the form again with the refusal in words and what was typed. The failed statements
// are abandoned with the transaction.
func (q *webReq) jobFormFailed(err error, f map[string][]string) error {
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
	q.jobForm(status, msg, f1(f))
	return errHandled
}

// ---- one job ----

type taskView struct {
	api.Task
	Ago      string
	CheckOK  bool
	Words    string
	Result   *api.TaskResult
	Lines    []diffLine // the result's diff, line by line
	IsPage   map[string]bool
	CanWatch bool   // the owner's terminal can be read from here
	Live     string // what the owner is doing, in a word
	// AcceptedBy says who accepted the task, from the audit log: a person's name, or a helper's.
	AcceptedBy string
}

// acceptorWords turns an audit actor ("human:afif", "agent:lead-9") into words.
func acceptorWords(actor string) string {
	switch {
	case strings.HasPrefix(actor, "agent:"):
		return "the lead " + strings.TrimPrefix(actor, "agent:")
	case strings.HasPrefix(actor, "human:"):
		return strings.TrimPrefix(actor, "human:")
	case actor == "":
		return ""
	}
	return actor
}

// diffLine is one line of a diff and how to colour it.
type diffLine struct{ Class, Text string }

func diffLines(diff string) []diffLine {
	var out []diffLine
	for i, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		if i >= 2500 {
			out = append(out, diffLine{"meta", "... (more lines are not shown)"})
			break
		}
		c := ""
		switch {
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") || strings.HasPrefix(l, "diff --git") || strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "new file"):
			c = "meta"
		case strings.HasPrefix(l, "@@"):
			c = "hunk"
		case strings.HasPrefix(l, "+"):
			c = "add"
		case strings.HasPrefix(l, "-"):
			c = "del"
		}
		out = append(out, diffLine{c, l})
	}
	return out
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
	Ready    bool     // every task is done: the job can be closed
	Profiles []string // for giving a task to a new helper

	Overlap    string        // other open jobs on the same repository: a warning
	Publishes  []publishView // newest first
	CanPublish bool          // a person who may push, on a repo job with merged work
	NoMerged   bool          // a repo job with nothing merged yet: say why there is no button
	RepoJob    bool
	Hosted     string // the hosted project (D106) this job works on, or ""
	CanMerge   bool   // a person who may merge, a hosted project, a finished publish not merged yet
	MergedInto string // the branch the job's work was merged into
	Confid     bool
}

type publishView struct {
	api.Publish
	Ago    string
	LinkOK bool // the pull request link is shown as a link only if it is an https address
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
	if pl, err := profileList(q.c); err == nil {
		for _, p := range pl.([]api.ProfileInfo) {
			v.Profiles = append(v.Profiles, p.Name)
		}
	}
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
	merged := false
	for _, tk := range tasks {
		tv := taskView{Task: tk.api(), Ago: ago(q.now, store.Time(tk.updated))}
		if tk.check != nil {
			tv.CheckOK = tk.check.ExitCode == 0 && !tk.check.TimedOut
			tv.Words = checkWords(tk.check)
		}
		if res, _, err := q.c.result(tk.id); err != nil {
			return err
		} else if res != nil {
			tv.Result, tv.Lines, tv.IsPage = res, diffLines(res.Diff), map[string]bool{}
			for _, p := range res.Pages {
				tv.IsPage[p] = true
			}
		}
		if tk.status == api.StatusDone {
			var actor string
			if err := q.c.tx.QueryRow(`SELECT actor FROM audit WHERE action = 'task.accept' AND target = ? ORDER BY seq DESC LIMIT 1`, tk.target()).Scan(&actor); err == nil {
				tv.AcceptedBy = acceptorWords(actor)
			}
		}
		if tk.owner.Valid && (tk.status == api.StatusClaimed || tk.status == api.StatusSubmitted) {
			if ow, err := q.c.agentByID(tk.owner.Int64); err == nil && ow != nil {
				tv.Live = ow.state
				if ok, _ := q.watchAllowed(ow); ok && ow.wakeTarget != "" {
					tv.CanWatch = true
				}
			}
		}
		if tv.Merge, err = q.c.mergeOf(tk.id); err != nil {
			return err
		}
		if tv.Handoff, err = q.c.latestHandoff(tk.id); err != nil {
			return err
		}
		if tv.Merge != nil && tv.Merge.Status == "merged" {
			merged = true
		}
		v.Tasks = append(v.Tasks, tv)
		targets = append(targets, tk.target())
	}
	v.Overlap = api.OverlapText(jv.Overlaps)
	if t.repo != "" && t.deviceID.Valid {
		v.RepoJob = true
		pubs, err := q.c.publishes(id)
		if err != nil {
			return err
		}
		for _, p := range pubs {
			v.Publishes = append(v.Publishes, publishView{Publish: p, Ago: ago(q.now, p.At), LinkOK: strings.HasPrefix(p.URL, "https://")})
		}
		v.CanPublish = q.c.allow(ActPublish) == nil && merged && t.status != api.StatusCancelled
		v.NoMerged = !merged
		if name, hosted := q.c.hostedFor(id); hosted {
			v.Hosted, v.Confid = name, t.confidential
			v.CanPublish = v.CanPublish && !t.confidential
			for _, p := range pubs {
				if p.MergedInto != "" {
					v.MergedInto = p.MergedInto
				}
			}
			if len(pubs) > 0 {
				for _, p := range pubs {
					if p.Status == "done" {
						v.CanMerge = p.MergedInto == "" && !t.confidential && q.c.allow(ActProjectMerge) == nil
						break
					}
				}
			}
		}
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
		rows, err := q.c.tx.Query(`SELECT actor, action, target, payload, created_at FROM audit WHERE target IN (`+marks+`) ORDER BY seq DESC LIMIT 60`, args...)
		if err != nil {
			return err
		}
		for rows.Next() && len(v.Activity) < 15 {
			var actor, action, target, payload string
			var at int64
			if err := rows.Scan(&actor, &action, &target, &payload, &at); err != nil {
				rows.Close()
				return err
			}
			if text, ok := activityText(actor, action, target, payload); ok && strings.HasPrefix(action, "publish.pr_") {
				v.Activity = append(v.Activity, activityView{ago(q.now, store.Time(at)), text})
				continue
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
	notice := map[string]string{"started": "Job started. Its lead is starting; this page updates as work arrives.", "closed": "Job closed.", "resumed": "Job resumed with the new lead.", "merged": "Merged. The work is now in the project's main branch; other computers pick it up when their next job starts.", "accepted": "Result used. It is queued to be merged into the job's branch.", "sentback": "Sent back with your note. Its helper has it again.", "taskstopped": "Task stopped.", "reassigned": "Given to a new helper. It appears under Team within a minute and picks the task up when it starts.", "published": "Publishing asked for: the computer that holds the repository is pushing the branch. The result appears under Publish."}[q.r.URL.Query().Get("done")]
	if q.r.URL.Query().Get("done") == "started" && v.Overlap != "" {
		notice += " " + v.Overlap
	}
	q.page(status, "job", pageData{Title: fmt.Sprintf("Job #%d", id), Error: errMsg, Extra: v, Notice: notice})
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
		if keepsWrites(err) {
			q.c.tx.Commit() // the audit row about a refused merge stays
		} else {
			q.c.tx.Rollback()
		}
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

func (h *Hub) webJobMerge(q *webReq) error {
	return q.jobAction("merged", func(id int64) error {
		if err := q.c.allow(ActProjectMerge); err != nil {
			return err
		}
		name, hosted := q.c.hostedFor(id)
		if !hosted {
			return conflict("job %d does not belong to a project that Handloom holds, so there is nothing for Handloom to merge", id)
		}
		_, err := q.c.mergeHosted(name, api.CodebaseMergeReq{Job: id})
		return err
	})
}

func (h *Hub) webJobPublish(q *webReq) error {
	return q.jobAction("published", func(id int64) error {
		if err := q.c.allow(ActPublish); err != nil {
			return err
		}
		_, err := q.c.publishJob(id, api.PublishReq{PR: q.r.PostForm.Get("pr") == "1"})
		return err
	})
}

// taskOnJob runs a person's action on one task of a job shown on the job page, and comes back to that page.
func (q *webReq) taskOnJob(done string, fn func(t *taskRow) error) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot change jobs.")
	}
	jid, err := strconv.ParseInt(q.r.PathValue("job"), 10, 64)
	if err != nil {
		q.page(404, "message", pageData{Title: "Not found", Error: "No such job."})
		return errHandled
	}
	t, err := q.c.pathTask()
	if err == nil && (!t.jobID.Valid || t.jobID.Int64 != jid) {
		err = notFound("no such task in this job")
	}
	if err == nil {
		err = fn(t)
	}
	if err != nil {
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
		q.jobPage(status, msg, jid)
		return errHandled
	}
	http.Redirect(q.w, q.r, fmt.Sprintf("/jobs/%d?done=%s#task-%d", jid, done, t.id), http.StatusSeeOther)
	return nil
}

func (h *Hub) webJobTaskAccept(q *webReq) error {
	return q.taskOnJob("accepted", func(t *taskRow) error { _, err := taskAccept(q.c); return err })
}

func (h *Hub) webJobTaskReject(q *webReq) error {
	return q.taskOnJob("sentback", func(t *taskRow) error {
		if err := q.c.allow(ActTaskManage); err != nil {
			return err
		}
		_, err := q.c.rejectTask(t, q.r.PostForm.Get("reason"))
		return err
	})
}

func (h *Hub) webJobTaskReassign(q *webReq) error {
	return q.taskOnJob("reassigned", func(t *taskRow) error {
		if err := q.c.allow(ActTaskManage); err != nil {
			return err
		}
		_, err := q.c.reassignTask(t, api.ReassignReq{Profile: q.r.PostForm.Get("profile"), Note: q.r.PostForm.Get("note")})
		return err
	})
}

func (h *Hub) webJobTaskCancel(q *webReq) error {
	return q.taskOnJob("taskstopped", func(t *taskRow) error { _, err := taskCancel(q.c); return err })
}
