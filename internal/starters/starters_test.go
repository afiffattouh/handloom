package starters

import (
	"strings"
	"testing"

	"handloom/internal/profile"
)

func TestEveryStarterIsWellFormed(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 40 {
		t.Fatalf("only %d starters", len(all))
	}
	seen := map[string]bool{}
	core := 0
	used := map[string]bool{}
	for _, st := range all {
		if seen[st.Name] || !profile.ValidName(st.Name) {
			t.Errorf("bad or repeated name %q", st.Name)
		}
		seen[st.Name] = true
		if st.Title == "" || st.Group == "" || st.Summary == "" {
			t.Errorf("%s lacks its title, group or summary", st.Name)
		}
		if st.Core {
			core++
		}
		for _, sk := range st.Skills {
			used[sk] = true
		}
		r, err := Build(st.Name, Choice{Kind: "claude", Runtime: "cloud"})
		if err != nil || len(r.Problems) != 0 {
			t.Errorf("%s as claude/cloud: %v %v", st.Name, err, r.Problems)
			continue
		}
		if len(strings.TrimSpace(r.Spec.Prompt)) < 200 {
			t.Errorf("%s has almost no instructions", st.Name)
		}
		if strings.Contains(r.Spec.Prompt, "lorem") || strings.Contains(r.Spec.Prompt, "TODO") {
			t.Errorf("%s has placeholder text", st.Name)
		}
		if len(r.Spec.Tools.Allow) == 0 {
			t.Errorf("%s has no tools", st.Name)
		}
		// Every skill file is a SKILL.md with the frontmatter the CLI reads.
		for _, f := range r.Spec.Skills {
			if !strings.HasPrefix(f.Content, "---\nname: ") || !strings.Contains(f.Content, "\ndescription: ") {
				t.Errorf("%s: skill %s lacks its frontmatter", st.Name, f.Path)
			}
		}
		if h1, h2 := profile.Hash(r.Spec), profile.Hash(mustTemplate(t, st.Name, "claude", "cloud")); h1 != h2 {
			t.Errorf("%s does not hash the same twice", st.Name)
		}
	}
	if core != 10 {
		t.Errorf("%d core starters, want 10", core)
	}
	for _, s := range Skills() {
		if !used[s.Name] {
			t.Errorf("the skill %s is used by no starter", s.Name)
		}
		if s.Description == "" {
			t.Errorf("the skill %s has no description", s.Name)
		}
	}
}

func mustTemplate(t *testing.T, name, kind, runtime string) *profile.Spec {
	t.Helper()
	r, err := Build(name, Choice{Kind: kind, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	return r.Spec
}

func TestCodexCannotHaveSkillsOrDeniedCommandsAndSaysSo(t *testing.T) {
	// A starter with skills and denied commands, as Codex: refused, in words, unless it is adapted.
	r, err := Build("coder", Choice{Kind: "codex", Runtime: "cloud"})
	if err != nil || len(r.Problems) == 0 {
		t.Fatalf("coder as codex should be refused: %v %v", err, r.Problems)
	}
	joined := strings.Join(r.Problems, " | ")
	for _, want := range []string{"cannot refuse specific shell commands"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the problems do not mention %q: %s", want, joined)
		}
	}
	a, err := Build("coder", Choice{Kind: "codex", Runtime: "cloud", Adapt: true})
	if err != nil || len(a.Problems) != 0 || len(a.Adapted) < 1 || len(a.Spec.Skills) == 0 {
		t.Fatalf("adapted: %+v %v", a, err)
	}
	// A read-only starter has no shell: Codex would add one, and says so.
	a, _ = Build("lead", Choice{Kind: "codex", Runtime: "cloud", Adapt: true})
	if len(a.Problems) != 0 || !strings.Contains(strings.Join(a.Adapted, " "), "shell") {
		t.Fatalf("lead as codex: %+v", a)
	}
	// The choice is required.
	if r, _ := Build("coder", Choice{}); len(r.Problems) == 0 {
		t.Fatal("no CLI chosen should be a problem")
	}
	if _, err := Build("nope", Choice{Kind: "claude", Runtime: "cloud"}); err == nil {
		t.Fatal("an unknown starter")
	}
}

func TestLeadsNeverApproveAndKnowledgeCuratorHasNoWritePaths(t *testing.T) {
	for _, n := range []string{"lead", "lead-engineering", "lead-consulting"} {
		s := mustTemplate(t, n, "claude", "cloud")
		if !strings.Contains(s.Prompt, "Only a human closes a job") {
			t.Errorf("%s does not say that only a human approves", n)
		}
		if len(s.Write) != 0 || len(s.Tools.Allow) > 2 {
			t.Errorf("%s should be read-mostly: %+v", n, s.Tools)
		}
	}
	if s := mustTemplate(t, "knowledge-curator", "claude", "local"); len(s.Write) != 0 {
		t.Errorf("the curator's work is in .handloom, which a write path cannot reach: %v", s.Write)
	}
	for _, n := range []string{"financial-analyst", "finance-reporter", "budget-reviewer"} {
		if s := mustTemplate(t, n, "claude", "cloud"); !strings.Contains(s.Prompt, "need a human check") {
			t.Errorf("%s does not say its figures need a human check", n)
		}
	}
}

func TestEveryStarterBecomesValidClaudeAndCodexFlags(t *testing.T) {
	all, _ := All()
	for _, st := range all {
		r, _ := Build(st.Name, Choice{Kind: "claude", Runtime: "cloud"})
		argv := profile.ClaudeArgv(r.Spec, "")
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "--allowedTools") {
			t.Errorf("%s: no allowed tools in %v", st.Name, argv)
		}
		for _, a := range argv {
			if a == "" {
				t.Errorf("%s: an empty argument in %v", st.Name, argv)
			}
		}
		for _, c := range r.Spec.Tools.DenyCommands {
			if !strings.Contains(joined, "Bash("+c) {
				t.Errorf("%s: the denied command %q is not in the flags: %v", st.Name, c, argv)
			}
		}
		for _, kind := range profile.Kinds {
			a, err := Build(st.Name, Choice{Kind: kind, Runtime: "cloud", Adapt: true})
			if err != nil || len(a.Problems) != 0 {
				t.Errorf("%s as %s, adapted: %v %v", st.Name, kind, err, a.Problems)
				continue
			}
			if len(a.Spec.Skills) == 0 && len(st.Skills) > 0 {
				t.Errorf("%s as %s lost its skills", st.Name, kind)
			}
			var line string
			switch kind {
			case "claude":
				continue
			case "codex":
				line = strings.Join(profile.CodexArgv(a.Spec, "", "handloom", "/tmp/h", "x"), " ")
			case "omp":
				line = strings.Join(profile.OmpArgv(a.Spec, "", "worker", "/w/e.ts"), " ")
			case "pi":
				line = strings.Join(profile.PiArgv(a.Spec, ""), " ")
			case "opencode":
				cfg, err := profile.OpenCodeConfig(a.Spec, "")
				if err != nil || !strings.Contains(string(cfg), `"handloom *": "allow"`) {
					t.Errorf("%s: opencode config: %v %s", st.Name, err, cfg)
				}
				line = strings.Join(profile.OpenCodeArgv(a.Spec, ""), " ")
			}
			if line == "" || strings.Contains(line, "  ") {
				t.Errorf("%s as %s: bad flags %q", st.Name, kind, line)
			}
		}
	}
}
