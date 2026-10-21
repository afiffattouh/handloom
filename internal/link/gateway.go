package link

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"handloom/internal/addon"
	"handloom/internal/api"
)

// The tool gateway: how an agent a link started reaches the add-ons on this computer. Each spawn gets a tool
// token that only that spawn holds; the gateway acts for the spawn the token belongs to and for nobody the
// caller names. What the spawn may call is the snapshot the hub put into the spawn request, and the link
// enforces it here: the hub is not asked about a call. See docs/direction/addons.md.

// toolSession is what the link remembers about one spawn's tools. It lives in memory and ends with the spawn.
type toolSession struct {
	spawn        int64
	agent        string
	job          int64
	dir          string // the spawn's work folder; results go under <dir>/.handloom/results
	confidential bool
	tools        map[string]api.SpawnTool       // by ADDON.TOOL
	unavailable  map[string]api.ToolUnavailable // by ADDON.TOOL
	made         time.Time

	mu   sync.Mutex
	used map[string]int // calls let through, by ADDON.TOOL: the counter is per spawn and per tool
}

func toolKey(addonName, tool string) string { return addonName + "." + tool }

// toolGateway holds the sessions, found by the hash of their token. The token itself is not kept.
type toolGateway struct {
	mu     sync.Mutex
	byHash map[string]*toolSession
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mint makes a new random token for a spawn and remembers the spawn's snapshot under it. A spawn started
// again under the same agent name replaces the earlier one: the old token stops working.
func (g *toolGateway) mint(s api.Spawn, dir string, now time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "hlt_" + hex.EncodeToString(raw)
	sess := &toolSession{spawn: s.ID, agent: s.Name, dir: dir, confidential: s.Confidential, made: now,
		tools: map[string]api.SpawnTool{}, unavailable: map[string]api.ToolUnavailable{}, used: map[string]int{}}
	if s.Job != nil {
		sess.job = *s.Job
	}
	for _, t := range s.Tools {
		sess.tools[toolKey(t.Addon, t.Tool)] = t
	}
	for _, u := range s.ToolsUnavailable {
		sess.unavailable[toolKey(u.Addon, u.Tool)] = u
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byHash == nil {
		g.byHash = map[string]*toolSession{}
	}
	for h, old := range g.byHash {
		if old.agent == s.Name {
			delete(g.byHash, h)
			removeResults(old.dir) // the same helper started again: what its last run saved is stale
		}
	}
	g.byHash[tokenHash(token)] = sess
	return token, nil
}

func (g *toolGateway) lookup(token string) *toolSession {
	if token == "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.byHash[tokenHash(token)]
}

func (g *toolGateway) revoke(token string) {
	g.mu.Lock()
	delete(g.byHash, tokenHash(token))
	g.mu.Unlock()
}

// reap forgets the sessions that keep says are over.
func (g *toolGateway) reap(keep func(*toolSession) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for h, s := range g.byHash {
		if !keep(s) {
			delete(g.byHash, h)
			removeResults(s.dir) // the spawn is over; its results go with its token, the worktree stays
		}
	}
}

func (g *toolGateway) sessions() []*toolSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*toolSession, 0, len(g.byHash))
	for _, s := range g.byHash {
		out = append(out, s)
	}
	return out
}

// toolSessionLife and toolSessionGrace: a session is dropped when its agent is gone from the hub's list or its
// terminal window is gone, but not in the first minutes (the agent registers itself only once it has started),
// and at the latest after a day.
const (
	toolSessionGrace = 2 * time.Minute
	toolSessionLife  = 24 * time.Hour
)

// reapToolSessions ends the tokens of spawns that are over. agents is what the hub lists for this computer.
func (l *Link) reapToolSessions(ctx context.Context, agents []api.DeviceAgent) {
	sessions := l.gate.sessions()
	if len(sessions) == 0 {
		return
	}
	now := l.opt.Now()
	known := map[string]bool{}
	for _, a := range agents {
		known[a.Name] = true
	}
	// The windows the link's terminal server still has. When it cannot say (no server, an error) every
	// window counts as present, so a token is never dropped on a guess.
	windows := map[string]bool{}
	haveWindows := false
	for _, s := range sessions {
		if now.Sub(s.made) >= toolSessionGrace {
			out, err := l.opt.Tmux(ctx, "tmux", "-L", l.opt.TmuxSocket, "list-windows", "-a", "-F", "#{window_name}")
			if err == nil {
				haveWindows = true
				for _, w := range strings.Fields(out) {
					windows[w] = true
				}
			}
			break
		}
	}
	l.gate.reap(func(s *toolSession) bool {
		age := now.Sub(s.made)
		switch {
		case age >= toolSessionLife:
			return false
		case age < toolSessionGrace:
			return true
		case !known[s.agent]:
			return false
		case haveWindows && !windows[s.agent]:
			return false
		}
		return true
	})
}

