package cli

import (
	"strings"
	"testing"
)

func TestAddToolDirsAddsOnlyWhatExistsAndIsMissing(t *testing.T) {
	exists := func(p string) bool {
		return p == "/opt/homebrew/bin" || p == "/usr/local/bin" || p == "/Users/ann/.local/bin"
	}
	got := addToolDirs("/usr/bin:/usr/local/bin", toolDirs("darwin", "/Users/ann"), exists)
	if got != "/usr/bin:/usr/local/bin:/opt/homebrew/bin:/Users/ann/.local/bin" {
		t.Fatalf("%s", got)
	}
	// run twice: no growth
	if again := addToolDirs(got, toolDirs("darwin", "/Users/ann"), exists); again != got {
		t.Fatalf("not stable: %s", again)
	}
	// an empty PATH (what a service can start with) still works
	if got := addToolDirs("", toolDirs("darwin", "/Users/ann"), exists); !strings.HasPrefix(got, "/opt/homebrew/bin") || strings.Contains(got, "::") {
		t.Fatalf("%q", got)
	}
}

func TestToolDirsOnlyHasHomebrewOnAMac(t *testing.T) {
	if strings.Contains(strings.Join(toolDirs("linux", "/home/a"), ":"), "/opt/homebrew") {
		t.Error("homebrew on linux")
	}
	if !strings.Contains(strings.Join(toolDirs("darwin", "/Users/a"), ":"), "/opt/homebrew/bin") {
		t.Error("no homebrew on a Mac")
	}
}
