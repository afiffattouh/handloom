package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"handloom/kit"
)

// Putting the kit into a project's folder, for projects the hub holds. This is `factory init`
// (kit/bin/factory) written again in Go, because the hub's image has git and a shell and no
// Python. The files themselves are the kit's own, read from the embedded copy; only the
// choices around them (which stages, the manifest, the hooks file) are repeated here, and
// kitseed_test.go runs the real `factory init` and compares, so the two cannot drift apart
// unnoticed.

// kv is an ordered pair: the order stages are added in is the order of the file.
type kv struct{ k, v string }

type omap []kv

func (m *omap) has(k string) bool {
	for _, p := range *m {
		if p.k == k {
			return true
		}
	}
	return false
}
func (m *omap) set(k, v string) {
	for i, p := range *m {
		if p.k == k {
			(*m)[i].v = v
			return
		}
	}
	*m = append(*m, kv{k, v})
}
func (m *omap) setDefault(k, v string) {
	if !m.has(k) {
		m.set(k, v)
	}
}
func (m omap) get(k string) string {
	for _, p := range m {
		if p.k == k {
			return p.v
		}
	}
	return ""
}

var knownStages = []string{"format", "lint", "typecheck", "unit", "suite", "acceptance", "build", "smoke"}

func isKnownStage(s string) bool {
	for _, k := range knownStages {
		if k == s {
			return true
		}
	}
	return false
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

func readText(root, rel string) string {
	b, _ := os.ReadFile(filepath.Join(root, rel))
	return string(b)
}

// detectStages is the kit's best guess at a project's checks, from the files in it.
func detectStages(root string) (stages, fast omap, notes []string) {
	if exists(root, "package.json") {
		var pkg struct {
			Scripts map[string]json.RawMessage `json:"scripts"`
		}
		_ = json.Unmarshal([]byte(readText(root, "package.json")), &pkg)
		for _, p := range [][2]string{{"lint", "lint"}, {"typecheck", "typecheck"}, {"unit", "test"}, {"build", "build"}} {
			if _, ok := pkg.Scripts[p[1]]; ok {
				if p[1] == "test" {
					stages.set(p[0], "npm test --silent")
				} else {
					stages.set(p[0], "npm run "+p[1]+" --silent")
				}
			}
		}
		all, _ := json.Marshal(pkg.Scripts)
		if !strings.Contains(string(all), "tsc") && exists(root, "tsconfig.json") && !stages.has("typecheck") {
			stages.set("typecheck", "npx tsc --noEmit")
		}
		if stages.has("build") {
			fast.set("build", "")
		}
	}
	python := "python3"
	if exists(root, "venv/bin/python") {
		python = "venv/bin/python"
	} else if exists(root, "uv.lock") {
		python = "uv run python"
	}
	pyTests, _ := filepath.Glob(filepath.Join(root, "tests", "test_*.py"))
	rootTests, _ := filepath.Glob(filepath.Join(root, "test_*.py"))
	if (len(pyTests) > 0 || len(rootTests) > 0) && !stages.has("unit") {
		usesPytest := false
		for _, f := range []string{"pyproject.toml", "requirements.txt", "requirements-dev.txt", "setup.cfg"} {
			if exists(root, f) && strings.Contains(readText(root, f), "pytest") {
				usesPytest = true
			}
		}
		if usesPytest {
			stages.set("unit", python+" -m pytest -q")
		} else {
			stages.set("unit", python+" -m unittest discover -s tests -q")
		}
	}
	if exists(root, "go.mod") {
		stages.setDefault("lint", "go vet ./...")
		stages.setDefault("unit", "go test ./...")
		fast.setDefault("unit", "go test -short ./...")
	}
	if exists(root, "Cargo.toml") {
		stages.setDefault("lint", "cargo clippy --quiet -- -D warnings")
		stages.setDefault("unit", "cargo test --quiet")
	}
	if exists(root, "Makefile") && regexp.MustCompile(`(?m)^check:`).MatchString(readText(root, "Makefile")) {
		stages.set("suite", "make check")
		notes = append(notes, "found `make check`; kept it as the `suite` stage")
	}
	for _, legacy := range []string{"check.sh", "ci.sh", "scripts/check.sh"} {
		if exists(root, legacy) {
			cmd := legacy
			if !strings.Contains(legacy, "/") {
				cmd = "./" + legacy
			}
			stages.set("suite", cmd)
			notes = append(notes, "found "+legacy+"; kept it as the `suite` stage (it still works on its own)")
			break
		}
	}
	if len(stages) == 0 {
		notes = append(notes, "no checks detected: fill in [stages] in factory.toml")
	}
	return
}

var webStages = omap{{"web-links", "python3 .factory/checks/web.py links"}, {"web-images", "python3 .factory/checks/web.py images"},
	{"web-meta", "python3 .factory/checks/web.py meta"}, {"web-budget", "python3 .factory/checks/web.py budget"}}

func factoryToml(name string, stages, fast omap, links []string, preset string) string {
	lines := []string{
		"# Checks for coding agents and people (factory-kit). ./check runs the stages below in order and",
		"# stops at the first failure. An empty command skips a stage. Recommended order:",
		"# format, lint, typecheck, unit, acceptance, build, smoke. A person owns this file.",
		"",
		"[project]",
		fmt.Sprintf("name = %q", name),
		fmt.Sprintf("preset = %q", preset),
		"",
		"[stages]",
	}
	for _, stage := range knownStages {
		if stages.has(stage) {
			lines = append(lines, fmt.Sprintf("%s = \"%s\"", stage, stages.get(stage)))
		} else if stage == "acceptance" {
			lines = append(lines, `acceptance = ""  # tests in tests/acceptance/ (owned by a person)`)
		} else if stage == "smoke" {
			lines = append(lines, `smoke = ""  # drive the real thing once, as a user would`)
		}
	}
	for _, p := range stages {
		if !isKnownStage(p.k) {
			lines = append(lines, fmt.Sprintf("%s = \"%s\"", p.k, p.v))
		}
	}
	lines = append(lines, "", "# ./check --fast uses these instead (empty = skip in --fast).", "[fast]")
	for _, p := range fast {
		lines = append(lines, fmt.Sprintf("%s = \"%s\"", p.k, p.v))
	}
	quoted := make([]string, len(links))
	for i, d := range links {
		quoted[i] = `"` + d + `"`
	}
	lines = append(lines, "", "# What a fresh git worktree needs from the main checkout (linked by ./check, never committed).",
		"[worktree]", "link = ["+strings.Join(quoted, ", ")+"]")
	lines = append(lines, "", "[timeouts]", "default = 900", "", "[gate]", `base = "main"`,
		`acceptance_dir = "tests/acceptance"`, "max_changed_files = 30", "max_agents_md_lines = 100", "",
		"# Limits for ./check --mode maintain (small changes to a live project).", "[maintain]", "max_changed_files = 10", "")
	if preset == "web" {
		lines = append(lines, "# The web checks read the built site. dir: where it is (default: dist, build, out, _site, public).",
			"[web]", `# dir = "dist"`, "max_image_kb = 500", "max_html_kb = 200", `# ignore = ["/api/"]`, "")
	}
	return strings.Join(lines, "\n")
}

func kitBytes(rel string) []byte {
	b, err := kit.Files.ReadFile(rel)
	if err != nil {
		panic("the kit is missing " + rel + " from this build: " + err.Error())
	}
	return b
}

var kitVersionRE = regexp.MustCompile(`(?m)^KIT_VERSION = "([^"]+)"`)

// kitVersion is the version the embedded kit says it is.
func kitVersion() string {
	if m := kitVersionRE.FindSubmatch(kitBytes("kit/check.py")); m != nil {
		return string(m[1])
	}
	return "unknown"
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}
type hookGroup struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []hookCmd `json:"hooks"`
}
type claudeSettings struct {
	Hooks struct {
		PreToolUse []hookGroup `json:"PreToolUse"`
		Stop       []hookGroup `json:"Stop"`
	} `json:"hooks"`
}

