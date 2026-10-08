package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"handloom/internal/api"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestHeaderShowsHubNameRoleAndTheNeedsYouPill(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	v := m.View()
	mustContain(t, v, "hub.example.com", "afif · owner", "[Needs you 1]")
}

func TestOverviewShowsTheNumbersAndTheInsights(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	v := m.View()
	mustContain(t, v, "Last 7 days", "1 working", "1 idle", "88%", "of 17", "12m", "over 7 tasks",
		"Questions wait a long time", "Based on: 1 open question", "█", "none reported yet")
	mustNotContain(t, v, "$") // nothing is priced, so no cost is invented
}

func TestOverviewShowsCostOnlyWhenPriced(t *testing.T) {
	f := seeded()
	f.metrics.Spend = &Spend{Tokens: 2_000_000, Cost: 1.2, Priced: 1_000_000, HasPrice: true}
	m := start(t, f, 120, 40)
	mustContain(t, m.View(), "2.0M tokens", "about $1.20", "50% priced")

	f.metrics.Spend = &Spend{Tokens: 5000}
	m = start(t, f, 120, 40)
	v := m.View()
	mustContain(t, v, "5.0k tokens", "no prices set")
	mustNotContain(t, v, "$")
}

func TestRKeyCyclesTheRange(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "r")
	mustContain(t, m.View(), "Last 30 days")
	press(m, "r")
	mustContain(t, m.View(), "Last 24 hours")
}

func TestNumberAndTabKeysSwitchScreens(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "3")
	if m.tab != tabJobs {
		t.Fatalf("3 should open Jobs, got tab %d", m.tab)
	}
	press(m, "tab")
	if m.tab != tabAgents {
		t.Fatalf("tab should go on to Agents, got %d", m.tab)
	}
	press(m, "shift+tab", "shift+tab", "shift+tab")
	if m.tab != tabOverview {
		t.Fatalf("shift+tab should go back to Overview, got %d", m.tab)
	}
	press(m, "shift+tab")
	if m.tab != tabLibrary {
		t.Fatalf("shift+tab wraps to Library, got %d", m.tab)
	}
}

func TestInboxListsQuestionsThenReview(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "2")
	v := m.View()
	mustContain(t, v, "Needs you 1  ·  Work to review 1", "Question", "Which database should it use?", "To review", "Write the sign-in form", "lead-ui", "[2 Inbox 2]")
	if strings.Index(v, "Which database") > strings.Index(v, "Write the sign-in form") {
		t.Error("what needs you should come before work to review")
	}
}

func TestInboxEmptyStateSaysWhatWillAppear(t *testing.T) {
	f := seeded()
	f.digest.NeedsYou, f.digest.ToReview = nil, nil
	m := start(t, f, 120, 40)
	press(m, "2")
	mustContain(t, m.View(), "Nothing needs you right now.", "will appear here", "press 3, then n")
}

func TestAcceptNeedsYAndNIsHarmless(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "down", "a")
	mustContain(t, m.View(), "Accept task #7?", "Write the sign-in form", "worker-1", "y Accept", "n Cancel")
	if f.changes() != 0 {
		t.Fatal("something was changed before y")
	}
	press(m, "n")
	if f.changes() != 0 {
		t.Fatal("n made a call")
	}
	mustContain(t, m.View(), "Cancelled. Nothing was changed.")

	press(m, "a", "x") // other keys while asking do nothing
	if f.changes() != 0 || m.mode != modeConfirm {
		t.Fatalf("a stray key must not answer the question (changes=%d mode=%d)", f.changes(), m.mode)
	}
	press(m, "y")
	if len(f.accepted) != 1 || f.accepted[0] != 7 {
		t.Fatalf("y should accept task 7, accepted=%v", f.accepted)
	}
	mustContain(t, m.View(), "Accepted task #7.")
}

