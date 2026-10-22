package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/profile"
)

// notesManifest is an add-on with one tool that stays on the computer and one that does not. v is a
// suffix on the summary so a test can make a second version with another manifest hash.
func notesManifest(v string) string {
	return `schema: 1
kind: tool
name: notes
version: 0.1.0
api: 1
title: Notes
summary: Keeps notes` + v + `.
run: ["python3", "service.py"]
hosts: []
secrets: []
tools:
  - name: search
    summary: Search the notes.
    input:
      query: {type: string, required: true, summary: What to look for}
      limit: {type: number, summary: How many to return}
    leaves_machine: false
    timeout: 20s
    max_output: 2MB
  - name: publish
    summary: Send a note to a shared place.
    input:
      text: {type: string, required: true}
    leaves_machine: true
    timeout: 1m
    max_output: 10KB
`
}

func manifestHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// reportNotes has a computer report the notes add-on with the given manifest text and status.
func (e *env) reportNotes(device, manifest, status string) {
	e.t.Helper()
	it := api.AddonItem{Name: "notes", Version: "0.1.0", PackageHash: hashA, ManifestHash: manifestHash(manifest), Manifest: manifest, Status: status}
	e.ok(caller{device, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{it}}, nil)
}

func (e *env) grant(c caller, profileName, addonName, tool string) api.AddonGrant {
	e.t.Helper()
	var g api.AddonGrant
	e.ok(c, "POST", "/v1/profiles/"+profileName+"/addon-grants", api.AddonGrantReq{Addon: addonName, Tool: tool}, &g)
	return g
}

// grantEnv is a hub whose "afif" is the owner, with one profile, and d1 reporting the notes add-on.
func grantEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.hub.db.Exec(`UPDATE human SET role = 'owner' WHERE name = 'afif'`)
	e.newProfile(e.afif(), "reader", researcher())
	e.reportNotes(e.d1, notesManifest(""), "ready")
	return e
}

// pendingSpawn starts a spawn of the profile on d1 (in job when it is not 0) and returns what d1's link is given.
func (e *env) pendingSpawn(name, prof string, job int64) api.Spawn {
	e.t.Helper()
	e.spawn(e.afif(), api.SpawnReq{Name: name, Profile: prof, Device: "d1", Job: job})
	var sp []api.Spawn
	e.ok(caller{e.d1, ""}, "GET", "/v1/device/spawns", nil, &sp)
	for _, s := range sp {
		if s.Name == name {
			return s
		}
	}
	e.t.Fatalf("the link was not given %s: %+v", name, sp)
	return api.Spawn{}
}

func unavailable(s api.Spawn, addonName, tool string) *api.ToolUnavailable {
	for i, u := range s.ToolsUnavailable {
		if u.Addon == addonName && u.Tool == tool {
			return &s.ToolsUnavailable[i]
		}
	}
	return nil
}

func TestAddonGrantsAreForTheOwnerOnly(t *testing.T) {
	e := grantEnv(t)
	mia := e.person("mia", "member")
	vera := e.person("vera", "viewer")
	var op api.TokenResp
	e.ok(mia, "POST", "/v1/me/operator-token", nil, &op)
	body := api.AddonGrantReq{Addon: "notes", Tool: "search"}
	for name, c := range map[string]caller{"member": mia, "viewer": vera, "lead": e.lead(), "worker": e.worker(), "observer": e.observer(),
		"admin": {e.admin, ""}, "an AI app": {op.Token, ""}} {
		if status, raw := e.do(c, "POST", "/v1/profiles/reader/addon-grants", body); status != 403 {
			t.Errorf("%s granted: %d %s", name, status, raw)
		}
		if status, _ := e.do(c, "DELETE", "/v1/profiles/reader/addon-grants/notes/search", nil); status != 403 {
			t.Errorf("%s revoked: %d", name, status)
		}
		if status, _ := e.do(c, "GET", "/v1/addon-grants", nil); status != 403 {
			t.Errorf("%s listed grants: %d", name, status)
		}
	}
	var none []api.AddonGrant
	e.ok(e.afif(), "GET", "/v1/addon-grants", nil, &none)
	if len(none) != 0 {
		t.Fatalf("a refused request left a grant: %+v", none)
	}
	g := e.grant(e.afif(), "reader", "notes", "search")
	if g.Profile != "reader" || g.ManifestHash != manifestHash(notesManifest("")) || !g.LeavesMachine || g.MaxCalls != 50 || g.GrantedBy != "human:afif" || g.GrantedAt.IsZero() {
		t.Fatalf("the grant, with its defaults: %+v", g)
	}
	// A refused attempt is in the audit log like any other.
	if !e.audited("denied", "") {
		t.Fatal("the refused grants are not in the audit log")
	}
}

