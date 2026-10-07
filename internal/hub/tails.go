package hub

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"handloom/internal/api"
)

// The terminal view. The hub cannot call a device, so a person who opens an
// agent's page asks for its screen: the hub notes "wanted" for a short time and
// wakes the device's link, which reads the end of the agent's terminal and
// posts it. The text is kept in memory only, for a minute: it is never written
// to the database, the audit log or a backup.

const (
	tailWant    = 45 * time.Second
	tailKeep    = 60 * time.Second
	tailMaxText = 8 << 10
)

type tailStore struct {
	mu   sync.Mutex
	want map[string]time.Time // agent name -> wanted until
	snap map[string]tailSnap
}

type tailSnap struct {
	text string
	at   time.Time
}

func (t *tailStore) init() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.want == nil {
		t.want, t.snap = map[string]time.Time{}, map[string]tailSnap{}
	}
}

// ask marks an agent's screen as wanted until a little from now.
func (t *tailStore) ask(name string, now time.Time) {
	t.init()
	t.mu.Lock()
	t.want[name] = now.Add(tailWant)
	t.mu.Unlock()
}

func (t *tailStore) wanted(names map[string]bool, now time.Time) []string {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for n, until := range t.want {
		if !until.After(now) {
			delete(t.want, n)
			delete(t.snap, n)
			continue
		}
		if names[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (t *tailStore) put(name, text string, now time.Time) bool {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	if until, ok := t.want[name]; !ok || !until.After(now) {
		return false
	}
	t.snap[name] = tailSnap{text, now}
	return true
}

func (t *tailStore) get(name string, now time.Time) (tailSnap, bool) {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.snap[name]
	if !ok || now.Sub(s.at) > tailKeep {
		return tailSnap{}, false
	}
	return s, true
}

// deviceTails is what a link asks for: the agents on its device whose screen is wanted.
func deviceTails(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list the screens wanted from it")
	}
	rows, err := c.tx.Query(`SELECT name FROM agent WHERE device_id = ?`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		mine[n] = true
	}
	rows.Close()
	out := c.h.tails.wanted(mine, c.now)
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// tailPut is the link posting what it read from a terminal.
func tailPut(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that runs an agent reports its screen")
	}
	var req api.TailReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	name := c.r.PathValue("name")
	a, err := c.agentByName(name)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not on this device", name)
	}
	text := req.Text
	if len(text) > tailMaxText {
		text = text[len(text)-tailMaxText:]
	}
	if !c.h.tails.put(name, text, c.now) {
		return nil, conflict("nobody is looking at %s's screen", name)
	}
	if err := c.emit(a.projectID, 0, "ui.tail", map[string]any{"agent": name}); err != nil {
		return nil, err
	}
	return nil, nil
}

// watchAllowed says whether a person may look at an agent's screen. Viewers
// may not: a screen can hold anything the agent printed.
func (q *webReq) watchAllowed(a *agentRow) (bool, string) {
	if q.human.Role == "viewer" {
		return false, "Viewers cannot look at an agent's terminal."
	}
	if a.jobID.Valid {
		if j, err := q.c.task(a.jobID.Int64); err == nil && j.confidential && !q.c.h.opt.AllowConfidential {
			return false, "This agent works on a confidential job, and this hub is not set up to carry that material."
		}
	}
	return true, ""
}

// webWatch is the page saying "I am looking": it keeps the screen wanted.
func (h *Hub) webWatch(q *webReq) error {
	a, err := q.c.agentByName(q.r.PathValue("name"))
	if err != nil {
		return err
	}
	if a == nil {
		http.Error(q.w, "no such agent", 404)
		return errHandled
	}
	if ok, why := q.watchAllowed(a); !ok {
		http.Error(q.w, why, 403)
		return errHandled
	}
	h.tails.ask(a.name, q.now)
	if err := q.c.emit(a.projectID, a.id, "pane.want", map[string]any{"agent": a.name}); err != nil {
		return err
	}
	q.w.WriteHeader(http.StatusNoContent)
	return nil
}

// webTail returns the last screen read, as plain text.
func (h *Hub) webTail(q *webReq) error {
	a, err := q.c.agentByName(q.r.PathValue("name"))
	if err != nil {
		return err
	}
	if a == nil {
		http.Error(q.w, "no such agent", 404)
		return errHandled
	}
	if ok, why := q.watchAllowed(a); !ok {
		http.Error(q.w, why, 403)
		return errHandled
	}
	q.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	s, ok := h.tails.get(a.name, q.now)
	if !ok {
		q.w.Write([]byte("Waiting for the machine to send the screen…"))
		return nil
	}
	q.w.Header().Set("X-Screen-Age", q.now.Sub(s.at).Round(time.Second).String())
	q.w.Write([]byte(strings.TrimRight(s.text, "\n ")))
	return nil
}
