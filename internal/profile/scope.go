package profile

import (
	"path"
	"strings"
)

// MatchWrite reports whether a changed path (relative to the worktree, with
// forward slashes) is allowed by the profile's write patterns. No patterns
// means no restriction. A pattern is a relative path with these wildcards:
// ? matches one character, * any characters inside one path element, and **
// any number of whole path elements. A pattern that names a directory
// (ending in /) allows everything under it.
func MatchWrite(globs []string, p string) bool {
	if len(globs) == 0 {
		return true
	}
	p = path.Clean(strings.TrimPrefix(p, "./"))
	for _, g := range globs {
		if strings.HasSuffix(g, "/") {
			g += "**"
		}
		if matchSegments(strings.Split(g, "/"), strings.Split(p, "/")) {
			return true
		}
	}
	return false
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return len(name) > 0 // ** alone matches anything below
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// AdapterPath reports whether a path is one of the files handloom and the
// agent CLIs write into a work directory themselves. They are not the agent's
// work and are left out of the scope check.
func AdapterPath(p string) bool {
	p = path.Clean(p)
	for _, prefix := range []string{".handloom/", ".claude/", ".codex/", ".pi/", ".opencode/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return p == "CLAUDE.md" || p == "AGENTS.md" || p == ".handloom" || p == ".claude" || p == ".codex"
}
