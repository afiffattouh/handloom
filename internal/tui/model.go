// Package tui is the full-screen terminal console for the signed-in person:
// the same picture the web UI shows (what needs you, jobs, agents, library),
// from a terminal, over the same API the command line uses.
package tui

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"handloom/internal/api"
	"handloom/internal/client"
)

const (
	refreshEvery = 3 * time.Second
	screenEvery  = 2 * time.Second
	flashFor     = 8 * time.Second
	minW, minH   = 80, 24
)

// Options say how to reach the hub.
type Options struct {
	API API
	Hub string // the hub's URL, shown in the header
}

type tab int

const (
	tabOverview tab = iota
	tabInbox
	tabJobs
	tabAgents
	tabLibrary
	tabCount
)

var tabNames = [tabCount]string{"Overview", "Inbox", "Jobs", "Agents", "Library"}

type mode int

const (
	modeNormal mode = iota
	modeFilter
	modeInput
	modeConfirm
	modeForm
	modeHelp
)

// Model is the whole console. Screens keep their own small state and draw
// from the shared data below.
type Model struct {
	api  API
	host string
	w, h int
	tab  tab
	mode mode
	back mode // where a confirmation returns to when answered no

	helpOff int

	// who is signed in
	name, kind, role string
	whoDone          bool

	// data from the hub; kept when a refresh fails
	metrics  *Metrics
	digest   *api.Digest
	jobs     []api.Job
	rng      string
	loaded   bool
	inflight bool
	lastOK   time.Time

	// connection state
	offline bool
	authErr bool
	apiErr  string

	flash   string
	flashAt time.Time
	flashOK bool

	// screens
	ov  overviewState
	ibx inboxState
	jb  jobsState
	ag  agentsState
	lib libState

	// overlays
	input    textinput.Model
	inputFor inputKind
	inputTo  int64
	filter   textinput.Model
	filters  [tabCount]string
	confirm  *pendingAction
	form     *jobForm
}

// New makes the console.
func New(o Options) *Model {
	m := &Model{api: o.API, host: hostOf(o.Hub), rng: "7d"}
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.CharLimit = 2000
	m.filter = textinput.New()
	m.filter.Prompt = ""
	m.filter.CharLimit = 80
	m.ag.follow = true
	return m
}

func hostOf(hub string) string {
	if u, err := url.Parse(hub); err == nil && u.Host != "" {
		return u.Host
	}
	return hub
}

// Run opens the console and returns when the person quits.
func Run(o Options) error {
	_, err := tea.NewProgram(New(o), tea.WithAltScreen()).Run()
	return err
}

// ---- messages ----

type tickMsg struct{}

type dataMsg struct {
	rng     string
	metrics *Metrics
	digest  *api.Digest
	jobs    *[]api.Job
	who     *api.WhoAmI
	role    string
	err     error
}

type actionMsg struct {
	text string
	err  error
	form bool // the action came from the new-job form
}

func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }

func tick() tea.Cmd { return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} }) }

// refresh fetches everything the screens share, in parallel, off the UI loop.
func (m *Model) refresh() tea.Cmd {
	m.inflight = true
	a, rng, needWho := m.api, m.rng, !m.whoDone
	return func() tea.Msg {
		d := dataMsg{rng: rng}
		var mu sync.Mutex
		var wg sync.WaitGroup
		fail := func(err error) {
			mu.Lock()
			if d.err == nil || isAuth(err) {
				d.err = err
			}
			mu.Unlock()
		}
		run := func(f func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := f(); err != nil {
					fail(err)
				}
			}()
		}
		run(func() error {
			v, err := a.Metrics(rng)
			if err == nil {
				d.metrics = &v
			}
			return err
		})
		run(func() error {
			v, err := a.Digest()
			if err == nil {
				d.digest = &v
			}
			return err
		})
		run(func() error {
			v, err := a.Jobs()
			if err == nil {
				d.jobs = &v
			}
			return err
		})
		if needWho {
			run(func() error {
				w, err := a.Whoami()
				if err != nil {
					return err
				}
				d.who = &w
				if w.Kind == "human" && w.Role == "" {
					// Only an owner may list people; for anyone else this is
					// refused and the role stays unknown.
					if ps, err := a.People(); err == nil {
						for _, p := range ps {
							if p.Name == w.Name {
								d.role = p.Role
							}
						}
					}
				}
				return nil
			})
		}
		wg.Wait()
		return d
	}
}

