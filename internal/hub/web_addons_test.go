package hub

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

// newAddonWeb is a hub with a computer d1 (its credential is returned) and a profile "boss".
func newAddonWeb(t *testing.T) (*webEnv, string) {
	t.Helper()
	e := newWebEnv(t, Options{})
	ag := e.agents()
	e.apiOK(e.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "boss", Spec: researcher()}, nil)
	return e, ag.device
}

// reportWeb has the computer report the notes add-on, as its link would.
func (e *webEnv) reportWeb(device, manifest, status, detail string) {
	e.t.Helper()
	it := api.AddonItem{Name: "notes", Version: "0.1.0", PackageHash: hashA, ManifestHash: manifestHash(manifest), Manifest: manifest, Status: status, Detail: detail}
	e.apiOK(device, "", "POST", "/v1/device/addons", api.AddonReport{Addons: []api.AddonItem{it}}, nil)
}

var inlineRE = regexp.MustCompile(`(?i)\sstyle=|<style|<script[^>]*>[^<]`)

// noInline is the web content policy: no inline styles and no inline scripts on any page.
func noInline(t *testing.T, what, body string) {
	t.Helper()
	if inlineRE.MatchString(body) {
		t.Errorf("%s: an inline style or script: %s", what, inlineRE.FindString(body))
	}
	// the words people see are Add-on, Tool and computer
	for _, bad := range []string{"plugin", "Plugin", "machine"} {
		if strings.Contains(body, bad) && !strings.Contains(what, "help") {
			t.Errorf("%s: says %q", what, bad)
		}
	}
}

func TestAddonsPageWithNoAddonsIsComplete(t *testing.T) {
	e, _ := newAddonWeb(t)
	c, _ := e.owner()
	r := e.req("GET", "/addons", nil, c, nil)
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	for _, want := range []string{"No add-ons yet", "On the computer that will run it:", "handloom addon add FOLDER", `class="inset wrap sel mono"`, "works fully without them", `<h1>Add-ons</h1>`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the empty page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "Grant") {
		t.Error("an empty page offers a grant")
	}
	noInline(t, "empty page", r.body)
	// the sidebar entry is there, marked as the current page, under Admin
	if !strings.Contains(r.body, `href="/addons" title="Add-ons" aria-current="page"`) {
		t.Error("the menu does not mark Add-ons as current")
	}
	if !strings.Contains(r.body, `<li><a href="/">Handloom</a></li><li class="sep" aria-hidden="true"><svg class="icon"><use href="#i-chevron-right"/></svg></li><li><span aria-current="page">Add-ons</span></li>`) {
		t.Error("the path above the page is wrong")
	}
	// other pages carry the entry but not as current
	if r := e.req("GET", "/profiles", nil, c, nil); !strings.Contains(r.body, `href="/addons" title="Add-ons">`) {
		t.Error("the entry is missing on another page")
	}
}

