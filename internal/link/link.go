// Package link is the per-device daemon. It holds the device credential,
// serves the local unix socket that the CLI and adapter hooks talk to, keeps a
// long-poll open to the hub, and wakes local agents.
package link

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/drivers"
)

type Options struct {
	Hub        string
	Device     string
	Credential string
	UserHome   string // where the agent CLIs keep their logs (default: the home of the user running the link)
	Socket     string

	NudgeEvery    time.Duration // at most one nudge per agent in this time; default 60s
	Renudge       time.Duration // nudge again when delivered mail stays unread this long; default 5m
	UnknownAfter  time.Duration // two unanswered nudges for this long mark the agent unknown; default 10m
	Heartbeat     time.Duration // extend leases of an active agent this often; default 2m
	BlockedGrace  time.Duration // an agent must be blocked this long before the lead is told; default 30s
	Tick          time.Duration // how often the ladder runs without events; default 10s
	LiveEvery     time.Duration // how often a live terminal is reported to the hub as alive; default 20s
	VerifyTimeout time.Duration // longest a job's verify command may run; default 10m
	PollWait      int           // long-poll seconds; default 25

	// Spawn: the link starts agents in a tmux server of its own.
	TmuxSocket string         // tmux -L name; default "handloom"
	WorkRoot   string         // where spawned agents get their directories; default <home>/work
	Binary     string         // the handloom binary a spawned pane runs; default this one
	Tmux       drivers.Runner // replaceable in tests
	Git        drivers.Runner // runs git; replaceable in tests

	Headless        map[string][]string // per-kind headless command overrides
	HeadlessTimeout time.Duration       // longest a headless turn may run; default 15m
	HeadlessRun     HeadlessRun         // replaceable in tests

	Log     *log.Logger
	Now     func() time.Time
	Drivers func(target string) (drivers.Driver, string, error) // replaceable in tests
}

type Link struct {
	opt  Options
	hub  *client.Client
	kick chan struct{}

	mu      sync.Mutex
	memo    map[string]*memo
	tailing bool // the screen-reading loop is running
	usage   usageReader
}

func New(opt Options) *Link {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opt.NudgeEvery, 60*time.Second)
	def(&opt.Renudge, 5*time.Minute)
	def(&opt.UnknownAfter, 10*time.Minute)
	def(&opt.Heartbeat, 2*time.Minute)
	def(&opt.BlockedGrace, 30*time.Second)
	def(&opt.Tick, 10*time.Second)
	def(&opt.LiveEvery, 20*time.Second)
	def(&opt.VerifyTimeout, 10*time.Minute)
	def(&opt.HeadlessTimeout, 15*time.Minute)
	if opt.HeadlessRun == nil {
		opt.HeadlessRun = execHeadless
	}
	if opt.PollWait <= 0 {
		opt.PollWait = 25
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.TmuxSocket == "" {
		opt.TmuxSocket = "handloom"
	}
	if opt.WorkRoot == "" {
		opt.WorkRoot = filepath.Join(client.Home(), "work")
	}
	if opt.Binary == "" {
		if exe, err := os.Executable(); err == nil {
			opt.Binary = exe
		}
	}
	if opt.Tmux == nil {
		opt.Tmux = execTmux
	}
	if opt.Git == nil {
		opt.Git = execGit
	}
	if opt.Log == nil {
		opt.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
	if opt.Drivers == nil {
		opt.Drivers = drivers.For
	}
	return &Link{
		opt:  opt,
		hub:  client.Direct(opt.Hub, opt.Credential),
		kick: make(chan struct{}, 1),
		memo: map[string]*memo{},
	}
}

// Run serves the socket and runs the wake loop until ctx is done.
func (l *Link) Run(ctx context.Context) error {
	ln, err := listen(l.opt.Socket)
	if err != nil {
		return err
	}
	defer os.Remove(l.opt.Socket)
	srv := &http.Server{Handler: l.handler()}
	go srv.Serve(ln)
	defer srv.Close()

	l.opt.Log.Printf("link up: device %s, hub %s, socket %s", l.opt.Device, l.opt.Hub, l.opt.Socket)
	go l.pollLoop(ctx)
	l.ladderLoop(ctx)
	return nil
}

// listen opens the unix socket, owner-only. A stale socket file left by a
// dead link is replaced; a live one is an error.
func listen(path string) (net.Listener, error) {
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		return nil, fmt.Errorf("a link is already running on %s", path)
	}
	os.Remove(path)
	old := umask(0o177)
	ln, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return nil, err
	}
	return ln, os.Chmod(path, 0o600)
}

