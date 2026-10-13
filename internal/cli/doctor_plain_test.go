package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"handloom/internal/client"
)

func hintFake(tool string) string { return "install-" + tool }

func TestDoctorRendersReadyInOneLine(t *testing.T) {
	checks := []doctorCheck{{"link", "ok", "running", ""}, {"git", "ok", "/usr/bin/git", ""}, {"tmux", "ok", "/usr/bin/tmux", ""}, {"claude", "ok", "x, logged in", ""}}
	got := renderDoctor(checks, hintFake)
	if !strings.HasPrefix(got, "Ready: this machine can run agents\n") || strings.Contains(got, "Not ready") || strings.Contains(got, "Do this") {
		t.Fatalf("%q", got)
	}
}

func TestDoctorRendersEachThingToDoAsASentenceAndAStep(t *testing.T) {
	checks := []doctorCheck{
		{"link", "fail", "not running", "paste the join line"},
		{"git", "ok", "/usr/bin/git", ""},
		{"tmux", "fail", "not found", "old text"},
		{"claude", "warn", "x, but no login found", "run claude and log in"},
		{"agent CLI", "fail", "none", "old text"},
	}
	got := renderDoctor(checks, hintFake)
	if !strings.HasPrefix(got, "Not ready yet: 3 things to do\n") {
		t.Fatalf("first line: %q", got)
	}
	for _, want := range []string{
		"1. The link,", "Do this: paste the join line",
		"2. tmux is not installed.", "Do this: install-tmux",
		"3. No agent program", "Do this: install one agent program and sign in",
		"claude is installed but not signed in. Do this: run claude and log in",
		"npm install -g @openai/codex", "`handloom doctor --fix`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old text") {
		t.Errorf("a git/tmux step must be the exact install command:\n%s", got)
	}
	one := renderDoctor([]doctorCheck{{"tmux", "fail", "", ""}}, hintFake)
	if !strings.HasPrefix(one, "Not ready yet: 1 thing to do\n") {
		t.Errorf("singular: %q", one)
	}
}

// doctorRig runs `doctor` with no link socket and fake tools.
func doctorRig(t *testing.T, tools ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		os.WriteFile(dir+"/"+tool, []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	_ = client.Home
}

func TestDoctorCommandFixInstallsNothingWithoutAnAnswerAndPrintsTheCommand(t *testing.T) {
	doctorRig(t) // no git, no tmux, no agent
	r := newFixRig("linux", 1000, false, "apt-get", "sudo")
	old := fixerFor
	fixerFor = func(*env) *fixer { return r.f }
	defer func() { fixerFor = old }()
	out := &bytes.Buffer{}
	e := &env{out: out, err: &bytes.Buffer{}}
	r.f.out = out
	err := e.doctor([]string{"--fix"})
	if err == nil {
		t.Fatal("still not ready: the command must fail")
	}
	got := out.String()
	for _, want := range []string{"sudo apt-get install -y git tmux", "Nothing was run", "Not ready yet: 4 things to do", "claude.ai/install.sh"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %v", r.ran)
	}
}

func TestDoctorCommandFixRunsAfterYesAndChecksAgain(t *testing.T) {
	doctorRig(t)
	dir := os.Getenv("PATH")
	r := newFixRig("linux", 1000, true, "apt-get", "sudo")
	r.answer = true
	// "installing" puts the two tools on the PATH, as the package manager would
	r.f.run = func(argv []string) error {
		r.ran = append(r.ran, argv)
		for _, tool := range []string{"git", "tmux"} {
			os.WriteFile(dir+"/"+tool, []byte("#!/bin/sh\n"), 0o755)
		}
		return nil
	}
	old := fixerFor
	fixerFor = func(*env) *fixer { return r.f }
	defer func() { fixerFor = old }()
	out := &bytes.Buffer{}
	r.f.out = out
	e := &env{out: out, err: &bytes.Buffer{}}
	e.doctor([]string{"--fix"})
	got := out.String()
	if !strings.Contains(got, "Installed. Checking again.") || strings.Contains(got, "git is not installed") || strings.Contains(got, "tmux is not installed") {
		t.Fatalf("after the install:\n%s", got)
	}
	if !strings.Contains(got, "Not ready yet: 2 things to do") { // the link and the agent are still to do
		t.Fatalf("count:\n%s", got)
	}
}

func TestDoctorJSONStaysAList(t *testing.T) {
	doctorRig(t)
	out := &bytes.Buffer{}
	e := &env{out: out, err: &bytes.Buffer{}}
	e.doctor([]string{"--json"})
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[") || !strings.Contains(out.String(), `"status": "fail"`) {
		t.Fatalf("%s", out)
	}
	_ = errors.New
}