func TestAGrantNeedsAKnownAddonAndItsTool(t *testing.T) {
	e := grantEnv(t)
	own := e.afif()
	post := func(profileName string, req api.AddonGrantReq) (int, string) {
		status, raw := e.do(own, "POST", "/v1/profiles/"+profileName+"/addon-grants", req)
		return status, string(raw)
	}
	if s, msg := post("reader", api.AddonGrantReq{Addon: "ghost", Tool: "search"}); s != 404 || !strings.Contains(msg, "install it on a computer first") {
		t.Errorf("unknown add-on: %d %s", s, msg)
	}
	if s, msg := post("reader", api.AddonGrantReq{Addon: "notes", Tool: "delete"}); s != 400 || !strings.Contains(msg, "no tool called delete") || !strings.Contains(msg, "publish, search") {
		t.Errorf("wrong tool: %d %s", s, msg)
	}
	if s, _ := post("nobody", api.AddonGrantReq{Addon: "notes", Tool: "search"}); s != 404 {
		t.Errorf("unknown profile: %d", s)
	}
	if s, _ := post("reader", api.AddonGrantReq{Addon: "Not An Addon", Tool: "search"}); s != 400 {
		t.Errorf("bad add-on name: %d", s)
	}
	if s, _ := post("reader", api.AddonGrantReq{Addon: "notes", Tool: "search", MaxCalls: -1}); s != 400 {
		t.Errorf("negative limit: %d", s)
	}
	if s, _ := post("reader", api.AddonGrantReq{Addon: "notes", Tool: "search", MaxCalls: 100000}); s != 400 {
		t.Errorf("huge limit: %d", s)
	}
	// An add-on that changed since it was approved cannot be granted: nobody approved what it is now.
	e.reportNotes(e.d1, notesManifest(" again"), "changed")
	if s, msg := post("reader", api.AddonGrantReq{Addon: "notes", Tool: "search"}); s != 409 || !strings.Contains(msg, "approve it on the computer") {
		t.Errorf("changed add-on: %d %s", s, msg)
	}
	var none []api.AddonGrant
	e.ok(own, "GET", "/v1/addon-grants", nil, &none)
	if len(none) != 0 {
		t.Fatalf("refused grants were stored: %+v", none)
	}

	// Two computers with two versions: the owner says which one the grant is for.
	e.reportNotes(e.d1, notesManifest(""), "ready")
	e.reportNotes(e.d2, notesManifest(" two"), "ready")
	if s, msg := post("reader", api.AddonGrantReq{Addon: "notes", Tool: "search"}); s != 409 || !strings.Contains(msg, "name the computer") {
		t.Errorf("two versions: %d %s", s, msg)
	}
	no := false
	var g api.AddonGrant
	e.ok(own, "POST", "/v1/profiles/reader/addon-grants", api.AddonGrantReq{Addon: "notes", Tool: "search", Computer: "d2", LeavesMachine: &no, MaxCalls: 7}, &g)
	if g.ManifestHash != manifestHash(notesManifest(" two")) || g.LeavesMachine || g.MaxCalls != 7 {
		t.Fatalf("grant for d2: %+v", g)
	}
	if s, _ := post("reader", api.AddonGrantReq{Addon: "notes", Tool: "search", Computer: "nowhere"}); s != 404 {
		t.Errorf("unknown computer: %d", s)
	}
}

