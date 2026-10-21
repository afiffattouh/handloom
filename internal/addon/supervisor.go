package addon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The result codes a call can end with here. The others in the design (not granted, over the call
// limit and so on) are decided by the gateway before a call reaches the add-on.
const (
	CodeOK           = "ok"
	CodeNotInstalled = "not-installed"
	CodeChanged      = "changed"
	CodeDown         = "plugin-down"
	CodeError        = "plugin-error"
	CodeTimeout      = "timeout"
)

const (
	maxRestarts   = 3
	restartWindow = 10 * time.Minute
	// replySlack is room, beyond a tool's output cap, for the JSON around the text.
	replySlack = 64 << 10
)

// Call is one request to a tool.
type Call struct {
	Tool string
	Args map[string]any
	Job  int64
	// Results is the folder the add-on may write files into. It is made if it is missing.
	Results string
}

// Result is how a call ended.
type Result struct {
	Code  string
	Text  string
	Files []string
	// Error is a sentence in plain words when Code is not ok.
	Error string
	Took  time.Duration
}

// Supervisor runs the add-ons under one folder, each as one service started on first use.
type Supervisor struct {
	Root  string
	Store *Store
	// IdleTimeout is how long a service may sit unused before it is stopped. Zero means ten minutes.
	IdleTimeout time.Duration
	// HealthTimeout bounds a _health call. Zero means ten seconds.
	HealthTimeout time.Duration
	// Now is the clock; tests replace it.
	Now func() time.Time

	mu       sync.Mutex
	services map[string]*service
}

type service struct {
	name   string
	callMu sync.Mutex // one call at a time to a service; everything below is guarded by it

	p        *proc
	crashed  bool        // the last process stopped without being asked to
	restarts []time.Time // when it was restarted after a crash, within the window
	down     string
	downAt   time.Time // when it was marked down
	downPkg  string    // the package that was marked down: another one starts afresh
	lastPkg  string    // the package the last call checked
	idle     *time.Timer

	live atomic.Pointer[proc] // the same process as p, readable without callMu, so Close can end a call that is running
}

// setProc is the only way p changes.
func (sv *service) setProc(p *proc) {
	sv.p = p
	sv.live.Store(p)
}

// NewSupervisor runs add-ons from root, approved in store.
func NewSupervisor(root string, store *Store) *Supervisor {
	return &Supervisor{Root: root, Store: store, services: map[string]*service{}}
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Supervisor) service(name string) *service {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.services == nil {
		s.services = map[string]*service{}
	}
	sv := s.services[name]
	if sv == nil {
		sv = &service{name: name}
		s.services[name] = sv
	}
	return sv
}

// Check says whether the add-on on disk is the one the owner approved. When it is not, the Result says
// why and the add-on must not run; ok is false.
func (s *Supervisor) Check(name string) (*Manifest, Result, bool) {
	m, _, _, res, ok := s.CheckHash(name)
	return m, res, ok
}

// CheckHash is Check that also gives the hashes of the manifest and of the package it checked, for a caller that must know the
// add-on is the one a grant was made for.
func (s *Supervisor) CheckHash(name string) (*Manifest, string, string, Result, bool) {
	if !nameRe.MatchString(name) {
		return nil, "", "", Result{Code: CodeNotInstalled, Error: fmt.Sprintf("%q is not an add-on name", name)}, false
	}
	dir := filepath.Join(s.Root, name)
	if _, err := os.Stat(dir); err != nil {
		return nil, "", "", Result{Code: CodeNotInstalled, Error: "this computer does not have the add-on " + name}, false
	}
	ap, ok, err := s.Store.Get(name)
	if err != nil {
		return nil, "", "", Result{Code: CodeError, Error: err.Error()}, false
	}
	if !ok {
		return nil, "", "", Result{Code: CodeNotInstalled, Error: name + " is on this computer but was never approved here"}, false
	}
	m, ph, mh, err := Load(dir)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return nil, "", "", Result{Code: CodeDown, Error: name + " is not started: " + apiErr.Error()}, false
	}
	if err != nil {
		return nil, "", "", Result{Code: CodeChanged, Error: name + " changed since it was approved and can no longer be read: " + err.Error()}, false
	}
	if ph != ap.PackageHash || mh != ap.ManifestHash || m.Name != name {
		return nil, "", "", Result{Code: CodeChanged, Error: name + " changed since it was approved; approve it again with handloom addon add"}, false
	}
	return m, mh, ph, Result{}, true
}

