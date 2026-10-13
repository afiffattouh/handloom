package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProfileBlockIsGuardedAndQuoted(t *testing.T) {
	b := profileBlock("/home/o'brien/.local/bin/handloom", "/home/o'brien/.config/handloom", true)
	for _, want := range []string{profileBegin, profileEnd, `[ -x '/home/o'\''brien/.local/bin/handloom' ] && HANDLOOM_HOME='/home/o'\''brien/.config/handloom' '/home/o'\''brien/.local/bin/handloom' link start --quiet >/dev/null 2>&1`} {
		if !strings.Contains(b, want) {
			t.Errorf("lacks %q in\n%s", want, b)
		}
	}
	if strings.Contains(profileBlock("/b/handloom", "/h", false), "HANDLOOM_HOME") {
		t.Error("the default folder needs no variable")
	}
}

func TestProfileBlockIsIdempotentAndRemovable(t *testing.T) {
	block := profileBlock("/b/handloom", "/h", false)
	original := "export A=1\nalias ll='ls -l'"
	once, changed := withProfileBlock(original, block)
	if !changed || !strings.HasPrefix(once, original+"\n") || !strings.HasSuffix(once, block) {
		t.Fatalf("first add:\n%s", once)
	}
	twice, changed := withProfileBlock(once, block)
	if changed || twice != once {
		t.Fatalf("a second add changed the file:\n%s", twice)
	}
	if strings.Count(twice, profileBegin) != 1 {
		t.Fatal("two blocks")
	}
	// a moved program replaces the block in place, the lines around it are untouched
	moved := profileBlock("/new/handloom", "/h", false)
	mid := "export A=1\n" + block + "export B=2\n"
	got, changed := withProfileBlock(mid, moved)
	if !changed || got != "export A=1\n"+moved+"export B=2\n" {
		t.Fatalf("replace:\n%s", got)
	}
	// removal gives back exactly what was there before
	gone, did := withoutProfileBlock(once)
	if !did || gone != original+"\n" {
		t.Fatalf("removed:\n%q", gone)
	}
	if g, did := withoutProfileBlock("export A=1\n"); did || g != "export A=1\n" {
		t.Fatal("nothing to remove")
	}
	if g, did := withProfileBlock("", block); !did || g != block {
		t.Fatalf("empty file: %q", g)
	}
	// a half-written block (no end line) is left alone rather than eating the file
	broken := "x\n" + profileBegin + "\ny\n"
	if g, did := withoutProfileBlock(broken); did || g != broken {
		t.Fatal("a block with no end must not be cut")
	}
}

func TestProfileFileForTheShell(t *testing.T) {
	none := func(string) bool { return false }
	has := func(p string) bool { return strings.HasSuffix(p, ".bash_profile") }
	for _, c := range []struct {
		shell  string
		exists func(string) bool
		want   string
	}{
		{"/bin/zsh", none, "/h/.zprofile"}, {"/usr/bin/zsh", has, "/h/.zprofile"},
		{"/bin/bash", none, "/h/.profile"}, {"/bin/bash", has, "/h/.bash_profile"},
		{"/bin/sh", none, "/h/.profile"}, {"", none, "/h/.profile"}, {"/usr/bin/fish", none, "/h/.profile"},
	} {
		if got := profileFileFor(c.shell, "/h", c.exists); got != c.want {
			t.Errorf("%q: %s, want %s", c.shell, got, c.want)
		}
	}
}

