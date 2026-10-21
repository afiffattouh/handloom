package addon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var echoDir = filepath.Join("..", "..", "examples", "addons", "echo")

func needPython(t *testing.T) {
	t.Helper()
	if _, err := lookPython(); err != nil {
		t.Skip("python3 is not available")
	}
}

// rig is a folder of add-ons with a store and a supervisor.
type rig struct {
	t     *testing.T
	root  string
	store *Store
	sup   *Supervisor
	now   time.Time
	mu    sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	needPython(t)
	r := &rig{t: t, root: filepath.Join(t.TempDir(), "addons"), now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	r.store = NewStore(r.root)
	r.sup = NewSupervisor(r.root, r.store)
	r.sup.Now = func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	t.Cleanup(r.sup.Close)
	return r
}

func (r *rig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

// install puts the add-on in src (after edits, a map from file name to a function on its text) in the rig.
func (r *rig) install(src string, edit map[string]func(string) string) Approval {
	r.t.Helper()
	dir := r.t.TempDir()
	if err := copyTree(src, dir); err != nil {
		r.t.Fatal(err)
	}
	for name, f := range edit {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f(string(b))), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	a, err := Install(r.root, r.store, dir, "", "tester", r.now)
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

func (r *rig) call(name string, c Call) Result {
	r.t.Helper()
	res, err := r.sup.Call(context.Background(), name, c)
	if err != nil {
		r.t.Fatalf("call %s.%s: %v", name, c.Tool, err)
	}
	return res
}

func TestEchoCallHealthAndFiles(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)

	if h := r.sup.Health(context.Background(), "echo"); h.Code != CodeOK {
		t.Fatalf("health = %+v", h)
	}
	res := r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "hello"}, Job: 7})
	if res.Code != CodeOK || res.Text != "hello" || len(res.Files) != 0 {
		t.Fatalf("say = %+v", res)
	}
	results := filepath.Join(t.TempDir(), "results", "echo", "c1")
	res = r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "kept", "file": "note.txt"}, Results: results})
	if res.Code != CodeOK || len(res.Files) != 1 || res.Files[0] != "note.txt" {
		t.Fatalf("say with file = %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(results, "note.txt")); err != nil || string(b) != "kept" {
		t.Fatalf("file = %q, %v", b, err)
	}
	// the add-on reports its own errors in its own words
	res = r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "x", "file": "../escape"}, Results: results})
	if res.Code != CodeError || !strings.Contains(res.Error, "plain name") {
		t.Fatalf("path escape = %+v", res)
	}
	res = r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "x", "file": "f"}})
	if res.Code != CodeError || !strings.Contains(res.Error, "no results folder") {
		t.Fatalf("no results folder = %+v", res)
	}
	if res = r.call("echo", Call{Tool: "wait", Args: map[string]any{"seconds": 0.05}}); res.Code != CodeOK || !strings.Contains(res.Text, "Waited") {
		t.Fatalf("wait = %+v", res)
	}
	// the service must not have written into its own folder (the bytes are approved)
	if _, ok, _ := r.store.Get("echo"); !ok {
		t.Fatal("approval lost")
	}
	if ins, _ := Installed(r.root, r.store); len(ins) != 1 || ins[0].State != "ready" {
		t.Fatalf("after running, installed = %+v", ins)
	}
}

func TestCallWrongUse(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)
	if _, err := r.sup.Call(context.Background(), "echo", Call{Tool: "shout"}); err == nil {
		t.Error("an unknown tool was run")
	}
	if _, err := r.sup.Call(context.Background(), "echo", Call{Tool: "say"}); err == nil {
		t.Error("a missing argument was accepted")
	}
	if _, err := r.sup.Call(context.Background(), "../echo", Call{Tool: "say"}); err == nil {
		t.Error("a path was taken for a name")
	}
}

func TestNotInstalled(t *testing.T) {
	r := newRig(t)
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeNotInstalled {
		t.Fatalf("no folder: %+v", res)
	}
	// a folder that was copied in by hand has never been approved
	if err := copyTree(echoDir, filepath.Join(r.root, "echo")); err != nil {
		os.MkdirAll(filepath.Join(r.root, "echo"), 0o700)
		copyTree(echoDir, filepath.Join(r.root, "echo"))
	}
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeNotInstalled || !strings.Contains(res.Error, "approved") {
		t.Fatalf("no approval: %+v", res)
	}
}

