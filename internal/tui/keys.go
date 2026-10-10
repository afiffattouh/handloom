package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// hint is one key and what it does, for the bottom bar and the help sheet.
// primary marks the one action worth pointing at on this screen.
type hint struct {
	key, label string
	primary    bool
}

// hints are the keys that make sense right now, for the bottom bar.
func (m *Model) hints() []hint {
	switch m.mode {
	case modeInput:
		return []hint{{"enter", "continue", true}, {"esc", "cancel", false}}
	case modeFilter:
		return []hint{{"enter", "keep filter", true}, {"esc", "clear", false}}
	case modeForm:
		return formHints(m.form)
	case modeSearch:
		return m.searchHints()
	case modeConfirm:
		return []hint{{"y", "yes, do it", true}, {"n", "no, cancel", false}}
	}
	switch m.tab {
	case tabOverview:
		return []hint{{"r", "change range", true}, {"ctrl+r", "refresh", false}, {"tab", "next screen", false}}
	case tabInbox:
		return m.inboxHints()
	case tabJobs:
		return m.jobsHints()
	case tabAgents:
		return m.agentsHints()
	case tabSearch:
		return m.searchHints()
	}
	return m.libraryHints()
}

// helpSections is every key, grouped, for the ? sheet.
func helpSections() []struct {
	title string
	keys  []hint
} {
	type sec = struct {
		title string
		keys  []hint
	}
	return []sec{
		{"Everywhere", []hint{
			{"1-6", "go to a screen", false}, {"tab / shift+tab", "next or previous screen", false},
			{"↑ ↓ / k j", "move; pgup pgdn scroll", false}, {"enter", "open the selected row", false},
			{"esc", "go back, or clear the filter", false}, {"/", "filter the list", false},
			{"ctrl+r", "refresh now (it also refreshes every 3 s)", false}, {"?", "this help", false}, {"q / ctrl+c", "quit", false},
		}},
		{"Overview", []hint{{"r", "change the range: 24h, 7d, 30d", false}}},
		{"Inbox", []hint{
			{"a", "accept work that is waiting for review", false}, {"x", "send it back, with a reason", false},
			{"e", "answer a question", false}, {"c", "close a finished job", false},
		}},
		{"Jobs", []hint{
			{"n", "start a new job", false}, {"c", "close the job as finished", false},
			{"C", "cancel the job and its unfinished tasks", false},
		}},
		{"Agents", []hint{{"enter", "watch the agent's terminal", false}, {"f", "follow the end of the terminal, on or off", false}}},
		{"Library", []hint{{"s", "switch between profiles and starters", false}}},
		{"Search", []hint{
			{"/ or enter", "type a search; enter runs it, esc stops typing", false},
			{"↑ ↓", "select a result", false}, {"enter", "open the result's job", false},
		}},
		{"New job form", []hint{
			{"tab / shift+tab", "next or previous field", false}, {"← →", "choose a profile or a machine", false},
			{"ctrl+s", "review and start", false}, {"esc", "cancel", false},
		}},
		{"Every change", []hint{{"y / n", "yes or no to what an action says it will do", false}}},
	}
}

func renderHints(hs []hint, w int) string {
	tail := sMuted.Render("?") + " " + sMuted.Render("help") + "  " + sMuted.Render("q") + " " + sMuted.Render("quit")
	room := w - lipgloss.Width(tail) - 3
	var parts []string
	used := 0
	for _, h := range hs {
		k := sBold.Render(h.key)
		if h.primary {
			k = sAccent.Render(h.key)
		}
		p := k + " " + sMuted.Render(h.label)
		if used+lipgloss.Width(p)+2 > room {
			break
		}
		parts = append(parts, p)
		used += lipgloss.Width(p) + 2
	}
	left := strings.Join(parts, "  ")
	gap := w - 2 - lipgloss.Width(left) - lipgloss.Width(tail)
	if gap < 1 {
		gap = 1
	}
	return " " + left + repeat(" ", gap) + tail
}

func (m *Model) keysLine() string { return fit(renderHints(m.hints(), m.w), m.w) }