func TestRegrantAfterAManifestChangeAndRevoke(t *testing.T) {
	e := grantEnv(t)
	e.grant(e.afif(), "reader", "notes", "search")
	s := e.pendingSpawn("a", "reader", 0)
	if len(s.Tools) != 1 || len(s.ToolsUnavailable) != 0 {
		t.Fatalf("before the change: %+v", s)
	}
	// The author ships a new version; the computer approves it. The old grant stops working there.
	e.reportNotes(e.d1, notesManifest(" v2"), "ready")
	s = e.pendingSpawn("b", "reader", 0)
	u := unavailable(s, "notes", "search")
	if len(s.Tools) != 0 || u == nil || u.Cause != "changed" || !strings.Contains(u.Action, "grant the tool again") {
		t.Fatalf("after the change: %+v", s)
	}
	// Granting again, with the new manifest, brings it back, and replaces the row (one grant, not two).
	g := e.grant(e.afif(), "reader", "notes", "search")
	if g.ManifestHash != manifestHash(notesManifest(" v2")) {
		t.Fatalf("regrant: %+v", g)
	}
	s = e.pendingSpawn("c", "reader", 0)
	if len(s.Tools) != 1 || len(s.ToolsUnavailable) != 0 {
		t.Fatalf("after the regrant: %+v", s)
	}
	var all []api.AddonGrant
	e.ok(e.afif(), "GET", "/v1/addon-grants?profile=reader", nil, &all)
	if len(all) != 1 {
		t.Fatalf("grants: %+v", all)
	}
	e.ok(e.afif(), "GET", "/v1/addon-grants?profile=other", nil, &all)
	if len(all) != 0 {
		t.Fatalf("another profile's grants: %+v", all)
	}

	e.ok(e.afif(), "DELETE", "/v1/profiles/reader/addon-grants/notes/search", nil, nil)
	e.fail(404, e.afif(), "DELETE", "/v1/profiles/reader/addon-grants/notes/search", nil)
	s = e.pendingSpawn("d", "reader", 0)
	if len(s.Tools) != 0 || len(s.ToolsUnavailable) != 0 {
		t.Fatalf("after the revoke: %+v", s)
	}
	var grants, revokes int
	for _, r := range e.audit() {
		switch r.Action {
		case "addon.grant":
			grants++
			if r.Target != "profile:reader" || !strings.Contains(string(r.Payload), `"tool":"search"`) {
				t.Errorf("grant audit: %+v %s", r, r.Payload)
			}
		case "addon.revoke":
			revokes++
		}
	}
	if grants != 2 || revokes != 1 {
		t.Fatalf("audit: %d grants, %d revokes", grants, revokes)
	}
}