func TestTimeoutKillsTheCall(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, map[string]func(string) string{"addon.yaml": func(s string) string { return strings.Replace(s, "timeout: 5m", "timeout: 400ms", 1) }})
	start := time.Now()
	res := r.call("echo", Call{Tool: "wait", Args: map[string]any{"seconds": 30}})
	if res.Code != CodeTimeout || !strings.Contains(res.Error, "400ms") {
		t.Fatalf("wait = %+v", res)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the call was not cut short")
	}
	if r.sup.Running("echo") {
		t.Fatal("the timed-out service was left running")
	}
	// the next call starts it again, and a timeout is not a crash
	if res := r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "again"}}); res.Code != CodeOK {
		t.Fatalf("after timeout: %+v", res)
	}
}

// Closing the supervisor ends a call that is running, instead of waiting for the add-on to finish it.
func TestCloseEndsACallThatIsRunning(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)
	done := make(chan Result, 1)
	go func() { done <- r.call("echo", Call{Tool: "wait", Args: map[string]any{"seconds": 60}}) }()
	deadline := time.Now().Add(10 * time.Second)
	for !r.sup.Running("echo") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // the request is written to the service
	start := time.Now()
	r.sup.Close()
	select {
	case res := <-done:
		if res.Code != CodeError {
			t.Fatalf("the call ended with %+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the call was not ended")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("Close waited for the add-on")
	}
}

// A small add-on for the cases the echo add-on does not have: it can stop, flood and stay silent.
const miscYAML = `schema: 1
kind: tool
name: misc
version: 0.0.1
api: 1
run: ["python3", "service.py"]
tools:
  - {name: ok, summary: Answer., leaves_machine: false}
  - {name: die, summary: Stop without answering., leaves_machine: false}
  - {name: flood, summary: Answer with too much text., leaves_machine: false, max_output: 1KB}
  - {name: endless, summary: Print one very long line., leaves_machine: false, max_output: 1KB}
  - {name: noisy, summary: Print junk before answering., leaves_machine: false}
  - {name: fail, summary: Report an error., leaves_machine: false}
`

const miscPy = `import json, os, sys
for line in sys.stdin:
    req = json.loads(line)
    t = req["tool"]
    if t == "die":
        sys.stderr.write("something broke\n")
        os._exit(3)
    if t == "endless":
        sys.stdout.write("x" * 500000)
        sys.stdout.flush()
        continue
    if t == "noisy":
        print("not json")
        print(json.dumps({"id": "stale", "ok": True, "text": "old"}))
    rep = {"id": req["id"], "ok": True, "text": "fine"}
    if t == "flood":
        rep["text"] = "y" * 5000
    if t == "fail":
        rep = {"id": req["id"], "ok": False, "error": "no luck"}
    print(json.dumps(rep), flush=True)
`

func miscRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "addon.yaml"), []byte(miscYAML), 0o644)
	os.WriteFile(filepath.Join(src, "service.py"), []byte(miscPy), 0o644)
	if _, err := Install(r.root, r.store, src, "", "tester", r.now); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOversizedOutput(t *testing.T) {
	r := miscRig(t)
	res := r.call("misc", Call{Tool: "flood"})
	if res.Code != CodeError || !strings.Contains(res.Error, "larger than its limit of 1KB") {
		t.Fatalf("flood = %+v", res)
	}
	res = r.call("misc", Call{Tool: "endless"})
	if res.Code != CodeError || !strings.Contains(res.Error, "larger than its limit of 1KB") {
		t.Fatalf("endless = %+v", res)
	}
	if r.sup.Running("misc") {
		t.Fatal("a service that flooded its output was left running")
	}
	if res := r.call("misc", Call{Tool: "ok"}); res.Code != CodeOK {
		t.Fatalf("after flood: %+v", res)
	}
}

func TestAddonErrorsAndStrayOutput(t *testing.T) {
	r := miscRig(t)
	if res := r.call("misc", Call{Tool: "fail"}); res.Code != CodeError || res.Error != "no luck" {
		t.Fatalf("fail = %+v", res)
	}
	if res := r.call("misc", Call{Tool: "noisy"}); res.Code != CodeOK || res.Text != "fine" {
		t.Fatalf("noisy = %+v", res)
	}
}