// Call runs one tool and waits for its answer, at most the tool's timeout. A Go error means the call was
// wrong or cancelled (an unknown tool, arguments that do not fit); how the add-on did is in the Result.
func (s *Supervisor) Call(ctx context.Context, name string, c Call) (Result, error) {
	if !nameRe.MatchString(name) {
		return Result{}, fmt.Errorf("%q is not an add-on name", name)
	}
	sv := s.service(name)
	sv.callMu.Lock()
	defer sv.callMu.Unlock()
	if sv.idle != nil {
		sv.idle.Stop()
	}
	defer s.armIdle(sv) // every way out that leaves a live process starts the idle clock again
	start := s.now()
	finish := func(r Result) (Result, error) {
		r.Took = s.now().Sub(start)
		return r, nil
	}

	m, _, ph, bad, ok := s.CheckHash(name)
	if !ok {
		s.stop(sv, false)
		return finish(bad)
	}
	sv.lastPkg = ph
	// Down lasts for the window, or until the owner approves another copy of the add-on.
	if sv.down != "" && (ph != sv.downPkg || s.now().Sub(sv.downAt) >= restartWindow) {
		sv.down, sv.crashed, sv.restarts = "", false, nil
	}
	var tool Tool
	if c.Tool == "_health" {
		d := s.HealthTimeout
		if d == 0 {
			d = 10 * time.Second
		}
		tool = Tool{Name: "_health", Timeout: d, MaxOutput: 64 << 10}
	} else {
		var found bool
		if tool, found = m.Tool(c.Tool); !found {
			return Result{}, fmt.Errorf("the add-on %s has no tool %q", name, c.Tool)
		}
		if err := tool.CheckArgs(c.Args); err != nil {
			return Result{}, err
		}
	}
	if sv.down != "" {
		return finish(Result{Code: CodeDown, Error: sv.down})
	}
	if c.Results != "" {
		if err := os.MkdirAll(c.Results, 0o700); err != nil {
			return Result{}, err
		}
	}
	if sv.p != nil && sv.p.exited() {
		sv.p, sv.crashed = nil, true
	}
	if sv.p == nil {
		if r, ok := s.begin(sv, m); !ok {
			return finish(r)
		}
	}
	p := sv.p
	p.limit.Store(tool.MaxOutput + replySlack)
	defer p.limit.Store(p.idleCap)

	id := p.nextID()
	req, err := json.Marshal(map[string]any{"id": id, "tool": c.Tool, "args": nonNil(c.Args), "job": c.Job, "results": c.Results})
	if err != nil {
		return Result{}, err
	}
	if _, err := p.in.Write(append(req, '\n')); err != nil {
		s.lost(sv)
		return finish(Result{Code: CodeError, Error: "the add-on is not answering" + p.stderrNote()})
	}

	timer := time.NewTimer(tool.Timeout)
	defer timer.Stop()
	var got int64
	for {
		select {
		case <-ctx.Done():
			s.stop(sv, false)
			return Result{}, ctx.Err()
		case <-timer.C:
			s.stop(sv, false)
			return finish(Result{Code: CodeTimeout, Error: fmt.Sprintf("the call took longer than its limit of %s and was stopped", tool.Timeout)})
		case ln, open := <-p.lines:
			if !open || ln.err != nil {
				if errors.Is(ln.err, errTooLong) {
					s.stop(sv, false)
					return finish(Result{Code: CodeError, Error: "the add-on's reply was larger than its limit of " + sizeText(tool.MaxOutput) + "; it was stopped"})
				}
				s.lost(sv)
				return finish(Result{Code: CodeError, Error: "the add-on stopped while answering" + p.stderrNote()})
			}
			got += int64(len(ln.b))
			if got > tool.MaxOutput+replySlack {
				s.stop(sv, false)
				return finish(Result{Code: CodeError, Error: "the add-on's reply was larger than its limit of " + sizeText(tool.MaxOutput) + "; it was stopped"})
			}
			var rep struct {
				ID    string   `json:"id"`
				OK    bool     `json:"ok"`
				Text  string   `json:"text"`
				Files []string `json:"files"`
				Error string   `json:"error"`
			}
			if json.Unmarshal(ln.b, &rep) != nil || rep.ID != id {
				continue // not an answer to this call: stray output or a late answer
			}
			if int64(len(rep.Text)) > tool.MaxOutput {
				return finish(Result{Code: CodeError, Error: "the add-on's reply was larger than its limit of " + sizeText(tool.MaxOutput)})
			}
			if !rep.OK {
				msg := rep.Error
				if msg == "" {
					msg = "the add-on reported an error without saying what"
				}
				return finish(Result{Code: CodeError, Error: msg})
			}
			return finish(Result{Code: CodeOK, Text: rep.Text, Files: rep.Files})
		}
	}
}

