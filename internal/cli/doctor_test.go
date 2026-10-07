package cli

import (
	"errors"
	"os"
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