func isAuth(err error) bool {
	var ce *client.Error
	return errors.As(err, &ce) && ce.Status == 401
}

// isOffline is true for errors where the hub did not answer at all.
func isOffline(err error) bool {
	var ce *client.Error
	if errors.As(err, &ce) {
		return false
	}
	return err != nil
}

// ---- update ----

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = max(10, msg.Width-52)
		m.filter.Width = 30
		return m, nil
	case tickMsg:
		if m.inflight {
			return m, tick()
		}
		if m.tab == tabLibrary {
			return m, tea.Batch(m.refresh(), tick(), m.lib.load(m))
		}
		return m, tea.Batch(m.refresh(), tick())
	case dataMsg:
		m.applyData(msg)
		return m, nil
	case actionMsg:
		return m.afterAction(msg)
	case taskMsg:
		m.ibx.gotTask(msg)
		return m, nil
	case jobMsg:
		m.gotJob(msg)
		return m, nil
	case screenMsg:
		return m, m.gotScreen(msg)
	case screenTickMsg:
		return m, m.screenTick(msg)
	case libMsg:
		m.lib.got(msg)
		return m, m.libWant()
	case libDetailMsg:
		m.lib.gotDetail(msg)
		return m, nil
	case formDataMsg:
		m.gotFormData(msg)
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) applyData(d dataMsg) {
	m.inflight = false
	if d.who != nil {
		m.name, m.kind, m.role, m.whoDone = d.who.Name, d.who.Kind, d.role, true
		if d.who.Role != "" {
			m.role = d.who.Role
		}
		if m.kind == "admin" {
			m.name = "admin"
		}
	}
	if d.metrics != nil && d.rng == m.rng {
		m.metrics = d.metrics
	}
	if d.digest != nil {
		m.digest = d.digest
		m.ibx.sync(m)
		m.ag.sync(m)
	}
	if d.jobs != nil {
		m.jobs = *d.jobs
		m.jb.sync(m)
	}
	if d.metrics != nil || d.digest != nil {
		m.loaded = true
	}
	switch {
	case d.err == nil:
		m.offline, m.authErr, m.apiErr = false, false, ""
		m.lastOK = time.Now()
	case isAuth(d.err):
		m.authErr, m.offline = true, false
	case isOffline(d.err):
		m.offline, m.authErr = true, false
	default:
		m.offline, m.authErr = false, false
		m.apiErr = d.err.Error()
	}
}

func (m *Model) say(text string, ok bool) {
	m.flash, m.flashOK, m.flashAt = text, ok, time.Now()
}

func (m *Model) afterAction(a actionMsg) (tea.Model, tea.Cmd) {
	if a.err != nil {
		if a.form && m.form != nil {
			m.form.err = plainErr(a.err)
			m.mode = modeForm
			return m, nil
		}
		m.say(plainErr(a.err), false)
		return m, nil
	}
	m.say(a.text, true)
	if a.form {
		m.form, m.mode = nil, modeNormal
		m.tab = tabJobs
	}
	m.ibx.open = false
	return m, m.refresh()
}

// plainErr turns an error into a sentence for the status line.
func plainErr(err error) string {
	var ce *client.Error
	if errors.As(err, &ce) {
		switch ce.Status {
		case 401:
			return "Your token was not accepted. Run `handloom token new`, then set HANDLOOM_TOKEN."
		case 403:
			return strings.TrimSpace(ce.Msg)
		}
		return strings.TrimSpace(ce.Msg)
	}
	return "Could not reach the hub: " + err.Error()
}

func (m *Model) needsYou() int {
	if m.digest != nil {
		return len(m.digest.NeedsYou)
	}
	return 0
}

// ---- view ----

func (m *Model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.w < minW || m.h < minH {
		msg := sBold.Render("Window too small") + "\n" +
			sMuted.Render("Handloom needs at least 80 columns by 24 rows.\nResize the terminal, or press q to quit.")
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, msg)
	}
	bodyH := m.h - 4
	var body string
	switch {
	case m.mode == modeHelp:
		body = lipgloss.Place(m.w, bodyH, lipgloss.Center, lipgloss.Center, m.helpBox(bodyH))
	case m.mode == modeConfirm:
		body = lipgloss.Place(m.w, bodyH, lipgloss.Center, lipgloss.Center, m.confirmBox())
	default:
		body = strings.Join(clip(strings.Split(m.bodyView(m.w, bodyH), "\n"), m.w, bodyH), "\n")
	}
	return strings.Join([]string{m.header(), m.tabBar(), body, m.statusLine(), m.keysLine()}, "\n")
}