// Health asks the add-on whether it is well, starting it if needed.
func (s *Supervisor) Health(ctx context.Context, name string) Result {
	r, err := s.Call(ctx, name, Call{Tool: "_health"})
	if err != nil {
		return Result{Code: CodeError, Error: err.Error()}
	}
	return r
}

// begin starts the service, counting a start after a crash against the limit of three in ten minutes.
func (s *Supervisor) begin(sv *service, m *Manifest) (Result, bool) {
	if sv.crashed {
		now := s.now()
		kept := sv.restarts[:0]
		for _, t := range sv.restarts {
			if now.Sub(t) < restartWindow {
				kept = append(kept, t)
			}
		}
		sv.restarts = kept
		if len(sv.restarts) >= maxRestarts {
			sv.down = fmt.Sprintf("the add-on stopped on its own %d times in ten minutes and is marked down; see handloom addon doctor", maxRestarts+1)
			sv.downAt, sv.downPkg = now, sv.lastPkg
			return Result{Code: CodeDown, Error: sv.down}, false
		}
		sv.restarts = append(sv.restarts, now)
	}
	p, err := s.launch(sv.name, m)
	if err != nil {
		sv.crashed = true
		return Result{Code: CodeDown, Error: "the add-on could not be started: " + err.Error()}, false
	}
	sv.setProc(p)
	sv.crashed = false
	return Result{}, true
}

// lost records that the process stopped without being asked to.
func (s *Supervisor) lost(sv *service) {
	if sv.p != nil {
		sv.p.kill()
	}
	s.stop(sv, true)
}

// stop ends the process, if one runs. crashed says whether that counts as the add-on's own doing.
func (s *Supervisor) stop(sv *service, crashed bool) {
	if sv.p != nil {
		sv.p.kill()
		sv.p.release(s.Root, sv.name)
		sv.setProc(nil)
	}
	sv.crashed = crashed
}

// armIdle stops the service after IdleTimeout without a call.
func (s *Supervisor) armIdle(sv *service) {
	d := s.IdleTimeout
	if d == 0 {
		d = 10 * time.Minute
	}
	if sv.p == nil {
		return
	}
	sv.idle = time.AfterFunc(d, func() {
		if !sv.callMu.TryLock() {
			return // a call is on; it arms the timer again when it ends
		}
		defer sv.callMu.Unlock()
		if sv.p != nil {
			sv.p.stopGently()
			sv.p.release(s.Root, sv.name)
			sv.setProc(nil)
		}
	})
}

// Running says whether the add-on has a live process under this supervisor.
func (s *Supervisor) Running(name string) bool {
	sv := s.service(name)
	if !sv.callMu.TryLock() {
		return true
	}
	defer sv.callMu.Unlock()
	return sv.p != nil && !sv.p.exited()
}

// Close stops every service.
func (s *Supervisor) Close() {
	s.mu.Lock()
	all := make([]*service, 0, len(s.services))
	for _, sv := range s.services {
		all = append(all, sv)
	}
	s.mu.Unlock()
	for _, sv := range all {
		if !sv.callMu.TryLock() {
			// A call is running. Ending its process ends the call (it answers "the add-on stopped"), which
			// frees the lock; waiting for the add-on to finish would hold the link back for up to five minutes.
			if p := sv.live.Load(); p != nil {
				p.kill()
			}
			sv.callMu.Lock()
		}
		if sv.idle != nil {
			sv.idle.Stop()
		}
		if sv.p != nil {
			sv.p.stopGently()
			sv.p.release(s.Root, sv.name)
			sv.setProc(nil)
		}
		sv.callMu.Unlock()
	}
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "MB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + "KB"
	}
	return strconv.FormatInt(n, 10) + "B"
}

// ---- the process ----

var errTooLong = errors.New("a line was too long")

type lineResult struct {
	b   []byte
	err error
}

type proc struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan lineResult
	done   chan struct{} // closed when the process has ended
	stderr *tailBuffer
	seq    int
	// limit is the longest line the reader accepts now: the running call's cap, or the largest cap of any tool between calls.
	limit       atomic.Int64
	idleCap     int64
	releaseOnce sync.Once
	pidFile     *os.File // holds the lock that says this service is alive
}

func (s *Supervisor) launch(name string, m *Manifest) (*proc, error) {
	dir := filepath.Join(s.Root, name)
	cmd := exec.Command(m.Run[0], m.Run[1:]...)
	cmd.Dir = dir
	cmd.Env = minimalEnv(name)
	ownGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	tail := &tailBuffer{max: 2048}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, err
	}
	max := int64(DefaultMaxOutput)
	for _, t := range m.Tools {
		if t.MaxOutput > max {
			max = t.MaxOutput
		}
	}
	p := &proc{cmd: cmd, in: in, lines: make(chan lineResult), done: make(chan struct{}), stderr: tail, idleCap: max + replySlack}
	p.limit.Store(p.idleCap)
	p.pidFile = writePid(s.Root, name, cmd.Process.Pid)
	go func() {
		cmd.Wait()
		pw.Close()
		p.release(s.Root, name) // a service that ends on its own no longer counts as running
		close(p.done)
	}()
	go readLines(pr, &p.limit, p.lines, p.done)
	return p, nil
}

