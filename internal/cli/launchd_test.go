package cli

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pretend runs the body as another system, with launchctl and friends faked.
func pretend(t *testing.T, system string, uid int, run func(name string, args ...string) ([]byte, error)) {
	t.Helper()
	oldOS, oldUID, oldRun := goos, getuid, runCmd
	goos, getuid, runCmd = system, func() int { return uid }, run
	t.Cleanup(func() { goos, getuid, runCmd = oldOS, oldUID, oldRun })
}

func TestLaunchdNamesAndPaths(t *testing.T) {
	if launchdLabel("handloom-link") != "ai.handloom.link" || launchdLabel("handloom-hub") != "ai.handloom.hub" {
		t.Fatal("labels")
	}
	if got := launchdPlistPath("/Users/ann", "handloom-link"); got != "/Users/ann/Library/LaunchAgents/ai.handloom.link.plist" {
		t.Fatal(got)
	}
	if got := launchdLogPath("/Users/ann", "handloom-link"); got != "/Users/ann/Library/Logs/handloom-link.log" {
		t.Fatal(got)
	}
}

func TestLaunchdPathHasHomebrewAndTheProgramFirstOnceEach(t *testing.T) {
	got := launchdPath("/Users/ann/.local/bin", "/opt/homebrew/bin:/Users/ann/.npm-global/bin:/usr/bin:/Users/ann/.local/bin")
	parts := strings.Split(got, ":")
	if parts[0] != "/Users/ann/.local/bin" {
		t.Fatalf("the program's folder comes first: %s", got)
	}
	seen := map[string]int{}
	for _, p := range parts {
		seen[p]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times in %s", p, n, got)
		}
	}
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/Users/ann/.npm-global/bin", "/usr/bin", "/sbin"} {
		if seen[want] == 0 {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if got := launchdPath("/b", ""); strings.Contains(got, "::") || strings.HasSuffix(got, ":") {
		t.Errorf("empty PATH leaves gaps: %s", got)
	}
}

func TestPlistTextIsValidXMLWithTheRightKeys(t *testing.T) {
	s := service{name: "handloom-link", args: []string{"link", "run"}, env: map[string]string{"HANDLOOM_HOME": "/Users/ann/.config/handloom"}}
	text := s.plistText("/usr/local/bin/handloom", "/Users/ann", "/opt/homebrew/bin:/usr/bin", "/Users/ann/Library/Logs/handloom-link.log")
	d := xml.NewDecoder(strings.NewReader(text))
	d.Strict = true
	d.Entity = xml.HTMLEntity
	for {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, text)
		}
	}
	for _, want := range []string{
		"<key>Label</key>\n\t<string>ai.handloom.link</string>",
		"<string>/usr/local/bin/handloom</string>\n\t\t<string>link</string>\n\t\t<string>run</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>",
		"<key>HANDLOOM_HOME</key>\n\t\t<string>/Users/ann/.config/handloom</string>",
		"<key>HOME</key>\n\t\t<string>/Users/ann</string>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin</string>",
		"<key>StandardOutPath</key>\n\t<string>/Users/ann/Library/Logs/handloom-link.log</string>",
		"<key>StandardErrorPath</key>\n\t<string>/Users/ann/Library/Logs/handloom-link.log</string>",
		"<key>WorkingDirectory</key>", "handloom link uninstall",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q\n%s", want, text)
		}
	}
	// the environment keys come out in a fixed order
	if strings.Index(text, "<key>HANDLOOM_HOME</key>") > strings.Index(text, "<key>HOME</key>") || strings.Index(text, "<key>HOME</key>") > strings.Index(text, "<key>PATH</key>") {
		t.Errorf("environment is not sorted:\n%s", text)
	}
	if text != s.plistText("/usr/local/bin/handloom", "/Users/ann", "/opt/homebrew/bin:/usr/bin", "/Users/ann/Library/Logs/handloom-link.log") {
		t.Error("not deterministic")
	}
}

func TestPlistTextEscapesWhatXMLCannotHold(t *testing.T) {
	s := service{name: "handloom-hub", args: []string{"hub", "serve", "--data", "/Users/a & b/<data>"}, env: map[string]string{"X": `say "hi" & 'bye'`}}
	text := s.plistText("/Users/a & b/bin/handloom", "/Users/a & b", "/x", "/log")
	if strings.Contains(text, "a & b") || strings.Contains(text, "<data>") {
		t.Fatalf("raw & or < in the file:\n%s", text)
	}
	for _, want := range []string{"/Users/a &amp; b/bin/handloom", "/Users/a &amp; b/&lt;data&gt;", "&amp;"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q", want)
		}
	}
	// and it still reads back as the original text
	dec := xml.NewDecoder(strings.NewReader(text))
	var strs []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "string" {
			var v string
			dec.DecodeElement(&v, &se)
			strs = append(strs, v)
		}
	}
	if strings.Join(strs, "|") == "" || !strings.Contains(strings.Join(strs, "|"), "/Users/a & b/<data>") || !strings.Contains(strings.Join(strs, "|"), `say "hi" & 'bye'`) {
		t.Fatalf("round trip: %q", strs)
	}
}