func TestCrashRestartLimit(t *testing.T) {
	r := miscRig(t)
	// the first run, then three restarts, each ending in a crash
	for i := 0; i < 4; i++ {
		res := r.call("misc", Call{Tool: "die"})
		if res.Code != CodeError || !strings.Contains(res.Error, "stopped while answering") || !strings.Contains(res.Error, "something broke") {
			t.Fatalf("crash %d = %+v", i+1, res)
		}
	}
	res := r.call("misc", Call{Tool: "ok"})
	if res.Code != CodeDown || !strings.Contains(res.Error, "marked down") {
		t.Fatalf("after four crashes = %+v", res)
	}
	// and it stays down, even a minute later
	r.advance(time.Minute)
	if res := r.call("misc", Call{Tool: "ok"}); res.Code != CodeDown {
		t.Fatalf("later = %+v", res)
	}
	// but not for ever: once the window has passed it is tried again
	r.advance(11 * time.Minute)
	if res := r.call("misc", Call{Tool: "ok"}); res.Code != CodeOK {
		t.Fatalf("after the window = %+v", res)
	}
}

func TestCrashesOutsideTheWindowDoNotCount(t *testing.T) {
	r := miscRig(t)
	for i := 0; i < 6; i++ {
		if res := r.call("misc", Call{Tool: "die"}); res.Code != CodeError {
			t.Fatalf("crash %d = %+v", i+1, res)
		}
		r.advance(4 * time.Minute) // never more than three restarts in ten minutes
	}
	if res := r.call("misc", Call{Tool: "ok"}); res.Code != CodeOK {
		t.Fatalf("after spaced crashes = %+v", res)
	}
}

func TestACrashBetweenCallsIsRestarted(t *testing.T) {
	r := miscRig(t)
	r.call("misc", Call{Tool: "ok"})
	sv := r.sup.service("misc")
	sv.callMu.Lock()
	sv.p.kill()
	sv.callMu.Unlock()
	if res := r.call("misc", Call{Tool: "ok"}); res.Code != CodeOK {
		t.Fatalf("after the service died idle: %+v", res)
	}
}

func TestChangedPackageIsRefused(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)
	if res := r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "a"}}); res.Code != CodeOK {
		t.Fatalf("before the change: %+v", res)
	}
	if !r.sup.Running("echo") {
		t.Fatal("the service should be running")
	}
	py := filepath.Join(r.root, "echo", "service.py")
	b, _ := os.ReadFile(py)
	os.WriteFile(py, append(b, []byte("\n# edited\n")...), 0o644)

	res := r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "a"}})
	if res.Code != CodeChanged || !strings.Contains(res.Error, "approve it again") {
		t.Fatalf("after the change: %+v", res)
	}
	if r.sup.Running("echo") {
		t.Fatal("a changed add-on kept running")
	}
	if ins, _ := Installed(r.root, r.store); ins[0].State != "changed" {
		t.Fatalf("installed = %+v", ins)
	}
	// a new file counts too, and so does a manifest edit
	os.WriteFile(py, b, 0o644)
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeOK {
		t.Fatalf("restored: %+v", res)
	}
	os.WriteFile(filepath.Join(r.root, "echo", "extra.py"), []byte("x"), 0o644)
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeChanged {
		t.Fatalf("with a new file: %+v", res)
	}
	os.Remove(filepath.Join(r.root, "echo", "extra.py"))
	yaml := filepath.Join(r.root, "echo", "addon.yaml")
	y, _ := os.ReadFile(yaml)
	os.WriteFile(yaml, []byte(strings.Replace(string(y), "leaves_machine: false", "leaves_machine: true", 1)), 0o644)
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeChanged {
		t.Fatalf("with leaves_machine changed: %+v", res)
	}
	os.WriteFile(yaml, []byte("not a manifest"), 0o644)
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeChanged {
		t.Fatalf("with a broken manifest: %+v", res)
	}
}

func TestStartFailureEndsDown(t *testing.T) {
	r := newRig(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "addon.yaml"), []byte(strings.Replace(miscYAML, `["python3", "service.py"]`, `["./no-such-program"]`, 1)), 0o644)
	if _, err := Install(r.root, r.store, src, "", "tester", r.now); err != nil {
		t.Fatal(err)
	}
	var last Result
	for i := 0; i < 5; i++ {
		last = r.call("misc", Call{Tool: "_health"})
		if last.Code != CodeDown {
			t.Fatalf("attempt %d = %+v", i+1, last)
		}
	}
	if !strings.Contains(last.Error, "marked down") {
		t.Fatalf("last = %+v", last)
	}
}

