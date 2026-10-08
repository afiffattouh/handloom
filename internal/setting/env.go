// Package setting reads the settings the product takes from the environment:
// every variable starts with HANDLOOM_.
package setting

import (
	"os"
	"strings"
)

// Get returns HANDLOOM_<name>, or "".
func Get(name string) string { return os.Getenv("HANDLOOM_" + name) }

// Bool is true for 1, true, yes and on.
func Bool(name string) bool {
	switch strings.ToLower(Get(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
