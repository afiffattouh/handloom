package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

func hits() []api.SearchHit {
	return []api.SearchHit{
		{Kind: "job", Job: 1, Title: "#1 Ship the login page: build the sign-in form", Snippet: "Build the sign-in form and wire it to the\nsession API. " + strings.Repeat("Done means a person can sign in. ", 12), Who: "afif", At: time.Now().Add(-time.Hour)},
		{Kind: "handoff", Job: 1, Task: 2, Title: "#1 Write the sign-in form: handoff", Snippet: "Tried a cookie first.", Who: "worker-1", At: time.Now().Add(-time.Minute)},
		{Kind: "answer", Job: 1, Title: "#1 Which database: sqlite", Snippet: "sqlite", Who: "afif", At: time.Now()},
	}
}

func search(m *Model, q string) {
	press(m, "/")
	typeText(m, q)
	press(m, "enter")
}

func TestSearchScreenEmptyState(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "6")
	mustContain(t, m.View(), "[6 Search]", "Search what earlier jobs recorded on the hub",
		"A client's notes are searched on the machine that holds them: handloom search --notes")
}

func TestSearchRunsOnceAndShowsHits(t *testing.T) {
	f := seeded()
	f.searchHits = hits()
	m := start(t, f, 120, 40)
	press(m, "6")
	search(m, "login")
	if len(f.searches) != 1 || f.searches[0] != "login" {
		t.Fatalf("searches = %v", f.searches)
	}
	v := m.View()
	mustContain(t, v, "3 results for “login”", "Job ", "Handoff note", "Answered question", "Ship the login page", "worker-1", "Tried a cookie first.", "…")
	mustNotContain(t, v, "Nothing recorded")
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "Done means a person") && strings.Count(l, "Done means") > 1 {
			t.Errorf("snippet is not truncated on one line: %q", l)
		}
	}
}

func TestSearchNoHits(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "6")
	search(m, "zzz")
	mustContain(t, m.View(), "Nothing recorded on the hub matches.", "Try fewer or different words.")
}

func TestSearchErrorIsShownInTheHubsWords(t *testing.T) {
	f := seeded()
	f.searchErr = hubError(400, "say what to look for")
	m := start(t, f, 120, 40)
	press(m, "6")
	search(m, "x")
	mustContain(t, m.View(), "say what to look for")
}

func TestSearchEmptyQueryDoesNotCall(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "6", "enter", "enter")
	if len(f.searches) != 0 {
		t.Fatalf("searched with nothing typed: %v", f.searches)
	}
}

func TestSearchIgnoresAStaleAnswer(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "6")
	press(m, "/")
	typeText(m, "old")
	_, c1 := m.Update(keyEnter())
	m.Update(keyEsc()) // no-op in normal mode
	press(m, "/")
	m.sr.in.SetValue("new")
	_, c2 := m.Update(keyEnter())
	// the newer answer arrives first, then the older one
	m.Update(c2())
	m.Update(searchMsg{seq: 1, q: "old", hits: []api.SearchHit{{Kind: "job", Job: 1, Title: "OLD-RESULT"}}})
	_ = c1
	mustNotContain(t, m.View(), "OLD-RESULT")
	if m.sr.query != "new" {
		t.Fatalf("query = %q", m.sr.query)
	}
}

func TestSearchEnterOpensTheHitsJob(t *testing.T) {
	f := seeded()
	f.searchHits = hits()
	m := start(t, f, 120, 40)
	press(m, "6")
	search(m, "login")
	press(m, "down", "enter") // a task hit: opens its job
	if m.tab != tabJobs || !m.jb.open || m.jb.openID != 1 {
		t.Fatalf("tab=%d open=%v id=%d", m.tab, m.jb.open, m.jb.openID)
	}
	mustContain(t, m.View(), "Job #1", "Ship the login page", "Lead        lead-ui")
}

func TestSearchEscLeavesTheInput(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "6", "enter")
	if m.mode != modeSearch {
		t.Fatal("enter should focus the input")
	}
	press(m, "5") // typed into the box, not a tab switch
	if m.tab != tabSearch {
		t.Fatal("digits typed in the box must not switch screens")
	}
	press(m, "esc")
	if m.mode != modeNormal {
		t.Fatal("esc should leave the input")
	}
	press(m, "5")
	if m.tab != tabLibrary {
		t.Fatal("5 should now switch screens")
	}
}

func TestSearchFitsAtEverySize(t *testing.T) {
	f := seeded()
	f.searchHits = append(hits(), hits()...)
	f.searchHits = append(f.searchHits, hits()...)
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {160, 50}} {
		m := start(t, f, sz[0], sz[1])
		press(m, "6")
		for i, label := range []string{"empty", "results", "typing"} {
			switch i {
			case 1:
				search(m, "login")
				press(m, "down", "down", "down", "down")
			case 2:
				press(m, "/")
			}
			lines := strings.Split(m.View(), "\n")
			if len(lines) != sz[1] {
				t.Errorf("%dx%d %s: %d lines", sz[0], sz[1], label, len(lines))
			}
			for _, l := range lines {
				if w := len([]rune(l)); w > sz[0] {
					t.Errorf("%dx%d %s: line is %d wide: %q", sz[0], sz[1], label, w, l)
				}
			}
		}
	}
}

func keyEnter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func keyEsc() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyEsc} }
