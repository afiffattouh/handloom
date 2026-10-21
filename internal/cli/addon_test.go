package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var echoAddon = filepath.Join("..", "..", "examples", "addons", "echo")

// addonRun runs one addon command with HANDLOOM_HOME in a fresh folder.
func addonRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := Main(append([]string{"addon"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func addonHome(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not available")
	}
	t.Setenv("HANDLOOM_HOME", t.TempDir())
}

func TestAddonHashAddListDoctorRemove(t *testing.T) {
	addonHome(t)

	code, out, errs := addonRun(t, "hash", echoAddon)
	hash := strings.TrimSpace(out)
	if code != 0 || len(hash) != 64 {
		t.Fatalf("hash: %d %q %q", code, out, errs)
	}

	if code, out, _ := addonRun(t, "list"); code != 0 || !strings.Contains(out, "No add-ons") {
		t.Fatalf("empty list: %d %q", code, out)
	}

	// without --yes and without a terminal, it reads what the add-on does and stops
	code, out, errs = addonRun(t, "add", echoAddon)
	if code == 0 || !strings.Contains(errs, "--yes") {
		t.Fatalf("add without yes: %d %q %q", code, out, errs)
	}
	if code, out, _ := addonRun(t, "list"); code != 0 || !strings.Contains(out, "No add-ons") {
		t.Fatalf("something was installed: %q", out)
	}

	// a wrong pin stops it
	if code, _, errs := addonRun(t, "add", echoAddon, "--sha256", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "nothing was installed") {
		t.Fatalf("wrong pin: %d %q", code, errs)
	}

	// the right pin goes ahead and says what it installs
	code, out, errs = addonRun(t, "add", echoAddon, "--sha256", hash)
	if code != 0 {
		t.Fatalf("add: %d %q %q", code, out, errs)
	}
	for _, want := range []string{"Add-on echo 0.1.0", "say:", "leaves this computer: no", "wait:", "Package hash: " + hash, "Installed echo 0.1.0", "health check"} {
		if !strings.Contains(out, want) {
			t.Errorf("add output lacks %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"plugin", "machine", "agent"} {
		if strings.Contains(strings.ToLower(out), banned) {
			t.Errorf("add output uses %q:\n%s", banned, out)
		}
	}

	code, out, _ = addonRun(t, "list")
	if code != 0 || !strings.Contains(out, "echo") || !strings.Contains(out, "Ready") || !strings.Contains(out, "say, wait") {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out, _ = addonRun(t, "list", "--json")
	var rows []map[string]any
	if json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0]["package_hash"] != hash {
		t.Fatalf("list --json: %q", out)
	}

	// installing again replaces it, and says so
	code, out, _ = addonRun(t, "add", echoAddon, "--yes")
	if code != 0 || !strings.Contains(out, "This replaces echo 0.1.0") {
		t.Fatalf("second add: %d %q", code, out)
	}

	if code, out, _ := addonRun(t, "doctor"); code != 0 || !strings.Contains(out, "ok   echo 0.1.0: ready; it answered its health check") {
		t.Fatalf("doctor: %d %q", code, out)
	}

	// a changed file is reported, and the doctor fails
	py := filepath.Join(addonRoot(), "echo", "service.py")
	b, _ := os.ReadFile(py)
	os.WriteFile(py, append(b, '\n', '#'), 0o644)
	code, out, errs = addonRun(t, "doctor")
	if code != 1 || !strings.Contains(out, "FAIL echo") || !strings.Contains(out, "approve it again") || !strings.Contains(errs, "need attention") {
		t.Fatalf("doctor after change: %d %q %q", code, out, errs)
	}
	if _, out, _ := addonRun(t, "list"); !strings.Contains(out, "Changed since approved") {
		t.Fatalf("list after change: %q", out)
	}

	if code, out, _ := addonRun(t, "remove", "echo"); code != 0 || !strings.Contains(out, "Removed echo") {
		t.Fatalf("remove: %d %q", code, out)
	}
	if code, _, errs := addonRun(t, "remove", "echo"); code != 1 || !strings.Contains(errs, "does not have") {
		t.Fatalf("remove twice: %d %q", code, errs)
	}
	if _, out, _ := addonRun(t, "list"); !strings.Contains(out, "No add-ons") {
		t.Fatalf("list after remove: %q", out)
	}
}

func TestAddonAddAsksFirst(t *testing.T) {
	addonHome(t)
	old := addonIn
	defer func() { addonIn = old }()
	addonIn = strings.NewReader("n\n")
	if code, out, _ := addonRun(t, "add", echoAddon); code != 0 || !strings.Contains(out, "Nothing was installed") {
		t.Fatalf("no: %d %q", code, out)
	}
	addonIn = strings.NewReader("yes\n")
	if code, out, errs := addonRun(t, "add", echoAddon); code != 0 || !strings.Contains(out, "Installed echo") {
		t.Fatalf("yes: %d %q %q", code, out, errs)
	}
}

func TestAddonRefusals(t *testing.T) {
	addonHome(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "addon.yaml"), []byte("schema: 1\nkind: kit\nname: research\nversion: 1.0.0\napi: 1\n"), 0o644)
	if code, _, errs := addonRun(t, "add", dir, "--yes"); code != 1 || !strings.Contains(errs, "kits are not installable yet; the factory kit is built in") {
		t.Fatalf("kit: %d %q", code, errs)
	}
	if code, _, errs := addonRun(t, "hash", t.TempDir()); code != 1 || !strings.Contains(errs, "no addon.yaml") {
		t.Fatalf("no manifest: %d %q", code, errs)
	}
	if code, _, _ := addonRun(t); code != 2 {
		t.Fatalf("bare addon: %d", code)
	}
	if code, _, _ := addonRun(t, "frobnicate"); code != 2 {
		t.Fatalf("unknown: %d", code)
	}
	if code, _, _ := addonRun(t, "add"); code != 2 {
		t.Fatalf("add without a folder: %d", code)
	}
}

func TestAddonHelpListsIt(t *testing.T) {
	var out strings.Builder
	Main([]string{"help"}, &out, &out)
	if !strings.Contains(out.String(), "handloom addon hash DIR | add DIR") || !strings.Contains(out.String(), "handloom adapter install") {
		t.Fatal("help lacks the add-on commands or lost the adapter ones")
	}
}
