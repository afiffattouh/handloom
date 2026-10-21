package hub

import (
	"strings"
	"testing"

	"handloom/internal/api"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func addonItem(name, status string) api.AddonItem {
	return api.AddonItem{Name: name, Version: "0.1.0", PackageHash: hashA, ManifestHash: hashB, Manifest: "name: " + name + "\n", Status: status}
}

func TestAddonReportReplacesTheComputersRows(t *testing.T) {
	m := newTeam(t)
	desk := caller{m.miaBox, ""}
	m.ok(desk, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "ready"), addonItem("notes", "ready")}}, nil)
	// another computer reports one of the same name: one row per computer and name
	m.ok(caller{m.benBox, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "changed")}}, nil)

	var all []api.Addon
	m.ok(m.olive, "GET", "/v1/addons", nil, &all)
	if len(all) != 3 {
		t.Fatalf("listed %+v", all)
	}
	for _, a := range all {
		if a.Manifest != "" {
			t.Errorf("the list carries a manifest: %+v", a)
		}
		if a.ReportedAt.IsZero() {
			t.Errorf("no report time: %+v", a)
		}
	}

	// the next report replaces: notes is gone, echo is now down with a detail
	down := addonItem("echo", "down")
	down.Detail = "it stopped three times"
	m.ok(desk, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{down}}, nil)
	var one []api.Addon
	m.ok(m.olive, "GET", "/v1/addons/echo", nil, &one)
	if len(one) != 2 {
		t.Fatalf("echo on %+v", one)
	}
	for _, a := range one {
		if a.Manifest != "name: echo\n" {
			t.Errorf("manifest of %s on %s: %q", a.Name, a.Computer, a.Manifest)
		}
		if a.Computer == "mia-box" && (a.Status != "down" || a.Detail != "it stopped three times") {
			t.Errorf("mia-box: %+v", a)
		}
		if a.Computer == "ben-box" && a.Status != "changed" {
			t.Errorf("another computer's row was touched: %+v", a)
		}
	}
	m.fail(404, m.olive, "GET", "/v1/addons/notes", nil)

	// an empty report removes everything that computer had
	m.ok(desk, "POST", "/v1/device/addons", api.AddonReport{}, nil)
	m.ok(m.olive, "GET", "/v1/addons", nil, &all)
	if len(all) != 1 || all[0].Computer != "ben-box" {
		t.Fatalf("after an empty report: %+v", all)
	}
}

func TestOnlyAComputerReportsItsAddons(t *testing.T) {
	m := newTeam(t)
	rep := api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "ready")}}
	for name, c := range map[string]caller{"owner": m.olive, "member": m.mia, "viewer": m.vera, "admin": {m.admin, ""}, "nobody": {}} {
		status, _ := m.do(c, "POST", "/v1/device/addons", rep)
		if status != 403 && status != 401 {
			t.Errorf("%s reported add-ons: status %d", name, status)
		}
	}
}

func TestAddonReportIsChecked(t *testing.T) {
	m := newTeam(t)
	desk := caller{m.miaBox, ""}
	bad := map[string]api.AddonItem{
		"a name that is not one":  {Name: "Echo", Status: "ready"},
		"a path as a name":        {Name: "../x", Status: "ready"},
		"a status not in the set": {Name: "echo", Status: "running"},
		"a hash that is not one":  {Name: "echo", Status: "ready", PackageHash: "abc"},
		"a manifest too long":     {Name: "echo", Status: "ready", Manifest: strings.Repeat("x", maxAddonManifest+1)},
	}
	for what, it := range bad {
		if status, _ := m.do(desk, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{it}}); status != 400 {
			t.Errorf("%s: status %d", what, status)
		}
	}
	twice := api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "ready"), addonItem("echo", "ready")}}
	if status, _ := m.do(desk, "POST", "/v1/device/addons", twice); status != 400 {
		t.Errorf("a name twice: status %d", status)
	}
	var many api.AddonReport
	for i := 0; i <= maxAddonsReported; i++ {
		many.Addons = append(many.Addons, addonItem("echo", "ready"))
	}
	if status, _ := m.do(desk, "POST", "/v1/device/addons", many); status != 400 {
		t.Errorf("too many: status %d", status)
	}
	// a refused report leaves what the computer had
	m.ok(desk, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "ready")}}, nil)
	m.do(desk, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{{Name: "Echo", Status: "ready"}}})
	var all []api.Addon
	m.ok(m.olive, "GET", "/v1/addons", nil, &all)
	if len(all) != 1 || all[0].Name != "echo" {
		t.Fatalf("a refused report changed the rows: %+v", all)
	}
}

func TestWhoSeesAddons(t *testing.T) {
	m := newTeam(t)
	m.ok(caller{m.miaBox, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "ready")}}, nil)
	m.ok(caller{m.benBox, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{addonItem("notes", "ready")}}, nil)
	count := func(c caller) int {
		var all []api.Addon
		m.ok(c, "GET", "/v1/addons", nil, &all)
		return len(all)
	}
	// the owner sees every computer; a member only their own; a viewer owns none; an agent reads like anyone
	if n := count(m.olive); n != 2 {
		t.Errorf("owner sees %d", n)
	}
	if n := count(m.mia); n != 1 {
		t.Errorf("a member sees %d", n)
	}
	if n := count(m.vera); n != 0 {
		t.Errorf("a viewer sees %d", n)
	}
	m.fail(404, m.mia, "GET", "/v1/addons/notes", nil) // on someone else's computer
	m.fail(401, caller{}, "GET", "/v1/addons", nil)
}

func TestRevokedComputersAddonsAreNotListed(t *testing.T) {
	m := newTeam(t)
	m.ok(caller{m.miaBox, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{addonItem("echo", "ready")}}, nil)
	m.ok(m.olive, "POST", "/v1/devices/mia-box/revoke", nil, nil)
	var all []api.Addon
	m.ok(m.olive, "GET", "/v1/addons", nil, &all)
	if len(all) != 0 {
		t.Fatalf("a revoked computer's add-ons are listed: %+v", all)
	}
}
