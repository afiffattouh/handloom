package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"handloom/internal/api"
)

type taskMsg struct {
	id  int64
	t   api.Task
	err error
}

type jobMsg struct {
	id    int64
	job   api.Job
	tasks []api.Task
	err   error
}

type inboxItem struct {
	key string
	it  api.DigestItem
}

type inboxState struct {
	list    listState
	open    bool
	item    api.DigestItem
	task    *api.Task
	job     *api.Job
	tasks   []api.Task
	loadErr string
	scroll  int
	asking  api.DigestItem
}

// items is what the inbox lists: what needs you first, then work to review.
func (m *Model) inboxItems() []inboxItem {
	if m.digest == nil {
		return nil
	}
	var out []inboxItem
	for _, group := range [][]api.DigestItem{m.digest.NeedsYou, m.digest.ToReview} {
		for _, it := range group {
			if !m.matches(tabInbox, it.Title, it.Who, it.Detail, kindWord(it.Kind), fmt.Sprint(it.ID)) {
				continue
			}
			out = append(out, inboxItem{key: fmt.Sprintf("%s:%d:%s", it.Kind, it.ID, it.Who), it: it})
		}
	}
	return out
}

func kindWord(kind string) string {
	switch {
	case kind == "escalation":
		return "Question"
	case kind == "blocked":
		return "Blocked"
	case kind == "lead-silent":
		return "Lead silent"
	case kind == "submitted":
		return "To review"
	case kind == "job-done":
		return "Finished job"
	case kind == "merge-conflict":
		return "Merge conflict"
	case kind == "merge-failed":
		return "Merge failed"
	case strings.HasPrefix(kind, "notes-"):
		return "Notes problem"
	}
	return kind
}

func kindIcon(kind string) string {
	switch {
	case kind == "escalation":
		return sBold.Render("●")
	case kind == "submitted":
		return sWarn.Render("▲")
	case kind == "job-done":
		return sSuccess.Render("✓")
	case kind == "blocked", kind == "lead-silent":
		return sWarn.Render("▲")
	}
	return sBad.Render("✗")
}

func (s *inboxState) keys(items []inboxItem) []string {
	ks := make([]string, len(items))
	for i, x := range items {
		ks[i] = x.key
	}
	return ks
}

// sync keeps the cursor on the same item after the data changed.
func (s *inboxState) sync(m *Model) {
	items := m.inboxItems()
	s.list.follow(s.keys(items))
	if s.open {
		for _, x := range items {
			if x.it.Kind == s.item.Kind && x.it.ID == s.item.ID {
				return
			}
		}
		s.open = false
		m.say("That item is no longer waiting for you.", true)
	}
}

func (s *inboxState) selected(items []inboxItem) (api.DigestItem, bool) {
	if s.open {
		return s.item, true
	}
	if s.list.sel < len(items) {
		return items[s.list.sel].it, true
	}
	return api.DigestItem{}, false
}

func (s *inboxState) gotTask(msg taskMsg) {
	if !s.open || s.item.ID != msg.id {
		return
	}
	if msg.err != nil {
		s.loadErr = plainErr(msg.err)
		return
	}
	t := msg.t
	s.task, s.loadErr = &t, ""
}

func (m *Model) gotJob(msg jobMsg) {
	if m.ibx.open && m.ibx.item.ID == msg.id {
		if msg.err != nil {
			m.ibx.loadErr = plainErr(msg.err)
		} else {
			j := msg.job
			m.ibx.job, m.ibx.tasks, m.ibx.loadErr = &j, msg.tasks, ""
		}
	}
	if m.jb.open && m.jb.openID == msg.id {
		if msg.err != nil {
			m.jb.loadErr = plainErr(msg.err)
		} else {
			j := msg.job
			m.jb.job, m.jb.tasks, m.jb.loadErr = &j, msg.tasks, ""
		}
	}
}

func fetchTask(a API, id int64) tea.Cmd {
	return func() tea.Msg {
		t, err := a.Task(id)
		return taskMsg{id: id, t: t, err: err}
	}
}

func fetchJob(a API, id int64) tea.Cmd {
	return func() tea.Msg {
		j, err := a.Job(id)
		if err != nil {
			return jobMsg{id: id, err: err}
		}
		ts, err := a.JobTasks(id)
		return jobMsg{id: id, job: j, tasks: ts, err: err}
	}
}

func (m *Model) inboxHints() []hint {
	items := m.inboxItems()
	it, ok := m.ibx.selected(items)
	var hs []hint
	if ok {
		switch it.Kind {
		case "submitted":
			hs = append(hs, hint{"a", "accept", true}, hint{"x", "send back", false})
		case "escalation":
			hs = append(hs, hint{"e", "answer", true})
		case "job-done":
			hs = append(hs, hint{"c", "close job", true})
		}
	}
	if m.ibx.open {
		return append(hs, hint{"esc", "back", false}, hint{"↑↓", "scroll", false})
	}
	return append(hs, hint{"enter", "open", len(hs) == 0}, hint{"↑↓", "select", false}, hint{"/", "filter", false})
}

