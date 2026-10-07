package profile

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Caps is what an agent CLI can be told to enforce. Everything the profile
// asks for beyond that is refused in words (or left out when the person says
// so): a limit that is not enforced is never silently dropped.
type Caps struct {
	Label        string // how people say it
	DenyCommands bool   // can refuse specific shell commands
	NativeSkills bool   // loads SKILL.md folders itself; the others get the text in their instructions
	AlwaysShell  bool   // cannot be given a profile without a shell: handloom's own commands run through it
	Web          bool   // has a web tool at all
	LocalCapable bool   // can run a model on the owner's machine
	Sandbox      bool   // limits files and network itself (Codex)
}

// Kinds are the agent CLIs a profile can name, in the order people see them.
var Kinds = []string{"claude", "codex", "omp", "pi", "opencode"}

var caps = map[string]Caps{
	"claude":   {Label: "Claude Code", DenyCommands: true, NativeSkills: true, Web: true},
	"codex":    {Label: "Codex", AlwaysShell: true, Web: true, Sandbox: true},
	"omp":      {Label: "OMP", AlwaysShell: true, Web: true, LocalCapable: true},
	"pi":       {Label: "Pi", AlwaysShell: true, Web: false, LocalCapable: true},
	"opencode": {Label: "OpenCode", DenyCommands: true, Web: true, LocalCapable: true},
}

// CapsFor returns what a kind can enforce. An unknown kind has no caps.
func CapsFor(kind string) (Caps, bool) { c, ok := caps[kind]; return c, ok }

// KindLabel is how people name a kind.
func KindLabel(kind string) string {
	if c, ok := caps[kind]; ok {
		return c.Label
	}
	return kind
}

func has(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}

