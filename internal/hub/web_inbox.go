package hub

import (
	"strings"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// ---- the inbox ----

type itemView struct {
	api.DigestItem
	Ago string
}

type agentView struct {
	api.Agent
	Glyph string
	Ago   string
}

type activityView struct {
	Ago  string
	Text string
}

type inboxView struct {
	NeedsYou []itemView
	ToReview []itemView
	Running  []itemView
	Agents   []agentView
	Activity []activityView
	Seq      int64
	CanAct   bool        // owners and members may answer and review; viewers only look
	Start    *startList // the first-run checklist, while something on it is not done
}

// startStep is one line of the "Get started" checklist.
type startStep struct {
	N                         int
	Title, Hint, Link, Action string
	Note                      string // what is true now, in words ("the office computer is connected")
	Icon                      string
	Done                      bool
}

// startList is the checklist and how far along it is.
type startList struct {
	Steps    []startStep
	Done     int
	Next     *startStep // the first step not done; nil when everything is
	Complete bool
	Pct      int // done, as a percentage, for the bar
}

// inboxStart is the checklist for the inbox card: nothing once it is all done, and for viewers, who cannot do any of it.
func (q *webReq) inboxStart() (*startList, error) {
	if q.human.Role == store.RoleViewer {
		return nil, nil
	}
	l, err := q.startList()
	if err != nil || l.Complete {
		return nil, err
	}
	return l, nil
}

// startList reads each step's state from what the hub already holds: nothing is stored for it.
func (q *webReq) startList() (*startList, error) {
	ds, err := store.ListDevices(q.c.tx, q.c.deviceScope())
	if err != nil {
		return nil, err
	}
	var joinedNames []string
	for _, d := range ds {
		if d.Joined && !d.Revoked.Valid {
			joinedNames = append(joinedNames, d.Name)
		}
	}
	var profiles, projects, withChecks, jobs, accepted int
	for _, c := range []struct {
		sql string
		to  *int
	}{
		{`SELECT count(DISTINCT name) FROM profile`, &profiles},
		{`SELECT count(*) FROM codebase`, &projects},
		{`SELECT count(*) FROM codebase WHERE check_cmd != ''`, &withChecks},
		{`SELECT count(*) FROM task WHERE kind = 'job'`, &jobs},
		{`SELECT count(*) FROM task WHERE kind = 'task' AND status = 'done'`, &accepted},
	} {
		if err := q.c.tx.QueryRow(c.sql).Scan(c.to); err != nil {
			return nil, err
		}
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	note := func(ok bool, s string) string {
		if ok {
			return s
		}
		return ""
	}
	steps := []startStep{
		{Title: "Join a computer", Icon: "server", Done: len(joinedNames) > 0, Link: "/devices#add", Action: "Add a computer",
			Note: note(len(joinedNames) > 0, strings.Join(joinedNames, ", ")+" connected"),
			Hint: "Create a join command, paste it on a computer that has git, tmux and an agent CLI (Claude Code, Codex, ...), then run handloom doctor there. This is the one step that happens outside the browser."},
		{Title: "Add a profile", Icon: "file-user", Done: profiles > 0, Link: "/profiles/starters", Action: "Browse the starter library",
			Note: note(profiles > 0, plural(profiles, "profile", "profiles")),
			Hint: "A profile says which agent program a helper uses and what it may do. Take a ready-made one, or write your own. A profile that writes code is enough to start."},
		{Title: "Add a project", Icon: "library", Done: projects > 0, Link: "/projects", Action: "Add a project",
			Note: note(projects > 0, plural(projects, "project", "projects")),
			Hint: "Let Handloom hold the repository, or attach one from GitHub or another host. Then ask your computer for its own copy."},
		{Title: "Add the checks", Icon: "circle-check", Done: withChecks > 0, Link: "/projects", Action: "Add checks to a project",
			Note: note(withChecks > 0, plural(withChecks, "project has", "projects have")+" checks"),
			Hint: "Checks give every result an objective pass or fail. Handloom prepares them and shows you the files first; nothing changes until you confirm."},
		{Title: "Ask for something small", Icon: "play", Done: jobs > 0, Link: "/jobs/new", Action: "Ask for a change",
			Note: note(jobs > 0, plural(jobs, "job", "jobs")+" asked for"),
			Hint: "Use Just ask: one sentence about a small change you could review in two minutes. A helper does it on a copy."},
		{Title: "Use a result", Icon: "git-merge", Done: accepted > 0, Link: "/jobs", Action: "Open your jobs",
			Note: note(accepted > 0, plural(accepted, "result", "results")+" accepted"),
			Hint: "Open the job, read what changed and the check result, then press Use this result. Merge it when you want it in the project."},
	}
	l := &startList{Steps: steps, Complete: true}
	for i := range l.Steps {
		l.Steps[i].N = i + 1
		if l.Steps[i].Done {
			l.Done++
		} else {
			l.Complete = false
			if l.Next == nil {
				l.Next = &l.Steps[i]
			}
		}
	}
	l.Pct = l.Done * 100 / len(l.Steps)
	return l, nil
}

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min ago", int(d.Minutes()+0.5))
	case d < 36*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()+0.5))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24+0.5))
}

