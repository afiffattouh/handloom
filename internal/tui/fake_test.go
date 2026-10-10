package tui

import (
	"errors"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"handloom/internal/api"
	"handloom/internal/client"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii) // plain text, so the tests can read what is drawn
	m.Run()
}

// fakeAPI answers from fields and records every change that is asked of it.
type fakeAPI struct {
	mu sync.Mutex

	who      api.WhoAmI
	people   []api.Person
	metrics  Metrics
	digest   api.Digest
	jobs     []api.Job
	jobTasks []api.Task
	task     api.Task
	screen   api.Screen
	profiles []api.ProfileInfo
	starters []api.StarterInfo

	// search
	searchHits []api.SearchHit
	searchErr  error
	searches   []string

	// errors to return, per call
	screenErr, newJobErr, metricsErr, digestErr, acceptErr error

	// the changes asked for
	accepted []int64
	rejected []reject
	answers  []answer
	newJobs  []api.JobNewReq
	closed   []closeCall
	screens  int
}

type reject struct {
	id     int64
	reason string
}
type answer struct {
	id   int64
	text string
}
type closeCall struct {
	id     int64
	cancel bool
}

func (f *fakeAPI) Whoami() (api.WhoAmI, error)   { return f.who, nil }
func (f *fakeAPI) People() ([]api.Person, error) { return f.people, nil }
func (f *fakeAPI) Metrics(string) (Metrics, error) {
	return f.metrics, f.metricsErr
}
func (f *fakeAPI) Digest() (api.Digest, error)        { return f.digest, f.digestErr }
func (f *fakeAPI) Jobs() ([]api.Job, error)           { return f.jobs, nil }
func (f *fakeAPI) Job(id int64) (api.Job, error)      { return f.jobs[0], nil }
func (f *fakeAPI) JobTasks(int64) ([]api.Task, error) { return f.jobTasks, nil }
func (f *fakeAPI) Task(int64) (api.Task, error)       { return f.task, nil }
func (f *fakeAPI) Screen(string) (api.Screen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screens++
	return f.screen, f.screenErr
}
func (f *fakeAPI) Profiles() ([]api.ProfileInfo, error) { return f.profiles, nil }
func (f *fakeAPI) Profile(name string) (api.ProfileFull, error) {
	return api.ProfileFull{ProfileInfo: api.ProfileInfo{Name: name}}, nil
}
func (f *fakeAPI) Starters() ([]api.StarterInfo, error) { return f.starters, nil }
func (f *fakeAPI) Starter(name string) (api.StarterFull, error) {
	return api.StarterFull{StarterInfo: api.StarterInfo{Name: name}, WhatItCanDo: "This agent can read files."}, nil
}
func (f *fakeAPI) Devices() ([]api.Device, error) { return nil, nil }
func (f *fakeAPI) Search(q string) ([]api.SearchHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches = append(f.searches, q)
	return f.searchHits, f.searchErr
}

func (f *fakeAPI) Accept(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptErr != nil {
		return f.acceptErr
	}
	f.accepted = append(f.accepted, id)
	return nil
}
func (f *fakeAPI) Reject(id int64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejected = append(f.rejected, reject{id, reason})
	return nil
}
func (f *fakeAPI) Answer(id int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, answer{id, text})
	return nil
}
func (f *fakeAPI) NewJob(req api.JobNewReq) (api.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newJobs = append(f.newJobs, req)
	if f.newJobErr != nil {
		return api.Job{}, f.newJobErr
	}
	return api.Job{ID: 9, Title: req.Title}, nil
}
func (f *fakeAPI) CloseJob(id int64, cancel bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, closeCall{id, cancel})
	return nil
}

func (f *fakeAPI) changes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.accepted) + len(f.rejected) + len(f.answers) + len(f.newJobs) + len(f.closed)
}

// ---- driving a model ----

