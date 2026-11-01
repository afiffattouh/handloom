package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/playbook"
)

func (e *env) installMVP() {
	e.t.Helper()
	e.ok(caller{e.admin, ""}, "POST", "/v1/playbooks/mvp-v1/install", api.StarterAddReq{Kind: "claude", Runtime: "cloud"}, nil)
}
func productRequest() api.ProductBuildReq {
	return api.ProductBuildReq{RequestID: "build-1", Title: "Booking", Brief: "Build an appointment booking MVP", Repo: "/srv/booking", Device: "d1", Verify: "./check"}
}
func TestMVPLaunchIsAtomicAndIdempotent(t *testing.T) {
	e := newEnv(t)
	req := productRequest()
	e.fail(404, e.afif(), "POST", "/v1/products", req)
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM task`).Scan(&n)
	if n != 0 {
		t.Fatal("failed preflight left a job behind")
	}
	e.installMVP()
	noCheck := req
	noCheck.Verify = ""
	e.fail(400, e.afif(), "POST", "/v1/products", noCheck)
	e.hub.db.QueryRow(`SELECT count(*) FROM spawn`).Scan(&n)
	if n != 0 {
		t.Fatal("failed preflight left a lead behind")
	}
	var j, again api.Job
	e.ok(e.afif(), "POST", "/v1/products", req, &j)
	e.ok(e.afif(), "POST", "/v1/products", req, &again)
	if j.ID != again.ID || j.Tasks.Open != 10 {
		t.Fatalf("launch: %+v %+v", j, again)
	}
	e.hub.db.QueryRow(`SELECT count(*) FROM spawn`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate lead: %d", n)
	}
	req.Brief = "a different product"
	e.fail(409, e.afif(), "POST", "/v1/products", req)
	s := e.planStatus(j.ID)
	for _, step := range s.Steps {
		if !strings.HasSuffix(step.Profile, "@1") {
			t.Fatal("profile not pinned")
		}
	}
	e.planSweep()
	s = e.planStatus(j.ID)
	for _, step := range s.Steps {
		if (step.Key == "brief") != (step.Attempts == 1) {
			t.Fatalf("wrong dispatch: %+v", s)
		}
	}
	// The installed lead is explicitly told that the graph already exists.
	var spawns []api.Spawn
	e.ok(e.afif(), "GET", "/v1/spawns", nil, &spawns)
	for _, sp := range spawns {
		if sp.Role == "lead" {
			e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: sp.Name, Kind: "claude"}, nil)
			if code, b := e.report(e.d1, sp.ID, api.SpawnReport{Status: "started"}); code != 200 {
				t.Fatalf("lead: %d %s", code, b)
			}
			lead := caller{e.d1, sp.Name}
			if !strings.Contains(e.inboxBodies(lead), "Do not create, assign, or spawn workers") {
				t.Fatal("lead still has manual scheduling instructions")
			}
			e.fail(409, lead, "POST", "/v1/tasks", api.TaskCreateReq{Title: "unplanned task"})
			e.fail(409, lead, "POST", "/v1/spawns", api.SpawnReq{Name: "unplanned-worker", Profile: "mvp-backend"})

		}
	}
}
func TestMVPInstallPreservesProfilesAndPermissions(t *testing.T) {
	e := newEnv(t)
	e.fail(403, e.afif(), "POST", "/v1/playbooks/mvp-v1/install", api.StarterAddReq{Kind: "claude", Runtime: "cloud"})
	e.installMVP()
	e.installMVP()
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM profile`).Scan(&n)
	if n != len(playbook.ProfileStarters) {
		t.Fatalf("non-idempotent install: %d", n)
	}
	spec := researcher()
	spec.Prompt = "workspace-specific reviewer"
	e.newProfile(caller{e.admin, ""}, "mvp-review", spec)
	e.installMVP()
	var p api.ProfileFull
	e.ok(e.afif(), "GET", "/v1/profiles/mvp-review", nil, &p)
	if p.Spec.Prompt != spec.Prompt || p.Version != 2 {
		t.Fatal("install overwrote existing settings")
	}
	e.fail(403, e.worker(), "POST", "/v1/products", productRequest())
	e.fail(403, e.lead(), "POST", "/v1/products", productRequest())
	e.fail(404, e.afif(), "POST", "/v1/products", api.ProductBuildReq{RequestID: "missing-machine", Title: "test", Brief: "build", Repo: "/repo", Device: "no-device", Verify: "true"})
}

func TestMVPPageShowsManagedStages(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	cookies, _ := e.owner()
	human := e.humanAPI()
	e.apiOK(e.admin, "", "POST", "/v1/playbooks/mvp-v1/install", api.StarterAddReq{Kind: "claude", Runtime: "cloud"}, nil)
	var j api.Job
	e.apiOK(human, "", "POST", "/v1/products", productRequest(), &j)
	page := e.req("GET", fmt.Sprintf("/jobs/%d", j.ID), nil, cookies, nil)
	if page.status != 200 {
		t.Fatalf("page: %d %s", page.status, page.body)
	}
	for _, want := range []string{"Build stages", "Deployment is not automatic", "Define the product and acceptance criteria", "Independently review security", "Prepare the release handoff"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestMVPInstallAdaptsCodexAndReportsCapabilities(t *testing.T) {
	e := newEnv(t)
	var result api.PlaybookInstallResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/playbooks/mvp-v1/install", api.StarterAddReq{Kind: "codex", Runtime: "cloud", Adapt: true}, &result)
	if len(result.Profiles) != len(playbook.ProfileStarters) || len(result.Warnings) == 0 {
		t.Fatalf("missing profiles or capability notice: %+v", result)
	}
	for _, p := range result.Profiles {
		if p.Kind != "codex" {
			t.Fatalf("wrong CLI: %+v", p)
		}
	}
}
