package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"handloom/adapters"
	"handloom/internal/api"
	"handloom/internal/client"
)

const (
	blockBegin = "<!-- handloom:begin (managed by `handloom adapter`; edits here are overwritten) -->"
	blockEnd   = "<!-- handloom:end -->"
	hookSuffix = " hook claude" // how handloom recognises its own hook commands
)

// claudeHookEvents are the Claude Code events the adapter listens to, with
// the matcher each needs ("" means none).
var claudeHookEvents = []struct{ event, matcher string }{
	{"SessionStart", "*"},
	{"UserPromptSubmit", ""},
	{"PostToolUse", "*"},
	{"PermissionRequest", "*"},
	{"Notification", "permission_prompt|elicitation_dialog"},
	{"Stop", ""},
	{"SessionEnd", ""},
}

func (e *env) adapter(args []string) error {
	fs := e.flags("adapter")
	name := fs.String("name", "", "agent name")
	dir := fs.String("dir", ".", "project directory the agent runs in")
	project := fs.String("project", "", "handloom project (default \"default\")")
	noPerm := fs.Bool("no-permissions", false, "do not add the allow rule for handloom commands")
	pos, err := fs.need(args, 2, 2, "adapter install|remove claude --name N [--dir D]")
	if err != nil {
		return err
	}
	if pos[1] != "claude" {
		return fmt.Errorf("no adapter for %q in this version (only claude)", pos[1])
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	switch pos[0] {
	case "install":
		if *name == "" {
			return usageErr("usage: handloom adapter install claude --name N [--dir D]")
		}
		return e.installClaude(root, *name, *project, !*noPerm)
	case "remove":
		return e.removeClaude(root)
	}
	return usageErr("usage: handloom adapter install|remove claude --name N [--dir D]")
}

func (e *env) installClaude(root, name, project string, permissions bool) error {
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
	if err := c.Post("/v1/agents", api.RegisterReq{Name: name, Kind: "claude", Project: project}, &a); err != nil {
		return fmt.Errorf("register %s: %w (is `handloom link run` running on this device?)", name, err)
	}

	// 1. Identity for the CLI and the hooks.
	if err := os.MkdirAll(filepath.Join(root, ".handloom"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".handloom", "agent"), []byte(name+"\n"), 0o644); err != nil {
		return err
	}

	// 2. Hooks (and the allow rule) in .claude/settings.local.json. This is
	// the per-machine settings file: the hook command is an absolute path.
	command := shellQuote(bin) + hookSuffix
	if home := os.Getenv("HANDLOOM_HOME"); home != "" {
		command = "HANDLOOM_HOME=" + shellQuote(home) + " " + command
	}
	settingsPath := filepath.Join(root, ".claude", "settings.local.json")
	if err := editJSON(settingsPath, func(settings map[string]any) {
		setClaudeHooks(settings, command)
		if permissions {
			addAllowRules(settings, "Bash(handloom *)", "Bash("+bin+" *)")
		}
	}); err != nil {
		return err
	}

	// 3. Protocol instructions in CLAUDE.md.
	snippet, err := adapters.FS.ReadFile("claude/AGENTS.snippet.md")
	if err != nil {
		return err
	}
	lead := ""
	if a.Role == api.RoleLead {
		b, err := adapters.FS.ReadFile("claude/LEAD.snippet.md")
		if err != nil {
			return err
		}
		lead = strings.TrimSpace(string(b))
	}
	block := strings.NewReplacer("{{name}}", a.Name, "{{role}}", a.Role, "{{project}}", a.Project, "{{lead}}", lead).Replace(string(snippet))
	if err := writeBlock(filepath.Join(root, "CLAUDE.md"), strings.TrimSpace(block)); err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Claude Code adapter installed in %s for %s (%s, project %s).\n", root, a.Name, a.Role, a.Project)
	fmt.Fprintf(e.out, "  hooks:        %s\n  instructions: %s\n  identity:     %s\n", settingsPath,
		filepath.Join(root, "CLAUDE.md"), filepath.Join(root, ".handloom", "agent"))
	fmt.Fprintln(e.out, "Start `claude` in that directory, inside tmux or herdr so it can be woken.")
	return nil
}

func (e *env) removeClaude(root string) error {
	settingsPath := filepath.Join(root, ".claude", "settings.local.json")
	if _, err := os.Stat(settingsPath); err == nil {
		if err := editJSON(settingsPath, func(settings map[string]any) { setClaudeHooks(settings, "") }); err != nil {
			return err
		}
	}
	if err := writeBlock(filepath.Join(root, "CLAUDE.md"), ""); err != nil {
		return err
	}
	os.Remove(filepath.Join(root, ".handloom", "agent"))
	os.Remove(filepath.Join(root, ".handloom"))
	fmt.Fprintf(e.out, "Claude Code adapter removed from %s. The agent stays registered on the hub.\n", root)
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

// setClaudeHooks removes handloom's hook entries and, if command is not empty,
// adds them again. Other hooks are left alone.
func setClaudeHooks(settings map[string]any, command string) {
	hooks, _ := settings["hooks"].(map[string]any)
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
		for _, ev := range claudeHookEvents {
			group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 15}}}
			if ev.matcher != "" {
				group["matcher"] = ev.matcher
			}
			groups, _ := hooks[ev.event].([]any)
			hooks[ev.event] = append(groups, group)
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
}

func isHiveHookGroup(g any) bool {
	group, _ := g.(map[string]any)
	list, _ := group["hooks"].([]any)
	for _, h := range list {
		hook, _ := h.(map[string]any)
		if cmd, _ := hook["command"].(string); strings.HasSuffix(cmd, hookSuffix) {
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