func (m *Model) inboxKey(s string) tea.Cmd {
	items := m.inboxItems()
	if m.ibx.open {
		switch s {
		case "esc", "backspace", "left", "h":
			m.ibx.open = false
			return nil
		}
		if d, ok := moveKey(s, 10); ok {
			m.ibx.scroll += d
			if m.ibx.scroll < 0 {
				m.ibx.scroll = 0
			}
			return nil
		}
	} else {
		switch s {
		case "esc":
			if m.filters[tabInbox] != "" {
				m.filters[tabInbox] = ""
				m.resetSel()
			}
			return nil
		case "/":
			m.startFilter()
			return nil
		case "enter", "right", "l":
			if it, ok := m.ibx.selected(items); ok {
				return m.openInbox(it)
			}
			return nil
		}
		if d, ok := moveKey(s, 10); ok {
			m.ibx.list.move(d, len(items))
			m.ibx.list.remember(m.ibx.keys(items))
			return nil
		}
	}
	it, ok := m.ibx.selected(items)
	if !ok {
		return nil
	}
	switch s {
	case "a":
		if it.Kind != "submitted" {
			m.say("Only work waiting for review can be accepted.", false)
			return nil
		}
		m.ask(m.acceptAction(it))
	case "x":
		if it.Kind != "submitted" {
			m.say("Only work waiting for review can be sent back.", false)
			return nil
		}
		m.startInput(inputReason, it)
	case "e":
		if it.Kind != "escalation" {
			m.say("This is not a question. Press enter to see what it needs.", false)
			return nil
		}
		m.startInput(inputAnswer, it)
	case "c":
		if it.Kind != "job-done" {
			m.say("Only a finished job can be closed here.", false)
			return nil
		}
		j := api.Job{ID: it.ID, Title: it.Title}
		if m.ibx.job != nil && m.ibx.job.ID == it.ID {
			j = *m.ibx.job
		}
		m.ask(closeJobAction(j, false))
	}
	return nil
}

func (m *Model) openInbox(it api.DigestItem) tea.Cmd {
	m.ibx.open, m.ibx.item = true, it
	m.ibx.task, m.ibx.job, m.ibx.tasks, m.ibx.loadErr, m.ibx.scroll = nil, nil, nil, "", 0
	switch {
	case it.Kind == "submitted", it.Kind == "blocked", strings.HasPrefix(it.Kind, "merge-"):
		return fetchTask(m.api, it.ID)
	case it.Kind == "job-done", strings.HasPrefix(it.Kind, "notes-"):
		return fetchJob(m.api, it.ID)
	}
	return nil
}

// ---- drawing ----

func (m *Model) inboxView(w, h int) string {
	items := m.inboxItems()
	if m.ibx.open {
		return panel(kindWord(m.ibx.item.Kind)+" "+idLabel(m.ibx.item), m.inboxDetail(w-4, h-3), w, h, true)
	}
	total := 0
	if m.digest != nil {
		total = len(m.digest.NeedsYou) + len(m.digest.ToReview)
	}
	if total == 0 {
		return panel("Needs you", []string{
			"",
			sBold.Render("Nothing needs you right now."),
			sMuted.Render("Questions from your agents and work waiting for review will appear here."),
			sMuted.Render("To get agents working, start a job: press 3, then n."),
		}, w, h, true)
	}
	title := fmt.Sprintf("Needs you %d  ·  Work to review %d", len(m.digest.NeedsYou), len(m.digest.ToReview))
	inner := w - 4
	vis := h - 2 - 2 // borders, title line, header line
	m.ibx.list.clamp(len(items), vis)
	var lines []string
	whoW, whenW, kindW := 16, 8, 15
	titleW := inner - 2 - 2 - kindW - whoW - whenW - 4
	lines = append(lines, sMuted.Render(fit("   "+fit("What", kindW)+" "+fit("Title", titleW)+" "+fit("From", whoW)+" "+fit("Waiting", whenW), inner)))
	if len(items) == 0 {
		lines = append(lines, "", sMuted.Render("No item matches the filter."))
	}
	for i := m.ibx.list.off; i < len(items) && i < m.ibx.list.off+vis; i++ {
		it := items[i].it
		mark := "  "
		title := fit(it.Title, titleW)
		if i == m.ibx.list.sel {
			mark = sAccent.Render("›") + " "
			title = sBold.Render(title)
		}
		lines = append(lines, mark+kindIcon(it.Kind)+" "+fit(kindWord(it.Kind), kindW)+" "+title+" "+sMuted.Render(fit(it.Who, whoW))+" "+sMuted.Render(fit(agoShort(it), whenW)))
	}
	return panel(title, lines, w, h, true)
}

func idLabel(it api.DigestItem) string {
	if it.ID == 0 {
		return ""
	}
	switch it.Kind {
	case "escalation":
		return "#" + itoa(int(it.ID))
	case "job-done":
		return "job #" + itoa(int(it.ID))
	}
	return "task #" + itoa(int(it.ID))
}

func agoShort(it api.DigestItem) string {
	if it.At.IsZero() || it.At.Unix() <= 0 {
		return ""
	}
	return span(time.Since(it.At))
}