func TestIdleStop(t *testing.T) {
	r := newRig(t)
	r.sup.IdleTimeout = 150 * time.Millisecond
	r.install(echoDir, nil)
	r.call("echo", Call{Tool: "_health"})
	if !r.sup.Running("echo") {
		t.Fatal("not running after a call")
	}
	if _, ok := RunningPID(r.root, "echo"); !ok {
		t.Fatal("no process id recorded")
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.sup.Running("echo") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.sup.Running("echo") {
		t.Fatal("not stopped after the idle time")
	}
	if _, ok := RunningPID(r.root, "echo"); ok {
		t.Fatal("process id file left behind")
	}
	// and the next call starts it without any fuss
	if res := r.call("echo", Call{Tool: "_health"}); res.Code != CodeOK {
		t.Fatalf("after idle stop: %+v", res)
	}
}

func TestMinimalEnvironment(t *testing.T) {
	t.Setenv("SOME_SECRET_TOKEN", "do-not-leak")
	env := strings.Join(minimalEnv("echo"), "\n")
	if strings.Contains(env, "do-not-leak") || strings.Contains(env, "HOME=") {
		t.Fatalf("environment too wide:\n%s", env)
	}
	if !strings.Contains(env, "PATH=") || !strings.Contains(env, "HANDLOOM_ADDON=echo") {
		t.Fatalf("environment too narrow:\n%s", env)
	}
}

func TestInstallRemoveAndRunning(t *testing.T) {
	r := newRig(t)
	a := r.install(echoDir, nil)
	if a.Name != "echo" || a.Version != "0.1.0" || a.By != "tester" || len(a.PackageHash) != 64 {
		t.Fatalf("approval = %+v", a)
	}
	if want, _ := PackageHash(echoDir); want != a.PackageHash {
		t.Fatal("approved hash differs from the source's")
	}
	// a pinned hash that does not match stops the install
	if _, err := Install(r.root, r.store, echoDir, strings.Repeat("0", 64), "tester", r.now); err == nil || !strings.Contains(err.Error(), "nothing was installed") {
		t.Fatalf("wrong pin: %v", err)
	}
	// installing the same add-on again is fine while it is not running
	r.install(echoDir, nil)
	// while it runs it is not replaced or removed
	r.call("echo", Call{Tool: "_health"})
	if _, err := Install(r.root, r.store, echoDir, "", "tester", r.now); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("replace while running: %v", err)
	}
	if err := Remove(r.root, r.store, "echo"); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("remove while running: %v", err)
	}
	r.sup.Close()
	if err := Remove(r.root, r.store, "echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "echo")); !os.IsNotExist(err) {
		t.Fatal("folder left behind")
	}
	if _, ok, _ := r.store.Get("echo"); ok {
		t.Fatal("approval left behind")
	}
	if err := Remove(r.root, r.store, "echo"); err == nil {
		t.Fatal("removing what is not there said nothing")
	}
	if entries, _ := os.ReadDir(r.root); len(entries) > 2 { // approvals.json and .running
		for _, e := range entries {
			t.Logf("left in the add-ons folder: %s", e.Name())
		}
		t.Fatal("temporary folders left behind")
	}
}

func TestInstalledStates(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)
	r.store.Put(Approval{Name: "gone", Version: "1.0.0", PackageHash: "x"})
	os.MkdirAll(filepath.Join(r.root, "loose"), 0o700)
	os.WriteFile(filepath.Join(r.root, "loose", "addon.yaml"), []byte(miscYAML), 0o644)
	got, err := Installed(r.root, r.store)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, i := range got {
		states[i.Name] = i.State
	}
	want := map[string]string{"echo": "ready", "gone": "missing", "loose": "changed"}
	for k, v := range want {
		if states[k] != v {
			t.Errorf("%s: %q, want %q (%v)", k, states[k], v, states)
		}
	}
	if len(states) != 3 {
		t.Errorf("states = %v", states)
	}
}