func stateGlyph(state string) string {
	switch state {
	case api.StateWorking:
		return "●"
	case api.StateIdle:
		return "○"
	case api.StateBlocked:
		return "!"
	}
	return "·"
}

func (q *webReq) inboxView() (*inboxView, error) {
	d, err := q.c.digest()
	if err != nil {
		return nil, err
	}
	v := &inboxView{Seq: d.Seq, CanAct: q.human.Role != store.RoleViewer}
	if v.Start, err = q.inboxStart(); err != nil {
		return nil, err
	}
	conv := func(in []api.DigestItem) []itemView {
		out := make([]itemView, 0, len(in))
		for _, it := range in {
			out = append(out, itemView{it, ago(q.now, it.At)})
		}
		return out
	}
	v.NeedsYou, v.ToReview, v.Running = conv(d.NeedsYou), conv(d.ToReview), conv(d.Running)
	for _, a := range d.Agents {
		v.Agents = append(v.Agents, agentView{a, stateGlyph(a.State), ago(q.now, a.StateAt)})
	}
	for _, a := range d.Activity {
		v.Activity = append(v.Activity, activityView{ago(q.now, a.At), a.Text})
	}
	return v, nil
}

var notices = map[string]string{
	"answered": "Answer sent. The lead is being woken.",
	"accepted": "Task accepted.",
	"rejected": "Task sent back to its owner.",
}

