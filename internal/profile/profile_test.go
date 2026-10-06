package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func good() *Spec {
	return &Spec{Description: "reads", Kind: "claude", Tools: Tools{Allow: []string{"shell", "read"}, DenyCommands: []string{"rm", "git push"}},
		Prompt: "Be careful.", Skills: []File{{Path: "cite/SKILL.md", Content: "---\nname: cite\n---\nCite."}, {Path: "cite/examples/one.md", Content: "x"}}}
}

func TestNormalizeFillsDefaultsAndAcceptsAGoodSpec(t *testing.T) {
	s := good()
	if bad := Normalize(s); len(bad) != 0 {
		t.Fatalf("problems: %v", bad)
	}
	if s.Runtime != Cloud || s.Kind != "claude" || s.Tools.Allow[0] != "read" {
		t.Fatalf("defaults or order: %+v", s)
	}
}

func TestNormalizeRefusesWhatIsNotPlain(t *testing.T) {
	cases := map[string]func(*Spec){
		"kind":               func(s *Spec) { s.Kind = "emacs" },
		"runtime":            func(s *Spec) { s.Runtime = "moon" },
		"model":              func(s *Spec) { s.Model = "x; rm -rf /" },
		"tool":               func(s *Spec) { s.Tools.Allow = append(s.Tools.Allow, "root") },
		"deny wildcard":      func(s *Spec) { s.Tools.DenyCommands = []string{"rm *"} },
		"deny quote":         func(s *Spec) { s.Tools.DenyCommands = []string{`rm "x"`} },
		"deny bracket":       func(s *Spec) { s.Tools.DenyCommands = []string{"rm)"} },
		"deny without shell": func(s *Spec) { s.Tools.Allow = []string{"read"} },
		"skill traversal":    func(s *Spec) { s.Skills = append(s.Skills, File{Path: "../evil/SKILL.md", Content: "x"}) },
		"skill absolute":     func(s *Spec) { s.Skills = append(s.Skills, File{Path: "/etc/passwd", Content: "x"}) },
		"skill dotdot":       func(s *Spec) { s.Skills = append(s.Skills, File{Path: "a/../../b/SKILL.md", Content: "x"}) },
		"skill backslash":    func(s *Spec) { s.Skills = append(s.Skills, File{Path: `a\b/SKILL.md`, Content: "x"}) },
		"skill no folder":    func(s *Spec) { s.Skills = append(s.Skills, File{Path: "SKILL.md", Content: "x"}) },
		"skill no SKILL.md":  func(s *Spec) { s.Skills = append(s.Skills, File{Path: "lonely/notes.md", Content: "x"}) },
		"skill big": func(s *Spec) {
			s.Skills = append(s.Skills, File{Path: "big/SKILL.md", Content: strings.Repeat("x", maxSkillFile+1)})
		},
		"prompt big": func(s *Spec) { s.Prompt = strings.Repeat("x", maxPrompt+1) },
	}
	for name, mutate := range cases {
		s := good()
		mutate(s)
		if bad := Normalize(s); len(bad) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHashChangesWithContentOnly(t *testing.T) {
	a, b := good(), good()
	Normalize(a)
	Normalize(b)
	if Hash(a) != Hash(b) {
		t.Fatal("equal specs hash differently")
	}
	b.Prompt += " More."
	if Hash(a) == Hash(b) {
		t.Fatal("a changed prompt keeps the hash")
	}
}

func TestDirectoryRoundTrip(t *testing.T) {
	s := good()
	Normalize(s)
	dir := filepath.Join(t.TempDir(), "researcher")
	if err := WriteDir(dir, "researcher", s); err != nil {
		t.Fatal(err)
	}
	name, back, err := ReadDir(dir)
	if err != nil || name != "researcher" {
		t.Fatalf("read: %q %v", name, err)
	}
	Normalize(back)
	if Hash(back) != Hash(s) {
		t.Fatalf("round trip changed the profile:\n%+v\n%+v", s, back)
	}
	if got := SkillNames(back); len(got) != 1 || got[0] != "cite" {
		t.Fatalf("skills: %v", got)
	}
}

func TestReadDirRefusesSymlinksAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte("name: x\nkind: claude\nsurprise: 1\n"), 0o644)
	if _, _, err := ReadDir(dir); err == nil {
		t.Fatal("an unknown key in profile.yaml was ignored")
	}
	os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte("name: x\nkind: claude\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "skills", "s"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "s", "SKILL.md"), []byte("ok"), 0o644)
	os.Symlink("/etc/passwd", filepath.Join(dir, "skills", "s", "leak.md"))
	if _, _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("a symlink in a skill: %v", err)
	}
}

