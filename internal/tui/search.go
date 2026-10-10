package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"handloom/internal/api"
)

// searchState is the Search screen: the query line, the last answer and the
// cursor in it. seq numbers the searches so a slow, older answer is ignored.
type searchState struct {
	in       textinput.Model
	list     listState
	hits     []api.SearchHit
	query    string // what the hits are for
	searched bool   // a search has finished
	busy     bool
	seq      int
}

type searchMsg struct {
	seq  int
	q    string
	hits []api.SearchHit
	err  error
}

func newSearchState() searchState {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 200
	in.Placeholder = "words to look for"
	return searchState{in: in}
}

func (m *Model) startSearchInput() {
	m.sr.in.CursorEnd()
	m.sr.in.Focus()
	m.mode = modeSearch
}

// runSearch starts a search off the UI loop. Only the answer to the newest
// search is kept.
func (m *Model) runSearch(q string) tea.Cmd {
	m.sr.seq++
	m.sr.busy = true
	seq, a := m.sr.seq, m.api
	return func() tea.Msg {
		hits, err := a.Search(q)
		return searchMsg{seq: seq, q: q, hits: hits, err: err}
	}
}

func (m *Model) gotSearch(msg searchMsg) {
	if msg.seq != m.sr.seq {
		return // an older search finishing after a newer one
	}
	m.sr.busy = false
	if msg.err != nil {
		m.say(plainErr(msg.err), false)
		return
	}
	m.sr.hits, m.sr.query, m.sr.searched = msg.hits, msg.q, true
	m.sr.list = listState{}
}

func (m *Model) searchInputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNormal
		m.sr.in.Blur()
		return m, nil
	case "enter":
		q := strings.TrimSpace(m.sr.in.Value())
		if q == "" {
			m.say("Say what to look for.", false)
			return m, nil
		}
		m.mode = modeNormal
		m.sr.in.Blur()
		return m, m.runSearch(q)
	}
	var cmd tea.Cmd
	m.sr.in, cmd = m.sr.in.Update(k)
	return m, cmd
}

func (m *Model) searchHints() []hint {
	if m.mode == modeSearch {
		return []hint{{"enter", "search", true}, {"esc", "stop typing", false}}
	}
	if len(m.sr.hits) > 0 {
		return []hint{{"enter", "open job", true}, {"↑↓", "select", false}, {"/", "new search", false}}
	}
	return []hint{{"/", "search", true}, {"enter", "type a search", false}}
}

func (m *Model) searchKey(s string) tea.Cmd {
	switch s {
	case "/":
		m.startSearchInput()
		return nil
	case "enter":
		if m.sr.list.sel < len(m.sr.hits) && len(m.sr.hits) > 0 {
			return m.openHit(m.sr.hits[m.sr.list.sel])
		}
		m.startSearchInput()
		return nil
	}
	if d, ok := moveKey(s, 10); ok {
		m.sr.list.move(d, len(m.sr.hits))
	}
	return nil
}

// openHit shows the hit's job in the Jobs screen's detail view.
func (m *Model) openHit(h api.SearchHit) tea.Cmd {
	if h.Job == 0 {
		m.say("That result does not say which job it belongs to.", false)
		return nil
	}
	m.goTab(tabJobs)
	j := api.Job{ID: h.Job, Title: strings.TrimSpace(h.Title)}
	for _, x := range m.jobs {
		if x.ID == h.Job {
			j = x
		}
	}
	m.jb.open, m.jb.openID, m.jb.scroll = true, h.Job, 0
	m.jb.job, m.jb.tasks, m.jb.loadErr = &j, nil, ""
	return fetchJob(m.api, h.Job)
}

func hitKind(k string) string {
	switch k {
	case "job":
		return "Job"
	case "task":
		return "Work"
	case "handoff":
		return "Handoff note"
	case "answer":
		return "Answered question"
	}
	return k
}

func (m *Model) searchView(w, h int) string {
	inner := w - 4
	m.sr.in.Width = max(10, inner-16)
	line := sMuted.Render(fit("Look for", 9)) + "  "
	if m.mode == modeSearch {
		line = sAccent.Render(fit("Look for", 9)) + "  "
	}
	lines := []string{line + m.sr.in.View()}
	if len(m.sr.hits) == 0 {
		lines = append(lines, "")
		switch {
		case m.sr.busy:
			lines = append(lines, sMuted.Render("Searching…"))
		case m.sr.searched:
			lines = append(lines, sBold.Render("Nothing recorded on the hub matches."), sMuted.Render("Try fewer or different words."))
		default:
			lines = append(lines,
				sBold.Render("Search what earlier jobs recorded on the hub."),
				sMuted.Render("That is briefs, finished work, handoff notes and answered questions."),
				"",
				sMuted.Render("A client's notes are searched on the machine that holds them: handloom search --notes"),
				"",
				sMuted.Render("Press ")+sAccent.Render("/")+sMuted.Render(" and type what to look for."))
		}
		return panel("Search", lines, w, h, true)
	}
	sub := plural(len(m.sr.hits), "result", "results") + " for “" + m.sr.query + "”"
	if m.sr.busy {
		sub = "Searching…  (showing " + sub + ")"
	}
	lines = append(lines, sMuted.Render(cut(sub, inner)), "")
	vis := (h - 2 - 1 - len(lines)) / 2
	if vis < 1 {
		vis = 1
	}
	m.sr.list.clamp(len(m.sr.hits), vis)
	kindW, whoW := 18, 20
	titleW := inner - 2 - kindW - 1 - whoW - 1
	for i := m.sr.list.off; i < len(m.sr.hits) && i < m.sr.list.off+vis; i++ {
		x := m.sr.hits[i]
		mark, title := "  ", fit(x.Title, titleW)
		if i == m.sr.list.sel {
			mark, title = sAccent.Render("›")+" ", sBold.Render(title)
		}
		who := strings.TrimPrefix(x.Who, "human:")
		if !x.At.IsZero() {
			if who != "" {
				who += " · "
			}
			who += ago(x.At)
		}
		lines = append(lines,
			mark+sMuted.Render(fit(hitKind(x.Kind), kindW))+" "+title+" "+sMuted.Render(fit(who, whoW)),
			"  "+strings.Repeat(" ", kindW+1)+sMuted.Render(cut(strings.Join(strings.Fields(x.Snippet), " "), inner-2-kindW-1)))
	}
	return panel("Search", lines, w, h, true)
}