func (q *webReq) inboxPage(status int, errMsg string) error {
	v, err := q.inboxView()
	if err != nil {
		q.c.h.render(q.w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
		return err
	}
	q.page(status, "inbox", pageData{Title: "Inbox", Error: errMsg, Notice: notices[q.r.URL.Query().Get("done")], Extra: v})
	return nil
}

func (h *Hub) webInbox(q *webReq) error { return q.inboxPage(200, "") }

// webInboxFragment is the inbox body alone, fetched by the page when an event arrives.
func (h *Hub) webInboxFragment(q *webReq) error {
	v, err := q.inboxView()
	if err != nil {
		return err
	}
	var buf = &bytesBuffer{}
	if err := h.pages["inbox"].ExecuteTemplate(buf, "inboxbody", pageData{Human: q.human, CSRF: q.sess.CSRF, Extra: v}); err != nil {
		return err
	}
	q.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q.w.Write(buf.b)
	return nil
}

type bytesBuffer struct{ b []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) { b.b = append(b.b, p...); return len(p), nil }

// actionFailed shows the hub's own refusal (already answered, not allowed, ...)
// on the inbox, in words.
func (q *webReq) actionFailed(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		// The failed statement may have left the transaction half-done, so it
		// is abandoned, and the page is rendered from a fresh read-only view.
		// The hub has a single connection: finish with the old transaction
		// (and the audit write) before opening the next.
		q.c.tx.Rollback()
		if ae.status == 403 {
			q.c.h.auditDenied(q.c.p, q.r, ae)
		}
		tx, terr := q.c.h.db.Begin()
		if terr != nil {
			return terr
		}
		defer tx.Rollback()
		q.c.tx = tx
		q.inboxPage(ae.status, ae.msg)
		return errHandled
	}
	q.c.h.render(q.w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
	return err
}

// errHandled tells the wrapper that the response is written and nothing should be committed.
var errHandled = errors.New("handled")

func (h *Hub) webAnswer(q *webReq) error {
	if err := q.c.allow(ActEscalationAnswer); err != nil {
		return q.actionFailed(err)
	}
	id, err := q.c.pathID()
	if err != nil {
		return q.actionFailed(err)
	}
	if _, err := q.c.answerEscalation(id, q.r.PostForm.Get("answer")); err != nil {
		return q.actionFailed(err)
	}
	http.Redirect(q.w, q.r, "/inbox?done=answered", http.StatusSeeOther)
	return nil
}

func (h *Hub) webAccept(q *webReq) error {
	if _, err := taskAccept(q.c); err != nil {
		return q.actionFailed(err)
	}
	http.Redirect(q.w, q.r, "/inbox?done=accepted", http.StatusSeeOther)
	return nil
}

func (h *Hub) webReject(q *webReq) error {
	if err := q.c.allow(ActTaskManage); err != nil {
		return q.actionFailed(err)
	}
	t, err := q.c.pathTask()
	if err != nil {
		return q.actionFailed(err)
	}
	if _, err := q.c.rejectTask(t, q.r.PostForm.Get("reason")); err != nil {
		return q.actionFailed(err)
	}
	http.Redirect(q.w, q.r, "/inbox?done=rejected", http.StatusSeeOther)
	return nil
}

// ---- live updates ----

const (
	maxStreamsPerHuman = 4
	maxStreamsTotal    = 100
	streamPing         = 15 * time.Second
	streamRecheck      = time.Minute
	streamDebounce     = 400 * time.Millisecond
)

var streams = struct {
	sync.Mutex
	perHuman map[int64]int
	total    int
}{perHuman: map[int64]int{}}

func acquireStream(human int64) bool {
	streams.Lock()
	defer streams.Unlock()
	if streams.total >= maxStreamsTotal || streams.perHuman[human] >= maxStreamsPerHuman {
		return false
	}
	streams.total++
	streams.perHuman[human]++
	return true
}

func releaseStream(human int64) {
	streams.Lock()
	defer streams.Unlock()
	streams.total--
	if streams.perHuman[human]--; streams.perHuman[human] <= 0 {
		delete(streams.perHuman, human)
	}
}

// webStream is a server-sent-events stream of hints: "something changed, refetch".
// It never carries content, and it never holds a database connection while it
// waits (the hub has one connection; a waiting stream must not starve it).
func (h *Hub) webStream(w http.ResponseWriter, r *http.Request) {
	now := h.opt.Now()
	sess, human, err := h.lookupSession(h.db, r, now)
	if err != nil || sess == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	if !acquireStream(human.ID) {
		http.Error(w, "too many open streams", http.StatusTooManyRequests)
		return
	}
	defer releaseStream(human.ID)

	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // a stream outlives any server write timeout
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache, no-transform") // no-transform: proxies must not compress it
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	seq := func() int64 {
		var n int64
		h.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM event`).Scan(&n)
		return n
	}
	send := func(event string) bool {
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: 1\n\n", seq(), event); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	fmt.Fprint(w, "retry: 3000\n\n")
	if !send("resync") { // a (re)connecting page refetches once, so nothing is missed
		return
	}
	ping := time.NewTicker(streamPing)
	recheck := time.NewTicker(streamRecheck)
	defer ping.Stop()
	defer recheck.Stop()
	for {
		wake := h.waitChan()
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-recheck.C:
			if s, _, err := h.lookupSession(h.db, r, h.opt.Now()); err != nil || s == nil {
				return // signed out or expired: the page reloads and lands on the login form
			}
		case <-wake:
			// Coalesce a burst of events into one hint.
			select {
			case <-time.After(streamDebounce):
			case <-r.Context().Done():
				return
			}
			if !send("inbox-changed") {
				return
			}
		}
	}
}
