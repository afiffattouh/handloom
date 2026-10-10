package hub

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/profile"
)

func TestLessonsPageShowsAndDecides(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	spec := profile.Spec{Description: "codes", Kind: "claude", Tools: profile.Tools{Allow: []string{"read"}},
		Skills: []profile.File{{Path: "tests/SKILL.md", Content: "---\nname: tests\ndescription: d\n---\nWrite a test first."}}}
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "coder", Spec: spec}, nil)
	ag.lead("POST", "/v1/lessons", api.LessonReq{Profile: "coder", Skill: "tests", Text: "Run it all <b>twice</b>.", Why: "flaky"}, nil)

	page := e.req("GET", "/lessons", nil, c, nil)
	for _, want := range []string{"Waiting for you (1)", "coder / tests", "Run it all &lt;b&gt;twice&lt;/b&gt;.", "Why: flaky", "/lessons/1/accept"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("lessons page lacks %q", want)
		}
	}
	if !strings.Contains(e.req("GET", "/profiles", nil, c, nil).body, `href="/lessons"`) {
		t.Error("no way to the lessons from the profiles page")
	}
	post := func(act string, csrf string) webResp {
		return e.req("POST", fmt.Sprintf("/lessons/1/%s", act), url.Values{"csrf": {csrf}}, c, nil)
	}
	if r := post("accept", ""); r.status != 403 {
		t.Fatalf("without a token: %d", r.status)
	}
	if r := post("accept", tok); r.status != 303 || r.header.Get("Location") != "/lessons?done=accepted" {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	after := e.req("GET", "/lessons?done=accepted", nil, c, nil).body
	for _, want := range []string{"Lesson accepted", "Nothing waiting", "version 2", "accepted"} {
		if !strings.Contains(after, want) {
			t.Errorf("after accepting, the page lacks %q", want)
		}
	}
	if r := post("reject", tok); r.status != 409 {
		t.Fatalf("a second decision: %d", r.status)
	}
}
