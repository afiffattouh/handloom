package profile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// What Claude Code is asked to do for a profile. The agent is unattended, so
// nothing may ever prompt: tools that are not listed are denied, not asked
// about (--permission-mode dontAsk), and the user's global settings, skills
// and rules do not apply (--setting-sources project,local).

// ClaudeArgv is the command line for a spawned Claude Code. model overrides
// the profile's own.
func ClaudeArgv(s *Spec, model string) []string {
	allow := []string{"Bash(handloom:*)"} // an agent must always be able to talk to its hub
	for _, t := range s.Tools.Allow {
		switch t {
		case "read":
			allow = append(allow, "Read", "Glob", "Grep")
		case "edit":
			allow = append(allow, "Write", "Edit")
		case "shell":
			allow = append(allow, "Bash")
		case "web":
			allow = append(allow, "WebFetch", "WebSearch")
		}
	}
	argv := []string{"claude", "--setting-sources", "project,local", "--permission-mode", "dontAsk", "--allowedTools"}
	argv = append(argv, allow...)
	if len(s.Tools.DenyCommands) > 0 {
		argv = append(argv, "--disallowedTools")
		for _, c := range s.Tools.DenyCommands {
			argv = append(argv, "Bash("+c+":*)")
		}
	}
	if model == "" {
		model = s.Model
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return argv
}

// InstallSkills writes the profile's skills into dir for Claude Code
// (.claude/skills/<name>/...). Paths were checked by Normalize and are
// checked again here, because the spec comes from the hub.
func InstallSkills(dir string, s *Spec) error {
	root := filepath.Join(dir, ".claude", "skills")
	for _, f := range s.Skills {
		clean := path.Clean(f.Path)
		if clean != f.Path || path.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, "\\") {
			return fmt.Errorf("refusing skill file path %q", f.Path)
		}
		dst := filepath.Join(root, filepath.FromSlash(clean))
		if rel, err := filepath.Rel(root, dst); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("refusing skill file path %q", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through the symlink %s", dst)
		}
		if err := os.WriteFile(dst, []byte(f.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Enforcement says, in words, what the CLI will actually enforce for this
// profile and what it will not.
func Enforcement(s *Spec) []string {
	var out []string
	allowed := map[string]bool{}
	for _, t := range s.Tools.Allow {
		allowed[t] = true
	}
	for _, t := range []string{"read", "edit", "shell", "web"} {
		if allowed[t] {
			out = append(out, "allowed: "+t)
		} else {
			out = append(out, "refused by Claude Code: "+t)
		}
	}
	for _, c := range s.Tools.DenyCommands {
		out = append(out, "refused by Claude Code: the shell command "+c)
	}
	out = append(out, "not enforced: file paths (an agent can read and write anywhere its tools reach)",
		"not enforced: time, turns or cost limits (the link only watches whether the terminal exists)")
	return out
}