type launchctlFake struct {
	calls   []string
	failing map[string]bool // "bootstrap" ...: these subcommands fail
}

func (f *launchctlFake) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "launchctl" && f.failing[args[0]] {
		return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
	}
	return nil, nil
}

func quietEnv() (*env, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &env{out: out, err: &bytes.Buffer{}}, out
}

func TestInstallLaunchdWritesThePlistAndBootstraps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &launchctlFake{}
	pretend(t, "darwin", 501, f.run)
	e, out := quietEnv()
	if err := e.installService(service{name: "handloom-link", description: "d", args: []string{"link", "run"}}); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "ai.handloom.link.plist")
	b, err := os.ReadFile(plist)
	if err != nil || !strings.Contains(string(b), "<string>ai.handloom.link</string>") {
		t.Fatalf("plist: %v %s", err, b)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "Logs")); err != nil {
		t.Fatal("the log folder was not made")
	}
	want := []string{"launchctl bootout gui/501/ai.handloom.link", "launchctl bootstrap gui/501 " + plist}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
	if !strings.Contains(out.String(), "Installed and started handloom-link") || !strings.Contains(out.String(), "handloom-link.log") {
		t.Fatalf("says: %s", out)
	}
	// uninstall: bootout, then the file is gone
	f.calls = nil
	out.Reset()
	if err := e.uninstallService("handloom-link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); err == nil {
		t.Fatal("plist still there")
	}
	if len(f.calls) != 1 || f.calls[0] != "launchctl bootout gui/501/ai.handloom.link" {
		t.Fatalf("calls: %v", f.calls)
	}
	// and a second uninstall says so instead of failing
	out.Reset()
	if err := e.uninstallService("handloom-link"); err != nil || !strings.Contains(out.String(), "is not installed") {
		t.Fatalf("again: %v %s", err, out)
	}
}

func TestInstallLaunchdFallsBackToLoadAndSaysWhenBothFail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &launchctlFake{failing: map[string]bool{"bootstrap": true}}
	pretend(t, "darwin", 501, f.run)
	e, _ := quietEnv()
	if err := e.installService(service{name: "handloom-link", args: []string{"link", "run"}}); err != nil {
		t.Fatalf("load should rescue a failed bootstrap: %v", err)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "launchctl load -w ") {
		t.Fatalf("calls: %v", f.calls)
	}
	f = &launchctlFake{failing: map[string]bool{"bootstrap": true, "load": true}}
	pretend(t, "darwin", 501, f.run)
	err := e.installService(service{name: "handloom-link", args: []string{"link", "run"}})
	if err == nil || !strings.Contains(err.Error(), "logged-in desktop session") {
		t.Fatalf("want a plain error, got %v", err)
	}
}

func TestUninstallLaunchdFallsBackToUnload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &launchctlFake{failing: map[string]bool{"bootout": true}}
	pretend(t, "darwin", 501, f.run)
	e, _ := quietEnv()
	plist := launchdPlistPath(home, "handloom-hub")
	os.MkdirAll(filepath.Dir(plist), 0o755)
	os.WriteFile(plist, []byte("x"), 0o644)
	if err := e.uninstallService("handloom-hub"); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last != "launchctl unload -w "+plist {
		t.Fatalf("calls: %v", f.calls)
	}
	if _, err := os.Stat(plist); err == nil {
		t.Fatal("plist still there")
	}
}

func TestUnitPathOnMacIsTheLaunchAgent(t *testing.T) {
	t.Setenv("HOME", "/Users/ann")
	pretend(t, "darwin", 501, nil)
	p, system, err := unitPath("handloom-link")
	if err != nil || system || p != "/Users/ann/Library/LaunchAgents/ai.handloom.link.plist" {
		t.Fatalf("%q %v %v", p, system, err)
	}
}

// join starts the link the way the system does it: on a Mac with launchd, with nothing about systemd.
func TestJoinKeepsTheLinkRunningWithLaunchdOnAMac(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HANDLOOM_HOME", filepath.Join(home, ".config", "handloom"))
	f := &launchctlFake{}
	pretend(t, "darwin", 501, f.run)
	e, out := quietEnv()
	if err := e.startLink(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "the link runs as a service and starts at login") {
		t.Fatalf("says: %s", out)
	}
	if _, err := os.Stat(launchdPlistPath(home, "handloom-link")); err != nil {
		t.Fatal("no plist")
	}
	b, _ := os.ReadFile(launchdPlistPath(home, "handloom-link"))
	if !strings.Contains(string(b), "<key>HANDLOOM_HOME</key>") || !strings.Contains(string(b), filepath.Join(home, ".config", "handloom")) {
		t.Fatalf("the link's folder was not passed on:\n%s", b)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "systemctl") {
			t.Fatalf("systemd on a Mac: %v", f.calls)
		}
	}
}