func TestAddonsPageNeedsASessionAndShowsViewersNoForms(t *testing.T) {
	e, dev := newAddonWeb(t)
	e.reportWeb(dev, notesManifest(""), "ready", "")
	c, tok := e.owner()
	if r := e.req("POST", "/addons/grant", url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}, c, nil); r.status != 303 {
		t.Fatalf("grant: %d %s", r.status, r.body)
	}
	if r := e.req("GET", "/addons", nil, nil, nil); r.status != 303 || !strings.HasPrefix(r.header.Get("Location"), "/login") {
		t.Fatalf("anonymous: %d %q", r.status, r.header.Get("Location"))
	}
	for _, p := range []string{"/addons/grant", "/addons/revoke"} {
		if r := e.req("POST", p, url.Values{}, nil, nil); r.status == 200 || r.status == 303 && !strings.HasPrefix(r.header.Get("Location"), "/login") {
			t.Fatalf("anonymous %s: %d", p, r.status)
		}
	}
	// A viewer sees no computers, so no add-ons; a member sees the add-ons on their own computers. Neither
	// sees a grant or a form for one.
	vc0, _ := e.member("vera", "viewer")
	if r := e.req("GET", "/addons", nil, vc0, nil); r.status != 200 || !strings.Contains(r.body, "No add-ons yet") {
		t.Fatalf("viewer: %d", r.status)
	}
	mc, mtok := e.member("mia", "member")
	e.hub.db.Exec(`UPDATE device SET owner_id = (SELECT id FROM human WHERE name = 'mia') WHERE name = 'd1'`)
	if r := e.req("GET", "/addons", nil, mc, nil); r.status != 200 || !strings.Contains(r.body, "notes") || !strings.Contains(r.body, "Ready") {
		t.Fatalf("member with a computer: %d %.300s", r.status, r.body)
	}
	for role, who := range map[string]struct {
		c   *http.Cookie
		tok string
	}{"viewer": {vc0, csrfOf(t, e.req("GET", "/inbox", nil, vc0, nil).body)}, "member": {mc, mtok}} {
		r := e.req("GET", "/addons", nil, who.c, nil)
		if strings.Contains(r.body, `action="/addons/grant"`) || strings.Contains(r.body, `action="/addons/revoke"`) || strings.Contains(r.body, "Who may use it") || strings.Contains(r.body, `name="tool"`) {
			t.Errorf("%s sees the owner's forms", role)
		}
		if !strings.Contains(r.body, `href="/addons" title="Add-ons"`) {
			t.Errorf("%s: no menu entry", role)
		}
		noInline(t, role+" page", r.body)
		for path, f := range map[string]url.Values{
			"/addons/grant":  {"csrf": {who.tok}, "profile": {"boss"}, "tool": {"notes.publish"}},
			"/addons/revoke": {"csrf": {who.tok}, "profile": {"boss"}, "tool": {"notes.search"}},
		} {
			if r := e.req("POST", path, f, who.c, nil); r.status != 403 {
				t.Errorf("%s %s: %d", role, path, r.status)
			}
		}
	}
	if r := e.req("GET", "/addons", nil, mc, nil); !strings.Contains(r.body, "Only the owner sees who may use each tool") {
		t.Error("no note to a member about the owner")
	}
	// nothing the refused people did changed a grant
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM addon_grant`).Scan(&n)
	if n != 1 {
		t.Fatalf("grants: %d", n)
	}
	if !e.audited("denied", "POST /addons/grant") || !e.audited("denied", "POST /addons/revoke") {
		t.Error("the refusals are not in the audit log")
	}
}

func TestAddonsPageShowsEachStatusInWords(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, _ := e.owner()
	for status, want := range map[string][]string{
		"ready":   {"Ready", "badge success"},
		"changed": {"Changed since approved", "badge warning", "the folder was edited", "Run handloom addon add FOLDER on this computer to approve it again."},
		"down":    {"Not running", "badge destructive", "did not start", "Run handloom addon doctor on this computer."},
		"missing": {"Missing on this computer", "badge warning", "folder is gone", "to bring it back"},
	} {
		detail := map[string]string{"changed": "the folder was edited", "down": "did not start", "missing": "folder is gone"}[status]
		e.reportWeb(dev, notesManifest(""), status, detail)
		r := e.req("GET", "/addons", nil, c, nil)
		for _, w := range append(want, "d1", "v0.1.0", "Keeps notes", "Search the notes.", "Send a note to a shared place.") {
			if !strings.Contains(r.body, w) {
				t.Errorf("%s: the page lacks %q", status, w)
			}
		}
		noInline(t, status, r.body)
	}
	// the tools say in words whether they leave the computer
	r := e.req("GET", "/addons", nil, c, nil)
	if !regexp.MustCompile(`(?s)search</span></td><td class="desc">Search the notes.</td><td class="nw">no</td>.*publish</span></td><td class="desc">Send a note to a shared place.</td><td class="nw">yes</td>`).MatchString(r.body) {
		t.Errorf("the tool table: %s", r.body)
	}
}

func TestGrantAndRevokeFromTheAddonsPage(t *testing.T) {
	e, dev := newAddonWeb(t)
	e.reportWeb(dev, notesManifest(""), "ready", "")
	c, tok := e.owner()
	grant := func(f url.Values) webResp {
		base := url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.publish"}, "leaves": {"yes"}}
		for k, v := range f {
			base[k] = v
		}
		return e.req("POST", "/addons/grant", base, c, nil)
	}
	page := e.req("GET", "/addons", nil, c, nil).body
	for _, want := range []string{"Who may use it", "No profile may use it yet", `action="/addons/grant"`, `<option value="boss"`, `<option value="notes.search"`, `<option value="notes.publish"`, `name="csrf" value="` + tok + `"`, `placeholder="50"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
	// no token, wrong token: refused, nothing granted
	if r := e.req("POST", "/addons/grant", url.Values{"profile": {"boss"}, "tool": {"notes.search"}}, c, nil); r.status != 403 {
		t.Fatalf("no token: %d", r.status)
	}
	if r := grant(url.Values{"csrf": {"nope"}}); r.status != 403 {
		t.Fatalf("wrong token: %d", r.status)
	}
	if r := e.req("POST", "/addons/grant", url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}, c, map[string]string{"Origin": "https://evil.test"}); r.status != 403 {
		t.Fatalf("another origin: %d", r.status)
	}
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM addon_grant`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused requests granted %d", n)
	}
	// refusals in words, with what was typed kept
	for name, tc := range map[string]struct {
		f      url.Values
		status int
		want   string
	}{
		"no tool":         {url.Values{"tool": {""}}, 400, "Choose a tool."},
		"unknown add-on":  {url.Values{"tool": {"ghost.x"}}, 404, "no computer has reported an add-on called ghost"},
		"unknown tool":    {url.Values{"tool": {"notes.nothing"}}, 400, "has no tool called nothing; its tools are: publish, search"},
		"unknown profile": {url.Values{"profile": {"nobody"}}, 404, "nobody"},
		"words as limit":  {url.Values{"max_calls": {"lots"}}, 400, "must be a whole number"},
		"too many":        {url.Values{"max_calls": {"99999"}}, 400, "between 1 and"},
	} {
		r := grant(tc.f)
		if r.status != tc.status || !strings.Contains(r.body, tc.want) || !strings.Contains(r.body, `role="alert"`) {
			t.Errorf("%s: %d %.300s", name, r.status, r.body)
		}
		noInline(t, name, r.body)
	}
	if r := grant(url.Values{"max_calls": {"lots"}}); !strings.Contains(r.body, `value="lots"`) || !strings.Contains(r.body, `<option value="notes.publish" selected`) {
		t.Error("the form did not keep what was typed")
	}

	r := grant(url.Values{"leaves": {"no"}, "max_calls": {"7"}, "tool": {"notes.search"}})
	if r.status != 303 || r.header.Get("Location") != "/addons?done=granted" {
		t.Fatalf("grant: %d %s", r.status, r.body)
	}
	var g api.AddonGrant
	var leaves int
	e.hub.db.QueryRow(`SELECT profile, addon, tool, leaves_machine, max_calls FROM addon_grant`).Scan(&g.Profile, &g.Addon, &g.Tool, &leaves, &g.MaxCalls)
	if g.Profile != "boss" || g.Addon != "notes" || g.Tool != "search" || leaves != 0 || g.MaxCalls != 7 {
		t.Fatalf("the grant: %+v leaves=%d", g, leaves)
	}
	// the choice defaults to leaving, and the limit to 50
	if r := grant(nil); r.status != 303 {
		t.Fatalf("grant publish: %d", r.status)
	}
	e.hub.db.QueryRow(`SELECT leaves_machine, max_calls FROM addon_grant WHERE tool = 'publish'`).Scan(&leaves, &g.MaxCalls)
	if leaves != 1 || g.MaxCalls != 50 {
		t.Fatalf("defaults: leaves=%d calls=%d", leaves, g.MaxCalls)
	}
	if !e.audited("addon.grant", "profile:boss") {
		t.Error("the grant is not in the audit log")
	}
	page = e.req("GET", "/addons?done=granted", nil, c, nil).body
	for _, want := range []string{"Granted. Helpers started from now on", `<a href="/profiles/boss">boss</a>`, `name="tool" value="notes.search"`, "Revoke", `<td class="nw">7</td>`} {
		if !strings.Contains(page, want) {
			t.Errorf("after granting, the page lacks %q", want)
		}
	}
	if strings.Contains(page, "Granted for an earlier version") {
		t.Error("a current grant is called old")
	}

	// the add-on changes: the grant is shown as made for an earlier version
	e.reportWeb(dev, notesManifest(" again"), "ready", "")
	if page := e.req("GET", "/addons", nil, c, nil).body; strings.Count(page, "Granted for an earlier version") != 2 {
		t.Errorf("old grants are not marked: %s", page)
	}

	rev := func(tool string) webResp {
		return e.req("POST", "/addons/revoke", url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {tool}}, c, nil)
	}
	if r := rev("notes.search"); r.status != 303 || r.header.Get("Location") != "/addons?done=revoked" {
		t.Fatalf("revoke: %d", r.status)
	}
	if r := rev("notes.search"); r.status != 404 || !strings.Contains(r.body, "has no grant for notes.search") {
		t.Fatalf("revoke again: %d %.200s", r.status, r.body)
	}
	if r := e.req("POST", "/addons/revoke", url.Values{"csrf": {"bad"}, "profile": {"boss"}, "tool": {"notes.publish"}}, c, nil); r.status != 403 {
		t.Fatalf("revoke without a token: %d", r.status)
	}
	e.hub.db.QueryRow(`SELECT count(*) FROM addon_grant`).Scan(&n)
	if n != 1 || !e.audited("addon.revoke", "profile:boss") {
		t.Fatalf("grants left: %d", n)
	}
	if page := e.req("GET", "/addons?done=revoked", nil, c, nil).body; !strings.Contains(page, "Revoked. Helpers started from now on do not get it") {
		t.Error("no word that the revoke worked")
	}
}

func TestComputersWithDifferentVersionsNeedTheComputerNamed(t *testing.T) {
	e, dev := newAddonWeb(t)
	var tok2 api.TokenResp
	e.apiOK(e.admin, "", "POST", "/v1/admin/devices", api.NameReq{Name: "d2"}, &tok2)
	var join api.JoinResp
	e.apiOK("", "", "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok2.Token}, &join)
	e.reportWeb(dev, notesManifest(""), "ready", "")
	e.reportWeb(join.Credential, notesManifest(" two"), "ready", "")
	c, tok := e.owner()
	page := e.req("GET", "/addons", nil, c, nil).body
	if !strings.Contains(page, `<select name="computer">`) || !strings.Contains(page, "they hold different versions") {
		t.Fatalf("no choice of computer: %s", page)
	}
	f := url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 409 || !strings.Contains(r.body, "name the computer") {
		t.Fatalf("without a computer: %d %.200s", r.status, r.body)
	}
	f.Set("computer", "d2")
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 303 {
		t.Fatalf("with a computer: %d", r.status)
	}
}

func TestTheProfilePageGrantsAndRevokesTools(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, tok := e.owner()
	// nothing installed: the section says so
	r := e.req("GET", "/profiles/boss", nil, c, nil)
	for _, want := range []string{"Tools from add-ons", "None yet.", "No computer has an add-on installed yet", `href="/addons"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the profile page lacks %q", want)
		}
	}
	e.reportWeb(dev, notesManifest(""), "ready", "")
	r = e.req("GET", "/profiles/boss", nil, c, nil)
	for _, want := range []string{`<option value="notes.search"`, `<option value="notes.publish"`, `name="from" value="profile:boss"`, `<option value="yes" selected`, `placeholder="50"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the grant form lacks %q", want)
		}
	}
	noInline(t, "profile page", r.body)
	f := url.Values{"csrf": {tok}, "from": {"profile:boss"}, "profile": {"boss"}, "tool": {"notes.search"}, "leaves": {"no"}, "max_calls": {"12"}}
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 303 || r.header.Get("Location") != "/profiles/boss?done=granted" {
		t.Fatalf("grant: %d %s", r.status, r.header.Get("Location"))
	}
	r = e.req("GET", "/profiles/boss?done=granted", nil, c, nil)
	if !strings.Contains(r.body, "Granted. Helpers started from now on") || !regexp.MustCompile(`(?s)notes\.search</span></td><td class="nw">no</td><td class="nw">12</td>.*Revoke`).MatchString(r.body) {
		t.Fatalf("the grant is not listed: %s", r.body)
	}
	// a refusal comes back on the profile page with what was typed
	f.Set("tool", "notes.nothing")
	f.Set("max_calls", "33")
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 400 || !strings.Contains(r.body, "Tools from add-ons") || !strings.Contains(r.body, "has no tool called nothing") || !strings.Contains(r.body, `value="33"`) {
		t.Fatalf("a refusal: %d %.300s", r.status, r.body)
	}
	rev := url.Values{"csrf": {tok}, "from": {"profile:boss"}, "profile": {"boss"}, "tool": {"notes.search"}}
	if r := e.req("POST", "/addons/revoke", rev, c, nil); r.status != 303 || r.header.Get("Location") != "/profiles/boss?done=revoked" {
		t.Fatalf("revoke: %d", r.status)
	}
	if r := e.req("GET", "/profiles/boss?done=revoked", nil, c, nil); !strings.Contains(r.body, "None yet.") || !strings.Contains(r.body, "Revoked.") {
		t.Fatal("the revoke is not shown")
	}
	// only the owner sees the section
	vc, _ := e.member("vera", "viewer")
	if r := e.req("GET", "/profiles/boss", nil, vc, nil); r.status != 200 || strings.Contains(r.body, "Tools from add-ons") {
		t.Fatalf("viewer: %d", r.status)
	}
	// a new profile has no grants to show yet
	if r := e.req("GET", "/profiles/new", nil, c, nil); strings.Contains(r.body, "Tools from add-ons") {
		t.Fatal("a profile that does not exist has a grant section")
	}
}

func TestTheJobPageSaysWhenAHelperStartedWithoutAGrantedTool(t *testing.T) {
	e, dev := newAddonWeb(t)
	e.reportWeb(dev, notesManifest(""), "ready", "")
	c, tok := e.owner()
	for _, tool := range []string{"notes.search", "notes.publish"} {
		if r := e.req("POST", "/addons/grant", url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {tool}}, c, nil); r.status != 303 {
			t.Fatalf("grant %s: %d", tool, r.status)
		}
	}
	start := func(title string) string {
		r := e.req("POST", "/jobs", url.Values{"title": {title}, "body": {"b"}, "device": {"d1"}, "repo": {"/srv/app"}, "verify": {"./check.sh"}, "lead_profile": {"boss"}, "csrf": {tok}}, c, nil)
		if r.status != 303 {
			t.Fatalf("start: %d %s", r.status, r.body)
		}
		return r.header.Get("Location")
	}
	loc := strings.SplitN(start("With tools"), "?", 2)[0]
	body := e.req("GET", loc, nil, c, nil).body
	if strings.Contains(body, "without") || strings.Contains(body, "granted</h3>") {
		t.Fatal("a notice about nothing")
	}
	// the add-on stops: the next look shows the cause and the one thing to do
	e.reportWeb(dev, notesManifest(""), "down", "did not start")
	body = e.req("GET", loc, nil, c, nil).body
	for _, want := range []string{"A helper started without some tools it was granted", "notes.search", "notes.publish", "The add-on is not running and could not be started.", "Run handloom addon doctor on this computer.", "so it does not guess"} {
		if !strings.Contains(body, want) {
			t.Errorf("the notice lacks %q", want)
		}
	}
	noInline(t, "job page", body)
	// one tool granted, one cause
	e.reportWeb(dev, notesManifest(""), "missing", "")
	if body := e.req("GET", loc, nil, c, nil).body; !strings.Contains(body, "This computer does not have the add-on.") || !strings.Contains(body, "Install the add-on on this computer.") {
		t.Error("the missing cause is not shown")
	}
	// it is well again: the notice goes away
	e.reportWeb(dev, notesManifest(""), "ready", "")
	if body := e.req("GET", loc, nil, c, nil).body; strings.Contains(body, "it was granted") {
		t.Error("the notice stays after the cause is gone")
	}
	// a job with no helper from a granted profile has nothing to say
	other := strings.SplitN(start("Another"), "?", 2)[0]
	e.reportWeb(dev, notesManifest(""), "down", "")
	e.req("POST", other+"/close", url.Values{"csrf": {tok}, "cancel": {"1"}}, c, nil)
	if body := e.req("GET", other, nil, c, nil).body; strings.Contains(body, "it was granted") {
		t.Error("a closed job shows today's trouble as if the helpers had seen it")
	}
}

func TestTheHelpPageHasAnAddonsSection(t *testing.T) {
	e, _ := newAddonWeb(t)
	c, _ := e.owner()
	r := e.req("GET", "/help", nil, c, nil)
	for _, want := range []string{`<li><a href="#addons">Add-ons</a></li>`, `id="addons"`, "What is an add-on?", "Is it safe?", "Why can't a confidential job use some tools?", "How do I install one?", "handloom addon add FOLDER"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the help page lacks %q", want)
		}
	}
	noInline(t, "help page", r.body)
	// the checklist is as it was
	if s := e.req("GET", "/start", nil, c, nil); s.status != 200 || strings.Contains(s.body, "handloom addon") || strings.Contains(s.body, "an add-on") {
		t.Error("Get started mentions add-ons")
	}
}

func TestAGrantWhoseAddonIsOnNoComputerCanStillBeRevoked(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, tok := e.owner()
	e.reportWeb(dev, notesManifest(""), "ready", "")
	f := url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 303 {
		t.Fatalf("grant: %d", r.status)
	}
	// the computer stops reporting the add-on
	e.apiOK(dev, "", "POST", "/v1/device/addons", api.AddonReport{}, nil)
	page := e.req("GET", "/addons", nil, c, nil).body
	if !strings.Contains(page, "Granted but not installed anywhere") || !strings.Contains(page, `value="notes.search"`) || !strings.Contains(page, "Revoke") {
		t.Fatalf("the orphan grant is not on the Add-ons page: %s", page)
	}
	prof := e.req("GET", "/profiles/boss", nil, c, nil).body
	if !strings.Contains(prof, "notes.search</span>") || !strings.Contains(prof, "Revoke") || strings.Contains(prof, "None yet.") {
		t.Fatalf("the orphan grant is not on the profile page: %s", prof)
	}
	rev := url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}
	if r := e.req("POST", "/addons/revoke", rev, c, nil); r.status != 303 {
		t.Fatalf("revoke: %d", r.status)
	}
	if page := e.req("GET", "/addons", nil, c, nil).body; strings.Contains(page, "Granted but not installed anywhere") {
		t.Fatal("still listed after the revoke")
	}
}

func TestTheProfilePageNamesTheComputerWhenVersionsDiffer(t *testing.T) {
	e, dev := newAddonWeb(t)
	var tok2 api.TokenResp
	e.apiOK(e.admin, "", "POST", "/v1/admin/devices", api.NameReq{Name: "d2"}, &tok2)
	var join api.JoinResp
	e.apiOK("", "", "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok2.Token}, &join)
	e.reportWeb(dev, notesManifest(""), "ready", "")
	c, tok := e.owner()
	if body := e.req("GET", "/profiles/boss", nil, c, nil).body; strings.Contains(body, `<select name="computer">`) {
		t.Fatal("a computer is asked for with one version")
	}
	e.reportWeb(join.Credential, notesManifest(" two"), "ready", "")
	body := e.req("GET", "/profiles/boss", nil, c, nil).body
	if !strings.Contains(body, `<select name="computer">`) {
		t.Fatalf("no choice of computer on the profile page: %s", body)
	}
	f := url.Values{"csrf": {tok}, "from": {"profile:boss"}, "profile": {"boss"}, "tool": {"notes.search"}}
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 409 || !strings.Contains(r.body, `<select name="computer">`) {
		t.Fatalf("refusal without a computer: %d", r.status)
	}
	f.Set("computer", "d2")
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 303 {
		t.Fatalf("with a computer: %d", r.status)
	}
}

// A grant for a version no computer has says so, and says what to do, on the Add-ons page and on the profile.
func TestAStaleGrantIsExplainedWhereItIsListed(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, tok := e.owner()
	e.reportWeb(dev, notesManifest(""), "ready", "")
	f := url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}
	if r := e.req("POST", "/addons/grant", f, c, nil); r.status != 303 {
		t.Fatalf("grant: %d", r.status)
	}
	const line = "A grant for an earlier version does not work. Check the new version, then grant the tool again."
	for _, path := range []string{"/addons", "/profiles/boss"} {
		if strings.Contains(e.req("GET", path, nil, c, nil).body, line) {
			t.Errorf("%s: the line is there with nothing stale", path)
		}
	}
	e.reportWeb(dev, notesManifest("")+"# a new version\n", "ready", "")
	for _, path := range []string{"/addons", "/profiles/boss"} {
		body := e.req("GET", path, nil, c, nil).body
		if !strings.Contains(body, "Granted for an earlier version") || !strings.Contains(body, line) {
			t.Errorf("%s: a stale grant is not explained", path)
		}
	}
}

// The words about a confidential job do not promise more than the rule does.
func TestTheConfidentialRuleSaysWhatItDoesNotCover(t *testing.T) {
	e, dev := newAddonWeb(t)
	c, _ := e.owner()
	e.reportWeb(dev, notesManifest(""), "ready", "")
	const limit = "This stops the tool, not everything a helper can do on the computer, and the answer you give is taken at its word."
	for _, path := range []string{"/addons", "/help"} {
		if body := e.req("GET", path, nil, c, nil).body; !strings.Contains(body, "That stops the tool") && !strings.Contains(body, limit) {
			t.Errorf("%s does not say what the rule leaves out", path)
		}
	}
}

// A call that ends badly after the helper started is on the job page too, with its cause and the one thing to do.
func TestTheJobPageShowsCallsThatDidNotGoThrough(t *testing.T) {
	e, dev := newAddonWeb(t)
	e.reportWeb(dev, notesManifest(""), "ready", "")
	c, tok := e.owner()
	if r := e.req("POST", "/addons/grant", url.Values{"csrf": {tok}, "profile": {"boss"}, "tool": {"notes.search"}}, c, nil); r.status != 303 {
		t.Fatalf("grant: %d", r.status)
	}
	r := e.req("POST", "/jobs", url.Values{"title": {"Calls"}, "body": {"b"}, "device": {"d1"}, "repo": {"/srv/app"}, "verify": {"./check.sh"}, "lead_profile": {"boss"}, "csrf": {tok}}, c, nil)
	if r.status != 303 {
		t.Fatalf("start: %d %s", r.status, r.body)
	}
	loc := strings.SplitN(r.header.Get("Location"), "?", 2)[0]
	if body := e.req("GET", loc, nil, c, nil).body; strings.Contains(body, "did not go through") {
		t.Fatal("a notice about nothing")
	}
	var sp []api.Spawn
	e.apiOK(dev, "", "GET", "/v1/device/spawns", nil, &sp)
	var s api.Spawn
	for _, x := range sp {
		if x.Job != nil {
			s = x
		}
	}
	if s.ID == 0 {
		t.Fatalf("no spawn for the job: %+v", sp)
	}
	line := func(id, phase, code string) api.AddonCall {
		return api.AddonCall{CallID: id, Spawn: s.ID, Agent: s.Name, Job: *s.Job, Addon: "notes", Tool: "search", Phase: phase, Code: code, Time: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	}
	e.apiOK(dev, "", "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{
		line("c1", "call", ""), line("c1", "result", "ok"),
		line("c2", "call", ""), line("c2", "result", "plugin-error"),
		line("c3", "call", ""), line("c3", "result", "plugin-error"),
		line("c4", "denied", "denied-budget"),
	}}, nil)
	body := e.req("GET", loc, nil, c, nil).body
	for _, want := range []string{"Some tool calls did not go through", "notes.search", "(2 times)", "The add-on reported an error.", "A helper used up its call limit", "Raise the limit on the grant."} {
		if !strings.Contains(body, want) {
			t.Errorf("the job page lacks %q", want)
		}
	}
	noInline(t, "job page", body)
}
