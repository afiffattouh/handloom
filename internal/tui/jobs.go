package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"handloom/internal/api"
)

type jobsState struct {
	list    listState
	open    bool
	openID  int64
	job     *api.Job
	tasks   []api.Task
	loadErr string
	scroll  int
}

func (m *Model) jobItems() []api.Job {
	var out []api.Job
	for _, j := range m.jobs {
		if m.matches(tabJobs, j.Title, j.Lead, j.Status, j.Device, j.Repo, fmt.Sprint(j.ID)) {
			out = append(out, j)
		}
	}
	return out
}

func jobKeys(js []api.Job) []string {
	ks := make([]string, len(js))
	for i, j := range js {
		ks[i] = itoa(int(j.ID))
	}
	return ks
}

func (s *jobsState) sync(m *Model) {
	js := m.jobItems()
	s.list.follow(jobKeys(js))
	if s.open {
		for _, j := range m.jobs {
			if j.ID == s.openID {
				jj := j
				s.job = &jj
			}
		}
	}
}

func jobMark(status string) string {
	switch status {
	case "open":
		return sSuccess.Render("●") + " open"
	case "done":
		return sSuccess.Render("✓") + " done"
	case "cancelled":
		return sMuted.Render("✗") + sMuted.Render(" cancelled")
	}
	return status
}

func (m *Model) jobsHints() []hint {
	if m.jb.open {
		return []hint{{"c", "close job", false}, {"C", "cancel job", false}, {"esc", "back", false}, {"↑↓", "scroll", false}}
	}
	return []hint{{"n", "new job", true}, {"enter", "open", false}, {"c", "close job", false}, {"↑↓", "select", false}, {"/", "filter", false}}
}

func (m *Model) selectedJob() (api.Job, bool) {
	if m.jb.open && m.jb.job != nil {
		return *m.jb.job, true
	}
	js := m.jobItems()
	if m.jb.list.sel < len(js) {
		return js[m.jb.list.sel], true
	}
	return api.Job{}, false
}

func (m *Model) jobsKey(s string) tea.Cmd {
	js := m.jobItems()
	if m.jb.open {
		switch s {
		case "esc", "backspace", "left", "h":
			m.jb.open = false
			return nil
		}
		if d, ok := moveKey(s, 10); ok {
			m.jb.scroll += d
			if m.jb.scroll < 0 {
				m.jb.scroll = 0
			}
			return nil
		}
	} else {
		switch s {
		case "esc":
			if m.filters[tabJobs] != "" {
				m.filters[tabJobs] = ""
				m.resetSel()
			}
			return nil
		case "/":
			m.startFilter()
			return nil
		case "enter", "right", "l":
			if j, ok := m.selectedJob(); ok {
				m.jb.open, m.jb.openID, m.jb.scroll = true, j.ID, 0
				jj := j
				m.jb.job, m.jb.tasks, m.jb.loadErr = &jj, nil, ""
				return fetchJob(m.api, j.ID)
			}
			return nil
		}
		if d, ok := moveKey(s, 10); ok {
			m.jb.list.move(d, len(js))
			m.jb.list.remember(jobKeys(js))
			return nil
		}
	}
	switch s {
	case "n":
		return m.openForm()
	case "c", "C":
		j, ok := m.selectedJob()
		if !ok {
			return nil
		}
		if j.Status != "open" {
			m.say(fmt.Sprintf("Job #%d is already %s.", j.ID, j.Status), false)
			return nil
		}
		m.ask(closeJobAction(j, s == "C"))
	}
	return nil
}

func (m *Model) jobsView(w, h int) string {
	if m.jb.open && m.jb.job != nil {
		return panel(fmt.Sprintf("Job #%d", m.jb.job.ID), m.jobDetail(w-4), w, h, true)
	}
	js := m.jobItems()
	if len(m.jobs) == 0 {
		return panel("Jobs", []string{
			"",
			sBold.Render("No jobs yet."),
			sMuted.Render("A job is a piece of work with its own lead agent, who plans tasks and starts workers."),
			sMuted.Render("Press ") + sAccent.Render("n") + sMuted.Render(" to start the first one."),
		}, w, h, true)
	}
	inner := w - 4
	vis := h - 4
	m.jb.list.clamp(len(js), vis)
	idW, stW, leadW, nW := 5, 11, 16, 7
	titleW := inner - 2 - idW - stW - leadW - 4*nW - 7
	hdr := "  " + fit("#", idW) + " " + fit("State", stW) + " " + fit("Title", titleW) + " " + fit("Lead", leadW) +
		" " + fit("Open", nW-1) + " " + fit("Claimed", nW) + " " + fit("Review", nW-1) + " " + fit("Done", nW-2)
	lines := []string{sMuted.Render(fit(hdr, inner))}
	if len(js) == 0 {
		lines = append(lines, "", sMuted.Render("No job matches the filter."))
	}
	for i := m.jb.list.off; i < len(js) && i < m.jb.list.off+vis; i++ {
		j := js[i]
		mark, title := "  ", fit(j.Title, titleW)
		if i == m.jb.list.sel {
			mark, title = sAccent.Render("›")+" ", sBold.Render(title)
		}
		lead := j.Lead
		if lead == "" {
			lead = "no lead yet"
		}
		t := j.Tasks
		lines = append(lines, mark+sMuted.Render(fit("#"+itoa(int(j.ID)), idW))+" "+fit(jobMark(j.Status), stW)+" "+title+" "+sMuted.Render(fit(lead, leadW))+
			" "+fit(itoa(t.Open), nW-1)+" "+fit(itoa(t.Claimed), nW)+" "+fit(itoa(t.Submitted), nW-1)+" "+fit(itoa(t.Done), nW-2))
	}
	return panel(fmt.Sprintf("Jobs %d", len(m.jobs)), lines, w, h, true)
}