// minimalEnv is all a service gets: where to find programs, and who it is. No keys, no tokens, no home.
func minimalEnv(name string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"TMPDIR=" + os.TempDir(),
		"PYTHONDONTWRITEBYTECODE=1", // a service must not add files to a folder whose bytes are approved
		"PYTHONUNBUFFERED=1",
		"HANDLOOM_ADDON=" + name,
		"HANDLOOM_ADDON_API=" + strconv.Itoa(APIVersion),
	}
	if v := os.Getenv("SYSTEMROOT"); v != "" {
		env = append(env, "SYSTEMROOT="+v)
	}
	return env
}

func (p *proc) nextID() string {
	p.seq++
	return "c" + strconv.Itoa(p.seq)
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *proc) kill() {
	killTree(p.cmd)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

// stopGently closes the service's input so it can finish, and kills it if it has not ended in two seconds.
func (p *proc) stopGently() {
	p.in.Close()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		p.kill()
	}
	killTree(p.cmd) // anything the service left running
}

func (p *proc) stderrNote() string {
	if t := strings.TrimSpace(p.stderr.String()); t != "" {
		return " (it said: " + lastLine(t) + ")"
	}
	return ""
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// StopStrays ends the services an earlier link left running when it stopped without closing them. Such a
// link let go of its lock on the id file, so the file alone proves nothing and its number may by now belong
// to another program; a process is ended only if it is in the group the file names and still has the
// add-on's folder as its working folder.
func (s *Supervisor) StopStrays() {
	files, err := os.ReadDir(filepath.Join(s.Root, ".running"))
	if err != nil {
		return
	}
	for _, f := range files {
		name := f.Name()
		if _, held := RunningPID(s.Root, name); held {
			continue // alive and held by a link, not a stray
		}
		b, err := os.ReadFile(pidPath(s.Root, name))
		pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if err == nil && perr == nil && pid > 1 && inFolder(pid, filepath.Join(s.Root, name)) {
			killGroup(pid)
		}
		os.Remove(pidPath(s.Root, name))
	}
}

// inFolder says whether the process has dir as its working folder. Where that cannot be told, it is false.
func inFolder(pid int, dir string) bool {
	cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil {
		return false
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		want = dir
	}
	return cwd == want || cwd == dir
}

// release forgets the process's id file.
func (p *proc) release(root, name string) {
	p.releaseOnce.Do(func() {
		os.Remove(pidPath(root, name))
		if p.pidFile != nil {
			p.pidFile.Close() // closing lets go of the lock
		}
	})
}

func readLines(r *io.PipeReader, limit *atomic.Int64, out chan<- lineResult, done <-chan struct{}) {
	defer close(out)
	defer r.Close()
	send := func(l lineResult) bool {
		select {
		case out <- l:
			return true
		case <-done:
			return false
		}
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		buf = append(buf, frag...)
		if int64(len(buf)) > limit.Load() {
			send(lineResult{err: errTooLong})
			return
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(buf) > 0 {
				if !send(lineResult{b: bytes.TrimSpace(buf)}) {
					return
				}
			}
			send(lineResult{err: err})
			return
		}
		if !send(lineResult{b: bytes.TrimSpace(buf)}) {
			return
		}
		buf = nil
	}
}

// tailBuffer keeps the last bytes written to it: what a service said on its error output.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// ---- who is running ----

// RunDir holds one small file per running service, named for the add-on, holding its process id, so the
// command line can tell that an add-on is in use without asking the link.
func pidPath(root, name string) string { return filepath.Join(root, ".running", name) }

// writePid records the process id and holds a lock on the file for as long as the service lives, so a file
// left behind by a link that died (and a process id since reused) is not taken for a running service.
func writePid(root, name string, pid int) *os.File {
	os.MkdirAll(filepath.Join(root, ".running"), 0o700)
	f, err := os.OpenFile(pidPath(root, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil
	}
	lockPid(f)
	f.WriteString(strconv.Itoa(pid) + "\n")
	return f
}

// RunningPID is the process id of the add-on's service if one is alive on this computer.
func RunningPID(root, name string) (int, bool) {
	b, err := os.ReadFile(pidPath(root, name))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || !pidHeld(pidPath(root, name)) {
		return 0, false
	}
	return pid, true
}
