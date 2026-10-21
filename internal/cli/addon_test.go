package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"handloom/internal/addon"
	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/profile"
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

func TestAddonStatusListsWhatTheHubHolds(t *testing.T) {
	r := newRig(t)
	if out := r.as(r.admin, "addon", "status"); !strings.Contains(out, "No computer has reported an add-on yet") {
		t.Fatalf("empty status: %s", out)
	}
	if code, _, errs := r.exec("addon", "status"); code == 0 || errs == "" {
		t.Fatal("status without a token should be refused")
	}

	cfg, err := client.LoadLinkConfig()
	if err != nil {
		t.Fatal(err)
	}
	item := api.AddonItem{Name: "echo", Version: "0.1.0", PackageHash: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64), Manifest: "name: echo\n", Status: "changed", Detail: "differs from what was approved"}
	if err := client.Direct(cfg.Hub, cfg.Credential).Post("/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{item}}, nil); err != nil {
		t.Fatal(err)
	}
	out := r.as(r.admin, "addon", "status")
	for _, want := range []string{"echo", "0.1.0", "on dev", "Changed since approved", "differs from what was approved"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(r.as(r.admin, "addon", "status", "echo", "--json")), &rows); err != nil || len(rows) != 1 || rows[0]["computer"] != "dev" || rows[0]["manifest"] != "name: echo\n" {
		t.Fatalf("status echo --json: %v %v", rows, err)
	}
	t.Setenv("HANDLOOM_HUB", r.hubURL)
	t.Setenv("HANDLOOM_TOKEN", r.admin)
	if code, _, errs := r.exec("addon", "status", "nothing"); code == 0 || !strings.Contains(errs, "nothing") {
		t.Fatalf("an unknown add-on: %d %q", code, errs)
	}
}

func TestAddonGrantVerbs(t *testing.T) {
	r := newRig(t)
	// A computer reports the echo add-on, as the link does after an install.
	m, _, mh, err := addon.Load(echoAddon)
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(echoAddon, "addon.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := client.LoadLinkConfig()
	if err != nil {
		t.Fatal(err)
	}
	item := api.AddonItem{Name: m.Name, Version: m.Version, PackageHash: strings.Repeat("a", 64), ManifestHash: mh, Manifest: string(text), Status: "ready"}
	if err := client.Direct(cfg.Hub, cfg.Credential).Post("/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{item}}, nil); err != nil {
		t.Fatal(err)
	}
	spec := profile.Spec{Kind: "claude", Tools: profile.Tools{Allow: []string{"read"}}}
	if err := client.Direct(r.hubURL, r.admin).Post("/v1/profiles", api.ProfileReq{Name: "reader", Spec: spec}, nil); err != nil {
		t.Fatal(err)
	}

	// afif is a member: refused. As the owner the verbs work.
	t.Setenv("HANDLOOM_HUB", r.hubURL)
	t.Setenv("HANDLOOM_TOKEN", r.human)
	if code, _, errs := r.exec("addon", "grant", "reader", "echo.say"); code == 0 || !strings.Contains(errs, "owner") {
		t.Fatalf("a member granted: %d %q", code, errs)
	}
	if _, err := r.db.Exec(`UPDATE human SET role = 'owner' WHERE name = 'afif'`); err != nil {
		t.Fatal(err)
	}
	if out := r.as(r.human, "addon", "grants"); !strings.Contains(out, "No tool is granted") {
		t.Fatalf("no grants yet: %s", out)
	}
	out := r.as(r.human, "addon", "grant", "reader", "echo.say", "--leaves", "no", "--max-calls", "5")
	if !strings.Contains(out, "Granted echo.say to reader") || !strings.Contains(out, "stays on this computer") || !strings.Contains(out, "up to 5 calls") {
		t.Fatalf("grant: %s", out)
	}
	out = r.as(r.human, "addon", "grants", "--profile", "reader")
	if !strings.Contains(out, "echo.say") || !strings.Contains(out, "stays on this computer") || !strings.Contains(out, "up to 5 calls a job") {
		t.Fatalf("grants: %s", out)
	}
	var rows []api.AddonGrant
	if err := json.Unmarshal([]byte(r.as(r.human, "addon", "grants", "--json")), &rows); err != nil || len(rows) != 1 || rows[0].ManifestHash != mh || rows[0].LeavesMachine {
		t.Fatalf("grants --json: %+v %v", rows, err)
	}
	r.as(r.human, "addon", "grant", "reader", "echo.wait")
	if out = r.as(r.human, "addon", "grants"); !strings.Contains(out, "leaves this computer") {
		t.Fatalf("the cautious default: %s", out)
	}
	if out = r.as(r.human, "addon", "revoke", "reader", "echo.wait"); !strings.Contains(out, "Revoked echo.wait from reader") {
		t.Fatalf("revoke: %s", out)
	}
	for _, bad := range [][]string{{"grant", "reader", "echo"}, {"grant", "reader", "echo.nothing"}, {"grant", "reader", "echo.say", "--leaves", "maybe"}, {"revoke", "reader", "echo.wait"}, {"grant", "nobody", "echo.say"}} {
		if code, _, errs := r.exec(append([]string{"addon"}, bad...)...); code == 0 || errs == "" {
			t.Errorf("%v was accepted", bad)
		}
	}
}