func TestAcceptRefusedByTheHubShowsItsWords(t *testing.T) {
	f := seeded()
	f.acceptErr = hubError(403, "a viewer may not accept work")
	m := start(t, f, 120, 40)
	press(m, "2", "down", "a", "y")
	mustContain(t, m.View(), "a viewer may not accept work")
}

func TestAcceptOnAQuestionExplains(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "a")
	mustContain(t, m.View(), "Only work waiting for review can be accepted.")
	if m.mode != modeNormal || f.changes() != 0 {
		t.Fatal("a on a question must do nothing")
	}
}

func TestInboxDetailShowsEvidenceCheckAndMerge(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "2", "down", "enter")
	v := m.View()
	mustContain(t, v, "To review task #7", "commit:3fa9", "test:go test -> ok", "all fields done", "passed on box", "./check.sh", "Waiting for the machine to merge", "Press a to accept")
	press(m, "esc")
	mustContain(t, m.View(), "Needs you 1  ·  Work to review 1")
}

func TestInboxDetailShowsAFailedCheckAndAConflict(t *testing.T) {
	f := seeded()
	f.task.Check = &api.TaskCheck{Device: "box", Command: "go test ./...", ExitCode: 1, Tail: "FAIL TestLogin\nexit status 1"}
	f.task.Merge = &api.TaskMerge{Status: "conflict", Detail: "both changed login.go"}
	m := start(t, f, 120, 40)
	press(m, "2", "down", "enter")
	mustContain(t, m.View(), "failed on box (exit 1)", "FAIL TestLogin", "Not merged (conflict)", "both changed login.go")
}

func TestSendBackAsksForAReasonThenConfirmsThenSends(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "down", "x")
	mustContain(t, m.View(), "Why send it back?")
	press(m, "enter") // empty reason
	mustContain(t, m.View(), "Say what to fix")
	if m.mode != modeInput {
		t.Fatal("an empty reason must keep the input open")
	}
	typeText(m, "add a wrong password test")
	press(m, "enter")
	mustContain(t, m.View(), "Send task #7 back?", "Reason: add a wrong password test", "y Send back")
	if f.changes() != 0 {
		t.Fatal("sent before y")
	}
	press(m, "y")
	if len(f.rejected) != 1 || f.rejected[0] != (reject{7, "add a wrong password test"}) {
		t.Fatalf("rejected = %+v", f.rejected)
	}
}

func TestEscCancelsTheReasonInput(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "down", "x")
	typeText(m, "no")
	press(m, "esc")
	if m.mode != modeNormal || f.changes() != 0 {
		t.Fatal("esc must cancel without a call")
	}
}

func TestAnswerAcceptsAnOptionNumberAndAsksFirst(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "e")
	mustContain(t, m.View(), "1-2 picks an option")
	typeText(m, "2")
	press(m, "enter")
	mustContain(t, m.View(), "Answer question #4?", "Your answer: postgres")
	press(m, "n")
	if f.changes() != 0 {
		t.Fatal("n sent an answer")
	}
	press(m, "e")
	typeText(m, "only sqlite please")
	press(m, "enter", "y")
	if len(f.answers) != 1 || f.answers[0] != (answer{4, "only sqlite please"}) {
		t.Fatalf("answers = %+v", f.answers)
	}
}

func TestFinishedJobCanBeClosedFromTheInbox(t *testing.T) {
	f := seeded()
	f.digest.ToReview = []api.DigestItem{{Kind: "job-done", ID: 1, Title: "Ship the login page", Detail: "Every task is done."}}
	m := start(t, f, 120, 40)
	press(m, "2", "down", "c")
	mustContain(t, m.View(), "Close job #1?", "marked finished")
	press(m, "y")
	if len(f.closed) != 1 || f.closed[0] != (closeCall{1, false}) {
		t.Fatalf("closed = %+v", f.closed)
	}
}

