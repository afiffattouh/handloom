package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"handloom/internal/api"
)

// The new-job form calls the same endpoint as `handloom job new`.

const (
	fTitle = iota
	fBrief
	fLead
	fDevice
	fRepo
	fVerify
	fStart
	fCount
)

type jobForm struct {
	title, repo, verify textinput.Model
	brief               textarea.Model
	focus               int
	profiles            []string // "" first: no lead yet
	pIdx                int
	devices             []string // "" first: let the hub choose
	dIdx                int
	err                 string
	loaded              bool
}

type formDataMsg struct {
	profiles []api.ProfileInfo
	devices  []api.Device
	perr     error
}

func newInput(placeholder string) textinput.Model {
	t := textinput.New()
	t.Prompt = ""
	t.Placeholder = placeholder
	t.CharLimit = 500
	return t
}

func (m *Model) openForm() tea.Cmd {
	f := &jobForm{
		title:    newInput("What should get done?"),
		repo:     newInput("path of a git repository on that machine (optional)"),
		verify:   newInput("command that proves the work, e.g. ./check.sh (optional)"),
		profiles: []string{""},
		devices:  []string{""},
	}
	f.brief = textarea.New()
	f.brief.Prompt = ""
	f.brief.Placeholder = "What to do, and what done looks like."
	f.brief.ShowLineNumbers = false
	f.brief.SetHeight(4)
	f.brief.CharLimit = 8000
	f.title.Focus()
	for _, d := range m.knownDevices() {
		f.devices = append(f.devices, d)
	}
	if len(f.devices) == 2 {
		f.dIdx = 1
	}
	for _, p := range m.lib.profiles {
		f.profiles = append(f.profiles, p.Name)
	}
	f.pIdx = preferredLead(f.profiles)
	m.form, m.mode = f, modeForm
	a := m.api
	return func() tea.Msg {
		var out formDataMsg
		out.profiles, out.perr = a.Profiles()
		out.devices, _ = a.Devices() // owners only; the others use the machines their agents are on
		return out
	}
}

func preferredLead(names []string) int {
	for i, n := range names {
		if strings.HasPrefix(n, "lead") {
			return i
		}
	}
	if len(names) > 1 {
		return 1
	}
	return 0
}