func (m *Model) helpBox(h int) string {
	lines := []string{""}
	for _, s := range helpSections() {
		lines = append(lines, sBold.Render(s.title))
		for _, k := range s.keys {
			lines = append(lines, "  "+fit(sBold.Render(k.key), 18)+sMuted.Render(k.label))
		}
		lines = append(lines, "")
	}
	w := 78
	if w > m.w-4 {
		w = m.w - 4
	}
	vis := h - 4 // borders, title and the closing hint
	scrolls := len(lines) > vis
	if len(lines) <= vis {
		m.helpOff = 0
	} else {
		if max := len(lines) - vis; m.helpOff > max {
			m.helpOff = max
		}
		if m.helpOff < 0 {
			m.helpOff = 0
		}
		lines = lines[m.helpOff:]
	}
	if len(lines) > vis {
		lines = lines[:vis]
	}
	hint := "Any key closes this."
	if scrolls {
		hint = "↑↓ scroll · any other key closes"
	}
	lines = append(lines, sMuted.Render(hint))
	return panel("Keys", lines, w, len(lines)+3, true)
}

// key routes a key press by mode, then to the global keys, then the screen.
func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeHelp:
		if d, ok := moveKey(s, 10); ok && s != "k" && s != "j" {
			m.helpOff += d
			return m, nil
		}
		m.mode, m.helpOff = modeNormal, 0
		return m, nil
	case modeConfirm:
		return m.confirmKey(s)
	case modeInput:
		return m.inputKey(k)
	case modeFilter:
		return m.filterKey(k)
	case modeForm:
		return m.formKey(k)
	case modeSearch:
		return m.searchInputKey(k)
	}
	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		return m, nil
	case "ctrl+r":
		return m, m.refresh()
	case "1", "2", "3", "4", "5", "6":
		return m, m.goTab(tab(s[0] - '1'))
	case "tab":
		return m, m.goTab((m.tab + 1) % tabCount)
	case "shift+tab":
		return m, m.goTab((m.tab + tabCount - 1) % tabCount)
	}
	switch m.tab {
	case tabOverview:
		return m, m.overviewKey(s)
	case tabInbox:
		return m, m.inboxKey(s)
	case tabJobs:
		return m, m.jobsKey(s)
	case tabAgents:
		return m, m.agentsKey(s)
	case tabSearch:
		return m, m.searchKey(s)
	}
	return m, m.libraryKey(s)
}

func (m *Model) goTab(t tab) tea.Cmd {
	if t == m.tab {
		return nil
	}
	m.tab = t
	m.ibx.open, m.jb.open, m.ag.term = false, false, nil
	if t == tabLibrary {
		return m.lib.load(m)
	}
	return nil
}

// ---- filter ----

func (m *Model) startFilter() {
	m.filter.SetValue(m.filters[m.tab])
	m.filter.CursorEnd()
	m.filter.Focus()
	m.mode = modeFilter
}

func (m *Model) filterKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		m.mode = modeNormal
		m.filter.Blur()
		return m, nil
	case "esc":
		m.filters[m.tab] = ""
		m.mode = modeNormal
		m.filter.Blur()
		m.resetSel()
		return m, nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(k)
	if m.filters[m.tab] != m.filter.Value() {
		m.filters[m.tab] = m.filter.Value()
		m.resetSel()
	}
	return m, cmd
}

func (m *Model) resetSel() {
	switch m.tab {
	case tabInbox:
		m.ibx.list = listState{}
	case tabJobs:
		m.jb.list = listState{}
	case tabAgents:
		m.ag.list = listState{}
	case tabLibrary:
		m.lib.list = listState{}
	}
}

// moveKey turns a navigation key into a step, or reports that it is not one.
func moveKey(s string, page int) (int, bool) {
	switch s {
	case "up", "k":
		return -1, true
	case "down", "j":
		return 1, true
	case "pgup":
		return -page, true
	case "pgdown":
		return page, true
	case "home", "g":
		return -1 << 20, true
	case "end", "G":
		return 1 << 20, true
	}
	return 0, false
}