// supervisor is the service supervisor for this computer's add-ons, made on first use.
func (l *Link) supervisor() *addon.Supervisor {
	l.jmu.Lock()
	defer l.jmu.Unlock()
	if l.sup == nil {
		l.sup = addon.NewSupervisor(l.opt.AddonRoot, addon.NewStore(l.opt.AddonRoot))
	}
	return l.sup
}

// closeAddons stops the add-on services the link started.
func (l *Link) closeAddons() {
	l.jmu.Lock()
	sup := l.sup
	l.jmu.Unlock()
	if sup != nil {
		sup.Close()
	}
}

// toolSessionFor is the spawn a request comes from, known by its tool token and by nothing else (the agent name a
// caller sends is not looked at). An unknown or missing token is refused.
func (l *Link) toolSessionFor(w http.ResponseWriter, r *http.Request) *toolSession {
	if s := l.gate.lookup(r.Header.Get(api.ToolTokenHeader)); s != nil {
		return s
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(api.Error{Error: "the link does not know this helper's tool token. Tools can be called only from the helper the link started (and not after the link was restarted, or the helper ended)", Code: "unknown_token"})
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// toolList answers GET /local/tools: what this spawn may call, and what its profile is granted but it cannot have.
func (l *Link) toolList(w http.ResponseWriter, r *http.Request) {
	s := l.toolSessionFor(w, r)
	if s == nil {
		return
	}
	out := api.ToolList{Tools: []api.SpawnTool{}, Confidential: s.confidential}
	for _, t := range s.tools {
		out.Tools = append(out.Tools, t)
	}
	for _, u := range s.unavailable {
		out.Unavailable = append(out.Unavailable, u)
	}
	sort.Slice(out.Tools, func(i, j int) bool {
		return toolKey(out.Tools[i].Addon, out.Tools[i].Tool) < toolKey(out.Tools[j].Addon, out.Tools[j].Tool)
	})
	sort.Slice(out.Unavailable, func(i, j int) bool {
		return toolKey(out.Unavailable[i].Addon, out.Unavailable[i].Tool) < toolKey(out.Unavailable[j].Addon, out.Unavailable[j].Tool)
	})
	writeJSON(w, 200, out)
}

// localFacts asks the computer about an add-on and a tool: the manifest when it is the approved one, and
// what is wrong when it is not.
func (l *Link) localFacts(addonName, tool string) (addon.Tool, localState) {
	m, mh, res, ok := l.supervisor().CheckHash(addonName)
	if !ok {
		switch res.Code {
		case addon.CodeNotInstalled:
			return addon.Tool{}, localState{Missing: true}
		case addon.CodeChanged:
			return addon.Tool{}, localState{Changed: true}
		}
		return addon.Tool{}, localState{Broken: res.Error}
	}
	t, found := m.Tool(tool)
	if !found {
		return addon.Tool{}, localState{Missing: true}
	}
	return t, localState{ManifestHash: mh}
}

// newCallID is a random id for one call, unique within the spawn that makes it.
func newCallID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "c" + hex.EncodeToString(b)
}

// toolCall answers POST /local/tools/call. It decides with decide and nothing else, writes the journal's call
// line before the add-on is asked and its result line when the call ends, and a denied call is journaled too.
func (l *Link) toolCall(w http.ResponseWriter, r *http.Request) {
	s := l.toolSessionFor(w, r)
	if s == nil {
		return
	}
	var req api.ToolCallReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, api.Error{Error: "the request is not a tool call: " + err.Error(), Code: "bad_request"})
		return
	}
	addonName, tool, ok := strings.Cut(req.Tool, ".")
	if !ok || !addon.ValidName(addonName) || !addon.ValidToolName(tool) {
		writeJSON(w, 400, api.Error{Error: fmt.Sprintf("%q is not a tool name; write it as ADDON.TOOL, for example echo.say", req.Tool), Code: "bad_request"})
		return
	}
	key := toolKey(addonName, tool)
	line := func(phase, id, code string, size int64, took time.Duration) error {
		return l.callJournal().append(api.AddonCall{CallID: id, Spawn: s.spawn, Agent: s.agent, Job: s.job, Addon: addonName, Tool: tool,
			Phase: phase, Code: code, Size: size, Millis: took.Milliseconds(), Time: l.opt.Now().UTC()})
	}

	def, local := l.localFacts(addonName, tool)

	// Decide and count under one lock, so two calls at once cannot both take the last place.
	s.mu.Lock()
	in := gateInput{Confidential: s.confidential, Used: s.used[key], Local: local}
	if t, found := s.tools[key]; found {
		in.Tool = &t
	}
	if u, found := s.unavailable[key]; found {
		in.Unavailable = &u
	}
	code := decide(in)
	if code == codeOK {
		if err := def.CheckArgs(req.Args); err != nil {
			s.mu.Unlock()
			writeJSON(w, 400, api.Error{Error: err.Error(), Code: "bad_arguments"})
			return
		}
		s.used[key]++
	}
	s.mu.Unlock()

	id := newCallID()
	if code != codeOK {
		if err := line("denied", id, code, 0, 0); err != nil {
			l.opt.Log.Printf("tool call %s.%s by %s: the journal could not be written: %v", addonName, tool, s.agent, err)
		}
		l.Kick()
		writeJSON(w, 200, api.ToolCallResp{Code: code, Text: refusal(code, local.Broken)})
		return
	}

	// The call line comes first and is on disk before the add-on hears of the call. If it cannot be
	// written the call is not made.
	if err := line("call", id, "", 0, 0); err != nil {
		s.mu.Lock()
		s.used[key]--
		s.mu.Unlock()
		l.opt.Log.Printf("tool call %s.%s by %s: the journal could not be written: %v", addonName, tool, s.agent, err)
		writeJSON(w, 500, api.Error{Error: "the link could not write its call journal, so the call was not made", Code: "journal"})
		return
	}
	l.Kick() // ships the call line soon, even if the call takes long

	results := filepath.Join(s.dir, ".handloom", "results", addonName, id)
	// The call goes on even if the caller hangs up: its result must still reach the journal.
	res, err := l.supervisor().Call(context.Background(), addonName, addon.Call{Tool: tool, Args: req.Args, Job: s.job, Results: results})
	if err != nil {
		res = addon.Result{Code: addon.CodeError, Error: err.Error()}
	}
	// Whatever the call did, the results folder is checked: what is not allowed is removed, and a call that
	// left such files is a plugin error.
	room := def.MaxOutput - int64(len(res.Text))
	if room < 0 {
		room = 0
	}
	files, problem := checkResults(results, room, res.Files)
	if problem != "" && res.Code == codeOK {
		res = addon.Result{Code: addon.CodeError, Error: problem, Took: res.Took}
	}
	out := api.ToolCallResp{OK: res.Code == codeOK, Code: res.Code}
	if res.Code == codeOK {
		out.Text = res.Text
		for _, f := range files {
			out.Files = append(out.Files, filepath.Join(results, f))
		}
	} else {
		out.Text = refusal(res.Code, res.Error)
	}
	if err := line("result", id, out.Code, int64(len(res.Text)), res.Took); err != nil {
		l.opt.Log.Printf("tool call %s.%s by %s: the result line could not be written: %v", addonName, tool, s.agent, err)
	}
	l.Kick()
	writeJSON(w, 200, out)
}

// toolPromote answers POST /local/tools/promote: it copies one result file of this spawn into the project tree of
// its worktree. The write scope is not looked at here; it applies when the helper submits, like for any file.
func (l *Link) toolPromote(w http.ResponseWriter, r *http.Request) {
	s := l.toolSessionFor(w, r)
	if s == nil {
		return
	}
	var req api.ToolPromoteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.File == "" || req.To == "" {
		writeJSON(w, 400, api.Error{Error: "say which result file to promote and where to put it (to)", Code: "bad_request"})
		return
	}
	to, err := promote(s.dir, req.File, req.To, req.Force)
	if err != nil {
		writeJSON(w, 400, api.Error{Error: err.Error(), Code: "bad_promote"})
		return
	}
	writeJSON(w, 200, api.ToolPromoteResp{To: to, Message: "Copied into the project as " + to + ". It is now a change like any other: the profile's write scope is checked when you submit, not now."})
}