func TestJobsListAndDetail(t *testing.T) {
	f := seeded()
	f.jobTasks = []api.Task{
		{ID: 2, Title: "Write the form", Status: "done", Owner: "worker-1", Merge: &api.TaskMerge{Status: "merged"}},
		{ID: 3, Title: "Wire the API", Status: "submitted", Owner: "worker-2", Check: &api.TaskCheck{ExitCode: 1}},
		{ID: 4, Title: "Rate limiting", Status: "open", BlockedReason: "waiting for a decision"},
	}
	m := start(t, f, 120, 40)
	press(m, "3")
	mustContain(t, m.View(), "Ship the login page", "lead-ui", "Claimed", "● open")
	press(m, "enter")
	mustContain(t, m.View(), "Job #1", "Started", "by afif", "Write the form", "worker-1", "✓ merged", "✗ failed", "to review", "blocked: waiting for a decision")
}

func TestJobsEmptyStateSaysHowToStartOne(t *testing.T) {
	f := seeded()
	f.jobs = nil
	m := start(t, f, 120, 40)
	press(m, "3")
	mustContain(t, m.View(), "No jobs yet.", "Press n to start the first one.")
}

func TestClosingAJobAsksAndCancelIsDifferent(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "3", "c")
	mustContain(t, m.View(), "Close job #1?", "still has 3 unfinished tasks")
	press(m, "n")
	press(m, "C")
	mustContain(t, m.View(), "Cancel job #1?", "cannot be undone")
	if f.changes() != 0 {
		t.Fatal("changed before y")
	}
	press(m, "y")
	if len(f.closed) != 1 || f.closed[0] != (closeCall{1, true}) {
		t.Fatalf("closed = %+v", f.closed)
	}
}

func TestNewJobFormSendsWhatWasTypedAfterConfirm(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "3", "n")
	mustContain(t, m.View(), "New job", "Title", "Brief", "Lead profile", "Machine", "Repository", "Check command")
	typeText(m, "Fix billing")
	press(m, "tab")
	typeText(m, "Totals are wrong")
	press(m, "ctrl+s")
	mustContain(t, m.View(), "Start this job?", "Title: Fix billing", "Totals are wrong", "profile lead", "Machine: box")
	if len(f.newJobs) != 0 {
		t.Fatal("started before y")
	}
	press(m, "y")
	if len(f.newJobs) != 1 {
		t.Fatalf("newJobs = %+v", f.newJobs)
	}
	got := f.newJobs[0]
	if got.Title != "Fix billing" || got.Body != "Totals are wrong" || got.LeadProfile != "lead" || got.Device != "box" {
		t.Errorf("request = %+v", got)
	}
	if m.mode != modeNormal || m.tab != tabJobs {
		t.Error("a started job closes the form and shows the jobs")
	}
	mustContain(t, m.View(), "Started job #9.")
}

func TestNewJobFormNeedsATitleAndShowsTheHubsWords(t *testing.T) {
	f := seeded()
	f.newJobErr = hubError(400, "a verify command needs a repository: it runs in the agent's worktree")
	m := start(t, f, 120, 40)
	press(m, "3", "n", "ctrl+s")
	mustContain(t, m.View(), "A job needs a title.")
	if m.mode != modeForm {
		t.Fatal("the form stays open")
	}
	typeText(m, "Fix billing")
	press(m, "ctrl+s", "y")
	v := m.View()
	mustContain(t, v, "a verify command needs a repository", "New job")
	if m.mode != modeForm || m.form == nil {
		t.Fatal("the form must stay open with its text after a hub error")
	}
	mustContain(t, v, "Fix billing")
}

func TestNewJobFormCancelAsksNothing(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "3", "n")
	typeText(m, "x")
	press(m, "esc")
	if m.mode != modeNormal || m.form != nil || f.changes() != 0 {
		t.Fatal("esc closes the form with no call")
	}
}

func TestAgentsTableAndDetail(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "4")
	v := m.View()
	mustContain(t, v, "lead-ui", "worker-1", "● working", "○ idle", "claude", "box", "job #1", "Selected agent", "Press enter to watch")
	press(m, "down")
	mustContain(t, m.View(), "codex")
}