func TestSystemdAvailable(t *testing.T) {
	for _, c := range []struct {
		goos        string
		ctl, booted bool
		forced      string
		want        bool
	}{
		{"linux", true, true, "", true},
		{"linux", true, false, "", false}, // WSL without systemd: systemctl is there, systemd is not running
		{"linux", false, true, "", false},
		{"linux", true, true, "1", false}, // switched off
		{"darwin", true, true, "", false},
	} {
		if got := systemdAvailable(c.goos, c.ctl, c.booted, c.forced); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestInstallServiceWithoutSystemdSaysWhatToDoInstead(t *testing.T) {
	pretend(t, "linux", 1000, nil)
	t.Setenv("HANDLOOM_NO_SYSTEMD", "1")
	e, _ := quietEnv()
	err := e.installService(service{name: "handloom-link", args: []string{"link", "run"}})
	if err == nil || !strings.Contains(err.Error(), "systemd is not running") || !strings.Contains(err.Error(), "WSL without systemd") {
		t.Fatalf("%v", err)
	}
}

func fakeLink(t *testing.T) (started *int) {
	t.Helper()
	n := 0
	up := false
	oldUp, oldStart, oldSelf := linkUp, startDetachedFn, selfPath
	linkUp = func() bool { return up }
	startDetachedFn = func(bin, home string) (int, error) { n++; up = true; return 4242, nil }
	selfPath = func() (string, error) { return "/opt/hl/handloom", nil }
	t.Cleanup(func() { linkUp, startDetachedFn, selfPath = oldUp, oldStart, oldSelf })
	return &n
}

func TestJoinWithoutAServiceStartsTheLinkAndWritesTheLoginLineOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("HANDLOOM_HOME", filepath.Join(home, ".config", "handloom"))
	t.Setenv("HANDLOOM_NO_SYSTEMD", "1")
	pretend(t, "linux", 1000, nil)
	started := fakeLink(t)
	os.WriteFile(filepath.Join(home, ".profile"), []byte("export KEEP=1\n"), 0o644)

	e, out := quietEnv()
	if err := e.startLink(false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".profile"))
	if !strings.HasPrefix(string(b), "export KEEP=1\n") || strings.Count(string(b), profileBegin) != 1 {
		t.Fatalf("profile:\n%s", b)
	}
	for _, want := range []string{"no service manager works here", "running in the background (process 4242)", "added one line to", "only at login", "handloom link uninstall"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("says nothing about %q:\n%s", want, out)
		}
	}
	// again: the link is up, the file is not touched twice
	out.Reset()
	if err := e.startLink(false); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(home, ".profile"))
	if string(b2) != string(b) || *started != 1 {
		t.Fatalf("not idempotent: started %d times\n%s", *started, b2)
	}
	if !strings.Contains(out.String(), "already starts the link at login") || !strings.Contains(out.String(), "already running") {
		t.Fatalf("says: %s", out)
	}
	// uninstall removes the line and leaves the rest
	out.Reset()
	if err := e.uninstallDetached(); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(filepath.Join(home, ".profile"))
	if string(b3) != "export KEEP=1\n" || !strings.Contains(out.String(), "Removed the start-at-login line") {
		t.Fatalf("after uninstall:\n%q\n%s", b3, out)
	}
}

func TestNoServiceFlagSkipsTheServiceManagerEvenWhereThereIsOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("HANDLOOM_HOME", filepath.Join(home, "h"))
	f := &launchctlFake{}
	pretend(t, "darwin", 501, f.run)
	fakeLink(t)
	e, out := quietEnv()
	if err := e.startLink(true); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("launchctl was used: %v", f.calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".zprofile")); err != nil || !strings.Contains(out.String(), "as asked") {
		t.Fatalf("no .zprofile on a zsh Mac: %v\n%s", err, out)
	}
}

func TestStopDetachedStopsOnlyAHandloomLinkItStarted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HANDLOOM_HOME", home)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Skip("no sleep")
	}
	done := make(chan error, 1)
	go func() { done <- sleeper.Wait() }()
	defer sleeper.Process.Kill()
	pid := sleeper.Process.Pid

	// the process number belongs to something that is not a handloom link: leave it alone
	pretend(t, "linux", 1000, func(name string, args ...string) ([]byte, error) { return []byte("sleep 60\n"), nil })
	os.WriteFile(linkPidFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600)
	if stopped, _ := stopDetached(); stopped {
		t.Fatal("stopped a process that is not the link")
	}
	if _, err := os.Stat(linkPidFile()); err == nil {
		t.Fatal("the stale pid file should go")
	}
	select {
	case <-done:
		t.Fatal("the other process was killed")
	case <-time.After(200 * time.Millisecond):
	}
	// now it does look like the link
	pretend(t, "linux", 1000, func(name string, args ...string) ([]byte, error) {
		if name != "ps" || args[len(args)-1] != strconv.Itoa(pid) {
			t.Errorf("asked %s %v", name, args)
		}
		return []byte("/home/a/.local/bin/handloom link run\n"), nil
	})
	os.WriteFile(linkPidFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600)
	if stopped, _ := stopDetached(); !stopped {
		t.Fatal("did not stop the link")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the process is still running")
	}
	// nothing to stop
	if stopped, _ := stopDetached(); stopped {
		t.Fatal("stopped nothing")
	}
}

func TestLinkStopSaysWhenThereIsNothingToStop(t *testing.T) {
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	e := &env{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	if err := e.linkStop(nil); err != nil || !strings.Contains(e.out.(*bytes.Buffer).String(), "No background link") {
		t.Fatalf("%v %s", err, e.out)
	}
}
