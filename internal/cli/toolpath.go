package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// A service (launchd, systemd, a Windows task) starts the link with a bare PATH, and the link has to
// find tmux, git, gh and the agent CLIs. toolDirs lists where those usually are.
func toolDirs(goos, home string) []string {
	dirs := []string{}
	if goos == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin", "/opt/homebrew/sbin")
	}
	dirs = append(dirs, "/usr/local/bin", "/usr/local/sbin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".claude", "local"))
	}
	return dirs
}

// addToolDirs appends the folders that exist and are not already on the PATH, keeping the order
// of what was there.
func addToolDirs(path string, dirs []string, exists func(string) bool) string {
	have := map[string]bool{}
	var parts []string
	for _, d := range strings.Split(path, ":") {
		if d != "" {
			have[d] = true
			parts = append(parts, d)
		}
	}
	for _, d := range dirs {
		if !have[d] && exists(d) {
			have[d] = true
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, ":")
}

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

func userHome() string { h, _ := os.UserHomeDir(); return h }