func (m *Model) inboxDetail(w, h int) []string {
	it := m.ibx.item
	var L []string
	add := func(s ...string) { L = append(L, s...) }
	para := func(s string) {
		for _, l := range wrap(s, w) {
			L = append(L, l)
		}
	}
	kv := func(k, v string) {
		if v != "" {
			add(sMuted.Render(fit(k, 12)) + cut(v, w-12))
		}
	}
	add("")
	for _, l := range wrap(it.Title, w) {
		add(sBold.Render(l))
	}
	add("")
	switch it.Kind {
	case "escalation":
		kv("From", it.Who)
		kv("Asked", ago(it.At))
		if len(it.Options) > 0 {
			add("", sMuted.Render("Options"))
			for i, o := range it.Options {
				add(fmt.Sprintf("  %d  %s", i+1, cut(o, w-6)))
			}
		}
		add("", sMuted.Render("Press ")+sAccent.Render("e")+sMuted.Render(" to answer. Your answer goes to the agent as a message."))
	case "submitted":
		kv("Done by", it.Who)
		kv("Submitted", ago(it.At))
		m.taskDetail(&L, w)
		add("", sMuted.Render("Press ")+sAccent.Render("a")+sMuted.Render(" to accept it, or ")+sBold.Render("x")+sMuted.Render(" to send it back with a reason."))
	case "job-done":
		kv("Finished", ago(it.At))
		para(it.Detail)
		m.jobTasksDetail(&L, m.ibx.job, m.ibx.tasks, m.ibx.loadErr, w)
		add("", sMuted.Render("Press ")+sAccent.Render("c")+sMuted.Render(" to close the job."))
	default:
		kv("About", it.Who)
		kv("Since", ago(it.At))
		para(it.Detail)
		if it.Kind == "blocked" || strings.HasPrefix(it.Kind, "merge-") {
			m.taskDetail(&L, w)
		}
		if it.Kind == "lead-silent" || strings.HasPrefix(it.Kind, "notes-") || strings.HasPrefix(it.Kind, "merge-") {
			add("", sMuted.Render("This needs a command; the console does not do it yet. Use handloom in a terminal."))
		}
	}
	if m.ibx.loadErr != "" {
		add("", sWarn.Render("▲ ")+cut(m.ibx.loadErr, w-2))
	}
	if m.ibx.scroll >= len(L) {
		m.ibx.scroll = len(L) - 1
	}
	if m.ibx.scroll < 0 {
		m.ibx.scroll = 0
	}
	return L[m.ibx.scroll:]
}

// taskDetail adds what the task holds: what was asked, the evidence, the
// device's check and what became of its branch.
func (m *Model) taskDetail(L *[]string, w int) {
	t := m.ibx.task
	if t == nil {
		if m.ibx.loadErr == "" {
			*L = append(*L, "", sMuted.Render("Loading the task…"))
		}
		return
	}
	add := func(s ...string) { *L = append(*L, s...) }
	if t.Body != "" {
		add("", sMuted.Render("What was asked"))
		for _, l := range wrap(t.Body, w-2) {
			add("  " + l)
		}
	}
	if t.RejectReason != "" {
		add("", sMuted.Render("Sent back before")+" "+cut(t.RejectReason, w-17))
	}
	if len(t.Evidence) > 0 {
		add("", sMuted.Render("Evidence"))
		for _, e := range t.Evidence {
			for i, l := range wrap(e, w-4) {
				p := "  - "
				if i > 0 {
					p = "    "
				}
				add(p + l)
			}
		}
	}
	if t.Note != "" {
		add("", sMuted.Render("Note from the agent"))
		for _, l := range wrap(t.Note, w-2) {
			add("  " + l)
		}
	}
	if ck := t.Check; ck != nil {
		icon := sSuccess.Render("✓")
		if ck.TimedOut || ck.ExitCode != 0 {
			icon = sBad.Render("✗")
		}
		add("", sMuted.Render("Check, run by the machine (not by the agent)"))
		add("  " + icon + " " + cut(ck.Command+" — "+checkWords(t), w-6))
		if tail := strings.TrimSpace(ck.Tail); tail != "" {
			ls := strings.Split(tail, "\n")
			if len(ls) > 8 {
				ls = ls[len(ls)-8:]
			}
			for _, l := range ls {
				add("    " + sMuted.Render(cut(l, w-6)))
			}
		}
	} else if t.Status == api.StatusSubmitted {
		add("", sMuted.Render("Check: none has run for this work."))
	}
	if mg := t.Merge; mg != nil {
		add("", sMuted.Render("Merge"))
		switch mg.Status {
		case "merged":
			add("  " + sSuccess.Render("✓") + " Merged into the job's integration branch")
		case "pending":
			add("  " + sMuted.Render("○") + " Waiting for the machine to merge its branch")
		default:
			add("  " + sBad.Render("✗") + " Not merged (" + mg.Status + ")")
			for _, l := range wrap(mg.Detail, w-4) {
				add("    " + l)
			}
		}
	}
}