func (m *Model) bodyView(w, h int) string {
	if m.mode == modeForm && m.form != nil {
		return m.formView(w, h)
	}
	if !m.loaded {
		msg := "Connecting to the hub…"
		switch {
		case m.authErr:
			msg = "The hub did not accept your token.\nRun `handloom token new`, then set HANDLOOM_TOKEN to the new token."
		case m.offline:
			msg = "Can't reach the hub. Retrying…"
		}
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sMuted.Render(msg))
	}
	switch m.tab {
	case tabInbox:
		return m.inboxView(w, h)
	case tabJobs:
		return m.jobsView(w, h)
	case tabAgents:
		return m.agentsView(w, h)
	case tabLibrary:
		return m.libraryView(w, h)
	}
	return m.overviewView(w, h)
}

func (m *Model) header() string {
	left := " " + sBold.Render("Handloom") + sMuted.Render("  "+m.host)
	who := m.name
	if who == "" {
		who = "…"
	}
	if m.role != "" {
		who += " · " + m.role
	}
	right := sMuted.Render(who) + "  " + m.pill() + " "
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = cut(left, m.w-lipgloss.Width(right)-1)
		gap = m.w - lipgloss.Width(left) - lipgloss.Width(right)
	}
	return left + repeat(" ", gap) + right
}

// pill is the one filled shape on screen: how many things need you.
func (m *Model) pill() string {
	n := m.needsYou()
	label := "Needs you " + itoa(n)
	if !colorOn() {
		return "[" + label + "]"
	}
	st := lipgloss.NewStyle().Padding(0, 1).Bold(true)
	if n == 0 {
		return st.Foreground(cMuted).Background(lipgloss.AdaptiveColor{Light: "#e3e9e5", Dark: "#2a332e"}).Render(label)
	}
	return st.Foreground(lipgloss.Color("#1c1a10")).Background(cWarn).Render(label)
}

func (m *Model) tabBar() string {
	var parts []string
	for i := tab(0); i < tabCount; i++ {
		label := itoa(int(i)+1) + " " + tabNames[i]
		if i == tabInbox && m.digest != nil {
			if n := len(m.digest.NeedsYou) + len(m.digest.ToReview); n > 0 {
				label += " " + itoa(n)
			}
		}
		switch {
		case i == m.tab && colorOn():
			parts = append(parts, sAccent.Underline(true).Render(" "+label+" "))
		case i == m.tab:
			parts = append(parts, "["+label+"]")
		default:
			parts = append(parts, sMuted.Render(" "+label+" "))
		}
	}
	line := " " + strings.Join(parts, " ")
	return fit(line, m.w)
}

func (m *Model) statusLine() string {
	switch m.mode {
	case modeInput:
		return fit(" "+m.inputPrompt()+m.input.View(), m.w)
	case modeFilter:
		return fit(" "+sMuted.Render("Filter  ")+m.filter.View(), m.w)
	}
	var s string
	switch {
	case m.authErr:
		s = sBad.Render("✗ Your token was not accepted. Run `handloom token new`, then set HANDLOOM_TOKEN.")
	case m.flash != "" && time.Since(m.flashAt) < flashFor:
		if m.flashOK {
			s = sSuccess.Render("✓ ") + m.flash
		} else {
			s = sBad.Render("✗ ") + m.flash
		}
	case m.offline:
		s = sWarn.Render("▲ Offline, retrying") + sMuted.Render(lastSeen(m.lastOK))
	case m.apiErr != "":
		s = sWarn.Render("▲ ") + m.apiErr
	case m.f(m.tab) != "":
		s = sMuted.Render("Filter: " + m.f(m.tab) + "  (esc clears)")
	case !m.lastOK.IsZero():
		s = sMuted.Render("Updated " + span(time.Since(m.lastOK)) + " ago · refreshes every 3 s")
	}
	return fit(" "+s, m.w)
}

func lastSeen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " — showing data from " + span(time.Since(t)) + " ago"
}

func (m *Model) f(t tab) string { return m.filters[t] }

// matches is true when any of the fields contains the active filter.
func (m *Model) matches(t tab, fields ...string) bool {
	q := strings.ToLower(strings.TrimSpace(m.filters[t]))
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}