// knownDevices are the machines the console has seen agents on.
func (m *Model) knownDevices() []string {
	seen := map[string]bool{}
	if m.digest != nil {
		for _, a := range m.digest.Agents {
			if a.Device != "" {
				seen[a.Device] = true
			}
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *Model) gotFormData(d formDataMsg) {
	f := m.form
	if f == nil {
		return
	}
	if d.perr != nil {
		f.err = "Could not load the profiles: " + plainErr(d.perr)
		return
	}
	cur := f.profiles[f.pIdx]
	f.profiles = []string{""}
	for _, p := range d.profiles {
		f.profiles = append(f.profiles, p.Name)
	}
	f.pIdx = 0
	for i, n := range f.profiles {
		if n == cur && cur != "" {
			f.pIdx = i
		}
	}
	if cur == "" && f.pIdx == 0 {
		f.pIdx = preferredLead(f.profiles)
	}
	curDev := f.devices[f.dIdx]
	set := map[string]bool{}
	for _, x := range f.devices {
		if x != "" {
			set[x] = true
		}
	}
	for _, dv := range d.devices {
		if dv.Joined && dv.RevokedAt == nil {
			set[dv.Name] = true
		}
	}
	f.devices = append([]string{""}, sortedKeys(set)...)
	f.dIdx = 0
	for i, n := range f.devices {
		if n == curDev && n != "" {
			f.dIdx = i
		}
	}
	if curDev == "" && len(f.devices) == 2 {
		f.dIdx = 1
	}
	f.loaded = true
}

func formHints(f *jobForm) []hint {
	return []hint{{"ctrl+s", "review and start", true}, {"tab", "next field", false}, {"←→", "choose", false}, {"esc", "cancel", false}}
}

func (f *jobForm) setFocus(i int) {
	f.focus = (i + fCount) % fCount
	f.title.Blur()
	f.brief.Blur()
	f.repo.Blur()
	f.verify.Blur()
	switch f.focus {
	case fTitle:
		f.title.Focus()
	case fBrief:
		f.brief.Focus()
	case fRepo:
		f.repo.Focus()
	case fVerify:
		f.verify.Focus()
	}
}

func (m *Model) formKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	s := k.String()
	switch s {
	case "esc":
		m.form, m.mode = nil, modeNormal
		return m, nil
	case "tab", "shift+tab":
		d := 1
		if s == "shift+tab" {
			d = -1
		}
		f.setFocus(f.focus + d)
		return m, nil
	case "ctrl+s":
		return m, m.reviewForm()
	}
	switch f.focus {
	case fLead, fDevice:
		sel, opts := &f.pIdx, f.profiles
		if f.focus == fDevice {
			sel, opts = &f.dIdx, f.devices
		}
		switch s {
		case "left", "h":
			*sel = (*sel + len(opts) - 1) % len(opts)
		case "right", "l", " ":
			*sel = (*sel + 1) % len(opts)
		case "up":
			f.setFocus(f.focus - 1)
		case "down", "enter":
			f.setFocus(f.focus + 1)
		}
		return m, nil
	case fStart:
		switch s {
		case "enter":
			return m, m.reviewForm()
		case "up":
			f.setFocus(f.focus - 1)
		}
		return m, nil
	}
	var cmd tea.Cmd
	switch f.focus {
	case fTitle:
		if s == "enter" || s == "down" {
			f.setFocus(f.focus + 1)
			return m, nil
		}
		f.title, cmd = f.title.Update(k)
	case fBrief:
		f.brief, cmd = f.brief.Update(k)
	case fRepo:
		if s == "enter" || s == "down" {
			f.setFocus(f.focus + 1)
			return m, nil
		}
		if s == "up" {
			f.setFocus(f.focus - 1)
			return m, nil
		}
		f.repo, cmd = f.repo.Update(k)
	case fVerify:
		if s == "enter" || s == "down" {
			f.setFocus(f.focus + 1)
			return m, nil
		}
		if s == "up" {
			f.setFocus(f.focus - 1)
			return m, nil
		}
		f.verify, cmd = f.verify.Update(k)
	}
	return m, cmd
}

// request is what the form will send.
func (f *jobForm) request() api.JobNewReq {
	return api.JobNewReq{
		Title:       strings.TrimSpace(f.title.Value()),
		Body:        strings.TrimSpace(f.brief.Value()),
		LeadProfile: f.profiles[f.pIdx],
		Device:      f.devices[f.dIdx],
		Repo:        strings.TrimSpace(f.repo.Value()),
		Verify:      strings.TrimSpace(f.verify.Value()),
	}
}

// reviewForm asks for the yes that starts the job; the hub's own checks run when it arrives.
func (m *Model) reviewForm() tea.Cmd {
	f := m.form
	req := f.request()
	f.err = ""
	if req.Title == "" {
		f.err = "A job needs a title."
		f.setFocus(fTitle)
		return nil
	}
	lines := []string{"Title: " + req.Title}
	if req.Body != "" {
		lines = append(lines, "Brief: "+req.Body)
	}
	if req.LeadProfile != "" {
		lines = append(lines, "A lead starts from the profile "+req.LeadProfile+".")
	} else {
		lines = append(lines, "No lead is started; you add one later.")
	}
	if req.Device != "" {
		lines = append(lines, "Machine: "+req.Device)
	}
	if req.Repo != "" {
		lines = append(lines, "Repository: "+req.Repo+" (each agent works in its own copy)")
	}
	if req.Verify != "" {
		lines = append(lines, "Check after each submission: "+req.Verify)
	}
	m.ask(&pendingAction{
		title: "Start this job?",
		lines: lines,
		yes:   "Start job",
		form:  true,
		run: func(a API) (string, error) {
			j, err := a.NewJob(req)
			if err != nil {
				return "", err
			}
			return "Started job #" + itoa(int(j.ID)) + ".", nil
		},
	})
	return nil
}

func (m *Model) formView(w, h int) string {
	f := m.form
	inner := w - 4
	lab := 15
	fw := inner - lab - 2
	f.title.Width = fw - 1
	f.repo.Width = fw - 1
	f.verify.Width = fw - 1
	f.brief.SetWidth(fw)
	line := func(i int, label, val string) string {
		mark := "  "
		l := sMuted.Render(fit(label, lab))
		if f.focus == i {
			mark = sAccent.Render("›") + " "
			l = sBold.Render(fit(label, lab))
		}
		return mark + l + val
	}
	chooser := func(i int, opts []string, idx int, none string) string {
		v := opts[idx]
		if v == "" {
			v = none
		}
		if f.focus == i && len(opts) > 1 {
			return sBold.Render("‹ " + v + " ›")
		}
		return v
	}
	var L []string
	L = append(L, "")
	if f.err != "" {
		for _, l := range wrap(f.err, inner-2) {
			L = append(L, sBad.Render("✗ ")+l)
		}
		L = append(L, "")
	}
	L = append(L, line(fTitle, "Title", f.title.View()))
	L = append(L, "")
	bl := strings.Split(f.brief.View(), "\n")
	for i := 0; i < 4; i++ {
		lb, v := "", ""
		if i == 0 {
			lb = "Brief"
		}
		if i < len(bl) {
			v = bl[i]
		}
		L = append(L, line(fBrief, lb, v))
	}
	L = append(L, "")
	L = append(L, line(fLead, "Lead profile", chooser(fLead, f.profiles, f.pIdx, "none, add a lead later")))
	L = append(L, line(fDevice, "Machine", chooser(fDevice, f.devices, f.dIdx, "let the hub choose")))
	L = append(L, line(fRepo, "Repository", f.repo.View()))
	L = append(L, line(fVerify, "Check command", f.verify.View()))
	L = append(L, "")
	btn := sMuted.Render("Start job")
	if f.focus == fStart {
		btn = sAccent.Render("Start job") + sMuted.Render("  (enter)")
	}
	L = append(L, line(fStart, "", btn))
	if !f.loaded {
		L = append(L, "", sMuted.Render("  Loading profiles and machines…"))
	} else if len(f.profiles) == 1 {
		L = append(L, "", sMuted.Render("  No profiles yet: add one in the web UI (Profiles, Starter library) to have a lead started for you."))
	}
	return panel("New job", L, w, h, true)
}
