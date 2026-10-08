package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"handloom/internal/api"
	"handloom/internal/profile"
)

type libState struct {
	list     listState
	group    int // 0 profiles, 1 starters
	profiles []api.ProfileInfo
	starters []api.StarterInfo
	loaded   bool
	err      string
	detail   map[string]libInfo // by "p:name" or "s:name"
	detailAt string             // what was last asked for
}

// libInfo is the plain "what it can do" text and the rest of a profile or starter.
type libInfo struct {
	can     string
	prompt  string
	skills  []string
	tools   []string
	err     string
	version int
}

type libMsg struct {
	profiles []api.ProfileInfo
	starters []api.StarterInfo
	err      error
}

type libDetailMsg struct {
	key  string
	info libInfo
}

func (s *libState) load(m *Model) tea.Cmd {
	a := m.api
	return func() tea.Msg {
		var out libMsg
		var err1, err2 error
		out.profiles, err1 = a.Profiles()
		out.starters, err2 = a.Starters()
		out.err = err1
		if out.err == nil {
			out.err = err2
		}
		return out
	}
}

func (s *libState) got(msg libMsg) {
	if msg.err != nil {
		s.err = plainErr(msg.err)
		return
	}
	s.err, s.loaded = "", true
	s.profiles, s.starters = msg.profiles, msg.starters
}

func (s *libState) gotDetail(msg libDetailMsg) {
	if s.detail == nil {
		s.detail = map[string]libInfo{}
	}
	s.detail[msg.key] = msg.info
}

func (m *Model) libraryRows() (names []string, cells [][3]string) {
	if m.lib.group == 0 {
		for _, p := range m.lib.profiles {
			if m.matches(tabLibrary, p.Name, p.Description, p.Kind, p.Runtime) {
				names = append(names, p.Name)
				cells = append(cells, [3]string{p.Kind, p.Runtime, p.Description})
			}
		}
		return
	}
	for _, st := range m.lib.starters {
		if m.matches(tabLibrary, st.Name, st.Title, st.Summary, st.Group) {
			added := ""
			if st.Added {
				added = "✓ added"
			}
			names = append(names, st.Name)
			cells = append(cells, [3]string{st.Group, added, st.Summary})
		}
	}
	return
}

func (m *Model) libraryHints() []hint {
	return []hint{{"s", "profiles / starters", true}, {"↑↓", "select", false}, {"/", "filter", false}}
}

func (m *Model) libraryKey(s string) tea.Cmd {
	names, _ := m.libraryRows()
	switch s {
	case "esc":
		if m.filters[tabLibrary] != "" {
			m.filters[tabLibrary] = ""
			m.resetSel()
		}
	case "/":
		m.startFilter()
	case "s", "left", "right":
		m.lib.group = 1 - m.lib.group
		m.lib.list = listState{}
	default:
		if d, ok := moveKey(s, 10); ok {
			m.lib.list.move(d, len(names))
		}
	}
	return m.libWant()
}

// libWant asks for the detail of the selected row if it has not been fetched.
func (m *Model) libWant() tea.Cmd {
	names, _ := m.libraryRows()
	if m.lib.list.sel >= len(names) {
		return nil
	}
	name := names[m.lib.list.sel]
	kind := "p"
	if m.lib.group == 1 {
		kind = "s"
	}
	key := kind + ":" + name
	if _, ok := m.lib.detail[key]; ok {
		return nil
	}
	a := m.api
	return func() tea.Msg {
		var info libInfo
		if kind == "p" {
			p, err := a.Profile(name)
			if err != nil {
				info.err = plainErr(err)
			} else {
				info = libInfo{can: profile.Summary(&p.Spec), prompt: p.Spec.Prompt, version: p.Version, tools: p.Tools, skills: p.Skills}
			}
		} else {
			st, err := a.Starter(name)
			if err != nil {
				info.err = plainErr(err)
			} else {
				info = libInfo{can: st.WhatItCanDo, tools: st.Tools, skills: st.Skills}
			}
		}
		return libDetailMsg{key: key, info: info}
	}
}