func seeded() *fakeAPI {
	one := int64(1)
	f := &fakeAPI{
		who: api.WhoAmI{Kind: "human", Name: "afif", Role: "owner"},
		digest: api.Digest{
			NeedsYou: []api.DigestItem{{Kind: "escalation", ID: 4, Title: "Which database should it use?", Who: "lead-ui", Options: []string{"sqlite", "postgres"}, At: time.Now().Add(-4 * time.Minute)}},
			ToReview: []api.DigestItem{{Kind: "submitted", ID: 7, Title: "Write the sign-in form", Who: "worker-1", Detail: "commit:3fa9", At: time.Now().Add(-2 * time.Minute)}},
			Agents: []api.Agent{
				{Name: "lead-ui", Role: "lead", Kind: "claude", Device: "box", State: "working", StateAt: time.Now().Add(-time.Minute), Job: &one},
				{Name: "worker-1", Role: "worker", Kind: "codex", Device: "box", State: "idle", StateAt: time.Now().Add(-time.Hour)},
			},
		},
		jobs: []api.Job{{ID: 1, Title: "Ship the login page", Status: "open", Lead: "lead-ui", Device: "box", CreatedBy: "human:afif", CreatedAt: time.Now().Add(-time.Hour),
			Tasks: api.JobCounts{Open: 1, Claimed: 1, Submitted: 1, Done: 2}}},
		task: api.Task{ID: 7, Title: "Write the sign-in form", Status: "submitted", Evidence: []string{"commit:3fa9", "test:go test -> ok"}, Note: "all fields done",
			Check: &api.TaskCheck{Device: "box", Command: "./check.sh", ExitCode: 0},
			Merge: &api.TaskMerge{Status: "pending"}},
		profiles: []api.ProfileInfo{{Name: "lead", Kind: "claude", Runtime: "cloud", Description: "Plans jobs"}, {Name: "coder", Kind: "claude", Runtime: "cloud"}},
		starters: []api.StarterInfo{{Name: "researcher", Title: "Researcher", Group: "Research", Summary: "Finds and reads sources"}},
	}
	f.metrics.Range = "7d"
	f.metrics.Needs.Count = 1
	f.metrics.Agents.Total, f.metrics.Agents.Working, f.metrics.Agents.Idle = 2, 1, 1
	f.metrics.Tasks.Claimed, f.metrics.Tasks.Submitted, f.metrics.Tasks.Open = 1, 1, 1
	f.metrics.Checks = Rate{Pct: 88, N: 17}
	f.metrics.TaskTime = Dur{Value: 12 * time.Minute, N: 7}
	f.metrics.Buckets = []Bucket{{Label: "Mon", Accepted: 1}, {Label: "Tue", Accepted: 4}, {Label: "Wed", Accepted: 2}}
	f.metrics.Insights = []Insight{{Sev: "warn", Title: "Questions wait a long time", Detail: "The oldest has waited an hour.", Basis: "1 open question"}}
	f.metrics.Fleet = []FleetRow{{Name: "lead-ui", Task: "job #1"}}
	return f
}

// run executes a command the way the program would, feeding what it returns
// back into the model. Commands that wait on a timer (the 3 s tick) are given a
// moment and then dropped.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				run(m, c)
			}
			return
		}
		if msg == nil {
			return
		}
		_, next := m.Update(msg)
		run(m, next)
	case <-time.After(40 * time.Millisecond):
	}
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		run(m, cmd)
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) // the cursor-blink command it returns is not needed
	}
}

func start(t *testing.T, f *fakeAPI, w, h int) *Model {
	t.Helper()
	m := New(Options{API: f, Hub: "https://hub.example.com"})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	run(m, m.Init())
	if !m.loaded {
		t.Fatal("the first refresh did not load")
	}
	return m
}

func mustContain(t *testing.T, view string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !contains(view, w) {
			t.Errorf("the screen does not show %q:\n%s", w, view)
		}
	}
}

func mustNotContain(t *testing.T, view string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if contains(view, w) {
			t.Errorf("the screen shows %q but should not:\n%s", w, view)
		}
	}
}

var errOffline = errors.New("dial tcp 127.0.0.1:7420: connect: connection refused")

func hubError(status int, msg string) error {
	return &client.Error{Status: status, Code: "x", Msg: msg}
}
