package cli

import (
	"os"
	"path/filepath"
	"testing"

	"handloom/internal/client"
)

// What an install made under the old name (handloom) left behind keeps working.

func TestOldIdentityFileAndEnvStillWork(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".handloom"), 0o755)
	os.WriteFile(filepath.Join(dir, ".handloom", "agent"), []byte("old-agent\n"), 0o644)
	t.Setenv("HANDLOOM_AGENT", "")
	t.Setenv("HANDLOOM_AGENT", "")
	if got := AgentName(dir); got != "old-agent" {
		t.Fatalf("AgentName from .handloom/agent: %q", got)
	}
	os.MkdirAll(filepath.Join(dir, ".handloom"), 0o755)
	os.WriteFile(filepath.Join(dir, ".handloom", "agent"), []byte("new-agent\n"), 0o644)
	if got := AgentName(dir); got != "new-agent" {
		t.Fatalf("the new identity file should win: %q", got)
	}
	t.Setenv("HANDLOOM_AGENT", "from-old-env")
	if got := AgentName(dir); got != "from-old-env" {
		t.Fatalf("HANDLOOM_AGENT: %q", got)
	}
	t.Setenv("HANDLOOM_AGENT", "from-new-env")
	if got := AgentName(dir); got != "from-new-env" {
		t.Fatalf("HANDLOOM_AGENT should win: %q", got)
	}
}

func TestOldDatabaseAndDataDirAreKept(t *testing.T) {
	dir := t.TempDir()
	if got := dbFile(dir); filepath.Base(got) != "handloom.db" {
		t.Fatalf("new hub: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "handloom.db"), []byte("x"), 0o600)
	if got := dbFile(dir); filepath.Base(got) != "handloom.db" {
		t.Fatalf("a hub made under the old name must keep its database: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "handloom.db"), []byte("x"), 0o600)
	if got := dbFile(dir); filepath.Base(got) != "handloom.db" {
		t.Fatalf("handloom.db should win when both exist: %s", got)
	}
}

func TestOldLinkHomeIsKept(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HANDLOOM_HOME", "")
	t.Setenv("HANDLOOM_HOME", "")
	if got := client.Home(); got != filepath.Join(home, ".config", "handloom") {
		t.Fatalf("fresh machine: %s", got)
	}
	os.MkdirAll(filepath.Join(home, ".config", "handloom"), 0o755)
	if got := client.Home(); got != filepath.Join(home, ".config", "handloom") {
		t.Fatalf("a device joined under the old name must keep its credential: %s", got)
	}
	os.MkdirAll(filepath.Join(home, ".config", "handloom"), 0o755)
	if got := client.Home(); got != filepath.Join(home, ".config", "handloom") {
		t.Fatalf("the new directory should win once it exists: %s", got)
	}
}

func TestRemoveLegacyDeletesOldShimsAndIdentity(t *testing.T) {
	root := t.TempDir()
	spec := adapterSpecs["pi"]
	old := filepath.Join(root, ".pi", "extensions", "handloom.ts")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, ".handloom"), 0o755)
	os.WriteFile(filepath.Join(root, ".handloom", "agent"), []byte("a\n"), 0o644)
	removeLegacy(root, spec)
	for _, p := range []string{old, filepath.Join(root, ".handloom", "agent"), filepath.Join(root, ".handloom")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still exists", p)
		}
	}
}
