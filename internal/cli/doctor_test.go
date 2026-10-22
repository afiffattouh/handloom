package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorSaysWhatIsMissing(t *testing.T) {
	has := map[string]bool{"git": true, "tmux": true, "claude": true}
	look := func(b string) (string, error) {
		if has[b] {
			return "/bin/" + b, nil
		}
		return "", errors.New("no")
	}
	up := func() (map[string]any, error) { return map[string]any{"device": "d", "hub": "h"}, nil }
	home := func() (string, error) { return t.TempDir(), nil } // no login files
	stat := os.Stat

	byName := func(cs []doctorCheck) map[string]doctorCheck {
		m := map[string]doctorCheck{}
		for _, c := range cs {
			m[c.Name] = c
		}
		return m
	}
	// claude is installed but not logged in: nothing can run an agent.
	got := byName(runDoctor(look, up, home, stat))
	if got["claude"].Status != "warn" || got["agent CLI"].Status != "fail" || got["link"].Status != "ok" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got["claude"].Fix, "log in") {
		t.Fatalf("no hint: %+v", got["claude"])
	}
	// A CLI whose login handloom cannot see counts as ready.
	has["omp"] = true
	got = byName(runDoctor(look, up, home, stat))
	if got["agent CLI"].Status != "" {
		t.Fatalf("one usable CLI is enough: %+v", got)
	}
	// No link, no tmux: both hard failures, each with a fix.
	delete(has, "tmux")
	down := func() (map[string]any, error) { return nil, errors.New("down") }
	got = byName(runDoctor(look, down, home, stat))
	if got["link"].Status != "fail" || got["tmux"].Status != "fail" || got["link"].Fix == "" || got["tmux"].Fix == "" {
		t.Fatalf("%+v", got)
	}
}

// A computer with no add-ons gets no add-on lines; one with add-ons gets a line each, and a problem with
// an add-on does not make the computer "not ready".
func TestDoctorHasALinePerAddonAndNothingWithout(t *testing.T) {
	addonHome(t)
	if got := addonChecks(); len(got) != 0 {
		t.Fatalf("no add-ons: %+v", got)
	}
	if out := renderDoctor(append([]doctorCheck{{"git", "ok", "/usr/bin/git", ""}}, addonChecks()...), installHint); strings.Contains(out, "Add-ons") {
		t.Fatalf("a heading with nothing under it: %q", out)
	}
	if code, _, errs := addonRun(t, "add", echoAddon, "--yes"); code != 0 {
		t.Fatalf("add: %q", errs)
	}
	got := addonChecks()
	if len(got) != 1 || got[0].Name != "add-on echo 0.1.0" || got[0].Status != "ok" || got[0].Detail != "ready" {
		t.Fatalf("ready: %+v", got)
	}
	out := renderDoctor(got, installHint)
	if !strings.HasPrefix(out, "Ready: this machine can run agents\n") || !strings.Contains(out, "\nAdd-ons:\n   echo 0.1.0: ready\n") {
		t.Fatalf("rendered: %q", out)
	}
	// a changed add-on is said in words, as a warning
	py := filepath.Join(addonRoot(), "echo", "service.py")
	b, _ := os.ReadFile(py)
	os.WriteFile(py, append(b, '\n', '#'), 0o644)
	got = addonChecks()
	if len(got) != 1 || got[0].Status != "warn" || !strings.Contains(got[0].Detail, "changed since it was approved") {
		t.Fatalf("changed: %+v", got)
	}
	out = renderDoctor(got, installHint)
	if !strings.HasPrefix(out, "Ready:") || !strings.Contains(out, "echo 0.1.0: changed since it was approved") || strings.Contains(out, "Also:") {
		t.Fatalf("rendered: %q", out)
	}
	// a folder that is gone
	os.RemoveAll(filepath.Join(addonRoot(), "echo"))
	if got = addonChecks(); len(got) != 1 || !strings.Contains(got[0].Detail, "folder is gone") {
		t.Fatalf("missing: %+v", got)
	}
}