// handler serves the local socket. /v1/ is forwarded to the hub with the
// device credential added; the caller says which agent it is in Handloom-Agent
// and never sees the credential.
func (l *Link) handler() http.Handler {
	hubURL, _ := url.Parse(l.opt.Hub)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(hubURL)
			r.Out.Header.Set("Authorization", "Bearer "+l.opt.Credential)
		},
		ModifyResponse: func(resp *http.Response) error {
			req := resp.Request
			agent := api.AgentFrom(req.Header)
			if resp.StatusCode == 200 && req.Method == "GET" && req.URL.Path == "/v1/inbox" && req.URL.Query().Get("all") == "" {
				l.sawInbox(agent)
			}
			if req.Method == "POST" {
				l.Kick() // state or board changed: rerun the ladder soon
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"the link cannot reach the hub at %s","code":"hub_unreachable"}`+"\n", l.opt.Hub)
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", proxy)
	mux.HandleFunc("POST /v1/tasks/{id}/submit", l.submitGate(proxy)) // more specific than /v1/: it wins
	// A refused submit is reported by the link itself, with its own credential. An
	// agent reaching the hub through this socket must not be able to file one.
	linkOnly := func(what string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"only the link reports %s","code":"forbidden"}`+"\n", what)
		}
	}
	mux.HandleFunc("/v1/device/scope-refused", linkOnly("refused submits"))
	mux.HandleFunc("POST /v1/tasks/{id}/verify", linkOnly("verification results"))
	mux.HandleFunc("/v1/device/merges", linkOnly("the merges to do"))
	mux.HandleFunc("/v1/device/kcollects", linkOnly("the notes to gather"))
	mux.HandleFunc("/v1/device/tails", linkOnly("the screens wanted"))
	mux.HandleFunc("POST /v1/device/usage", linkOnly("usage"))
	mux.HandleFunc("POST /v1/device/tails/{name}", linkOnly("screens"))
	mux.HandleFunc("POST /v1/kcollects/{id}/report", linkOnly("note collections"))
	mux.HandleFunc("POST /v1/merges/{id}/report", linkOnly("merge results"))
	mux.HandleFunc("POST /local/activity", func(w http.ResponseWriter, r *http.Request) {
		go l.activity(api.AgentFrom(r.Header))
		w.Write([]byte("{}\n"))
	})
	mux.HandleFunc("GET /local/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"device":%q,"hub":%q,"protocol":%q}`+"\n", l.opt.Device, l.opt.Hub, api.Version)
	})
	return mux
}

// Kick asks the ladder to run now.
func (l *Link) Kick() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// pollLoop keeps the long-poll open and kicks the ladder on every event.
func (l *Link) pollLoop(ctx context.Context) {
	cursor := client.LoadCursor()
	backoff := time.Second
	for ctx.Err() == nil {
		var resp struct {
			Events []api.Event `json:"events"`
			Cursor int64       `json:"cursor"`
		}
		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(l.opt.PollWait+15)*time.Second)
		err := l.hub.Do(reqCtx, "GET", fmt.Sprintf("/v1/events?after=%d&wait=%d", cursor, l.opt.PollWait), nil, &resp)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ae *client.Error
			if errors.As(err, &ae) && ae.Status == 401 {
				l.opt.Log.Printf("the hub rejected this device's credential: %v", err)
			} else {
				l.opt.Log.Printf("poll: %v", err)
			}
			client.Wait(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if len(resp.Events) > 0 {
			cursor = resp.Cursor
			client.SaveCursor(cursor)
			l.Kick()
		}
	}
}

func (l *Link) ladderLoop(ctx context.Context) {
	tick := time.NewTicker(l.opt.Tick)
	defer tick.Stop()
	for {
		l.runLadder(ctx)
		select {
		case <-ctx.Done():
			return
		case <-l.kick:
		case <-tick.C:
		}
	}
}

// activity is reported by adapter hooks while an agent works. The link
// extends the leases of that agent's claimed tasks, so a busy agent keeps
// its work and a dead one loses it. Activity also ends a "blocked" state.
func (l *Link) activity(agent string) {
	if agent == "" {
		return
	}
	now := l.opt.Now()
	l.mu.Lock()
	m := l.memoFor(agent)
	due := now.Sub(m.lastHeartbeat) >= l.opt.Heartbeat
	if due {
		m.lastHeartbeat = now
	}
	wasBlocked := m.state == api.StateBlocked
	if wasBlocked {
		m.state = api.StateWorking
	}
	l.mu.Unlock()
	as := *l.hub
	as.Agent = agent
	if wasBlocked {
		// A tool ran, so the approval prompt is gone.
		if err := as.Post("/v1/agents/"+url.PathEscape(agent)+"/state", api.StateReq{State: api.StateWorking}, nil); err != nil {
			l.opt.Log.Printf("state %s: %v", agent, err)
		}
	}
	if !due {
		return
	}
	var tasks []api.Task
	if err := as.Get("/v1/tasks?status=claimed&owner="+url.QueryEscape(agent), &tasks); err != nil {
		l.opt.Log.Printf("heartbeat %s: %v", agent, err)
		return
	}
	for _, t := range tasks {
		if err := as.Post(fmt.Sprintf("/v1/tasks/%d/heartbeat", t.ID), nil, nil); err != nil {
			l.opt.Log.Printf("heartbeat %s task %d: %v", agent, t.ID, err)
		}
	}
}

func (l *Link) sawInbox(agent string) {
	if agent == "" {
		return
	}
	l.mu.Lock()
	m := l.memoFor(agent)
	m.nudges, m.reportedUnknown = 0, false
	l.mu.Unlock()
}

// NudgeLine is the only text handloom ever types into a terminal. It has no
// shell metacharacters, in case the pane is not what we think it is.
func NudgeLine(unread int) string {
	if unread == 1 {
		return "You have 1 new handloom message. Run: handloom inbox"
	}
	return fmt.Sprintf("You have %d new handloom messages. Run: handloom inbox", unread)
}

func reasonText(reason string) string { return strings.ReplaceAll(reason, "\n", " ") }
