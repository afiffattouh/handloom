package link

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handloom/internal/addon"
	"handloom/internal/api"
	"handloom/internal/client"
)

var echoAddon = filepath.Join("..", "..", "examples", "addons", "echo")

// addonFixture is a hub and a link whose add-ons live in a folder of the test's own.
func addonFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	root := filepath.Join(t.TempDir(), "addons")
	f.link.opt.AddonRoot = root
	return f, root
}

func (f *fixture) addons() []api.Addon {
	f.t.Helper()
	var out []api.Addon
	f.must(f.human.Get("/v1/addons", &out))
	return out
}

func TestAnAgentCannotReportAddons(t *testing.T) {
	f := newFixture(t)
	err := f.worker.Post("/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{{Name: "echo", Status: "ready"}}}, nil)
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("add-ons from an agent: %v", err)
	}
	if got := f.addons(); len(got) != 0 {
		t.Fatalf("the hub took an agent's report: %+v", got)
	}
}

// The whole of what the link owes the hub about add-ons: install echo and it is listed; change a file and it is
// "changed"; remove it and it is gone. Installing is done by the command in another process, so here it is
// done to the folder directly, as the command does.
func TestTheLinkReportsInstalledAddons(t *testing.T) {
	f, root := addonFixture(t)
	store := addon.NewStore(root)

	// on start: nothing installed, and the report is made anyway
	f.ladder()
	if got := f.addons(); len(got) != 0 {
		t.Fatalf("before install: %+v", got)
	}
	if f.link.addonsSent == nil {
		t.Fatal("the first report was not made")
	}

	_, ph, mh, err := addon.Load(echoAddon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addon.Install(root, store, echoAddon, ph, "tester", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	got := f.addons()
	if len(got) != 1 {
		t.Fatalf("after install: %+v", got)
	}
	a := got[0]
	if a.Computer != "dev" || a.Name != "echo" || a.Version != "0.1.0" || a.Status != "ready" || a.PackageHash != ph || a.ManifestHash != mh {
		t.Fatalf("listed %+v", a)
	}
	var one []api.Addon
	f.must(f.human.Get("/v1/addons/echo", &one))
	if len(one) != 1 || !strings.Contains(one[0].Manifest, "name: echo") {
		t.Fatalf("the manifest was not reported: %+v", one)
	}

	// nothing changed: nothing is sent again
	sent := f.link.addonsSent
	f.advance(time.Minute)
	f.ladder()
	if string(f.link.addonsSent) != string(sent) || f.addons()[0].ReportedAt != a.ReportedAt {
		t.Fatal("an unchanged report was sent again")
	}

	// a file changes on disk: the hash differs from the approved one
	svc := filepath.Join(root, "echo", "service.py")
	body, err := os.ReadFile(svc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc, append(body, []byte("\n# edited\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.ladder()
	got = f.addons()
	if len(got) != 1 || got[0].Status != "changed" || got[0].PackageHash == ph || !got[0].ReportedAt.After(a.ReportedAt) {
		t.Fatalf("after a change: %+v", got)
	}

	// put it back and it is ready again
	if err := os.WriteFile(svc, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	if got = f.addons(); len(got) != 1 || got[0].Status != "ready" || got[0].PackageHash != ph {
		t.Fatalf("after putting it back: %+v", got)
	}

	// its folder goes but the approval stays: missing
	if err := os.RemoveAll(filepath.Join(root, "echo")); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	if got = f.addons(); len(got) != 1 || got[0].Status != "missing" || got[0].Version != "0.1.0" {
		t.Fatalf("after the folder went: %+v", got)
	}

	// removed for good: gone from the hub
	if err := addon.Remove(root, store, "echo"); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	if got = f.addons(); len(got) != 0 {
		t.Fatalf("after remove: %+v", got)
	}
}

func TestAReportTheHubRefusedIsSentAgain(t *testing.T) {
	f, root := addonFixture(t)
	if _, err := addon.Install(root, addon.NewStore(root), echoAddon, "", "tester", f.clock()); err != nil {
		t.Fatal(err)
	}
	// the link is pointed at a credential the hub does not know: the report fails and is not remembered
	good := f.link.hub
	f.link.hub = client.Direct(f.url, "hvd_not-a-credential")
	f.link.runAddons(t.Context())
	if f.link.addonsSent != nil {
		t.Fatal("a failed report was remembered as sent")
	}
	f.link.hub = good
	f.ladder()
	if got := f.addons(); len(got) != 1 || got[0].Status != "ready" {
		t.Fatalf("after the hub came back: %+v", got)
	}
}

func TestADamagedApprovalFileKeepsTheLastReport(t *testing.T) {
	f, root := addonFixture(t)
	store := addon.NewStore(root)
	if _, err := addon.Install(root, store, echoAddon, "", "tester", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	if err := os.WriteFile(filepath.Join(root, addon.ApprovalsFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	if got := f.addons(); len(got) != 1 {
		t.Fatalf("the hub's rows were dropped because the file could not be read: %+v", got)
	}
}

// The link cannot see the hub's rows, so a report it was told was accepted is still sent again now and then:
// that is what puts the rows right after the hub lost or cleared them.
func TestTheHubsRowsAreRepairedByAnOccasionalFullReport(t *testing.T) {
	f, root := addonFixture(t)
	if _, err := addon.Install(root, addon.NewStore(root), echoAddon, "", "tester", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.ladder()
	if len(f.addons()) != 1 {
		t.Fatal("the first report was not made")
	}
	// the hub's rows are cleared behind the link's back, as a restore from an older backup would
	if err := f.link.hub.Do(t.Context(), "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{}}, nil); err != nil {
		t.Fatal(err)
	}
	f.advance(addonsResendEvery - time.Minute)
	f.ladder()
	if len(f.addons()) != 0 {
		t.Fatal("the full report was sent again too soon")
	}
	f.advance(2 * time.Minute)
	f.ladder()
	if got := f.addons(); len(got) != 1 || got[0].Name != "echo" {
		t.Fatalf("the hub's rows were not put right: %+v", got)
	}
}