func TestTheSnapshotInTheSpawn(t *testing.T) {
	e := grantEnv(t)
	no, yes := false, true
	e.ok(e.afif(), "POST", "/v1/profiles/reader/addon-grants", api.AddonGrantReq{Addon: "notes", Tool: "search", LeavesMachine: &no, MaxCalls: 9}, nil)
	e.ok(e.afif(), "POST", "/v1/profiles/reader/addon-grants", api.AddonGrantReq{Addon: "notes", Tool: "publish", LeavesMachine: &yes}, nil)

	s := e.pendingSpawn("a", "reader", 0)
	if len(s.ToolsUnavailable) != 0 || len(s.Tools) != 2 || s.Confidential {
		t.Fatalf("snapshot: %+v", s)
	}
	pub, search := s.Tools[0], s.Tools[1]
	if pub.Tool != "publish" || search.Tool != "search" {
		t.Fatalf("order: %+v", s.Tools)
	}
	if search.Addon != "notes" || search.Summary != "Search the notes." || search.LeavesMachine || search.MaxCalls != 9 ||
		search.TimeoutSeconds != 20 || search.MaxOutput != 2<<20 {
		t.Fatalf("search: %+v", search)
	}
	if len(search.Input) != 2 || search.Input[0] != (api.ToolField{Name: "limit", Type: "number", Summary: "How many to return"}) ||
		search.Input[1] != (api.ToolField{Name: "query", Type: "string", Required: true, Summary: "What to look for"}) {
		t.Fatalf("search input: %+v", search.Input)
	}
	if !pub.LeavesMachine || pub.MaxCalls != 50 || pub.TimeoutSeconds != 60 || pub.MaxOutput != 10<<10 {
		t.Fatalf("publish: %+v", pub)
	}

	// The owner's value wins over the add-on's: search declares that it stays, and the owner says it leaves.
	e.ok(e.afif(), "POST", "/v1/profiles/reader/addon-grants", api.AddonGrantReq{Addon: "notes", Tool: "search", LeavesMachine: &yes}, nil)
	if s = e.pendingSpawn("b", "reader", 0); !s.Tools[1].LeavesMachine {
		t.Fatalf("the owner's value is not used: %+v", s.Tools)
	}

	// A spawn with no profile gets nothing, and another computer that has not got the add-on says so.
	plain := e.pendingSpawn("plain", "", 0)
	if len(plain.Tools) != 0 || len(plain.ToolsUnavailable) != 0 {
		t.Fatalf("no profile: %+v", plain)
	}
	e.spawn(e.afif(), api.SpawnReq{Name: "far", Profile: "reader", Device: "d2"})
	var far []api.Spawn
	e.ok(caller{e.d2, ""}, "GET", "/v1/device/spawns", nil, &far)
	if len(far) != 1 || len(far[0].Tools) != 0 || len(far[0].ToolsUnavailable) != 2 {
		t.Fatalf("another computer: %+v", far)
	}
	u := far[0].ToolsUnavailable[0]
	if u.Cause != "not-installed" || u.Action != "Install the add-on on this computer." || u.Why == "" {
		t.Fatalf("not installed: %+v", u)
	}
}

func TestEveryReasonAToolIsUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		status string
		text   string // what the computer reports; "" leaves the add-on out
		cause  string
		action string
	}{
		{"missing", "missing", "", "not-installed", "Install the add-on"},
		{"changed since approved", "changed", notesManifest(""), "changed", "Approve the changed add-on"},
		{"another manifest", "ready", notesManifest(" new"), "changed", "grant the tool again"},
		{"down", "down", notesManifest(""), "plugin-down", "handloom addon doctor"},
		{"manifest not reported", "ready", "", "changed", "grant the tool again"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := grantEnv(t)
			e.grant(e.afif(), "reader", "notes", "search")
			it := api.AddonItem{Name: "notes", Version: "0.1.0", PackageHash: hashA, ManifestHash: manifestHash(tc.text), Manifest: tc.text, Status: tc.status}
			if tc.text == "" {
				it.ManifestHash = manifestHash(notesManifest(""))
			}
			e.ok(caller{e.d1, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{it}}, nil)
			s := e.pendingSpawn("a", "reader", 0)
			u := unavailable(s, "notes", "search")
			if len(s.Tools) != 0 || u == nil || u.Cause != tc.cause || !strings.Contains(u.Action, tc.action) || u.Why == "" {
				t.Fatalf("%+v", s)
			}
		})
	}
	t.Run("written for another add-on API", func(t *testing.T) {
		e := grantEnv(t)
		e.grant(e.afif(), "reader", "notes", "search")
		it := api.AddonItem{Name: "notes", PackageHash: hashA, Status: "down", Detail: "this add-on was written for add-on API 2; this program speaks API 1"}
		e.ok(caller{e.d1, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{it}}, nil)
		s := e.pendingSpawn("a", "reader", 0)
		if u := unavailable(s, "notes", "search"); len(s.Tools) != 0 || u == nil || u.Cause != "plugin-down" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("the add-on is gone", func(t *testing.T) {
		e := grantEnv(t)
		e.grant(e.afif(), "reader", "notes", "search")
		e.ok(caller{e.d1, ""}, "POST", "/v1/device/addons", api.AddonReport{}, nil)
		if u := unavailable(e.pendingSpawn("a", "reader", 0), "notes", "search"); u == nil || u.Cause != "not-installed" {
			t.Fatalf("%+v", u)
		}
	})
}

func TestAConfidentialJobGetsNoToolThatLeavesTheComputer(t *testing.T) {
	e := grantEnv(t)
	e.hub.opt.AllowConfidential = true
	no := false
	e.ok(e.afif(), "POST", "/v1/profiles/reader/addon-grants", api.AddonGrantReq{Addon: "notes", Tool: "search", LeavesMachine: &no}, nil)
	e.grant(e.afif(), "reader", "notes", "publish")
	local := researcher()
	local.Runtime = profile.Local
	local.Model = "local-server/model-name"
	e.newProfile(e.afif(), "local-reader", local)
	e.ok(e.afif(), "POST", "/v1/profiles/local-reader/addon-grants", api.AddonGrantReq{Addon: "notes", Tool: "search", LeavesMachine: &no}, nil)
	e.grant(e.afif(), "local-reader", "notes", "publish")

	var job api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "Private", Confidential: true}, &job)
	s := e.pendingSpawn("a", "local-reader", job.ID)
	if !s.Confidential {
		t.Fatalf("the job's flag is not in the snapshot: %+v", s)
	}
	if len(s.Tools) != 1 || s.Tools[0].Tool != "search" || s.Tools[0].LeavesMachine {
		t.Fatalf("a confidential job's tools: %+v", s.Tools)
	}
	u := unavailable(s, "notes", "publish")
	if len(s.ToolsUnavailable) != 1 || u == nil || u.Cause != "denied-confidential" || !strings.Contains(u.Action, "stays on the computer") {
		t.Fatalf("unavailable: %+v", s.ToolsUnavailable)
	}

	// The same grants for an ordinary job give both tools, and the flag is off.
	open := e.newJob("Open", "")
	if s2 := e.pendingSpawn("b", "local-reader", open.ID); s2.Confidential || len(s2.Tools) != 2 {
		t.Fatalf("an ordinary job: %+v", s2)
	}

	// A confidential job's unavailable tool is not hidden by a computer that lacks the add-on: the add-on being
	// absent is the first thing to put right.
	e.ok(caller{e.d1, ""}, "POST", "/v1/device/addons", api.AddonReport{}, nil)
	s3 := e.pendingSpawn("c", "local-reader", job.ID)
	for _, u := range s3.ToolsUnavailable {
		if u.Cause != "not-installed" {
			t.Fatalf("%+v", s3.ToolsUnavailable)
		}
	}
}