func (m *Model) jobDetail(w int) []string {
	j := m.jb.job
	var L []string
	add := func(s ...string) { L = append(L, s...) }
	kv := func(k, v string) {
		if v != "" {
			add(sMuted.Render(fit(k, 12)) + cut(v, w-12))
		}
	}
	add("")
	for _, l := range wrap(j.Title, w) {
		add(sBold.Render(l))
	}
	add("")
	add(sMuted.Render(fit("State", 12)) + jobMark(j.Status))
	lead := j.Lead
	if lead == "" {
		lead = "no lead yet"
	}
	kv("Lead", lead)
	kv("Machine", j.Device)
	kv("Repository", j.Repo)
	if j.Repo != "" {
		kv("Branch", fmt.Sprintf("job/%d/integration", j.ID))
	}
	kv("Check", j.Verify)
	kv("Started", ago(j.CreatedAt)+" by "+strings.TrimPrefix(j.CreatedBy, "human:"))
	if j.Confidential {
		kv("Handling", "confidential")
	}
	if j.Body != "" {
		add("", sMuted.Render("Brief"))
		for _, l := range wrap(j.Body, w-2) {
			add("  " + l)
		}
	}
	m.jobTasksDetail(&L, j, m.jb.tasks, m.jb.loadErr, w)
	if m.jb.scroll >= len(L) {
		m.jb.scroll = len(L) - 1
	}
	if m.jb.scroll < 0 {
		m.jb.scroll = 0
	}
	return L[m.jb.scroll:]
}

// jobTasksDetail lists a job's tasks with owner, check and merge state.
func (m *Model) jobTasksDetail(L *[]string, j *api.Job, tasks []api.Task, loadErr string, w int) {
	add := func(s ...string) { *L = append(*L, s...) }
	if loadErr != "" {
		add("", sWarn.Render("▲ ")+cut(loadErr, w-2))
		return
	}
	if j == nil {
		add("", sMuted.Render("Loading…"))
		return
	}
	var real []api.Task
	for _, t := range tasks {
		if t.Kind != "job" {
			real = append(real, t)
		}
	}
	add("", sMuted.Render(fmt.Sprintf("Tasks  %d open, %d claimed, %d to review, %d done", j.Tasks.Open, j.Tasks.Claimed, j.Tasks.Submitted, j.Tasks.Done)))
	if len(real) == 0 {
		add(sMuted.Render("  No tasks yet. The lead adds them as it plans."))
		return
	}
	stW, ownW, ckW, mgW := 14, 14, 8, 10
	titleW := w - 2 - 5 - stW - ownW - ckW - mgW - 5
	if titleW < 10 {
		titleW = 10
	}
	add(sMuted.Render("  " + fit("#", 5) + " " + fit("Status", stW) + " " + fit("Title", titleW) + " " + fit("Owner", ownW) + " " + fit("Check", ckW) + " " + fit("Merge", mgW)))
	for _, t := range real {
		ck, mg := "", ""
		if t.Check != nil {
			ck = "✓ passed"
			if t.Check.TimedOut {
				ck = "✗ timeout"
			} else if t.Check.ExitCode != 0 {
				ck = "✗ failed"
			}
		}
		if t.Merge != nil {
			switch t.Merge.Status {
			case "merged":
				mg = "✓ merged"
			case "pending":
				mg = "○ waiting"
			default:
				mg = "✗ " + t.Merge.Status
			}
		}
		owner := t.Owner
		if owner == "" {
			owner = t.AssignedTo
		}
		add("  " + sMuted.Render(fit("#"+itoa(int(t.ID)), 5)) + " " + fit(statusMark(t.Status), stW) + " " + fit(t.Title, titleW) + " " +
			sMuted.Render(fit(owner, ownW)) + " " + colorCheck(fit(ck, ckW)) + " " + colorCheck(fit(mg, mgW)))
		if t.BlockedReason != "" {
			add("        " + sWarn.Render("▲ blocked: ") + cut(t.BlockedReason, w-20))
		}
	}
}

func colorCheck(s string) string {
	switch {
	case strings.HasPrefix(s, "✓"):
		return sSuccess.Render(s)
	case strings.HasPrefix(s, "✗"):
		return sBad.Render(s)
	}
	return sMuted.Render(s)
}