func TestAgentTerminalWaitsThenShowsTextAndFollowToggles(t *testing.T) {
	f := seeded()
	f.screen = api.Screen{Pending: true}
	m := start(t, f, 120, 40)
	press(m, "4", "enter")
	mustContain(t, m.View(), "lead-ui · terminal", "Waiting for the machine")

	f.screen = api.Screen{Text: "line one\n\x1b[31mred line\x1b[0m\nline three", AgeSeconds: 3}
	_, cmd := m.Update(screenTickMsg{agent: "lead-ui", gen: m.ag.gen})
	run(m, cmd)
	v := m.View()
	mustContain(t, v, "line one", "red line", "Read 3s ago", "following")
	mustNotContain(t, v, "\x1b")
	mustContain(t, m.keysLine(), "follow: on")

	press(m, "f")
	mustContain(t, m.View(), "follow off")
	mustContain(t, m.keysLine(), "follow: off")
	press(m, "esc")
	mustContain(t, m.View(), "Selected agent")
}

func TestAgentTerminalRefusedForViewersIsAPlainMessageAndStopsAsking(t *testing.T) {
	f := seeded()
	f.screenErr = hubError(403, "Viewers cannot look at an agent's terminal.")
	m := start(t, f, 120, 40)
	press(m, "4")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	mustContain(t, m.View(), "Viewers cannot look at an agent's terminal.")
	before := f.screens
	_, next := m.Update(screenTickMsg{agent: "lead-ui", gen: m.ag.gen})
	if next != nil {
		t.Fatal("it keeps asking after a refusal")
	}
	if f.screens != before {
		t.Fatal("it called the hub after a refusal")
	}
}

func TestStaleScreenAnswersAreIgnored(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "4", "enter")
	old := m.ag.gen
	press(m, "esc", "enter")
	m.Update(screenMsg{agent: "lead-ui", gen: old, s: api.Screen{Text: "from the first look"}})
	mustNotContain(t, m.View(), "from the first look")
}

func TestLibraryShowsProfilesAndStartersWithThePlainSummary(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "5")
	run(m, m.lib.load(m))
	v := m.View()
	mustContain(t, v, "Profiles 2", "Starters 1", "lead", "coder", "What it can do")
	press(m, "s")
	v = m.View()
	mustContain(t, v, "researcher", "Research", "This agent can read files.", "Finds and reads sources")
}

func TestFilterNarrowsTheListAndEscClears(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	press(m, "4", "/")
	typeText(m, "worker")
	press(m, "enter")
	v := m.View()
	mustContain(t, v, "worker-1", "Filter: worker")
	mustNotContain(t, v, "lead-ui  ")
	press(m, "esc")
	mustContain(t, m.View(), "lead-ui")
}

func TestOfflineKeepsTheLastDataAndRecovers(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	m.Update(dataMsg{rng: m.rng, err: errOffline})
	v := m.View()
	mustContain(t, v, "Offline, retrying", "1 working", "Needs you 1")
	m.Update(dataMsg{rng: m.rng, metrics: &f.metrics, digest: &f.digest})
	mustNotContain(t, m.View(), "Offline")
}

func TestRealRefreshFailureKeepsData(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	f.digestErr = errOffline
	f.metricsErr = errOffline
	run(m, m.refresh())
	mustContain(t, m.View(), "Offline, retrying", "Needs you 1")
}

func TestUnauthorizedTellsThePersonWhatToDo(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	m.Update(dataMsg{rng: m.rng, err: hubError(401, "bad token")})
	mustContain(t, m.View(), "handloom token new")
}

func TestHubErrorThatIsNotOfflineIsShownInWords(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	m.Update(dataMsg{rng: m.rng, err: hubError(500, "the database is busy")})
	v := m.View()
	mustContain(t, v, "the database is busy")
	mustNotContain(t, v, "Offline")
}

func TestTooSmallWindowGetsAMessage(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	mustContain(t, m.View(), "at least 80 columns by 24 rows")
	press(m, "q") // still quits
}

