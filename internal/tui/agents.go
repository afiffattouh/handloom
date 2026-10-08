package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"handloom/internal/api"
)

type agentsState struct {
	list   listState
	term   *termView
	gen    int
	follow bool
}

// termView is the open terminal of one agent: the end of its screen, read by
// its machine and held by the hub for a minute.
type termView struct {
	agent   string
	gen     int
	text    string
	age     int
	got     bool
	pending bool
	err     string
	stopped bool // the hub refused, so there is no point asking again
	offline bool
	off     int
}

type screenMsg struct {
	agent string
	gen   int
	s     api.Screen
	err   error
}

type screenTickMsg struct {
	agent string
	gen   int
}

func (m *Model) agentRows() []api.Agent {
	if m.digest == nil {
		return nil
	}
	var out []api.Agent
	for _, a := range m.digest.Agents {
		if m.matches(tabAgents, a.Name, a.Role, a.Kind, a.Device, a.State, m.agentTask(a)) {
			out = append(out, a)
		}
	}
	return out
}

func agentKeys(as []api.Agent) []string {
	ks := make([]string, len(as))
	for i, a := range as {
		ks[i] = a.Name
	}
	return ks
}

func (s *agentsState) sync(m *Model) { s.list.follow(agentKeys(m.agentRows())) }

// agentTask is what the agent is working on, in words: from the fleet table
// when the hub has one, else from the running tasks in the digest.
func (m *Model) agentTask(a api.Agent) string {
	if m.metrics != nil {
		for _, f := range m.metrics.Fleet {
			if f.Name == a.Name && f.Task != "" {
				return f.Task
			}
		}
	}
	if m.digest != nil {
		for _, it := range m.digest.Running {
			if it.Who == a.Name {
				return fmt.Sprintf("#%d %s", it.ID, it.Title)
			}
		}
	}
	if a.Job != nil {
		return fmt.Sprintf("job #%d", *a.Job)
	}
	return ""
}

func (m *Model) agentsHints() []hint {
	if t := m.ag.term; t != nil {
		f := "follow: off"
		if m.ag.follow {
			f = "follow: on"
		}
		return []hint{{"f", f, true}, {"esc", "back", false}, {"↑↓", "scroll", false}}
	}
	return []hint{{"enter", "watch terminal", true}, {"↑↓", "select", false}, {"/", "filter", false}}
}

func (m *Model) agentsKey(s string) tea.Cmd {
	if t := m.ag.term; t != nil {
		switch s {
		case "esc", "backspace", "left", "h":
			m.ag.term = nil
			return nil
		case "f":
			m.ag.follow = !m.ag.follow
			if !m.ag.follow {
				t.off = m.termMaxOff()
			}
			return nil
		case "end", "G":
			m.ag.follow = true
			return nil
		}
		if d, ok := moveKey(s, 10); ok {
			if m.ag.follow {
				t.off = m.termMaxOff()
			}
			m.ag.follow = false
			t.off += d
			if mx := m.termMaxOff(); t.off > mx {
				t.off = mx
			}
			if t.off < 0 {
				t.off = 0
			}
		}
		return nil
	}
	rows := m.agentRows()
	switch s {
	case "esc":
		if m.filters[tabAgents] != "" {
			m.filters[tabAgents] = ""
			m.resetSel()
		}
	case "/":
		m.startFilter()
	case "enter", "right", "l":
		if m.ag.list.sel < len(rows) {
			name := rows[m.ag.list.sel].Name
			m.ag.gen++
			m.ag.term = &termView{agent: name, gen: m.ag.gen, pending: true}
			return fetchScreen(m.api, name, m.ag.gen)
		}
	default:
		if d, ok := moveKey(s, 10); ok {
			m.ag.list.move(d, len(rows))
			m.ag.list.remember(agentKeys(rows))
		}
	}
	return nil
}

func fetchScreen(a API, agent string, gen int) tea.Cmd {
	return func() tea.Msg {
		s, err := a.Screen(agent)
		return screenMsg{agent: agent, gen: gen, s: s, err: err}
	}
}

func (m *Model) gotScreen(msg screenMsg) tea.Cmd {
	t := m.ag.term
	if t == nil || t.gen != msg.gen || m.tab != tabAgents {
		return nil
	}
	t.offline = false
	switch {
	case msg.err != nil && isAuth(msg.err):
		m.authErr = true
		t.stopped = true
		return nil
	case msg.err != nil && !isOffline(msg.err):
		// The hub answered and said no: a viewer, a confidential job, no such agent.
		t.err, t.stopped = plainErr(msg.err), true
		return nil
	case msg.err != nil:
		t.offline = true
	case msg.s.Pending:
		t.pending, t.err = !t.got, ""
	default:
		t.text, t.age, t.got, t.pending, t.err = cleanScreen(msg.s.Text), msg.s.AgeSeconds, true, false, ""
	}
	return screenAfter(t)
}

func screenAfter(t *termView) tea.Cmd {
	agent, gen := t.agent, t.gen
	return tea.Tick(screenEvery, func(time.Time) tea.Msg { return screenTickMsg{agent: agent, gen: gen} })
}

func (m *Model) screenTick(msg screenTickMsg) tea.Cmd {
	t := m.ag.term
	if t == nil || t.gen != msg.gen || t.stopped || m.tab != tabAgents {
		return nil
	}
	return fetchScreen(m.api, t.agent, t.gen)
}

// cleanScreen removes escape sequences and control characters: the screen
// is whatever the agent printed and must not move our cursor.
func cleanScreen(s string) string {
	s = ansi.Strip(strings.ReplaceAll(s, "\t", "    "))
	return strings.Map(func(r rune) rune {
		if r == '\n' || r >= 32 && r != 127 {
			return r
		}
		return -1
	}, strings.ReplaceAll(s, "\r", ""))
}

