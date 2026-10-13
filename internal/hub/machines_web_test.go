package hub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
)

// has says whether a machines page lists the machine called name.
func has(page, name string) bool { return strings.Contains(page, `data-name="`+name+`"`) }

type webTeam struct {
	*webEnv
	owner, mia, ben, vera  *http.Cookie
	otok, mtok, btok, vtok string
}

// newWebTeam is a hub with the owner afif, members mia and ben, a viewer, and a machine "shared"
// that the admin token added.
func newWebTeam(t *testing.T) *webTeam {
	t.Helper()
	e := newWebEnv(t, Options{})
	w := &webTeam{webEnv: e}
	w.owner, w.otok = e.owner()
	w.mia, w.mtok = e.member("mia", "member")
	w.ben, w.btok = e.member("ben", "member")
	w.vera, w.vtok = e.member("vera", "viewer")
	var tok api.TokenResp
	e.apiOK(e.admin, "", "POST", "/v1/admin/devices", api.NameReq{Name: "shared"}, &tok)
	e.apiOK("", "", "POST", "/v1/devices/join", api.JoinReq{JoinToken: tok.Token}, nil)
	return w
}

func (w *webTeam) addAs(c *http.Cookie, tok, name string) webResp {
	return w.post("/devices", c, tok, url.Values{"name": {name}, "current_password": {goodPassword}})
}