func TestIdleStopAfterRejectedCall(t *testing.T) {
	r := newRig(t)
	r.sup.IdleTimeout = 150 * time.Millisecond
	r.install(echoDir, nil)
	r.call("echo", Call{Tool: "_health"})
	if _, err := r.sup.Call(context.Background(), "echo", Call{Tool: "nope"}); err == nil {
		t.Fatal("an unknown tool was accepted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.sup.Running("echo") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.sup.Running("echo") {
		t.Fatal("not stopped after the idle time, following a rejected call")
	}
}

func TestStalePidFileIsNotRunning(t *testing.T) {
	r := newRig(t)
	if err := os.MkdirAll(filepath.Dir(pidPath(r.root, "echo")), 0o700); err != nil {
		t.Fatal(err)
	}
	// a file left by a link that died, naming a process that is alive (this one) but is not the service
	if err := os.WriteFile(pidPath(r.root, "echo"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := RunningPID(r.root, "echo"); ok {
		t.Fatal("a stale file counted as a running service")
	}
	r.install(echoDir, nil) // and it does not block an install
	r.call("echo", Call{Tool: "_health"})
	if _, ok := RunningPID(r.root, "echo"); !ok {
		t.Fatal("a live service was not seen as running")
	}
}

func TestServiceThatDiesBetweenCallsIsNotRunning(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)
	r.call("echo", Call{Tool: "_health"})
	pid, ok := RunningPID(r.root, "echo")
	if !ok {
		t.Fatal("a live service was not seen as running")
	}
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Kill()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := RunningPID(r.root, "echo"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a dead service still counts as running")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := Remove(r.root, r.store, "echo"); err != nil {
		t.Fatalf("remove after the service died: %v", err)
	}
}

// A link that was killed leaves its service running and an id file whose lock is gone. The next link ends the
// service, but only a process that is in the add-on's folder: a number that now belongs to something else is
// left alone.
func TestStopStraysEndsAnOrphanedServiceAndNothingElse(t *testing.T) {
	needPython(t)
	r := newRig(t)
	r.install(echoDir, nil)
	dir := filepath.Join(r.root, "echo")
	stray := exec.Command("sleep", "60")
	stray.Dir = dir
	ownGroup(stray)
	if err := stray.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { stray.Wait(); close(done) }()
	defer killTree(stray)
	other := exec.Command("sleep", "60") // not in the add-on's folder
	other.Dir = t.TempDir()
	ownGroup(other)
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { killTree(other); other.Wait() }()
	os.MkdirAll(filepath.Join(r.root, ".running"), 0o700)
	os.WriteFile(pidPath(r.root, "echo"), []byte(strconv.Itoa(stray.Process.Pid)+"\n"), 0o600)
	os.WriteFile(pidPath(r.root, "other"), []byte(strconv.Itoa(other.Process.Pid)+"\n"), 0o600)

	r.sup.StopStrays()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphaned service is still running")
	}
	if syscall.Kill(other.Process.Pid, 0) != nil {
		t.Fatal("a process outside the add-on's folder was ended")
	}
	if _, err := os.Stat(pidPath(r.root, "echo")); err == nil {
		t.Fatal("the id file was left")
	}
}

// An add-on written for another add-on API is not changed, it is one this program cannot run: it is listed as
// not running with the reason, a call answers plugin-down in plain words, and nothing is started.
func TestOtherAPIIsRefusedAsNotRunning(t *testing.T) {
	r := newRig(t)
	r.install(echoDir, nil)
	mf := filepath.Join(r.root, "echo", ManifestFile)
	b, _ := os.ReadFile(mf)
	os.WriteFile(mf, []byte(strings.Replace(string(b), "api: 1", "api: 2", 1)), 0o644)

	ins, _ := Installed(r.root, r.store)
	if len(ins) != 1 || ins[0].State != "down" || !strings.Contains(ins[0].Detail, "add-on API 2") || !strings.Contains(ins[0].Detail, "speaks API 1") || ins[0].PackageHash == "" {
		t.Fatalf("installed = %+v", ins)
	}
	res := r.call("echo", Call{Tool: "say", Args: map[string]any{"text": "a"}})
	if res.Code != CodeDown || !strings.Contains(res.Error, "not started") || !strings.Contains(res.Error, "API 2") {
		t.Fatalf("call = %+v", res)
	}
	if r.sup.Running("echo") {
		t.Fatal("an add-on for another API was started")
	}
	// a folder that is not installed yet is refused when added, with the same words
	dir := t.TempDir()
	if err := copyTree(echoDir, dir); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ManifestFile), []byte(strings.Replace(string(b), "api: 1", "api: 3", 1)), 0o644)
	if _, err := Install(r.root, r.store, dir, "", "tester", r.now); err == nil || !strings.Contains(err.Error(), "API 3") {
		t.Fatalf("install = %v", err)
	}
}
