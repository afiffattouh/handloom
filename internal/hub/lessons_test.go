package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/profile"
)

func TestALessonIsProposedReviewedAndThenUsedByLaterAgents(t *testing.T) {
	e := newEnv(t)
	e.hub.db.Exec(`UPDATE human SET role = 'owner' WHERE name = 'afif'`)
	owner := e.afif()
	e.newProfile(owner, "coder", profile.Spec{Description: "codes", Kind: "claude",
		Tools:  profile.Tools{Allow: []string{"read", "edit", "shell"}},
		Skills: []profile.File{{Path: "tests/SKILL.md", Content: "---\nname: tests\ndescription: write tests\n---\nWrite a test first."}}})
	var j api.Job
	e.ok(owner, "POST", "/v1/jobs", api.JobNewReq{Title: "A job", Lead: "lead"}, &j)

	// the lead proposes; nothing changes yet
	var l api.Lesson
	e.ok(e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: "Run the whole suite, not just the new test: a shared fixture broke twice.", Why: "two rejected submissions in the job"}, &l)
	if l.Status != "proposed" || l.Job != j.ID || l.ProposedBy == "" {
		t.Fatalf("proposal: %+v", l)
	}
	var cur api.ProfileFull
	e.ok(owner, "GET", "/v1/profiles/coder", nil, &cur)
	if cur.Version != 1 {
		t.Fatalf("a proposal changed the profile: v%d", cur.Version)
	}
	// bad proposals
	e.fail(400, e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "nope", Text: "x", Why: "y"})
	e.fail(400, e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: "x"})
	e.fail(404, e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "ghost", Skill: "tests", Text: "x", Why: "y"})
	e.fail(400, e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: strings.Repeat("a", 2000), Why: "y"})
	// a member may read but not accept, since accepting changes a profile
	var mem api.TokenResp
	e.ok(caller{e.admin, ""}, "POST", "/v1/admin/humans", api.NameReq{Name: "mia"}, &mem)
	e.hub.db.Exec(`UPDATE human SET role = 'member' WHERE name = 'mia'`)
	e.fail(403, caller{mem.Token, ""}, "POST", fmt.Sprintf("/v1/lessons/%d/accept", l.ID), nil)
	// a worker may not propose, nor an agent decide
	e.fail(403, e.worker(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: "x", Why: "y"})
	e.fail(403, e.lead(), "POST", fmt.Sprintf("/v1/lessons/%d/accept", l.ID), nil)
	// readable by an AI app, not decidable
	var list []api.Lesson
	e.ok(owner, "GET", "/v1/lessons?status=proposed", nil, &list)
	if len(list) != 1 || list[0].ID != l.ID {
		t.Fatalf("list: %+v", list)
	}
	// accept: a new version with the lesson in the skill; the old version is untouched
	var got api.Lesson
	e.ok(owner, "POST", fmt.Sprintf("/v1/lessons/%d/accept", l.ID), nil, &got)
	if got.Status != "accepted" || got.NewVersion != 2 || got.Used != 0 {
		t.Fatalf("accepted: %+v", got)
	}
	var v2, v1 api.ProfileFull
	e.ok(owner, "GET", "/v1/profiles/coder", nil, &v2)
	e.ok(owner, "GET", "/v1/profiles/coder?version=1", nil, &v1)
	txt := v2.Spec.Skills[0].Content
	if v2.Version != 2 || !strings.Contains(txt, "## Lessons learned") || !strings.Contains(txt, "Run the whole suite") || !strings.Contains(txt, fmt.Sprintf("(from job #%d)", j.ID)) || !strings.HasPrefix(txt, "---\nname: tests") {
		t.Fatalf("v2 skill: %q", txt)
	}
	if strings.Contains(v1.Spec.Skills[0].Content, "Lessons") {
		t.Fatal("version 1 was changed")
	}
	e.fail(409, owner, "POST", fmt.Sprintf("/v1/lessons/%d/accept", l.ID), nil)
	// a second lesson goes under the same heading
	var l2 api.Lesson
	e.ok(e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: "Name the failing test in the handoff.", Why: "the handoff was vague"}, &l2)
	e.ok(owner, "POST", fmt.Sprintf("/v1/lessons/%d/accept", l2.ID), nil, nil)
	e.ok(owner, "GET", "/v1/profiles/coder", nil, &v2)
	if strings.Count(v2.Spec.Skills[0].Content, "## Lessons learned") != 1 || v2.Version != 3 {
		t.Fatalf("v3 skill: %q", v2.Spec.Skills[0].Content)
	}
	// a later agent started from the profile has it, and the lesson says how many did
	s := e.spawn(owner, api.SpawnReq{Name: "coder-9", Profile: "coder", Device: "d1", Job: j.ID})
	if s.Profile != "coder@3" {
		t.Fatalf("a later agent did not get the lesson: %+v", s)
	}
	e.ok(owner, "GET", "/v1/lessons?status=accepted", nil, &list)
	used := map[int64]int{}
	for _, x := range list {
		used[x.ID] = x.Used
	}
	if used[l.ID] != 1 || used[l2.ID] != 1 {
		t.Fatalf("used: %v", used)
	}
	// reject
	var l3 api.Lesson
	e.ok(e.lead(), "POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: "Never test.", Why: "bad idea"}, &l3)
	e.ok(owner, "POST", fmt.Sprintf("/v1/lessons/%d/reject", l3.ID), api.LessonDecideReq{Reason: "wrong"}, &got)
	if got.Status != "rejected" || got.Note != "wrong" {
		t.Fatalf("rejected: %+v", got)
	}
	e.ok(owner, "GET", "/v1/profiles/coder", nil, &v2)
	if v2.Version != 3 {
		t.Fatalf("a rejected lesson changed the profile: v%d", v2.Version)
	}
}
