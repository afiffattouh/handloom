package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"handloom/internal/api"
)

// pendingAction is a change waiting for a yes. Nothing reaches the hub until
// the person presses y; the lines say exactly what will happen.
type pendingAction struct {
	title string
	lines []string
	yes   string // what the button says: "Accept", "Send back"
	done  string // the status line after it worked
	run   func(API) (string, error)
	form  bool
}

func (m *Model) ask(p *pendingAction) {
	m.confirm = p
	m.back = m.mode
	if m.back == modeConfirm || m.back == modeInput {
		m.back = modeNormal
	}
	m.mode = modeConfirm
}

func (m *Model) confirmBox() string {
	p := m.confirm
	if p == nil {
		return ""
	}
	w := 70
	if w > m.w-4 {
		w = m.w - 4
	}
	var lines []string
	for _, l := range p.lines {
		lines = append(lines, wrap(l, w-4)...)
	}
	lines = append(lines, "", sAccent.Render("y")+" "+sBold.Render(p.yes)+"    "+sBold.Render("n")+" "+sMuted.Render("Cancel"))
	return panel(p.title, append([]string{""}, lines...), w, len(lines)+4, true)
}

func (m *Model) confirmKey(s string) (tea.Model, tea.Cmd) {
	p := m.confirm
	switch s {
	case "y", "Y":
		m.confirm, m.mode = nil, m.back
		if p == nil {
			return m, nil
		}
		a := m.api
		return m, func() tea.Msg {
			text, err := p.run(a)
			if text == "" {
				text = p.done
			}
			return actionMsg{text: text, err: err, form: p.form}
		}
	case "n", "N", "esc":
		m.confirm, m.mode = nil, m.back
		m.say("Cancelled. Nothing was changed.", true)
	}
	return m, nil
}

// ---- one-line inputs: a reason, an answer ----

type inputKind int

const (
	inputNone inputKind = iota
	inputReason
	inputAnswer
)

func (m *Model) startInput(kind inputKind, it api.DigestItem) {
	m.inputFor, m.inputTo, m.ibx.asking = kind, it.ID, it
	m.input.SetValue("")
	m.input.Placeholder = ""
	if kind == inputReason {
		m.input.Placeholder = "what should the agent change?"
	}
	m.input.Focus()
	m.mode = modeInput
}

func (m *Model) inputPrompt() string {
	switch m.inputFor {
	case inputReason:
		return sMuted.Render("Why send it back? ")
	case inputAnswer:
		if len(m.ibx.asking.Options) > 0 {
			return sMuted.Render("Your answer (1-" + itoa(len(m.ibx.asking.Options)) + " picks an option) ")
		}
	}
	return sMuted.Render("Your answer ")
}

func (m *Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNormal
		m.input.Blur()
		return m, nil
	case "enter":
		return m, m.finishInput()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *Model) finishInput() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	it := m.ibx.asking
	if text == "" {
		if m.inputFor == inputReason {
			m.say("Say what to fix, so the agent knows what to do.", false)
			m.input.Placeholder = "Say what to fix, so the agent knows what to do."
		} else {
			m.say("Type an answer first, or press esc to cancel.", false)
		}
		return nil
	}
	m.input.Blur()
	m.mode = modeNormal
	switch m.inputFor {
	case inputReason:
		id := it.ID
		m.ask(&pendingAction{
			title: fmt.Sprintf("Send task #%d back?", id),
			lines: []string{
				"Task: " + it.Title,
				"Reason: " + text,
				fmt.Sprintf("%s gets your reason and the task becomes theirs again to fix and submit.", orWord(it.Who, "The agent")),
			},
			yes:  "Send back",
			done: fmt.Sprintf("Sent task #%d back.", id),
			run:  func(a API) (string, error) { return "", a.Reject(id, text) },
		})
	case inputAnswer:
		if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(it.Options) {
			text = it.Options[n-1]
		}
		id := it.ID
		m.ask(&pendingAction{
			title: fmt.Sprintf("Answer question #%d?", id),
			lines: []string{
				"Question: " + it.Title,
				"Your answer: " + text,
				fmt.Sprintf("%s is told your answer and carries on.", orWord(it.Who, "The agent")),
			},
			yes:  "Send answer",
			done: fmt.Sprintf("Answered question #%d.", id),
			run:  func(a API) (string, error) { return "", a.Answer(id, text) },
		})
	}
	return nil
}

func orWord(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (m *Model) acceptAction(it api.DigestItem) *pendingAction {
	id := it.ID
	lines := []string{"Task: " + it.Title}
	if it.Who != "" {
		lines = append(lines, "Done by: "+it.Who)
	}
	if t := m.ibx.task; t != nil && t.ID == id {
		lines = append(lines, "Check: "+checkWords(t))
	}
	lines = append(lines, "The task is marked done. If the job has a repository, its machine merges the branch into the job's integration branch.")
	return &pendingAction{
		title: fmt.Sprintf("Accept task #%d?", id),
		lines: lines,
		yes:   "Accept",
		done:  fmt.Sprintf("Accepted task #%d.", id),
		run:   func(a API) (string, error) { return "", a.Accept(id) },
	}
}

func closeJobAction(j api.Job, cancel bool) *pendingAction {
	id := j.ID
	p := &pendingAction{run: func(a API) (string, error) { return "", a.CloseJob(id, cancel) }}
	open := j.Tasks.Open + j.Tasks.Claimed + j.Tasks.Submitted
	if cancel {
		p.title, p.yes, p.done = fmt.Sprintf("Cancel job #%d?", id), "Cancel job", fmt.Sprintf("Cancelled job #%d.", id)
		p.lines = []string{"Job: " + j.Title, fmt.Sprintf("The job and its %s are cancelled.", plural(open, "unfinished task", "unfinished tasks")), "This cannot be undone."}
	} else {
		p.title, p.yes, p.done = fmt.Sprintf("Close job #%d?", id), "Close job", fmt.Sprintf("Closed job #%d.", id)
		p.lines = []string{"Job: " + j.Title, "The job is marked finished."}
		if open > 0 {
			p.lines = append(p.lines, fmt.Sprintf("It still has %s; the hub may refuse until they are done or cancelled.", plural(open, "unfinished task", "unfinished tasks")))
		}
	}
	return p
}

// checkWords says in words what the device found when it ran the job's check.
func checkWords(t *api.Task) string {
	ck := t.Check
	if ck == nil {
		return "no check has run for this work"
	}
	switch {
	case ck.TimedOut:
		return "timed out on " + ck.Device
	case ck.ExitCode != 0:
		return fmt.Sprintf("failed on %s (exit %d)", ck.Device, ck.ExitCode)
	}
	return "passed on " + ck.Device
}