func TestNothingOverflowsAtAnySize(t *testing.T) {
	f := seeded()
	for _, sz := range [][2]int{{80, 24}, {100, 30}, {120, 40}, {160, 50}} {
		m := start(t, f, sz[0], sz[1])
		for tabN := 1; tabN <= 5; tabN++ {
			press(m, itoa(tabN))
			run(m, m.lib.load(m))
			check := func(label string) {
				lines := strings.Split(m.View(), "\n")
				if len(lines) != sz[1] {
					t.Errorf("%dx%d %s: %d lines, want %d", sz[0], sz[1], label, len(lines), sz[1])
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w > sz[0] {
						t.Errorf("%dx%d %s line %d is %d wide: %q", sz[0], sz[1], label, i, w, l)
					}
				}
			}
			check("tab " + itoa(tabN))
			press(m, "enter")
			check("tab " + itoa(tabN) + " opened")
			press(m, "esc")
		}
		press(m, "?")
		lines := strings.Split(m.View(), "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d help: %d lines", sz[0], sz[1], len(lines))
		}
		press(m, "x")
		press(m, "3", "n")
		if got := len(strings.Split(m.View(), "\n")); got != sz[1] {
			t.Errorf("%dx%d form: %d lines", sz[0], sz[1], got)
		}
	}
}

func TestHelpListsEveryKey(t *testing.T) {
	m := start(t, seeded(), 160, 60)
	press(m, "?")
	v := m.View()
	mustContain(t, v, "go to a screen", "accept work that is waiting for review", "send it back, with a reason", "answer a question",
		"start a new job", "watch the agent's terminal", "follow the end of the terminal", "switch between profiles and starters",
		"filter the list", "refresh now", "quit", "yes or no to what an action")
	press(m, "x")
	if m.mode != modeNormal {
		t.Fatal("a key closes the help")
	}
}

func TestQuitKeys(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	for _, k := range []string{"q", "ctrl+c"} {
		var msg tea.KeyMsg
		if k == "q" {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		}
		_, cmd := m.Update(msg)
		if cmd == nil {
			t.Fatalf("%s should quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s did not return a quit", k)
		}
	}
	press(m, "?")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c quits from any mode")
	}
}

func TestTheCursorStaysOnTheSameItemWhenDataChanges(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "down") // on the task to review
	f.digest.NeedsYou = append([]api.DigestItem{{Kind: "escalation", ID: 99, Title: "A new question", Who: "lead-ui"}}, f.digest.NeedsYou...)
	run(m, m.refresh())
	items := m.inboxItems()
	if items[m.ibx.list.sel].it.ID != 7 {
		t.Fatalf("the cursor jumped to %+v", items[m.ibx.list.sel].it)
	}
}

func TestAnItemThatDisappearsClosesItsDetail(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	press(m, "2", "down", "enter")
	f.digest.ToReview = nil
	run(m, m.refresh())
	if m.ibx.open {
		t.Fatal("the detail of work that is gone should close")
	}
	mustContain(t, m.View(), "no longer waiting for you")
}

func TestTickRefreshesWithoutStackingRequests(t *testing.T) {
	f := seeded()
	m := start(t, f, 120, 40)
	m.inflight = true
	_, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("the tick must re-arm itself")
	}
	m.inflight = false
	_, cmd = m.Update(tickMsg{})
	if cmd == nil || !m.inflight {
		t.Fatal("a tick starts a refresh when none is running")
	}
}

func TestNoColourTerminalMarksTheActiveTab(t *testing.T) {
	m := start(t, seeded(), 120, 40)
	if !strings.Contains(m.View(), "[1 Overview]") {
		t.Error("without colour the active tab needs a visible mark")
	}
}

func TestSpanWords(t *testing.T) {
	cases := map[time.Duration]string{5 * time.Second: "5s", 3 * time.Minute: "3m", 90 * time.Minute: "1h 30m", 5 * time.Hour: "5h", 72 * time.Hour: "3d"}
	for d, want := range cases {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
}
