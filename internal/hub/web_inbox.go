package hub

import (
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
	Start    []startStep // the first-run checklist, while something on it is not done
}

// startStep is one line of the "Get started" checklist.
type startStep struct {
	Title, Hint, Link, Action string
	Done                      bool
}

// startSteps is what a new owner still has to do, in order. It is empty once
// all of it is done, and for viewers, who cannot do any of it.
func (q *webReq) startSteps() ([]startStep, error) {
	if q.human.Role == store.RoleViewer {
		return nil, nil
	}
	ds, err := store.ListDevices(q.c.tx)
	if err != nil {
		return nil, err
	}
	joined := 0
	for _, d := range ds {
		if d.Joined && !d.Revoked.Valid {
			joined++
		}
	}
	var profiles, jobs int
	if err := q.c.tx.QueryRow(`SELECT count(DISTINCT name) FROM profile`).Scan(&profiles); err != nil {
		return nil, err
	}
	if err := q.c.tx.QueryRow(`SELECT count(*) FROM task WHERE kind = 'job'`).Scan(&jobs); err != nil {
		return nil, err
	}
	steps := []startStep{
		{Title: "The hub is running", Done: true},
		{Title: "Join a machine", Done: joined > 0, Link: "/devices#add", Action: "Add a device",
			Hint: "Create a join command, paste it on a machine that has git, tmux and an agent CLI (Claude Code, Codex, ...), then run handloom doctor there."},
		{Title: "Make a profile", Done: profiles > 0, Link: "/profiles/new", Action: "New profile",
			Hint: "A profile says which CLI an agent uses and what it may do. Make one for a lead and one for workers."},
		{Title: "Start your first job", Done: jobs > 0, Link: "/jobs/new", Action: "New job",
			Hint: "Describe the work. The lead plans it and starts the workers."},
	}
	for _, s := range steps {
		if !s.Done {
			return steps, nil
		}
	}
	return nil, nil
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
	if v.Start, err = q.startSteps(); err != nil {
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