func TestWhatAJobWasStartedWithout(t *testing.T) {
	e := grantEnv(t)
	e.hub.opt.AllowConfidential = true
	e.grant(e.afif(), "reader", "notes", "publish")
	e.grant(e.afif(), "reader", "notes", "search")
	open := e.newJob("Open", "")
	e.pendingSpawn("a", "reader", open.ID)

	c := &call{h: e.hub, now: time.Now()}
	un := func(job int64) []api.ToolUnavailable {
		t.Helper()
		tx, err := e.hub.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		c.tx = tx
		out, err := c.jobToolsUnavailable(job)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := un(open.ID); len(got) != 0 {
		t.Fatalf("a job with everything: %+v", got)
	}
	// The add-on stops: the job notice names the tools with the cause and the action.
	e.reportNotes(e.d1, notesManifest(""), "down")
	got := un(open.ID)
	if len(got) != 2 || got[0].Cause != "plugin-down" || got[0].Action == "" || got[0].Why == "" {
		t.Fatalf("down: %+v", got)
	}
	// A job with no helper from a profile has nothing to report; a failed start is not counted.
	other := e.newJob("Other", "")
	if got := un(other.ID); len(got) != 0 {
		t.Fatalf("no helpers: %+v", got)
	}
	s := e.spawn(e.afif(), api.SpawnReq{Name: "late", Profile: "reader", Device: "d1", Job: other.ID})
	e.report(e.d1, s.ID, api.SpawnReport{Status: "failed", Error: "no terminal"})
	if got := un(other.ID); len(got) != 0 {
		t.Fatalf("a failed start: %+v", got)
	}
}

// ---- the call journal ----

func callLine(id, phase string, s api.Spawn) api.AddonCall {
	e := api.AddonCall{CallID: id, Spawn: s.ID, Agent: s.Name, Addon: "notes", Tool: "search", Phase: phase, Time: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	if s.Job != nil {
		e.Job = *s.Job
	}
	if phase == "result" {
		e.Code, e.Size, e.Millis = "ok", 1200, 340
	}
	return e
}

func (e *env) addonAudit() []api.AuditRow {
	var out []api.AuditRow
	for _, r := range e.audit() {
		if strings.HasPrefix(r.Action, "addon.notes.") {
			out = append(out, r)
		}
	}
	return out
}

func TestACallBatchIsStoredOnceUnderTheAddonsOwnName(t *testing.T) {
	e := grantEnv(t)
	job := e.newJob("A job", "")
	s := e.pendingSpawn("a", "reader", job.ID)
	d1 := caller{e.d1, ""}
	denied := callLine("c2", "denied", s)
	denied.Code = "denied-budget"
	batch := api.AddonCallReport{Calls: []api.AddonCall{callLine("c1", "call", s), callLine("c1", "result", s), denied}}
	e.ok(d1, "POST", "/v1/device/addon-calls", batch, nil)
	rows := e.addonAudit()
	if len(rows) != 3 || rows[0].Action != "addon.notes.call" || rows[1].Action != "addon.notes.result" || rows[2].Action != "addon.notes.denied" {
		t.Fatalf("rows: %+v", rows)
	}
	if want := fmt.Sprintf("job:%d", job.ID); rows[0].Target != want {
		t.Fatalf("target %q, want %q", rows[0].Target, want)
	}
	p := string(rows[1].Payload)
	for _, want := range []string{`"call":"c1"`, `"agent":"a"`, `"tool":"search"`, `"code":"ok"`, `"size":1200`, `"millis":340`} {
		if !strings.Contains(p, want) {
			t.Errorf("payload %s lacks %s", p, want)
		}
	}
	// The same batch again, and a batch that is half new: only the new line is stored.
	e.ok(d1, "POST", "/v1/device/addon-calls", batch, nil)
	again := api.AddonCallReport{Calls: append([]api.AddonCall{callLine("c3", "call", s)}, batch.Calls...)}
	e.ok(d1, "POST", "/v1/device/addon-calls", again, nil)
	if rows = e.addonAudit(); len(rows) != 4 {
		t.Fatalf("a resent batch was stored again: %d rows", len(rows))
	}
	// The same line twice in one batch is stored once.
	e.ok(d1, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{callLine("c4", "call", s), callLine("c4", "call", s)}}, nil)
	if rows = e.addonAudit(); len(rows) != 5 {
		t.Fatalf("a line twice in a batch: %d rows", len(rows))
	}
	// Two spawns on one computer may use the same call id; both are kept.
	s2 := e.pendingSpawn("b", "reader", job.ID)
	e.ok(d1, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{callLine("c1", "call", s2)}}, nil)
	if rows = e.addonAudit(); len(rows) != 6 {
		t.Fatalf("a call id reused by another spawn was dropped: %d rows", len(rows))
	}
	e.ok(d1, "POST", "/v1/device/addon-calls", api.AddonCallReport{}, nil)
}

