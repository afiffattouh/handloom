package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"handloom/adapters"
	"handloom/internal/api"
	"handloom/internal/client"
)

const (
	blockBegin = "<!-- handloom:begin (managed by `handloom adapter`; edits here are overwritten) -->"
	blockEnd   = "<!-- handloom:end -->"
)

// hookEvent is one lifecycle event an adapter listens to, with the matcher
// it needs ("" means none).
type hookEvent struct{ event, matcher string }

// adapterSpec describes how one agent kind is wired to handloom.
type adapterSpec struct {
	instructions string      // file in the project that gets the protocol block
	hooksFile    string      // JSON hooks file, for kinds with native command hooks
	events       []hookEvent // events registered in hooksFile
	allowRule    bool        // Claude Code: also allow `handloom` commands in the settings
	shim         string      // embedded extension/plugin, for kinds without command hooks
	shimPath     string      // where the shim goes in the project
	shimVars     map[string]string
	start        string // how to start the agent so the adapter is active
}

var adapterSpecs = map[string]*adapterSpec{
	"claude": {
		instructions: "CLAUDE.md",
		hooksFile:    ".claude/settings.local.json",
		events: []hookEvent{
			{"SessionStart", "*"}, {"UserPromptSubmit", ""}, {"PostToolUse", "*"}, {"PermissionRequest", "*"},
			{"Notification", "permission_prompt|elicitation_dialog"}, {"Stop", ""}, {"SessionEnd", ""},
		},
		allowRule: true,
		start:     "Start `claude` in that directory, inside tmux or herdr so it can be woken.",
	},
	"codex": {
		instructions: "AGENTS.md",
		hooksFile:    ".codex/hooks.json",
		events: []hookEvent{
			{"SessionStart", ""}, {"UserPromptSubmit", ""}, {"PostToolUse", ""}, {"PermissionRequest", ""},
			{"Stop", ""}, {"SessionEnd", ""},
		},
		start: "Start `codex` in that directory, inside tmux or herdr. Codex asks you to review the hooks once\n" +
			"(unattended: --dangerously-bypass-hook-trust). Its sandbox blocks the link's socket for shell\n" +
			"commands, so give it the handloom tools:\n" +
			"  codex -c mcp_servers.handloom.command={{handloom}} -c 'mcp_servers.handloom.args=[\"mcp\"]'\n" +
			"or allow the socket: codex -c sandbox_workspace_write.network_access=true",
	},
	"pi": {
		instructions: "AGENTS.md",
		shim:         "pi/handloom.ts",
		shimPath:     ".pi/extensions/handloom.ts",
		shimVars:     map[string]string{"{{kind}}": "pi", "{{stop_event}}": "agent_settled", "{{extra}}": ""},
		start:        "Start `pi --approve` in that directory (project extensions need project trust), inside tmux or herdr.",
	},
	"omp": {
		instructions: "AGENTS.md",
		shim:         "pi/handloom.ts",
		shimPath:     ".handloom/omp-extension.ts",
		shimVars: map[string]string{"{{kind}}": "omp", "{{stop_event}}": "agent_end", "{{extra}}": `  pi.on("tool_approval_requested", async (_event, ctx) => {
    await hook("PermissionRequest", ctx);
  });
`},
		start: "Start `omp -e {{dir}}/.handloom/omp-extension.ts` in that directory, inside tmux or herdr.",
	},
	"opencode": {
		instructions: "AGENTS.md",
		shim:         "opencode/handloom.js",
		shimPath:     ".opencode/plugins/handloom.js",
		start:        "Start `opencode` in that directory, inside tmux or herdr.",
	},
}

func adapterKinds() string {
	kinds := make([]string, 0, len(adapterSpecs))
	for k := range adapterSpecs {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return strings.Join(kinds, "|")
}

func (e *env) adapter(args []string) error {
	fs := e.flags("adapter")
	name := fs.String("name", "", "agent name")
	dir := fs.String("dir", ".", "project directory the agent runs in")
	project := fs.String("project", "", "handloom project (default \"default\")")
	noPerm := fs.Bool("no-permissions", false, "claude: do not add the allow rule for handloom commands")
	synopsis := "adapter install|remove " + adapterKinds() + " --name N [--dir D]"
	pos, err := fs.need(args, 2, 2, synopsis)
	if err != nil {
		return err
	}
	spec := adapterSpecs[pos[1]]
	if spec == nil {
		return fmt.Errorf("no adapter for %q (have: %s)", pos[1], adapterKinds())
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	switch pos[0] {
	case "install":
		if *name == "" {
			return usageErr("usage: handloom %s", synopsis)
		}
		return e.installAdapter(pos[1], spec, root, *name, *project, !*noPerm)
	case "remove":
		return e.removeAdapter(pos[1], spec, root)
	}
	return usageErr("usage: handloom %s", synopsis)
}

func (e *env) installAdapter(kind string, spec *adapterSpec, root, name, project string, permissions bool) error {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}

	// Register through the link. An existing agent keeps its role, so the
	// instructions carry the role the human gave it.
	c := client.Socket(client.SocketPath(), name)
	var a api.Agent
	if err := c.Post("/v1/agents", api.RegisterReq{Name: name, Kind: kind, Project: project, Dir: root}, &a); err != nil {
		return fmt.Errorf("register %s: %w (is `handloom link run` running on this device?)", name, err)
	}

	// 1. Identity for the CLI and the hooks.
	if err := os.MkdirAll(filepath.Join(root, ".handloom"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".handloom", "agent"), []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s adapter installed in %s for %s (%s, project %s).\n", kind, root, a.Name, a.Role, a.Project)
	fmt.Fprintf(e.out, "  identity:     %s\n", filepath.Join(root, ".handloom", "agent"))

	// 2. Lifecycle events: native command hooks, or a shim that forwards to
	// `handloom hook <kind>`. The command is an absolute path, so these files
	// are per machine.
	home := os.Getenv("HANDLOOM_HOME")
	if spec.hooksFile != "" {
		command := shellQuote(bin) + " hook " + kind
		if home != "" {
			command = "HANDLOOM_HOME=" + shellQuote(home) + " " + command
		}
		path := filepath.Join(root, spec.hooksFile)
		if err := editJSON(path, func(doc map[string]any) {
			setHooks(doc, kind, spec.events, command)
			if spec.allowRule && permissions {
				addAllowRules(doc, "Bash(handloom *)", "Bash("+bin+" *)")
			}
		}); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "  hooks:        %s\n", path)
	}
	if spec.shim != "" {
		src, err := adapters.FS.ReadFile(spec.shim)
		if err != nil {
			return err
		}
		envVars := map[string]string{}
		if home != "" {
			envVars["HANDLOOM_HOME"] = home
		}
		binJSON, _ := json.Marshal(bin)
		envJSON, _ := json.Marshal(envVars)
		text := strings.NewReplacer("{{handloom}}", string(binJSON), "{{env}}", string(envJSON)).Replace(string(src))
		for k, v := range spec.shimVars {
			text = strings.ReplaceAll(text, k, v)
		}
		path := filepath.Join(root, spec.shimPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "  extension:    %s\n", path)
	}

	// 3. Protocol instructions.
	block, err := instructionsBlock(a)
	if err != nil {
		return err
	}
	path := filepath.Join(root, spec.instructions)
	if err := writeBlock(path, block); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "  instructions: %s\n", path)
	fmt.Fprintln(e.out, strings.NewReplacer("{{handloom}}", bin, "{{dir}}", root).Replace(spec.start))
	return nil
}