func (m *Model) termLines() []string {
	t := m.ag.term
	if !t.got {
		return nil
	}
	return strings.Split(t.text, "\n")
}

func (m *Model) termVisible() int { return m.h - 4 - 6 }

func (m *Model) termMaxOff() int {
	n := len(m.termLines()) - m.termVisible()
	if n < 0 {
		return 0
	}
	return n
}

// ---- drawing ----

func (m *Model) agentsView(w, h int) string {
	if t := m.ag.term; t != nil {
		return m.termPanel(w, h)
	}
	if m.digest == nil || len(m.digest.Agents) == 0 {
		return panel("Agents", []string{
			"",
			sBold.Render("No agents yet."),
			sMuted.Render("Agents appear here when they register on a machine, or when a job starts a lead."),
			sMuted.Render("Start a job on the Jobs screen (press 3, then n) and its lead will show up here."),
		}, w, h, true)
	}
	lw, lh, rw, rh := panes(w, h)
	rows := m.agentRows()
	inner := lw - 4
	vis := lh - 4
	m.ag.list.clamp(len(rows), vis)
	nameW, roleW, kindW, devW, stW := 16, 8, 8, 12, 11
	showKind := inner >= 72
	taskW := inner - 2 - nameW - roleW - devW - stW - 5
	if showKind {
		taskW -= kindW + 1
	}
	hdr := "  " + fit("Name", nameW) + " " + fit("Role", roleW) + " "
	if showKind {
		hdr += fit("Kind", kindW) + " "
	}
	hdr += fit("Machine", devW) + " " + fit("State", stW) + " " + fit("Task", taskW)
	lines := []string{sMuted.Render(fit(hdr, inner))}
	if len(rows) == 0 {
		lines = append(lines, "", sMuted.Render("No agent matches the filter."))
	}
	for i := m.ag.list.off; i < len(rows) && i < m.ag.list.off+vis; i++ {
		a := rows[i]
		mark, name := "  ", fit(a.Name, nameW)
		if i == m.ag.list.sel {
			mark, name = sAccent.Render("›")+" ", sBold.Render(name)
		}
		row := mark + name + " " + sMuted.Render(fit(a.Role, roleW)) + " "
		if showKind {
			row += sMuted.Render(fit(a.Kind, kindW)) + " "
		}
		row += fit(a.Device, devW) + " " + fit(stateMark(a.State), stW) + " " + sMuted.Render(fit(m.agentTask(a), taskW))
		lines = append(lines, row)
	}
	left := panel(fmt.Sprintf("Agents %d", len(m.digest.Agents)), lines, lw, lh, true)
	var sel *api.Agent
	if m.ag.list.sel < len(rows) {
		sel = &rows[m.ag.list.sel]
	}
	right := panel("Selected agent", m.agentDetail(sel, rw-4), rw, rh, false)
	if lw == w {
		return left + "\n" + right
	}
	return side(left, right)
}

func (m *Model) agentDetail(a *api.Agent, w int) []string {
	if a == nil {
		return []string{"", sMuted.Render("Nothing selected.")}
	}
	var L []string
	kv := func(k, v string) {
		if v != "" {
			L = append(L, sMuted.Render(fit(k, 12))+cut(v, w-12))
		}
	}
	L = append(L, "", sBold.Render(cut(a.Name, w)))
	L = append(L, sMuted.Render(fit("State", 12))+stateMark(a.State)+sMuted.Render(" for "+span(time.Since(a.StateAt))))
	kv("Role", a.Role)
	kv("Kind", a.Kind)
	kv("Machine", a.Device)
	if a.Job != nil {
		kv("Job", "#"+itoa(int(*a.Job)))
	}
	kv("Working on", m.agentTask(*a))
	kv("Folder", a.Dir)
	L = append(L, "", sMuted.Render("Press ")+sAccent.Render("enter")+sMuted.Render(" to watch its terminal."))
	return L
}

func (m *Model) termPanel(w, h int) string {
	t := m.ag.term
	inner := w - 4
	vis := h - 6 // title, note, and the inset's two border lines and its own
	if vis < 1 {
		vis = 1
	}
	note := "Waiting for the machine…"
	switch {
	case t.err != "":
		note = ""
	case t.got:
		note = fmt.Sprintf("Read %ds ago. Held in the hub's memory for a minute, never stored.", t.age)
	}
	if t.offline {
		note = "Offline, retrying…"
	}
	var body []string
	switch {
	case t.err != "":
		body = append(body, sWarn.Render("▲ ")+t.err)
	case !t.got:
		body = append(body, sMuted.Render("Waiting for the machine…"), sMuted.Render("It answers when its link next checks in, usually within a few seconds."))
	default:
		ls := m.termLines()
		off := t.off
		if m.ag.follow {
			off = len(ls) - vis
		}
		if off > len(ls)-vis {
			off = len(ls) - vis
		}
		if off < 0 {
			off = 0
		}
		end := off + vis
		if end > len(ls) {
			end = len(ls)
		}
		body = ls[off:end]
	}
	// The inset is the one muted surface inside the card.
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).Padding(0, 1).
		Render(strings.Join(clip(body, inner-4, vis), "\n"))
	follow := "follow off"
	if m.ag.follow {
		follow = "following"
	}
	lines := append([]string{sMuted.Render(cut(note, inner))}, strings.Split(box, "\n")...)
	return panel(t.agent+" · terminal · "+follow, lines, w, h, true)
}
