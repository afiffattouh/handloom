package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/profile"
)

func researcher() profile.Spec {
	return profile.Spec{Description: "reads and writes", Kind: "claude",
		Tools:  profile.Tools{Allow: []string{"read", "edit", "shell"}, DenyCommands: []string{"rm"}},
		Prompt: "You are careful.",
		Skills: []profile.File{{Path: "cite/SKILL.md", Content: "---\nname: cite\ndescription: cite sources\n---\nAlways cite."}}}
}

func (e *env) newProfile(c caller, name string, spec profile.Spec) api.ProfileFull {
	e.t.Helper()
	var p api.ProfileFull
	e.ok(c, "POST", "/v1/profiles", api.ProfileReq{Name: name, Spec: spec}, &p)
	return p
}

func TestProfilesAreWrittenByTheOwnerOnly(t *testing.T) {
	e := newEnv(t)
	req := api.ProfileReq{Name: "researcher", Spec: researcher()}
	for _, c := range []caller{e.lead(), e.worker(), e.observer(), e.afif()} { // afif is a member
		e.fail(403, c, "POST", "/v1/profiles", req)
	}
	e.hub.db.Exec(`UPDATE human SET role = 'owner' WHERE name = 'afif'`)
	e.ok(e.afif(), "POST", "/v1/profiles", req, nil)
	e.ok(caller{e.admin, ""}, "POST", "/v1/profiles", api.ProfileReq{Name: "second", Spec: researcher()}, nil)
	// Everybody can read them.
	for _, c := range []caller{e.lead(), e.worker(), e.observer()} {
		var list []api.ProfileInfo
		e.ok(c, "GET", "/v1/profiles", nil, &list)
		if len(list) != 2 {
			t.Fatalf("profiles: %+v", list)
		}
	}
}

func TestProfileVersionsAreImmutable(t *testing.T) {
	e := newEnv(t)
	own := caller{e.admin, ""}
	v1 := e.newProfile(own, "researcher", researcher())
	if v1.Version != 1 || len(v1.Hash) != 64 || v1.Spec.Runtime != "cloud" {
		t.Fatalf("v1: %+v", v1)
	}
	// The same content again is not a new version.
	if again := e.newProfile(own, "researcher", researcher()); again.Version != 1 || again.Hash != v1.Hash {
		t.Fatalf("an unchanged profile made a new version: %+v", again)
	}
	changed := researcher()
	changed.Prompt = "You are very careful."
	v2 := e.newProfile(own, "researcher", changed)
	if v2.Version != 2 || v2.Hash == v1.Hash {
		t.Fatalf("v2: %+v", v2)
	}
	var latest, old api.ProfileFull
	e.ok(e.worker(), "GET", "/v1/profiles/researcher", nil, &latest)
	e.ok(e.worker(), "GET", "/v1/profiles/researcher?version=1", nil, &old)
	if latest.Version != 2 || old.Version != 1 || old.Spec.Prompt != "You are careful." {
		t.Fatalf("latest %d, old %d %q", latest.Version, old.Version, old.Spec.Prompt)
	}
	var vs []api.ProfileInfo
	e.ok(e.afif(), "GET", "/v1/profiles/researcher/versions", nil, &vs)
	if len(vs) != 2 || vs[0].Version != 1 || vs[1].Version != 2 {
		t.Fatalf("versions: %+v", vs)
	}
	var list []api.ProfileInfo
	e.ok(e.afif(), "GET", "/v1/profiles", nil, &list)
	if len(list) != 1 || list[0].Version != 2 || len(list[0].Skills) != 1 || list[0].Skills[0] != "cite" {
		t.Fatalf("list shows the latest only: %+v", list)
	}
	e.fail(404, e.afif(), "GET", "/v1/profiles/researcher?version=9", nil)
	e.fail(404, e.afif(), "GET", "/v1/profiles/nobody", nil)
	for _, q := range []string{`UPDATE profile SET spec = '{}'`, `DELETE FROM profile`} {
		if _, err := e.hub.db.Exec(q); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Errorf("%s: %v", q, err)
		}
	}
	if !e.audited("profile.new", "profile:researcher@2") {
		t.Fatal("a new version is not audited")
	}
}

