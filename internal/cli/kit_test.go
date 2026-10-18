package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheKitShipsInsideTheProgramAndSetsAProjectUp(t *testing.T) {
	if findPython() == "" {
		t.Skip("no Python 3.8+ here")
	}
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	proj := t.TempDir()
	if out, err := exec.Command("git", "-C", proj, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git: %v %s", err, out)
	}
	var out, errb bytes.Buffer
	if code := Main([]string{"kit", "init", proj}, &out, &errb); code != 0 {
		t.Fatalf("kit init: %d\n%s%s", code, out.String(), errb.String())
	}
	for _, f := range []string{"check", "factory.toml", "AGENTS.md", ".factory/check.py", ".factory/_toml.py"} {
		if _, err := os.Stat(filepath.Join(proj, f)); err != nil {
			t.Errorf("%s was not added: %v", f, err)
		}
	}
	// The project now runs its own check, with no program of ours involved.
	cmd := exec.Command("./check")
	cmd.Dir = proj
	if b, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(b), "evidence: runs/") {
		t.Fatalf("./check: %v\n%s", err, b)
	}
	// A second run finds the kit already unpacked.
	if d1, _ := unpackKit(); d1 == "" {
		t.Fatal("no kit folder")
	}
	// A bad subcommand's exit code comes back to the caller.
	if code := Main([]string{"kit", "nonsense"}, &out, &errb); code == 0 {
		t.Fatal("a bad kit command must not succeed")
	}
	if code := Main([]string{"kit"}, &out, &errb); code != 2 {
		t.Fatalf("bare kit: %d", code)
	}
}