func TestACallBatchIsRefusedWhole(t *testing.T) {
	e := grantEnv(t)
	s := e.pendingSpawn("a", "reader", 0)
	far := e.spawn(e.afif(), api.SpawnReq{Name: "far", Profile: "reader", Device: "d2"})
	d1 := caller{e.d1, ""}
	good := callLine("g1", "call", s)
	line := func(f func(*api.AddonCall)) api.AddonCall {
		l := callLine("x1", "result", s)
		f(&l)
		return l
	}
	long := strings.Repeat("x", 65)
	bad := map[string]api.AddonCall{
		"a call id with a space":       line(func(l *api.AddonCall) { l.CallID = "two words" }),
		"a call id too long":           line(func(l *api.AddonCall) { l.CallID = long }),
		"no call id":                   line(func(l *api.AddonCall) { l.CallID = "" }),
		"a spawn that does not exist":  line(func(l *api.AddonCall) { l.Spawn = 999 }),
		"another computer's spawn":     line(func(l *api.AddonCall) { l.Spawn, l.Agent = far.ID, "far" }),
		"another agent's name":         line(func(l *api.AddonCall) { l.Agent = "someone" }),
		"a job the spawn is not in":    line(func(l *api.AddonCall) { l.Job = 5 }),
		"an add-on name with a dot":    line(func(l *api.AddonCall) { l.Addon = "a.b" }),
		"an add-on name in capitals":   line(func(l *api.AddonCall) { l.Addon = "Notes" }),
		"a long add-on name":           line(func(l *api.AddonCall) { l.Addon = strings.Repeat("a", 33) }),
		"a tool name with text":        line(func(l *api.AddonCall) { l.Tool = "search for the acme contract" }),
		"a phase that is not one":      line(func(l *api.AddonCall) { l.Phase = "query" }),
		"a code that is text":          line(func(l *api.AddonCall) { l.Code = "the address did not answer" }),
		"a long code":                  line(func(l *api.AddonCall) { l.Code = long }),
		"a result with no code":        line(func(l *api.AddonCall) { l.Code = "" }),
		"a call with a code":           line(func(l *api.AddonCall) { l.Phase, l.Code = "call", "ok" }),
		"a denied call that is ok":     line(func(l *api.AddonCall) { l.Phase, l.Code = "denied", "ok" }),
		"a negative size":              line(func(l *api.AddonCall) { l.Size = -1 }),
		"a size beyond any output cap": line(func(l *api.AddonCall) { l.Size = 1 << 40 }),
		"a call lasting for days":      line(func(l *api.AddonCall) { l.Millis = 1 << 40 }),
		"no time":                      line(func(l *api.AddonCall) { l.Time = time.Time{} }),
		"a time far ahead":             line(func(l *api.AddonCall) { l.Time = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) }),
	}
	for name, l := range bad {
		if status, raw := e.do(d1, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{good, l}}); status != 400 && status != 403 {
			t.Errorf("%s: status %d: %s", name, status, raw)
		}
	}
	// Query text and results have nowhere to go: a field the report does not have is refused, in a line or beside it.
	extra := []string{
		`{"calls":[{"call_id":"q1","spawn":%d,"agent":"a","addon":"notes","tool":"search","phase":"call","time":"2026-10-03T12:00:00Z","query":"acme contract terms"}]}`,
		`{"calls":[{"call_id":"q1","spawn":%d,"agent":"a","addon":"notes","tool":"search","phase":"result","code":"ok","time":"2026-10-03T12:00:00Z","text":"the answer"}]}`,
		`{"calls":[{"call_id":"q1","spawn":%d,"agent":"a","addon":"notes","tool":"search","phase":"call","time":"2026-10-03T12:00:00Z","args":{"query":"x"}}]}`,
		`{"query":"acme","calls":[{"call_id":"q1","spawn":%d,"agent":"a","addon":"notes","tool":"search","phase":"call","time":"2026-10-03T12:00:00Z"}]}`,
	}
	for _, body := range extra {
		e.fail(400, d1, "POST", "/v1/device/addon-calls", fmt.Sprintf(body, s.ID))
	}
	if many := make([]api.AddonCall, maxCallBatch+1); true {
		for i := range many {
			many[i] = callLine(fmt.Sprintf("m%d", i), "call", s)
		}
		e.fail(400, d1, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: many})
	}
	if rows := e.addonAudit(); len(rows) != 0 {
		t.Fatalf("a refused batch stored %d rows", len(rows))
	}
	// Refused lines did not use up their call ids either: the good line still goes in.
	e.ok(d1, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{good}}, nil)
	if rows := e.addonAudit(); len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	// Only a computer's link reports.
	for name, c := range map[string]caller{"a person": e.afif(), "the admin": {e.admin, ""}, "nobody": {}} {
		if status, _ := e.do(c, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{good}}); status != 403 && status != 401 {
			t.Errorf("%s reported calls: %d", name, status)
		}
	}
}