func TestClaudeArgv(t *testing.T) {
	s := good()
	Normalize(s)
	argv := strings.Join(ClaudeArgv(s, ""), " ")
	for _, want := range []string{"claude --setting-sources project,local --permission-mode dontAsk --allowedTools Bash(handloom:*) Read Glob Grep Bash",
		"--disallowedTools Bash(rm:*) Bash(git push:*)"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q: %s", want, argv)
		}
	}
	if strings.Contains(argv, "Write") || strings.Contains(argv, "WebFetch") {
		t.Errorf("a tool the profile does not allow is allowed: %s", argv)
	}
	if strings.Contains(argv, "--model") {
		t.Error("model set without one")
	}
	s.Model = "sonnet"
	if got := strings.Join(ClaudeArgv(s, "opus"), " "); !strings.HasSuffix(got, "--model opus") {
		t.Errorf("the spawn's model should win: %s", got)
	}
	if got := strings.Join(ClaudeArgv(s, ""), " "); !strings.HasSuffix(got, "--model sonnet") {
		t.Errorf("the profile's model: %s", got)
	}
	// A profile that allows nothing still lets the agent talk to the hub.
	none := &Spec{Kind: "claude"}
	Normalize(none)
	if got := strings.Join(ClaudeArgv(none, ""), " "); !strings.Contains(got, "Bash(handloom:*)") || strings.Contains(got, "Read") {
		t.Errorf("an empty profile: %s", got)
	}
}

func TestInstallSkillsStaysInsideItsDirectory(t *testing.T) {
	dir := t.TempDir()
	s := good()
	Normalize(s)
	if err := InstallSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "cite", "SKILL.md")); err != nil || !strings.Contains(string(b), "Cite.") {
		t.Fatalf("skill file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "cite", "examples", "one.md")); err != nil {
		t.Fatal(err)
	}
	// The hub could be wrong or hostile: the installer checks again.
	for _, p := range []string{"../escape.md", "/abs.md", "a/../../b.md", `a\b.md`} {
		if err := InstallSkills(t.TempDir(), &Spec{Skills: []File{{Path: p, Content: "x"}}}); err == nil {
			t.Errorf("installed %q", p)
		}
	}
	// A symlink planted in the work directory is not followed.
	d2 := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(d2, ".claude", "skills", "cite"), 0o755)
	os.Symlink(filepath.Join(outside, "target"), filepath.Join(d2, ".claude", "skills", "cite", "SKILL.md"))
	if err := InstallSkills(d2, s); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "target")); err == nil {
		t.Fatal("the symlink target was written")
	}
}

func TestEnforcementIsHonest(t *testing.T) {
	s := good()
	Normalize(s)
	text := strings.Join(Enforcement(s), "\n")
	for _, want := range []string{"allowed: shell", "refused by Claude Code: web", "the shell command rm", "not enforced: file paths", "not enforced: time"} {
		if !strings.Contains(text, want) {
			t.Errorf("enforcement lacks %q:\n%s", want, text)
		}
	}
}

func TestCodexProfilesAreHonestAboutWhatCodexCannotDo(t *testing.T) {
	ok := &Spec{Kind: "codex", Tools: Tools{Allow: []string{"read", "shell"}}, Prompt: "x"}
	if bad := Normalize(ok); len(bad) != 0 {
		t.Fatalf("a plain codex profile: %v", bad)
	}
	for name, mutate := range map[string]func(*Spec){
		"deny commands": func(s *Spec) { s.Tools.DenyCommands = []string{"rm"} },
		"skills":        func(s *Spec) { s.Skills = []File{{Path: "a/SKILL.md", Content: "x"}} },
		"no shell":      func(s *Spec) { s.Tools.Allow = []string{"read"} },
	} {
		s := &Spec{Kind: "codex", Tools: Tools{Allow: []string{"read", "shell"}}}
		mutate(s)
		if bad := Normalize(s); len(bad) == 0 {
			t.Errorf("codex with %s was accepted", name)
		}
	}
}