func TestAMemberAddsAMachineAndSeesOnlyTheirOwn(t *testing.T) {
	w := newWebTeam(t)
	// The Add card shows for the owner and for a member, with the join command; not for a viewer.
	for name, c := range map[string]*http.Cookie{"owner": w.owner, "member": w.mia, "other member": w.ben} {
		if r := w.req("GET", "/devices", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "Add a machine") || !strings.Contains(r.body, `action="/devices"`) {
			t.Errorf("%s: no Add a machine card (%d)", name, r.status)
		}
	}
	if r := w.req("GET", "/devices", nil, w.vera, nil); r.status != 200 || strings.Contains(r.body, "Add a machine") || has(r.body, "shared") {
		t.Errorf("viewer's page: %d", r.status)
	}
	if r := w.addAs(w.vera, w.vtok, "vera-box"); r.status != 403 {
		t.Errorf("a viewer added a machine: %d", r.status)
	}
	// The password is asked first, and the form needs the CSRF token.
	if r := w.post("/devices", w.mia, w.mtok, url.Values{"name": {"mia-box"}, "current_password": {"wrong wrong wrong"}}); r.status != 403 {
		t.Errorf("member without the password: %d", r.status)
	}
	if r := w.req("POST", "/devices", url.Values{"name": {"mia-box"}, "current_password": {goodPassword}}, w.mia, nil); r.status == 200 || r.status == 303 {
		t.Errorf("member added a machine without the CSRF token: %d", r.status)
	}
	var n int
	w.hub.db.QueryRow(`SELECT count(*) FROM device WHERE name = 'mia-box'`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused form created a machine")
	}
	ok := w.addAs(w.mia, w.mtok, "mia-box")
	m := joinCmdRE.FindStringSubmatch(ok.body)
	if ok.status != 200 || m == nil || !strings.Contains(ok.body, "Windows (PowerShell)") || !strings.Contains(ok.body, "Linux or macOS") {
		t.Fatalf("member adds: %d %s", ok.status, ok.body)
	}
	if code, _ := w.join(m[2]); code != 200 {
		t.Fatalf("join: %d", code)
	}
	var owner string
	w.hub.db.QueryRow(`SELECT h.name FROM device d JOIN human h ON h.id = d.owner_id WHERE d.name = 'mia-box'`).Scan(&owner)
	if owner != "mia" {
		t.Fatalf("the machine belongs to %q", owner)
	}
	if !w.audited("device.add", "device:mia-box") {
		t.Fatal("device.add is not in the audit log")
	}
	m2 := joinCmdRE.FindStringSubmatch(w.addAs(w.ben, w.btok, "ben-box").body)
	w.join(m2[2])

	// Each member sees their own machine only; the owner sees all, and whose each is.
	mia := w.req("GET", "/devices", nil, w.mia, nil).body
	if !has(mia, "mia-box") || has(mia, "ben-box") || has(mia, "shared") || strings.Contains(mia, `class="desc">belongs to`) {
		t.Errorf("mia's page shows the wrong machines")
	}
	frag := w.req("GET", "/devices/fragment", nil, w.mia, nil).body
	if !has(frag, "mia-box") || has(frag, "ben-box") || has(frag, "shared") {
		t.Errorf("mia's fragment shows the wrong machines")
	}
	ben := w.req("GET", "/devices", nil, w.ben, nil).body
	if !has(ben, "ben-box") || has(ben, "mia-box") {
		t.Errorf("ben's page shows the wrong machines")
	}
	own := w.req("GET", "/devices", nil, w.owner, nil).body
	for _, want := range []string{`data-name="mia-box"`, `data-name="ben-box"`, `data-name="shared"`, `class="desc">belongs to mia`, `class="desc">belongs to ben`, "belongs to no one (shared by the owner)"} {
		if !strings.Contains(own, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
	// A member with no machine is told what to do.
	if r := w.req("GET", "/devices", nil, w.vera, nil); !strings.Contains(r.body, "No machines to show") {
		t.Error("viewer's empty page")
	}
}

func TestRevokeAndHandOverFromTheWebNeedTheRightPersonAndCSRF(t *testing.T) {
	w := newWebTeam(t)
	for _, x := range []struct {
		c    *http.Cookie
		tok  string
		name string
	}{{w.mia, w.mtok, "mia-box"}, {w.ben, w.btok, "ben-box"}} {
		m := joinCmdRE.FindStringSubmatch(w.addAs(x.c, x.tok, x.name).body)
		w.join(m[2])
	}
	// No CSRF token: nothing happens.
	for _, path := range []string{"/devices/mia-box/revoke", "/devices/mia-box/assign"} {
		if r := w.req("POST", path, url.Values{"to": {"ben"}}, w.owner, nil); r.status == 303 || r.status == 200 {
			t.Errorf("%s without the token: %d", path, r.status)
		}
	}
	// Ben cannot revoke or hand over mia's machine; mia cannot revoke the shared one.
	if r := w.post("/devices/mia-box/revoke", w.ben, w.btok, nil); r.status != 403 || !strings.Contains(r.body, "belongs to mia") {
		t.Errorf("ben revokes mia's: %d", r.status)
	}
	if r := w.post("/devices/shared/revoke", w.mia, w.mtok, nil); r.status != 403 || !strings.Contains(r.body, "shared by the owner") {
		t.Errorf("mia revokes the shared one: %d", r.status)
	}
	if r := w.post("/devices/mia-box/revoke", w.vera, w.vtok, nil); r.status != 403 {
		t.Errorf("a viewer revokes: %d", r.status)
	}
	for name, x := range map[string]struct {
		c   *http.Cookie
		tok string
	}{"member": {w.mia, w.mtok}, "viewer": {w.vera, w.vtok}} {
		if r := w.post("/devices/shared/assign", x.c, x.tok, url.Values{"to": {"mia"}}); r.status != 403 {
			t.Errorf("%s hands a machine over: %d", name, r.status)
		}
	}
	// The member's page shows Revoke for their own machine only; the owner's page has the hand-over control.
	if page := w.req("GET", "/devices", nil, w.mia, nil).body; !strings.Contains(page, `action="/devices/mia-box/revoke"`) || strings.Contains(page, "/assign") {
		t.Error("mia's controls")
	}
	own := w.req("GET", "/devices", nil, w.owner, nil).body
	if !strings.Contains(own, `action="/devices/shared/assign"`) || !strings.Contains(own, `<option>mia</option>`) || strings.Contains(own, `<option>vera</option>`) {
		t.Errorf("the owner's controls: %s", own)
	}
	// The owner hands the shared machine to mia: she sees it afterwards, ben does not.
	if r := w.post("/devices/shared/assign", w.owner, w.otok, url.Values{"to": {"mia"}}); r.status != 303 || r.header.Get("Location") != "/devices?done=assigned" {
		t.Fatalf("hand over: %d %s", r.status, r.body)
	}
	if r := w.req("GET", "/devices", nil, w.mia, nil); !has(r.body, "shared") {
		t.Error("mia does not see the machine handed to her")
	}
	if r := w.req("GET", "/devices", nil, w.ben, nil); has(r.body, "shared") {
		t.Error("ben sees a machine handed to mia")
	}
	if r := w.post("/devices/shared/assign", w.owner, w.otok, url.Values{"to": {"vera"}}); r.status != 400 {
		t.Errorf("hand over to a viewer: %d", r.status)
	}
	if !w.audited("device.assign", "device:shared") {
		t.Fatal("device.assign is not in the audit log")
	}
	// Mia revokes her own; the owner revokes ben's.
	if r := w.post("/devices/mia-box/revoke", w.mia, w.mtok, nil); r.status != 303 {
		t.Errorf("mia revokes her own: %d", r.status)
	}
	if r := w.post("/devices/ben-box/revoke", w.owner, w.otok, nil); r.status != 303 {
		t.Errorf("the owner revokes ben's: %d", r.status)
	}
}

func TestHostileNamesAreEscapedOnTheMachinesPage(t *testing.T) {
	w := newWebTeam(t)
	evil := `<script>alert(1)</script>`
	w.hub.db.Exec(`INSERT INTO human(name, token_hash, created_at, role) VALUES (?, 'x', 1, 'member')`, evil)
	w.hub.db.Exec(`INSERT INTO device(name, credential_hash, created_at, owner_id) VALUES (?, 'y', 1, (SELECT id FROM human WHERE name = ?))`, `<img src=x onerror=alert(2)>`, evil)
	for name, c := range map[string]*http.Cookie{"owner": w.owner, "mia": w.mia} {
		for _, path := range []string{"/devices", "/devices/fragment"} {
			body := w.req("GET", path, nil, c, nil).body
			if strings.Contains(body, evil) || strings.Contains(body, "<img src=x") {
				t.Errorf("%s %s: markup got through", name, path)
			}
		}
	}
	if body := w.req("GET", "/devices", nil, w.owner, nil).body; !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the owner's name is not shown escaped")
	}
}

func TestFormsOfAMemberUseOnlyTheirOwnMachines(t *testing.T) {
	w := newWebTeam(t)
	for _, x := range []struct {
		c    *http.Cookie
		tok  string
		name string
	}{{w.mia, w.mtok, "mia-box"}, {w.ben, w.btok, "ben-box"}} {
		m := joinCmdRE.FindStringSubmatch(w.addAs(x.c, x.tok, x.name).body)
		w.join(m[2])
	}
	w.apiOK(w.admin, "", "POST", "/v1/profiles", api.ProfileReq{Name: "researcher", Spec: researcher()}, nil)
	// The agents page, the new-job form and the projects page offer a member their own machines only.
	for _, path := range []string{"/agents", "/jobs/new", "/projects"} {
		page := w.req("GET", path, nil, w.mia, nil).body
		if !strings.Contains(page, `<option value="mia-box"`) && !strings.Contains(page, `value="mia-box"`) {
			t.Errorf("%s does not offer mia's machine", path)
		}
		for _, other := range []string{"ben-box", `"shared"`, `>shared<`} { // the sidebar and prose never use these words in quotes
			if strings.Contains(page, other) {
				t.Errorf("%s offers or names %s to mia", path, other)
			}
		}
	}
	// Starting an agent on someone else's machine from the form is refused in words.
	spawn := func(c *http.Cookie, tok, name, dev string) webResp {
		return w.req("POST", "/agents/spawn", url.Values{"csrf": {tok}, "name": {name}, "device": {dev}, "profile": {"researcher"}}, c, nil)
	}
	if r := spawn(w.mia, w.mtok, "m1", "ben-box"); r.status != 403 || !strings.Contains(r.body, "That machine belongs to ben") || !strings.Contains(r.body, "mia-box") {
		t.Errorf("mia on ben's machine: %d %s", r.status, r.body)
	}
	if r := spawn(w.mia, w.mtok, "m2", "shared"); r.status != 403 {
		t.Errorf("mia on the shared machine: %d", r.status)
	}
	if r := spawn(w.mia, w.mtok, "m3", "mia-box"); r.status != 303 {
		t.Errorf("mia on her own: %d", r.status)
	}
	if r := spawn(w.owner, w.otok, "o1", "ben-box"); r.status != 303 {
		t.Errorf("the owner on ben's: %d", r.status)
	}
	// The job form: a project's copies on other machines are not offered, and a job cannot be made on another's machine.
	job := func(c *http.Cookie, tok, dev string) webResp {
		return w.req("POST", "/jobs", url.Values{"csrf": {tok}, "title": {"Fix"}, "repo": {"/srv/app"}, "device": {dev}, "lead_profile": {"researcher"}}, c, nil)
	}
	if r := job(w.mia, w.mtok, "ben-box"); r.status != 403 || !strings.Contains(r.body, "belongs to ben") {
		t.Errorf("a job on ben's machine: %d %s", r.status, r.body)
	}
	if r := job(w.mia, w.mtok, "mia-box"); r.status != 303 {
		t.Errorf("a job on her own: %d", r.status)
	}
	// Cloning a project onto someone else's machine from the Projects page.
	w.apiOK(w.admin, "", "POST", "/v1/admin/projects", api.NameReq{Name: "unused"}, nil)
	w.req("POST", "/projects/attach", url.Values{"csrf": {w.otok}, "a_name": {"billing"}, "a_remote": {"https://github.com/acme/billing"}}, w.owner, nil)
	if r := w.req("POST", "/projects/billing/clone", url.Values{"csrf": {w.mtok}, "device": {"ben-box"}}, w.mia, nil); r.status != 403 || !strings.Contains(r.body, "belongs to ben") {
		t.Errorf("clone onto ben's: %d %s", r.status, r.body)
	}
	if r := w.req("POST", "/projects/billing/clone", url.Values{"csrf": {w.mtok}, "device": {"mia-box"}}, w.mia, nil); r.status != 303 {
		t.Errorf("clone onto her own: %d", r.status)
	}
	w.req("POST", "/projects/billing/clone", url.Values{"csrf": {w.otok}, "device": {"ben-box"}}, w.owner, nil)
	if page := w.req("GET", "/projects", nil, w.mia, nil).body; !strings.Contains(page, "mia-box") || strings.Contains(page, "ben-box") {
		t.Errorf("the project page's clone status shows other machines: %s", page)
	}
	if page := w.req("GET", "/projects", nil, w.owner, nil).body; !strings.Contains(page, "mia-box") || !strings.Contains(page, "ben-box") {
		t.Error("the owner does not see every machine's copy")
	}
}

// The agent's page and the terminal view are also a use of its machine: a member who opens an agent of
// someone else's machine gets the page with the reason in words, not a terminal and not an error.
func TestAnAgentPageOnSomeoneElsesMachineSaysWhyNoTerminal(t *testing.T) {
	w := newWebTeam(t)
	m := joinCmdRE.FindStringSubmatch(w.addAs(w.ben, w.btok, "ben-box").body)
	_, body := w.join(m[2])
	var j api.JoinResp
	if err := json.Unmarshal([]byte(body), &j); err != nil {
		t.Fatal(err)
	}
	w.apiOK(j.Credential, "", "POST", "/v1/agents", api.RegisterReq{Name: "bens", Kind: "claude", WakeTarget: "tmux:/tmp/s:%1"}, nil)
	page := w.req("GET", "/agents/bens", nil, w.mia, nil)
	if page.status != 200 || !strings.Contains(page.body, "That machine belongs to ben") {
		t.Fatalf("mia opens ben's agent: %d", page.status)
	}
	if r := w.post("/agents/bens/watch", w.mia, w.mtok, nil); r.status != 403 {
		t.Errorf("mia asks for ben's terminal: %d", r.status)
	}
	if r := w.req("GET", "/agents/bens/tail", nil, w.mia, nil); r.status == 200 && strings.Contains(r.body, "screen") && !strings.Contains(r.body, "belongs") {
		t.Errorf("mia reads ben's terminal: %d", r.status)
	}
	for name, c := range map[string]struct {
		c   *http.Cookie
		tok string
	}{"ben": {w.ben, w.btok}, "the owner": {w.owner, w.otok}} {
		if r := w.post("/agents/bens/watch", c.c, c.tok, nil); r.status == 403 {
			t.Errorf("%s may not watch: %d", name, r.status)
		}
	}
}