func TestProfileValidationAtTheDoor(t *testing.T) {
	e := newEnv(t)
	own := caller{e.admin, ""}
	bad := func(name string, mutate func(*profile.Spec)) {
		s := researcher()
		mutate(&s)
		e.fail(400, own, "POST", "/v1/profiles", api.ProfileReq{Name: name, Spec: s})
	}
	bad("bad name!", func(*profile.Spec) {})
	bad("ok", func(s *profile.Spec) { s.Tools.Allow = []string{"root"} })
	bad("ok", func(s *profile.Spec) { s.Tools.DenyCommands = []string{"rm *"} })
	bad("ok", func(s *profile.Spec) { s.Skills = append(s.Skills, profile.File{Path: "../x/SKILL.md", Content: "x"}) })
	bad("ok", func(s *profile.Spec) { s.Kind = "emacs" })
	got := e.fail(400, own, "POST", "/v1/profiles", api.ProfileReq{Name: "ok", Spec: profile.Spec{Tools: profile.Tools{Allow: []string{"root"}}}})
	if !strings.Contains(got, "unknown tool") {
		t.Fatalf("the error does not say what is wrong: %s", got)
	}
}

func TestSpawnIsPinnedToAProfileVersion(t *testing.T) {
	e := newEnv(t)
	own := caller{e.admin, ""}
	e.newProfile(own, "researcher", researcher())
	s1 := e.spawn(e.afif(), api.SpawnReq{Name: "a", Profile: "researcher", Device: "d1"})
	if s1.Profile != "researcher@1" || s1.Kind != "claude" {
		t.Fatalf("spawn: %+v", s1)
	}
	changed := researcher()
	changed.Tools.Allow = []string{"read", "edit", "shell", "web"}
	e.newProfile(own, "researcher", changed)
	s2 := e.spawn(e.afif(), api.SpawnReq{Name: "b", Profile: "researcher", Device: "d1"})
	s3 := e.spawn(e.afif(), api.SpawnReq{Name: "c", Profile: "researcher@1", Device: "d1"})
	if s2.Profile != "researcher@2" || s3.Profile != "researcher@1" {
		t.Fatalf("pinning: %s %s", s2.Profile, s3.Profile)
	}

	// The device fetches exactly what the spawn was pinned to.
	e.report(e.d1, s1.ID, api.SpawnReport{Status: "launching"})
	var p api.ProfileFull
	e.ok(caller{e.d1, ""}, "GET", fmt.Sprintf("/v1/device/spawns/%d/profile", s1.ID), nil, &p)
	if p.Version != 1 || strings.Contains(strings.Join(p.Spec.Tools.Allow, ","), "web") {
		t.Fatalf("the device got %+v, not version 1", p)
	}
	e.fail(403, caller{e.d2, ""}, "GET", fmt.Sprintf("/v1/device/spawns/%d/profile", s1.ID), nil)
	e.fail(403, e.afif(), "GET", fmt.Sprintf("/v1/device/spawns/%d/profile", s1.ID), nil)
	// A spawn without a profile has none to fetch.
	plain := e.spawn(e.afif(), api.SpawnReq{Name: "plain", Kind: "claude", Device: "d1"})
	e.fail(404, caller{e.d1, ""}, "GET", fmt.Sprintf("/v1/device/spawns/%d/profile", plain.ID), nil)

	e.fail(404, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "d", Profile: "nobody", Device: "d1"})
	e.fail(404, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "d", Profile: "researcher@9", Device: "d1"})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "d", Profile: "bad name", Device: "d1"})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "d", Profile: "researcher", Kind: "codex", Device: "d1"})
	// A lead can use a profile too, in its own job.
	lead := e.agent("lead1")
	e.newJob("a job", "lead1")
	if s := e.spawn(lead, api.SpawnReq{Name: "e", Profile: "researcher"}); s.Profile != "researcher@2" {
		t.Fatalf("a lead's spawn: %+v", s)
	}
}

func TestConfidentialJobsOnlyStartLocalProfiles(t *testing.T) {
	e := newEnv(t)
	e.hub.opt.AllowConfidential = true
	own := caller{e.admin, ""}
	e.newProfile(own, "cloudy", researcher())
	localSpec := researcher()
	localSpec.Runtime = "local"
	e.newProfile(own, "private", localSpec)
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "client work", Confidential: true}, &j)
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "a", Kind: "claude", Device: "d1", Job: j.ID})
	e.fail(400, e.afif(), "POST", "/v1/spawns", api.SpawnReq{Name: "a", Profile: "cloudy", Device: "d1", Job: j.ID})
	if s := e.spawn(e.afif(), api.SpawnReq{Name: "a", Profile: "private", Device: "d1", Job: j.ID}); s.Profile != "private@1" {
		t.Fatalf("a local profile on a confidential job: %+v", s)
	}
	// Outside a confidential job, cloud profiles are fine.
	e.spawn(e.afif(), api.SpawnReq{Name: "b", Profile: "cloudy", Device: "d1"})
}