func instructionsBlock(a api.Agent) (string, error) {
	snippet, err := adapters.FS.ReadFile("AGENTS.snippet.md")
	if err != nil {
		return "", err
	}
	lead := ""
	if a.Role == api.RoleLead {
		b, err := adapters.FS.ReadFile("LEAD.snippet.md")
		if err != nil {
			return "", err
		}
		lead = strings.TrimSpace(string(b))
	}
	block := strings.NewReplacer("{{name}}", a.Name, "{{role}}", a.Role, "{{project}}", a.Project, "{{lead}}", lead).Replace(string(snippet))
	return strings.TrimSpace(block), nil
}

func (e *env) removeAdapter(kind string, spec *adapterSpec, root string) error {
	if spec.hooksFile != "" {
		path := filepath.Join(root, spec.hooksFile)
		if _, err := os.Stat(path); err == nil {
			if err := editJSON(path, func(doc map[string]any) { setHooks(doc, kind, nil, "") }); err != nil {
				return err
			}
		}
	}
	if spec.shimPath != "" {
		os.Remove(filepath.Join(root, spec.shimPath))
	}
	if err := writeBlock(filepath.Join(root, spec.instructions), ""); err != nil {
		return err
	}
	os.Remove(filepath.Join(root, ".handloom", "agent"))
	os.Remove(filepath.Join(root, ".handloom"))
	fmt.Fprintf(e.out, "%s adapter removed from %s. The agent stays registered on the hub.\n", kind, root)
	return nil
}

// editJSON loads a JSON object from path (or starts an empty one), lets edit
// change it, and writes it back. Everything handloom does not own is kept.
func editJSON(path string, edit func(map[string]any)) error {
	doc := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	edit(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// handloomHookRE recognises handloom's own hook commands, whatever the kind.
var handloomHookRE = regexp.MustCompile(` hook (claude|codex|pi|omp|opencode)$`)

// setHooks removes handloom's hook entries and, if command is not empty, adds
// them again for the given events. Other hooks are left alone. Hooks must be
// registered in exactly one place: two Stop hooks would ask the hub twice.
func setHooks(doc map[string]any, kind string, events []hookEvent, command string) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, v := range hooks {
		groups, _ := v.([]any)
		var kept []any
		for _, g := range groups {
			if !isHiveHookGroup(g) {
				kept = append(kept, g)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if command != "" {
		for _, ev := range events {
			group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 15}}}
			if ev.matcher != "" {
				group["matcher"] = ev.matcher
			}
			groups, _ := hooks[ev.event].([]any)
			hooks[ev.event] = append(groups, group)
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}
}

func isHiveHookGroup(g any) bool {
	group, _ := g.(map[string]any)
	list, _ := group["hooks"].([]any)
	for _, h := range list {
		hook, _ := h.(map[string]any)
		if cmd, _ := hook["command"].(string); handloomHookRE.MatchString(cmd) {
			return true
		}
	}
	return false
}

func addAllowRules(settings map[string]any, rules ...string) {
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	for _, rule := range rules {
		found := false
		for _, have := range allow {
			found = found || have == rule
		}
		if !found {
			allow = append(allow, rule)
		}
	}
	perms["allow"] = allow
	settings["permissions"] = perms
}

// writeBlock puts block between the handloom markers in the file at path,
// replacing an earlier block. An empty block removes the markers.
func writeBlock(path, block string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(b)
	if i := strings.Index(text, blockBegin); i >= 0 {
		if j := strings.Index(text[i:], blockEnd); j >= 0 {
			text = strings.TrimRight(text[:i], "\n") + "\n" + strings.TrimLeft(text[i+j+len(blockEnd):], "\n")
		}
	}
	text = strings.TrimSpace(text)
	if block != "" {
		if text != "" {
			text += "\n\n"
		}
		text += blockBegin + "\n" + block + "\n" + blockEnd
	}
	if text == "" {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(text+"\n"), 0o644)
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '_' || r == '-' || r == '+' || r == ':' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
