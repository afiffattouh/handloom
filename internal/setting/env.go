// Package setting reads the settings that the product was renamed under. The
// project is called Handloom and its variables start with HANDLOOM_; it used
// to be called handloom, and existing installs, service files and agent
// configuration still set HANDLOOM_*. Both work, HANDLOOM_* wins.
package setting

import (
	"os"
	"strings"
)

// Get returns HANDLOOM_<name>, else HANDLOOM_<name>, else "".
func Get(name string) string {
	if v := os.Getenv("HANDLOOM_" + name); v != "" {
		return v
	}
	return os.Getenv("HANDLOOM_" + name)
}

// Bool is true for 1, true, yes and on.
func Bool(name string) bool {
	switch strings.ToLower(Get(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Both returns KEY=value pairs for a child process under both names, so a
// child that is still an old binary or an old hook script sees the setting.
func Both(name, value string) []string {
	return []string{"HANDLOOM_" + name + "=" + value, "HANDLOOM_" + name + "=" + value}
}
