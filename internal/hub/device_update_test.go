package hub

import (
	"net/http"
	"strings"
	"testing"

	"handloom/internal/api"
)

func TestComputersPageUpdatesALink(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, tok := e.owner()

	page := func() string { return e.req("GET", "/devices", nil, c, nil).body }
	if strings.Contains(page(), "Update to") {
		t.Fatal("an update is offered before the computer has reported anything")
	}

	e.apiOK(dev, "", "POST", "/v1/device/self", api.LinkSelf{Version: "v0.1.0", Latest: "v0.2.0", CanUpdate: true}, nil)
	body := page()
	for _, want := range []string{"v0.1.0", "v0.2.0 is out", "Update to v0.2.0", `action="/devices/d1/update"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if inlineRE.MatchString(body) {
		t.Errorf("an inline style or script: %s", inlineRE.FindString(body))
	}

	r := e.post("/devices/d1/update", c, tok, nil)
	if r.status != 303 || !strings.Contains(r.header.Get("Location"), "done=updating") {
		t.Fatalf("update request: %d %s", r.status, r.body)
	}
	body = page()
	if !strings.Contains(body, "update requested") || strings.Contains(body, "Update to") {
		t.Errorf("after the request the page should say so and stop offering it:\n%s", body)
	}
	var want api.LinkUpdateWant
	e.apiOK(dev, "", "GET", "/v1/device/update", nil, &want)
	if !want.Want {
		t.Fatal("the computer is not told")
	}

	// the link reports a failure: the request is over and the page says why
	e.apiOK(dev, "", "POST", "/v1/device/self", api.LinkSelf{Version: "v0.1.0", Latest: "v0.2.0", CanUpdate: true, State: "failed", Detail: "the download does not match the release's checksum"}, nil)
	e.apiOK(dev, "", "GET", "/v1/device/update", nil, &want)
	if want.Want {
		t.Fatal("a failed request is still asked for")
	}
	if body = page(); !strings.Contains(body, "does not match the release") || !strings.Contains(body, "Update to v0.2.0") {
		t.Errorf("after a failure the reason shows and the button returns")
	}
}

func TestAnUpdateRequestForAComputerThatCannotUpdateIsRefused(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, tok := e.owner()
	e.apiOK(dev, "", "POST", "/v1/device/self", api.LinkSelf{Version: "v0.1.0", Latest: "v0.2.0", CanUpdate: false, Note: "the folder /usr/bin is not writable by this user"}, nil)
	body := e.req("GET", "/devices", nil, c, nil).body
	if strings.Contains(body, "Update to") || !strings.Contains(body, "not writable") {
		t.Error("the page should explain, not offer")
	}
	r := e.post("/devices/d1/update", c, tok, nil)
	if r.status != 400 || !strings.Contains(r.body, "not writable") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestOnlyAComputerReportsItsVersion(t *testing.T) {
	e, _ := newAddonWeb(t)
	r, _ := http.NewRequest("POST", e.srv.URL+"/v1/device/self", strings.NewReader(`{"version":"v0.1.0"}`))
	r.Header.Set("Authorization", "Bearer "+e.admin)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
