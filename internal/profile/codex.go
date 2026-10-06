package profile

import (
	"fmt"
	"strings"
)

// CodexArgv is the command line for a spawned Codex. The agent is unattended:
// it never asks for approval (-a never), writes only inside its work
// directory when the profile allows editing (workspace-write, else
// read-only), and has no network unless the profile allows web. The handloom
// verbs reach it as MCP tools, which run outside the sandbox and are
// pre-approved, because the sandbox blocks the link's socket for a shell.
// bin and home are the handloom binary and its home; name is the agent.
func CodexArgv(s *Spec, model, bin, home, name string) []string {
	sandbox := "read-only"
	for _, t := range s.Tools.Allow {
		if t == "edit" {
			sandbox = "workspace-write"
		}
	}
	argv := []string{"codex", "--enable", "hooks", "--dangerously-bypass-hook-trust", "-a", "never", "-s", sandbox}
	for _, t := range s.Tools.Allow {
		if t == "web" {
			argv = append(argv, "--search")
		}
	}
	if model == "" {
		model = s.Model
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	argv = append(argv,
		"-c", "mcp_servers.handloom.command="+TOMLString(bin),
		"-c", `mcp_servers.handloom.args=["mcp"]`,
		"-c", `mcp_servers.handloom.default_tools_approval_mode="approve"`,
		"-c", "mcp_servers.handloom.env={HANDLOOM_HOME="+TOMLString(home)+",HANDLOOM_AGENT="+TOMLString(name)+"}")
	return argv
}

// TOMLString quotes s as a TOML basic string.
func TOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// codexEnforcement says what Codex will and will not enforce for a profile.
func codexEnforcement(s *Spec) []string {
	allowed := map[string]bool{}
	for _, t := range s.Tools.Allow {
		allowed[t] = true
	}
	out := []string{"allowed: read (Codex's sandbox can read files)"}
	if allowed["edit"] {
		out = append(out, "allowed: edit, inside the work directory only (workspace-write sandbox)")
	} else {
		out = append(out, "refused by the Codex sandbox: writing files (read-only)")
	}
	out = append(out, "allowed: shell, inside the sandbox")
	if allowed["web"] {
		out = append(out, "allowed: web search")
	} else {
		out = append(out, "refused by the Codex sandbox: network access")
	}
	if len(s.Write) > 0 {
		out = append(out, "checked at submit, in repo jobs: it may change only "+strings.Join(s.Write, ", ")+" (a submit with other changed files is refused)")
	}
	out = append(out, "never asks for approval (-a never)",
		"not enforced: which shell commands run (the sandbox limits files and network, not commands)",
		"not enforced: time, turns or cost limits (the link only watches whether the terminal exists)")
	return out
}
