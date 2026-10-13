package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
)

// team is the cast of D108: the owner olive, members mia and ben (each with a machine of their own),
// a viewer, an AI app of mia's, the admin token, an agent, and a machine "shared" that the admin token
// added (no owner). afif and his machines d1 and d2 come from newEnv.
type team struct {
	*env
	olive, mia, ben, vera, miaApp caller
	miaBox, benBox, shared        string // device credentials
}

func (e *env) person(name, role string) caller {
	e.t.Helper()
	var tok api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: name}, &tok)
	if _, err := e.hub.db.Exec(`UPDATE human SET role = ? WHERE name = ?`, role, name); err != nil {
		e.t.Fatal(err)
	}
	return caller{tok.Token, ""}
}

// addMachine adds a machine as c and joins it; it returns the machine's credential.
func (e *env) addMachine(c caller, name string) string {
	e.t.Helper()
	var tok api.TokenResp
	e.ok(c, "POST", "/v1/devices", api.NameReq{Name: name}, &tok)
	var join api.JoinResp
	e.ok(caller{}, "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok.Token}, &join)
	return join.Credential
}

func newTeam(t *testing.T) *team {
	t.Helper()
	e := newEnv(t)
	m := &team{env: e}
	e.hub.opt.MaxSpawns = 50
	m.olive = e.person("olive", "owner")
	m.mia = e.person("mia", "member")
	m.ben = e.person("ben", "member")
	m.vera = e.person("vera", "viewer")
	var op api.TokenResp
	e.ok(m.mia, "POST", "/v1/me/operator-token", nil, &op)
	m.miaApp = caller{op.Token, ""}
	m.shared = e.device("shared") // added by the admin token: shared by the owner
	m.miaBox = e.addMachine(m.mia, "mia-box")
	m.benBox = e.addMachine(m.ben, "ben-box")
	return m
}

func (m *team) names(c caller) []string {
	m.t.Helper()
	var ds []api.Device
	m.ok(c, "GET", "/v1/devices", nil, &ds)
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

func TestWhoMayAddAMachine(t *testing.T) {
	m := newTeam(t)
	// Owners and members add machines and own what they add; the admin token's are shared by the owner.
	m.ok(m.olive, "POST", "/v1/devices", api.NameReq{Name: "olive-box"}, nil)
	m.ok(caller{m.admin, ""}, "POST", "/v1/devices", api.NameReq{Name: "admin-box"}, nil)
	m.ok(caller{m.admin, ""}, "POST", "/v1/admin/devices", api.NameReq{Name: "admin-box2"}, nil) // the older spelling still works
	var owners = map[string]string{}
	var ds []api.Device
	m.ok(m.olive, "GET", "/v1/devices", nil, &ds)
	for _, d := range ds {
		owners[d.Name] = d.Owner
	}
	for name, want := range map[string]string{"mia-box": "mia", "ben-box": "ben", "olive-box": "olive", "admin-box": "", "admin-box2": "", "shared": "", "d1": "afif"} {
		if got, ok := owners[name]; !ok || got != want {
			t.Errorf("%s belongs to %q (listed %v), want %q", name, got, ok, want)
		}
	}
	// Viewers, agents and AI apps do not.
	for name, c := range map[string]caller{"viewer": m.vera, "lead": m.lead(), "device": {m.d1, ""}, "ai app": m.miaApp, "nobody": {}} {
		if code, _ := m.do(c, "POST", "/v1/devices", api.NameReq{Name: "sneaky-" + strings.ReplaceAll(name, " ", "")}); code != 403 && code != 401 {
			t.Errorf("%s added a machine: status %d", name, code)
		}
	}
	if sameSet(m.names(m.olive), "sneaky-viewer") || strings.Contains(strings.Join(m.names(m.olive), ","), "sneaky") {
		t.Fatal("a refused request left a machine behind")
	}
	// A bad name and a taken name are refused in words for a member too.
	m.fail(400, m.mia, "POST", "/v1/devices", api.NameReq{Name: "bad name!"})
	m.fail(409, m.mia, "POST", "/v1/devices", api.NameReq{Name: "ben-box"})
	// The audit log records who owns what was added.
	var rows []api.AuditRow
	m.ok(caller{m.admin, ""}, "GET", "/v1/audit?limit=500", nil, &rows)
	found := map[string]string{}
	for _, r := range rows {
		if r.Action == "device.add" {
			var p struct{ Owner string }
			json.Unmarshal(r.Payload, &p)
			found[r.Target] = r.Actor + "/" + p.Owner
		}
	}
	if found["device:mia-box"] != "human:mia/mia" || found["device:admin-box"] != "admin/" {
		t.Fatalf("device.add audit: %v", found)
	}
}

func TestEachPersonSeesOnlyTheirOwnMachines(t *testing.T) {
	m := newTeam(t)
	all := []string{"d1", "d2", "shared", "mia-box", "ben-box"}
	if got := m.names(m.olive); !sameSet(got, all...) {
		t.Errorf("owner sees %v", got)
	}
	if got := m.names(caller{m.admin, ""}); !sameSet(got, all...) {
		t.Errorf("admin token sees %v", got)
	}
	if got := m.names(m.mia); !sameSet(got, "mia-box") {
		t.Errorf("mia sees %v", got)
	}
	if got := m.names(m.miaApp); !sameSet(got, "mia-box") {
		t.Errorf("mia's AI app sees %v", got)
	}
	if got := m.names(m.ben); !sameSet(got, "ben-box") {
		t.Errorf("ben sees %v", got)
	}
	if got := m.names(m.afif()); !sameSet(got, "d1", "d2") {
		t.Errorf("afif sees %v", got)
	}
	if got := m.names(m.vera); len(got) != 0 {
		t.Errorf("a viewer sees %v", got)
	}
	// An agent's device does not list machines.
	m.fail(403, m.lead(), "GET", "/v1/devices", nil)
	// The owner's list says whose each is; a machine without a person says it is shared.
	var ds []api.Device
	m.ok(m.olive, "GET", "/v1/devices", nil, &ds)
	for _, d := range ds {
		if d.Name == "shared" && (!d.Shared || d.Owner != "") {
			t.Errorf("shared machine: %+v", d)
		}
		if d.Name == "mia-box" && (d.Shared || d.Owner != "mia") {
			t.Errorf("mia's machine: %+v", d)
		}
	}
}

// useAs says what happens when c asks for each way of using a machine.
func (m *team) spawnOn(c caller, name, device string) (int, string) {
	code, raw := m.do(c, "POST", "/v1/spawns", api.SpawnReq{Name: name, Kind: "claude", Device: device})
	return code, string(raw)
}

func TestSpawnOnlyOnMachinesYouOwn(t *testing.T) {
	m := newTeam(t)
	type row struct {
		who    string
		c      caller
		device string
		want   int
	}
	rows := []row{
		{"owner on a member's machine", m.olive, "mia-box", 200},
		{"owner on a shared machine", m.olive, "shared", 200},
		{"mia on her own", m.mia, "mia-box", 200},
		{"ben on his own", m.ben, "ben-box", 200},
		{"mia on ben's", m.mia, "ben-box", 403},
		{"ben on mia's", m.ben, "mia-box", 403},
		{"mia on a shared machine", m.mia, "shared", 403},
		{"mia on afif's", m.mia, "d1", 403},
		{"viewer on any", m.vera, "mia-box", 403},
		{"admin token on any", caller{m.admin, ""}, "mia-box", 403}, // the admin token never starts work
		{"ai app on own (no spawn call for AI apps)", m.miaApp, "mia-box", 403},
		{"agent naming a device", m.lead(), "mia-box", 403},
	}
	for i, r := range rows {
		code, body := m.spawnOn(r.c, fmt.Sprintf("a%d", i), r.device)
		if code != r.want {
			t.Errorf("%s: status %d, want %d: %s", r.who, code, r.want, body)
		}
	}
	// The refusal is in plain words and names the owner and the person's own machines.
	_, msg := m.spawnOn(m.mia, "x1", "ben-box")
	for _, want := range []string{"That machine belongs to ben.", "You can use your own machines: mia-box", "add one under Machines"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q: %s", want, msg)
		}
	}
	_, msg = m.spawnOn(m.mia, "x2", "shared")
	if !strings.Contains(msg, "shared by the owner") {
		t.Errorf("shared refusal: %s", msg)
	}
	_, msg = m.spawnOn(m.vera, "x3", "mia-box")
	if !strings.Contains(msg, "viewer") {
		t.Errorf("viewer refusal: %s", msg)
	}
	// Nothing was queued for the refused ones.
	var n int
	m.hub.db.QueryRow(`SELECT count(*) FROM spawn WHERE name IN ('a4','a5','a6','a7','a8','a9','a10','a11','x1','x2','x3')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d refused spawns were recorded", n)
	}
	// Without naming a machine, a member gets the only one they own; without any, a plain answer.
	var s api.Spawn
	m.ok(m.mia, "POST", "/v1/spawns", api.SpawnReq{Name: "implicit", Kind: "claude"}, &s)
	if s.Device != "mia-box" {
		t.Fatalf("implicit machine: %+v", s)
	}
	nobody := m.person("nina", "member")
	if msg := m.fail(400, nobody, "POST", "/v1/spawns", api.SpawnReq{Name: "nowhere", Kind: "claude"}); !strings.Contains(msg, "None of your machines is ready") {
		t.Fatalf("a member with no machine: %s", msg)
	}
	// The owner with several machines must still name one.
	m.fail(400, m.olive, "POST", "/v1/spawns", api.SpawnReq{Name: "which", Kind: "claude"})
	// A lead starts workers on its own machine: unchanged.
	m.ok(m.lead(), "POST", "/v1/spawns", api.SpawnReq{Name: "helper", Kind: "claude"}, nil)
}

func TestJobsOnlyOnMachinesYouOwn(t *testing.T) {
	m := newTeam(t)
	job := func(c caller, device string, extra func(*api.JobNewReq)) (int, string) {
		req := api.JobNewReq{Title: "work", Repo: "/srv/app", Device: device}
		if extra != nil {
			extra(&req)
		}
		code, raw := m.do(c, "POST", "/v1/jobs", req)
		return code, string(raw)
	}
	for who, r := range map[string]struct {
		c      caller
		device string
		want   int
	}{
		"owner anywhere":         {m.olive, "ben-box", 200},
		"owner shared":           {m.olive, "shared", 200},
		"mia own":                {m.mia, "mia-box", 200},
		"mia own, none named":    {m.mia, "", 200},
		"mia on ben's":           {m.mia, "ben-box", 403},
		"ben on mia's":           {m.ben, "mia-box", 403},
		"mia on shared":          {m.mia, "shared", 403},
		"viewer":                 {m.vera, "mia-box", 403},
		"ai app on its own":      {m.miaApp, "mia-box", 200},
		"ai app on another's":    {m.miaApp, "ben-box", 403},
		"ai app, none named":     {m.miaApp, "", 200},
		"agent (jobs are human)": {m.lead(), "d1", 403},
	} {
		if code, body := job(r.c, r.device, nil); code != r.want {
			t.Errorf("%s: status %d, want %d: %s", who, code, r.want, body)
		}
	}
	// A job's device is fixed at creation: --repo plus someone else's device is refused, as is a lead profile alone.
	if code, _ := job(m.ben, "mia-box", func(r *api.JobNewReq) { r.Repo = ""; r.LeadProfile = "lead" }); code != 403 {
		t.Errorf("a lead profile on someone else's machine: %d", code)
	}
	// Taking another person's agent as lead puts the job on their machine: refused. (afif's lead runs on d1.)
	if code, body := job(m.mia, "", func(r *api.JobNewReq) { r.Repo = ""; r.Lead = "lead" }); code != 403 {
		t.Errorf("someone else's agent as lead: %d %s", code, body)
	}
	if code, _ := job(m.olive, "", func(r *api.JobNewReq) { r.Repo = ""; r.Lead = "lead" }); code != 200 {
		t.Errorf("the owner may take any agent as lead: %d", code)
	}
	// A refused job leaves nothing behind.
	var n int
	m.hub.db.QueryRow(`SELECT count(*) FROM task WHERE kind = 'job' AND created_by IN ('human:ben', 'human:vera')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d jobs of refused people were recorded", n)
	}
	// An agent for a job that sits on someone's machine can be started only by those who may use it.
	var j api.Job
	m.ok(m.mia, "POST", "/v1/jobs", api.JobNewReq{Title: "mine", Repo: "/srv/mine", Device: "mia-box"}, &j)
	m.fail(403, m.ben, "POST", "/v1/spawns", api.SpawnReq{Name: "intruder", Kind: "claude", Job: j.ID})
	m.fail(403, m.ben, "POST", "/v1/spawns", api.SpawnReq{Name: "intruder", Kind: "claude", Job: j.ID, Device: "mia-box"})
	m.ok(m.mia, "POST", "/v1/spawns", api.SpawnReq{Name: "mine1", Kind: "claude", Job: j.ID}, nil)
	m.ok(m.olive, "POST", "/v1/spawns", api.SpawnReq{Name: "owner1", Kind: "claude", Job: j.ID}, nil)
	// An agent of another machine cannot be moved into the job by ben, and ben cannot resume it with one.
	m.fail(403, m.ben, "POST", fmt.Sprintf("/v1/jobs/%d/resume", j.ID), api.JobResumeReq{Lead: "worker2"})
	m.fail(403, m.ben, "POST", "/v1/agents/worker2/job", api.AgentJobReq{Job: j.ID})
}

func TestProjectsOnlyOnMachinesYouOwn(t *testing.T) {
	m := newTeam(t)
	m.ok(m.olive, "POST", "/v1/codebases", api.CodebaseReq{Name: "billing", Remote: "https://github.com/acme/billing"}, nil)
	clone := func(c caller, device string) (int, string) {
		code, raw := m.do(c, "POST", "/v1/codebases/billing/clone", api.CodebaseCloneReq{Device: device})
		return code, string(raw)
	}
	if code, body := clone(m.mia, "ben-box"); code != 403 || !strings.Contains(body, "belongs to ben") {
		t.Fatalf("clone onto someone else's machine: %d %s", code, body)
	}
	if code, _ := clone(m.mia, "shared"); code != 403 {
		t.Fatalf("clone onto a shared machine: %d", code)
	}
	if code, _ := clone(m.vera, "mia-box"); code != 403 {
		t.Fatalf("a viewer's clone: %d", code)
	}
	if code, body := clone(m.mia, "mia-box"); code != 200 {
		t.Fatalf("clone onto her own: %d %s", code, body)
	}
	if code, body := clone(m.ben, ""); code != 200 {
		t.Fatalf("clone onto his only machine: %d %s", code, body)
	}
	if code, body := clone(m.olive, "shared"); code != 200 {
		t.Fatalf("the owner's clone: %d %s", code, body)
	}
	// Creating a repository names a machine too.
	create := func(c caller, name, device string) int {
		code, _ := m.do(c, "POST", "/v1/codebases/create", api.CodebaseCreateReq{Name: name, Device: device})
		return code
	}
	if code := create(m.ben, "fresh1", "mia-box"); code != 403 {
		t.Errorf("create on someone else's machine: %d", code)
	}
	if code := create(m.ben, "fresh2", "ben-box"); code != 200 {
		t.Errorf("create on his own: %d", code)
	}
	var n int
	m.hub.db.QueryRow(`SELECT count(*) FROM codebase WHERE name = 'fresh1'`).Scan(&n)
	if n != 0 {
		t.Error("a refused create left a project behind")
	}
	// Each person sees the copies on their own machines only; the owner sees all.
	seen := func(c caller) []string {
		var cb api.Codebase
		m.ok(c, "GET", "/v1/codebases/billing", nil, &cb)
		var out []string
		for _, k := range cb.Clones {
			out = append(out, k.Device)
		}
		return out
	}
	if got := seen(m.olive); !sameSet(got, "mia-box", "ben-box", "shared") {
		t.Errorf("owner sees clones on %v", got)
	}
	if got := seen(m.mia); !sameSet(got, "mia-box") {
		t.Errorf("mia sees clones on %v", got)
	}
	if got := seen(m.ben); !sameSet(got, "ben-box") {
		t.Errorf("ben sees clones on %v", got)
	}
	if got := seen(m.vera); len(got) != 0 {
		t.Errorf("a viewer sees clones on %v", got)
	}
	if got := seen(m.lead()); len(got) != 3 { // an agent's path is unchanged
		t.Errorf("an agent sees clones on %v", got)
	}
}

func TestAnAgentsScreenIsReadOnlyThroughItsMachinesOwner(t *testing.T) {
	m := newTeam(t)
	m.ok(caller{m.benBox, ""}, "POST", "/v1/agents", api.RegisterReq{Name: "bens", Kind: "claude", WakeTarget: "tmux:/tmp/s:%1"}, nil)
	if msg := m.fail(403, m.mia, "GET", "/v1/agents/bens/screen", nil); !strings.Contains(msg, "belongs to ben") {
		t.Fatalf("mia reads ben's screen: %s", msg)
	}
	m.fail(403, m.miaApp, "GET", "/v1/agents/bens/screen", nil)
	m.ok(m.ben, "GET", "/v1/agents/bens/screen", nil, nil)
	m.ok(m.olive, "GET", "/v1/agents/bens/screen", nil, nil)
}

func TestRevokeOnlyYourOwnMachine(t *testing.T) {
	m := newTeam(t)
	m.fail(403, m.mia, "POST", "/v1/devices/ben-box/revoke", nil)
	m.fail(403, m.mia, "POST", "/v1/devices/shared/revoke", nil)
	m.fail(403, m.vera, "POST", "/v1/devices/mia-box/revoke", nil)
	m.fail(403, m.miaApp, "POST", "/v1/devices/mia-box/revoke", nil)
	m.fail(403, m.lead(), "POST", "/v1/devices/mia-box/revoke", nil)
	m.fail(404, m.mia, "POST", "/v1/devices/nothing/revoke", nil)
	if got := m.names(m.olive); len(got) != 5 {
		t.Fatalf("a refused revoke changed something: %v", got)
	}
	m.ok(m.mia, "POST", "/v1/devices/mia-box/revoke", nil, nil)
	m.fail(404, m.mia, "POST", "/v1/devices/mia-box/revoke", nil) // already revoked
	// The revoked machine's credential stops working at once.
	if code, _ := m.do(caller{m.miaBox, ""}, "GET", "/v1/device/spawns", nil); code != 401 {
		t.Fatalf("a revoked machine still works: %d", code)
	}
	// The owner revokes anyone's, and the admin token too (the older path as well).
	m.ok(m.olive, "POST", "/v1/devices/ben-box/revoke", nil, nil)
	m.ok(caller{m.admin, ""}, "POST", "/v1/admin/devices/shared/revoke", nil, nil)
	if !m.audited("device.revoke", "device:mia-box") || !m.audited("device.revoke", "device:ben-box") {
		t.Fatal("device.revoke is not in the audit log")
	}
}

func TestOnlyTheOwnerHandsAMachineToAPerson(t *testing.T) {
	m := newTeam(t)
	assign := func(c caller, device, to string) int {
		code, _ := m.do(c, "POST", "/v1/devices/"+device+"/assign", api.DeviceAssignReq{To: to})
		return code
	}
	for name, c := range map[string]caller{"member": m.mia, "viewer": m.vera, "ai app": m.miaApp, "agent": m.lead()} {
		if code := assign(c, "shared", "mia"); code != 403 {
			t.Errorf("%s handed a machine over: %d", name, code)
		}
		if code := assign(c, "mia-box", "ben"); code != 403 {
			t.Errorf("%s handed their own machine over: %d", name, code)
		}
	}
	if got := m.names(m.mia); !sameSet(got, "mia-box") {
		t.Fatalf("a refused hand-over changed mia's machines: %v", got)
	}
	// The shared machine reaches mia, and she can use it from then on, and ben no longer could.
	if code, _ := m.spawnOn(m.mia, "before", "shared"); code != 403 {
		t.Fatalf("before the hand-over: %d", code)
	}
	if code := assign(m.olive, "shared", "mia"); code != 200 {
		t.Fatalf("owner hands over: %d", code)
	}
	if got := m.names(m.mia); !sameSet(got, "mia-box", "shared") {
		t.Fatalf("after: %v", got)
	}
	if code, _ := m.spawnOn(m.mia, "after", "shared"); code != 200 {
		t.Fatalf("after the hand-over: %d", code)
	}
	if code, _ := m.spawnOn(m.ben, "ben-after", "shared"); code != 403 {
		t.Fatalf("ben on the handed-over machine: %d", code)
	}
	// The admin token may do it too; a viewer, an unknown person and a revoked machine are refused in words.
	if code := assign(caller{m.admin, ""}, "shared", "ben"); code != 200 {
		t.Fatalf("admin hands over: %d", code)
	}
	m.fail(400, m.olive, "POST", "/v1/devices/shared/assign", api.DeviceAssignReq{To: "vera"})
	m.fail(400, m.olive, "POST", "/v1/devices/shared/assign", api.DeviceAssignReq{})
	m.fail(404, m.olive, "POST", "/v1/devices/shared/assign", api.DeviceAssignReq{To: "nobody"})
	m.fail(404, m.olive, "POST", "/v1/devices/nothing/assign", api.DeviceAssignReq{To: "mia"})
	m.ok(m.olive, "POST", "/v1/devices/ben-box/revoke", nil, nil)
	m.fail(404, m.olive, "POST", "/v1/devices/ben-box/assign", api.DeviceAssignReq{To: "mia"})
	var rows []api.AuditRow
	m.ok(caller{m.admin, ""}, "GET", "/v1/audit?limit=500", nil, &rows)
	hands := 0
	for _, r := range rows {
		if r.Action == "device.assign" && r.Target == "device:shared" {
			hands++
		}
	}
	if hands != 2 {
		t.Fatalf("device.assign audit rows: %d", hands)
	}
}

// A machine made before this rule (or by the admin token) has no owner: only owners use it, which is
// the single-owner reality of the hubs that exist today.
func TestAMachineWithNoOwnerIsTheOwnersAlone(t *testing.T) {
	m := newTeam(t)
	var n int
	m.hub.db.QueryRow(`SELECT count(*) FROM device WHERE name = 'shared' AND owner_id IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatal("the admin token's machine has an owner")
	}
	for name, c := range map[string]caller{"member": m.mia, "other member": m.ben} {
		if code, _ := m.spawnOn(c, "np-"+strings.ReplaceAll(name, " ", ""), "shared"); code != 403 {
			t.Errorf("%s used the shared machine: %d", name, code)
		}
	}
	if code, _ := m.spawnOn(m.olive, "np-owner", "shared"); code != 200 {
		t.Errorf("the owner could not use the shared machine: %d", code)
	}
}

func TestTheMachinesToolOfAnAIAppShowsItsPersonsMachines(t *testing.T) {
	m := newTeam(t)
	var ds []api.Device
	m.ok(m.miaApp, "GET", "/v1/devices", nil, &ds)
	if len(ds) != 1 || ds[0].Name != "mia-box" || ds[0].Owner != "mia" {
		t.Fatalf("%+v", ds)
	}
	// but it cannot change anything about machines
	m.fail(403, m.miaApp, "POST", "/v1/devices/mia-box/revoke", nil)
	m.fail(403, m.miaApp, "POST", "/v1/devices/mia-box/assign", api.DeviceAssignReq{To: "ben"})
	m.fail(403, m.miaApp, "GET", "/v1/admin/devices", nil)
}