func TestCodexArgv(t *testing.T) {
	s := &Spec{Kind: "codex", Tools: Tools{Allow: []string{"edit", "read", "shell"}}}
	Normalize(s)
	argv := CodexArgv(s, "", "/opt/hl/handloom", "/home/u/.config/handloom", "worker-1")
	line := strings.Join(argv, " ")
	for _, want := range []string{"codex --enable hooks --dangerously-bypass-hook-trust -a never -s workspace-write",
		`-c mcp_servers.handloom.command="/opt/hl/handloom"`, `-c mcp_servers.handloom.args=["mcp"]`,
		`mcp_servers.handloom.default_tools_approval_mode="approve"`,
		`-c mcp_servers.handloom.env={HANDLOOM_HOME="/home/u/.config/handloom",HANDLOOM_AGENT="worker-1"}`} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "--search") || strings.Contains(line, "--model") {
		t.Errorf("web search or a model without being asked: %s", line)
	}
	// Read-only without edit; search with web; the model of the spawn wins.
	ro := &Spec{Kind: "codex", Model: "gpt-x", Tools: Tools{Allow: []string{"read", "shell", "web"}}}
	line = strings.Join(CodexArgv(ro, "gpt-y", "/b", "/h", "n"), " ")
	for _, want := range []string{"-s read-only", "--search", "--model gpt-y"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q: %s", want, line)
		}
	}
	// Quotes and backslashes in a path cannot break out of the TOML string.
	got := strings.Join(CodexArgv(s, "", `/a"b\c`, "/h", "n"), " ")
	if !strings.Contains(got, `command="/a\"b\\c"`) {
		t.Errorf("TOML quoting: %s", got)
	}
}

func TestCodexEnforcementText(t *testing.T) {
	s := &Spec{Kind: "codex", Tools: Tools{Allow: []string{"read", "shell"}}}
	text := strings.Join(Enforcement(s), "\n")
	for _, want := range []string{"refused by the Codex sandbox: writing files", "refused by the Codex sandbox: network", "not enforced: which shell commands run"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
}

// A profile that exists on a hub is pinned by its hash, and a device recomputes
// that hash before it trusts the content. Adding a field to Spec must therefore
// leave the hash of every spec stored before it unchanged, or every spawn of an
// existing profile would be refused. If this test fails, the new field needs
// `omitempty` (and a zero value that means "absent").
func TestTheHashOfAStoredSpecNeverChanges(t *testing.T) {
	const wantV1 = "2af7bbca33d1e4f9c5f5e25830c46b76c3f5eb203b57e03bb928075affb52a53"
	s := good()
	Normalize(s)
	if got := Hash(s); got != wantV1 {
		t.Fatalf("the hash of the reference profile changed: %s, want %s", got, wantV1)
	}
}

func TestMatchWrite(t *testing.T) {
	cases := []struct {
		globs []string
		path  string
		want  bool
	}{
		{nil, "anything/at/all.go", true},
		{[]string{"src/**"}, "src/a.go", true},
		{[]string{"src/**"}, "src/deep/er/a.go", true},
		{[]string{"src/**"}, "src", false},
		{[]string{"src/**"}, "srcx/a.go", false},
		{[]string{"src/**"}, "other/src/a.go", false},
		{[]string{"src/"}, "src/a/b.go", true},
		{[]string{"*.md"}, "README.md", true},
		{[]string{"*.md"}, "docs/README.md", false},
		{[]string{"**/*.md"}, "docs/a/README.md", true},
		{[]string{"**/*.md"}, "README.md", true},
		{[]string{"docs/*.md", "src/**"}, "docs/x.md", true},
		{[]string{"docs/*.md", "src/**"}, "docs/x/y.md", false},
		{[]string{"a?c.txt"}, "abc.txt", true},
		{[]string{"a?c.txt"}, "abbc.txt", false},
		{[]string{"src/**"}, "./src/a.go", true},
		{[]string{"src/**"}, "src/../etc/passwd", false},
		{[]string{"main.go"}, "main.go", true},
		{[]string{"main.go"}, "main.go.bak", false},
	}
	for _, c := range cases {
		if got := MatchWrite(c.globs, c.path); got != c.want {
			t.Errorf("MatchWrite(%v, %q) = %v, want %v", c.globs, c.path, got, c.want)
		}
	}
}

func TestWriteGlobsAreValidated(t *testing.T) {
	for _, g := range []string{"/etc/**", "../x/**", "a/../b", "a b", "a;rm", "src/**\n", "$(x)"} {
		s := good()
		s.Write = []string{g}
		if bad := Normalize(s); len(bad) == 0 {
			t.Errorf("write entry %q accepted", g)
		}
	}
	s := good()
	s.Write = []string{"src/**", "docs/*.md", "main.go", "tests/"}
	if bad := Normalize(s); len(bad) != 0 {
		t.Fatalf("good globs refused: %v", bad)
	}
}

func TestAdapterFilesAreNotTheAgentsWork(t *testing.T) {
	for _, p := range []string{".handloom/scope.json", ".claude/settings.local.json", ".codex/hooks.json", "CLAUDE.md", "AGENTS.md"} {
		if !AdapterPath(p) {
			t.Errorf("%s should be ignored by the scope check", p)
		}
	}
	for _, p := range []string{"src/CLAUDE.md", "docs/AGENTS.md", "main.go", ".github/workflows/x.yml", ".claudex"} {
		if AdapterPath(p) {
			t.Errorf("%s is the agent's work", p)
		}
	}
}