func TestAnAddonCanNeverTakeABuiltInAuditName(t *testing.T) {
	for _, name := range []string{"grant", "revoke", "manage", "notes", "a-b"} {
		for phase := range callPhases {
			a := addonCallAction(name, phase)
			if a == "addon.grant" || a == "addon.revoke" || strings.Count(a, ".") != 2 || !strings.HasPrefix(a, "addon."+name+".") {
				t.Errorf("%s %s gives %q", name, phase, a)
			}
		}
	}
	// An add-on called grant is a valid name: its lines are addon.grant.call, which no built-in action is.
	e := grantEnv(t)
	s := e.pendingSpawn("a", "reader", 0)
	l := callLine("k1", "call", s)
	l.Addon = "grant"
	e.ok(caller{e.d1, ""}, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{l}}, nil)
	if !e.audited("addon.grant.call", "") || e.audited("addon.grant", "") {
		t.Fatal("an add-on called grant is not under its own prefix")
	}
	// Names that could make a built-in name or a longer path are refused before any audit row is made.
	for _, name := range []string{"", "grant.call", "a.b", "../x", "GRANT"} {
		l.Addon = name
		l.CallID = "k2"
		if status, _ := e.do(caller{e.d1, ""}, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{l}}); status != 400 {
			t.Errorf("add-on name %q: status %d", name, status)
		}
	}
}

// Code that changed under the same manifest is a new version: the grant stops until it is made again.
func TestAGrantStopsWhenOnlyTheCodeChanged(t *testing.T) {
	e := grantEnv(t)
	e.grant(e.afif(), "reader", "notes", "search")
	if u := unavailable(e.pendingSpawn("first", "reader", 0), "notes", "search"); u != nil {
		t.Fatalf("the tool should be there before the change: %+v", u)
	}
	it := api.AddonItem{Name: "notes", Version: "0.1.0", PackageHash: hashB, ManifestHash: manifestHash(notesManifest("")), Manifest: notesManifest(""), Status: "ready"}
	e.ok(caller{e.d1, ""}, "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{it}}, nil)
	u := unavailable(e.pendingSpawn("second", "reader", 0), "notes", "search")
	if u == nil || u.Cause != api.CauseChanged || u.Action != actionGrantAgain {
		t.Fatalf("code changed under one manifest: %+v", u)
	}
	e.grant(e.afif(), "reader", "notes", "search")
	if u := unavailable(e.pendingSpawn("third", "reader", 0), "notes", "search"); u != nil {
		t.Fatalf("granting again should bring it back: %+v", u)
	}
}