// OmpArgv is the command line for a spawned OMP. It is unattended (yolo
// approval), limited to the tools the profile allows, and thinks harder when
// it leads. ext is the handloom extension file in the work directory.
func OmpArgv(s *Spec, model, role, ext string) []string {
	var tools []string
	if has(s.Tools.Allow, "read") {
		tools = append(tools, "read", "grep", "glob")
	}
	if has(s.Tools.Allow, "edit") {
		tools = append(tools, "write", "edit")
	}
	tools = append(tools, "bash") // handloom's commands run through it
	if has(s.Tools.Allow, "web") {
		tools = append(tools, "web_search")
	}
	thinking := "medium"
	if role == "lead" {
		thinking = "high"
	}
	argv := []string{"omp", "--approval-mode", "yolo", "--tools", strings.Join(tools, ","), "--thinking", thinking, "-e", ext}
	if model == "" {
		model = s.Model
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return argv
}

// PiArgv is the command line for a spawned Pi. Pi trusts the project's own
// files (the handloom extension) with --approve, and is limited to the tools
// the profile allows. Pi has no web tool.
func PiArgv(s *Spec, model string) []string {
	var tools []string
	if has(s.Tools.Allow, "read") {
		tools = append(tools, "read")
	}
	if has(s.Tools.Allow, "edit") {
		tools = append(tools, "edit", "write")
	}
	tools = append(tools, "bash")
	argv := []string{"pi", "--approve", "--tools", strings.Join(tools, ",")}
	if model == "" {
		model = s.Model
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return argv
}

// OpenCodeArgv is the command line for a spawned OpenCode. What it may do is
// in opencode.json (OpenCodeConfig), written into the work directory.
func OpenCodeArgv(s *Spec, model string) []string {
	argv := []string{"opencode"}
	if model == "" {
		model = s.Model
	}
	if model != "" {
		argv = append(argv, "-m", model)
	}
	return argv
}

// OpenCodeConfig is the opencode.json for a profile: tool permissions, with
// the denied shell commands refused one by one, and handloom's commands always allowed.
func OpenCodeConfig(s *Spec, model string) ([]byte, error) {
	allow := func(ok bool) string {
		if ok {
			return "allow"
		}
		return "deny"
	}
	bash := map[string]string{}
	if has(s.Tools.Allow, "shell") {
		bash["*"] = "allow"
		for _, c := range s.Tools.DenyCommands {
			bash[c+"*"] = "deny"
		}
	} else {
		bash["*"] = "deny"
	}
	bash["handloom *"] = "allow" // an agent must always be able to talk to its hub
	read := allow(has(s.Tools.Allow, "read"))
	perm := map[string]any{
		"read": read, "glob": read, "grep": read, "list": read,
		"edit":               allow(has(s.Tools.Allow, "edit")),
		"bash":               bash,
		"webfetch":           allow(has(s.Tools.Allow, "web")),
		"websearch":          allow(has(s.Tools.Allow, "web")),
		"external_directory": "allow", // a git worktree keeps its repository outside the work directory
	}
	doc := map[string]any{"$schema": "https://opencode.ai/config.json", "permission": perm}
	if model == "" {
		model = s.Model
	}
	if model != "" {
		// Pin every place OpenCode picks a model: a user's own config can name a
		// different one for an agent (the default "build" agent) or for small
		// tasks such as titles, and a profile that says "local" must not leak to it.
		doc["model"] = model
		doc["small_model"] = model
		doc["agent"] = map[string]any{"build": map[string]any{"model": model}, "plan": map[string]any{"model": model}, "general": map[string]any{"model": model}}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// InlineSkills is the text of a profile's skills for CLIs that do not load
// SKILL.md folders: it goes into the agent's instructions.
func InlineSkills(s *Spec) string {
	var b strings.Builder
	for _, f := range s.Skills {
		if !strings.HasSuffix(f.Path, "/SKILL.md") {
			continue
		}
		name := strings.TrimSuffix(f.Path, "/SKILL.md")
		body := f.Content
		if strings.HasPrefix(body, "---\n") {
			if i := strings.Index(body[4:], "\n---"); i >= 0 {
				body = strings.TrimLeft(body[4+i+4:], "\n")
			}
		}
		fmt.Fprintf(&b, "### Skill: %s\n\n%s\n\n", name, strings.TrimSpace(body))
	}
	return strings.TrimSpace(b.String())
}

// shellNote is why OMP, Pi and Codex keep their shell.
const shellNote = "allowed: shell, always (handloom's own commands run through it; it cannot be taken away without cutting the agent off from the team)"

// toolEnforcement is the enforcement text for OMP, Pi and OpenCode.
func toolEnforcement(kind string, s *Spec) []string {
	label := KindLabel(kind)
	allow := func(t string) bool { return has(s.Tools.Allow, t) }
	var out []string
	if allow("read") {
		out = append(out, "allowed: read")
	} else {
		out = append(out, "refused by "+label+": read")
	}
	if allow("edit") {
		out = append(out, "allowed: edit")
	} else {
		out = append(out, "refused by "+label+": edit and write")
	}
	switch kind {
	case "opencode":
		if allow("shell") {
			out = append(out, "allowed: shell")
		} else {
			out = append(out, "refused by OpenCode: every shell command except handloom's")
		}
		for _, c := range s.Tools.DenyCommands {
			out = append(out, "refused by OpenCode: the shell command "+c)
		}
	default:
		out = append(out, shellNote)
		if len(s.Tools.DenyCommands) > 0 {
			out = append(out, "not enforced: denied commands ("+strings.Join(s.Tools.DenyCommands, ", ")+"): "+label+" cannot refuse specific commands")
		} else {
			out = append(out, "not enforced: which shell commands run")
		}
	}
	switch {
	case kind == "pi":
		if allow("web") {
			out = append(out, "not available: Pi has no web tool")
		} else {
			out = append(out, "no web tool: Pi has none")
		}
	case allow("web"):
		out = append(out, "allowed: web")
	default:
		out = append(out, "refused by "+label+": web")
	}
	if len(s.Write) > 0 {
		out = append(out, "checked at submit, in repo jobs: it may change only "+strings.Join(s.Write, ", ")+" (a submit with other changed files is refused)",
			"not enforced while it works: it can still write elsewhere; the check is what it has changed when it submits")
	} else {
		out = append(out, "not enforced: file paths (an agent can read and write anywhere its tools reach)")
	}
	if c, _ := CapsFor(kind); !c.NativeSkills && len(s.Skills) > 0 {
		out = append(out, "skills: their text is put in the agent's instructions")
	}
	out = append(out, "not enforced: time, turns or cost limits (the link only watches whether the terminal exists)")
	return out
}