func (m *Model) libraryView(w, h int) string {
	if !m.lib.loaded {
		msg := "Loading the library…"
		if m.lib.err != "" {
			msg = m.lib.err
		}
		return panel("Library", []string{"", sMuted.Render(msg)}, w, h, true)
	}
	title := fmt.Sprintf("Profiles %d  ·  Starters %d", len(m.lib.profiles), len(m.lib.starters))
	if m.lib.group == 0 {
		title = sBold.Render("Profiles "+itoa(len(m.lib.profiles))) + sMuted.Render("  ·  Starters "+itoa(len(m.lib.starters)))
	} else {
		title = sMuted.Render("Profiles "+itoa(len(m.lib.profiles))+"  ·  ") + sBold.Render("Starters "+itoa(len(m.lib.starters)))
	}
	if (m.lib.group == 0 && len(m.lib.profiles) == 0) || (m.lib.group == 1 && len(m.lib.starters) == 0) {
		what := "A profile says which CLI an agent uses and what it may do. Add one from the starter library (press s), in the web UI under Profiles."
		if m.lib.group == 1 {
			what = "Starters are ready-made profiles. This hub has none."
		}
		return panel(title, []string{"", sBold.Render("Nothing here yet."), sMuted.Render(what)}, w, h, true)
	}
	lw, lh, rw, rh := panes(w, h)
	names, cells := m.libraryRows()
	inner := lw - 4
	vis := lh - 4
	m.lib.list.clamp(len(names), vis)
	nameW, aW, bW := 22, 10, 9
	descW := inner - 2 - nameW - aW - bW - 3
	h2 := [3]string{"Kind", "Runtime", "Description"}
	if m.lib.group == 1 {
		h2 = [3]string{"Group", "", "Summary"}
		aW, bW = 14, 7
		descW = inner - 2 - nameW - aW - bW - 3
	}
	lines := []string{sMuted.Render(fit("  "+fit("Name", nameW)+" "+fit(h2[0], aW)+" "+fit(h2[1], bW)+" "+h2[2], inner))}
	if len(names) == 0 {
		lines = append(lines, "", sMuted.Render("Nothing matches the filter."))
	}
	for i := m.lib.list.off; i < len(names) && i < m.lib.list.off+vis; i++ {
		mark, name := "  ", fit(names[i], nameW)
		if i == m.lib.list.sel {
			mark, name = sAccent.Render("›")+" ", sBold.Render(name)
		}
		c := cells[i]
		second := sMuted.Render(fit(c[1], bW))
		if strings.HasPrefix(c[1], "✓") {
			second = sSuccess.Render(fit(c[1], bW))
		}
		lines = append(lines, mark+name+" "+sMuted.Render(fit(c[0], aW))+" "+second+" "+sMuted.Render(fit(c[2], descW)))
	}
	left := panel(title, lines, lw, lh, true)
	right := panel("What it can do", m.libDetail(names, rw-4), rw, rh, false)
	if lw == w {
		return left + "\n" + right
	}
	return side(left, right)
}

func (m *Model) libDetail(names []string, w int) []string {
	if m.lib.list.sel >= len(names) {
		return []string{"", sMuted.Render("Nothing selected.")}
	}
	name := names[m.lib.list.sel]
	kind := "p"
	if m.lib.group == 1 {
		kind = "s"
	}
	var L []string
	L = append(L, "", sBold.Render(cut(name, w)))
	if m.lib.group == 1 {
		for _, st := range m.lib.starters {
			if st.Name == name {
				L = append(L, sMuted.Render(cut(st.Title+" · "+st.Group, w)))
				for _, l := range wrap(st.Summary, w) {
					L = append(L, l)
				}
				if st.Added {
					L = append(L, sSuccess.Render("✓")+" Already one of this hub's profiles")
				}
			}
		}
	} else {
		for _, p := range m.lib.profiles {
			if p.Name == name {
				L = append(L, sMuted.Render(cut(fmt.Sprintf("version %d · %s · runs on %s", p.Version, p.Kind, p.Runtime), w)))
			}
		}
	}
	info, ok := m.lib.detail[kind+":"+name]
	switch {
	case !ok:
		L = append(L, "", sMuted.Render("Loading…"))
	case info.err != "":
		L = append(L, "", sWarn.Render("▲ ")+info.err)
	default:
		L = append(L, "")
		L = append(L, wrap(info.can, w)...)
		if len(info.tools) > 0 {
			L = append(L, "", sMuted.Render("Tools  ")+cut(strings.Join(info.tools, ", "), w-7))
		}
		if len(info.skills) > 0 {
			L = append(L, sMuted.Render("Skills ")+cut(strings.Join(info.skills, ", "), w-7))
		}
	}
	return L
}