func claudeSettingsText() string {
	var s claudeSettings
	s.Hooks.PreToolUse = []hookGroup{{Matcher: "Edit|Write|MultiEdit|NotebookEdit",
		Hooks: []hookCmd{{Type: "command", Command: `python3 "$CLAUDE_PROJECT_DIR/.factory/hooks/protect.py"`}}}}
	s.Hooks.Stop = []hookGroup{{Hooks: []hookCmd{{Type: "command", Command: `python3 "$CLAUDE_PROJECT_DIR/.factory/hooks/stop_check.py"`, Timeout: 600}}}}
	b, _ := json.MarshalIndent(s, "", "  ")
	return string(b) + "\n"
}

// seedKit writes the kit into root the way `factory init` does: it adds files and never overwrites one.
// It returns the files it added and what the person should know.
func seedKit(root, name, preset string) (created, notes []string, err error) {
	if preset == "" {
		preset = "generic"
	}
	if preset != "generic" && preset != "web" {
		return nil, nil, badRequest("%q is not a kind of project the kit knows: use generic or web", preset)
	}
	stages, fast, notes := detectStages(root)
	if preset == "web" {
		for _, p := range webStages {
			stages.set(p.k, p.v)
		}
		if !stages.has("build") && !exists(root, "package.json") {
			notes = append(notes, "web preset: no build step found; the checks read the HTML in the project folder (or [web] dir)")
		}
	}
	var links []string
	for _, d := range []string{"venv", ".venv", "node_modules"} {
		if st, e := os.Stat(filepath.Join(root, d)); e == nil && st.IsDir() {
			links = append(links, d)
		}
	}
	put := func(rel string, data []byte, mode os.FileMode) error {
		target := filepath.Join(root, rel)
		if _, e := os.Lstat(target); e == nil {
			return nil // never overwrite
		}
		if e := os.MkdirAll(filepath.Dir(target), 0o755); e != nil {
			return e
		}
		if e := os.WriteFile(target, data, mode); e != nil {
			return e
		}
		created = append(created, rel)
		return nil
	}
	tpl := func(n string) []byte { return kitBytes("kit/templates/" + n) }
	agents := strings.ReplaceAll(strings.ReplaceAll(string(tpl("AGENTS.md")), "{name}", name), "{commands}", "")
	steps := []struct {
		rel  string
		data []byte
		mode os.FileMode
	}{
		{"factory.toml", []byte(factoryToml(name, stages, fast, links, preset)), 0o644},
		{"AGENTS.md", []byte(agents), 0o644},
		{"CLAUDE.md", tpl("CLAUDE.md"), 0o644},
		{"specs/TEMPLATE.md", tpl("spec.md"), 0o644},
		{"LESSONS.md", tpl("LESSONS.md"), 0o644},
		{"tests/acceptance/README.md", tpl("acceptance-README.md"), 0o644},
		{".factory/check.py", kitBytes("kit/check.py"), 0o755},
		{".factory/_toml.py", kitBytes("kit/_toml.py"), 0o644},
		{".factory/hooks/protect.py", kitBytes("kit/hooks/protect.py"), 0o755},
		{".factory/hooks/stop_check.py", kitBytes("kit/hooks/stop_check.py"), 0o755},
		{"check", tpl("check"), 0o755},
	}
	if preset == "web" {
		steps = append(steps, struct {
			rel  string
			data []byte
			mode os.FileMode
		}{".factory/checks/web.py", kitBytes("kit/checks/web.py"), 0o755})
	}
	for _, s := range steps {
		if err = put(s.rel, s.data, s.mode); err != nil {
			return
		}
	}
	if err = put(".factory/VERSION", []byte(kitVersion()+"\n"), 0o644); err != nil {
		return
	}
	if exists(root, ".claude/settings.json") {
		notes = append(notes, ".claude/settings.json already exists, so the two Claude Code hooks were not added")
	} else if err = put(".claude/settings.json", []byte(claudeSettingsText()), 0o644); err != nil {
		return
	}
	if exists(root, "CLAUDE.md") && !hasString(created, "CLAUDE.md") && !strings.Contains(readText(root, "CLAUDE.md"), "@AGENTS.md") {
		notes = append(notes, "CLAUDE.md exists without `@AGENTS.md`; add that line so Claude Code reads the guide")
	}
	gi := readText(root, ".gitignore")
	if !regexp.MustCompile(`(?m)^/?runs/?$`).MatchString(gi) {
		if gi != "" && !strings.HasSuffix(gi, "\n") {
			gi += "\n"
		}
		if err = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gi+"runs/\n"), 0o644); err != nil {
			return
		}
		created = append(created, ".gitignore (runs/)")
	}
	manifest, e := acceptanceManifest(filepath.Join(root, "tests", "acceptance"))
	if e != nil {
		return created, notes, e
	}
	if err = os.WriteFile(filepath.Join(root, "tests", "acceptance", ".manifest"), []byte(manifest), 0o644); err != nil {
		return
	}
	created = append(created, "tests/acceptance/.manifest")
	return
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// acceptanceManifest is the kit's manifest text for a folder of acceptance tests: the sha256 of every file.
func acceptanceManifest(dir string) (string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == ".manifest" || strings.Contains(p, "__pycache__") {
			return err
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		hashes[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(hashes))
	for n := range hashes {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# sha256 of every acceptance test. Regenerate with `factory accept-tests` (a person, on the base branch).\n")
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", hashes[n], n)
	}
	return b.String(), nil
}
