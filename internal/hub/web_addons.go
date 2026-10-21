package hub

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"handloom/internal/addon"
	"handloom/internal/api"
)

// The Add-ons page and the grant forms (docs/direction/addons.md, "What people see"). Everyone signed in
// reads the add-ons on the computers they may see. Only the owner sees the grants and changes them: the
// API keeps the grant list behind the same action.

// addonWords are what a person reads for each status a link reports, and the badge that goes with it.
var addonWords = map[string]struct{ Words, Badge string }{
	"ready":   {"Ready", "success"},
	"changed": {"Changed since approved", "warning"},
	"down":    {"Not running", "destructive"},
	"missing": {"Missing on this computer", "warning"},
}

type addonComputer struct {
	Name, Version, Words, Badge, Detail string
	Ready                               bool
}

type addonToolView struct {
	Name, Summary string
	Leaves        bool
}

type grantView struct {
	Profile, Addon, Tool string
	Leaves               bool
	MaxCalls             int
	Stale                bool // made for a manifest no computer has now
}

// addonCard is one add-on across the computers that report it.
type addonCard struct {
	Name, Title, Summary string
	Version              string // empty when computers hold different versions
	Computers            []addonComputer
	Tools                []addonToolView
	Grants               []grantView
	Hashes               int      // how many different manifests the computers hold
	Names                []string // computers that report it, for the grant form when they differ
}

// knownTool is a tool a profile can be granted, as "add-on.tool".
type knownTool struct{ Value, Label string }

type addonsView struct {
	Cards     []addonCard
	Profiles  []string
	CanManage bool
	Form      map[string]string
}

// addonCards reads the add-ons on the computers the caller may see, one card each, with the grants when
// the caller is the owner.
func (q *webReq) addonCards() ([]addonCard, error) {
	rows, err := q.c.addonRows("", true)
	if err != nil {
		return nil, err
	}
	var grants []api.AddonGrant
	if q.isOwner() {
		if grants, err = q.c.grants(""); err != nil {
			return nil, err
		}
	}
	var cards []addonCard
	idx := map[string]int{}
	hashes := map[string]map[string]bool{}
	fromReady := map[string]bool{} // whether the tools shown come from a ready computer
	for _, r := range rows {
		i, ok := idx[r.Name]
		if !ok {
			i = len(cards)
			idx[r.Name] = i
			cards = append(cards, addonCard{Name: r.Name, Version: r.Version})
			hashes[r.Name] = map[string]bool{}
		}
		c := &cards[i]
		w, known := addonWords[r.Status]
		if !known {
			w.Words, w.Badge = r.Status, "outline"
		}
		c.Computers = append(c.Computers, addonComputer{Name: r.Computer, Version: r.Version, Words: w.Words, Badge: w.Badge, Detail: r.Detail, Ready: r.Status == "ready"})
		c.Names = append(c.Names, r.Computer)
		if r.Version != c.Version {
			c.Version = ""
		}
		if r.ManifestHash != "" {
			hashes[r.Name][r.ManifestHash] = true
		}
		// The tools come from the first manifest that reads; a ready computer's is the one to show.
		if r.Manifest != "" && (len(c.Tools) == 0 || r.Status == "ready" && !fromReady[r.Name]) {
			if m, err := addon.Parse([]byte(r.Manifest)); err == nil {
				fromReady[r.Name] = r.Status == "ready"
				c.Title, c.Summary = m.Title, m.Summary
				c.Tools = c.Tools[:0]
				for _, t := range m.Tools {
					c.Tools = append(c.Tools, addonToolView{Name: t.Name, Summary: t.Summary, Leaves: t.LeavesMachine})
				}
			}
		}
	}
	for i := range cards {
		cards[i].Hashes = len(hashes[cards[i].Name])
	}
	for _, g := range grants {
		i, ok := idx[g.Addon]
		if !ok {
			continue
		}
		cards[i].Grants = append(cards[i].Grants, grantView{Profile: g.Profile, Addon: g.Addon, Tool: g.Tool, Leaves: g.LeavesMachine, MaxCalls: g.MaxCalls,
			Stale: !hashes[g.Addon][g.ManifestHash]})
	}
	return cards, nil
}

func (q *webReq) profileNames() []string {
	var out []string
	if pl, err := profileList(q.c); err == nil {
		for _, p := range pl.([]api.ProfileInfo) {
			out = append(out, p.Name)
		}
	}
	return out
}

func (q *webReq) addonsPage(status int, errMsg string, form map[string]string) error {
	cards, err := q.addonCards()
	if err != nil {
		return err
	}
	v := &addonsView{Cards: cards, Profiles: q.profileNames(), CanManage: q.c.allow(ActAddonManage) == nil, Form: form}
	if v.Form == nil {
		v.Form = map[string]string{}
	}
	q.page(status, "addons", pageData{Title: "Add-ons", Error: errMsg, Notice: grantNotice[q.r.URL.Query().Get("done")], Extra: v})
	return nil
}

var grantNotice = map[string]string{
	"granted": "Granted. Helpers started from now on can use it; a helper that is already running keeps what it started with.",
	"revoked": "Revoked. Helpers started from now on do not get it; a helper that is already running keeps what it started with.",
}

func (h *Hub) webAddons(q *webReq) error { return q.addonsPage(200, "", nil) }

// knownTools is every tool of every add-on the owner can see, for the profile page's grant form.
func knownTools(cards []addonCard) []knownTool {
	var out []knownTool
	for _, c := range cards {
		for _, t := range c.Tools {
			out = append(out, knownTool{c.Name + "." + t.Name, c.Name + " · " + t.Name})
		}
	}
	return out
}

// addonFailed shows the page the form was on again, with the refusal in words and what was typed. from is
// "profile:NAME" for the profile page and empty for the Add-ons page.
func (q *webReq) addonFailed(err error, from string, form map[string]string) error {
	var ae *apiError
	status, msg := 500, "Something went wrong."
	if errors.As(err, &ae) {
		status, msg = ae.status, ae.msg
	}
	q.c.tx.Rollback()
	if ae != nil && ae.status == 403 {
		q.c.h.auditDenied(q.c.p, q.r, ae)
	}
	tx, terr := q.c.h.db.Begin()
	if terr != nil {
		return terr
	}
	defer tx.Rollback()
	q.c.tx = tx
	if name, ok := strings.CutPrefix(from, "profile:"); ok {
		if p, perr := q.c.profileVersion(name, 0); perr == nil {
			q.r.SetPathValue("name", name)
			vs, _ := profileVersions(q.c)
			list, _ := vs.([]api.ProfileInfo)
			q.profileForm(status, msg, &profileView{Info: p.info(), Spec: p.spec, Versions: list, Name: p.name, Hash: p.hash, GrantForm: form})
			return errHandled
		}
	}
	q.addonsPage(status, msg, form)
	return errHandled
}

// grantsDone sends the person back to the page they came from.
func (q *webReq) grantsDone(from, done string) error {
	to := "/addons"
	if name, ok := strings.CutPrefix(from, "profile:"); ok {
		to = "/profiles/" + url.PathEscape(name)
	}
	http.Redirect(q.w, q.r, to+"?done="+done, http.StatusSeeOther)
	return nil
}

// splitToolValue reads "add-on.tool", the value both grant forms send.
func splitToolValue(s string) (string, string, error) {
	a, t, ok := strings.Cut(s, ".")
	if !ok || a == "" || t == "" {
		return "", "", badRequest("Choose a tool.")
	}
	return a, t, nil
}

func (h *Hub) webAddonGrant(q *webReq) error {
	f := q.r.PostForm
	from, form := f.Get("from"), f1(f)
	if err := q.c.allow(ActAddonManage); err != nil {
		return q.addonFailed(err, from, form)
	}
	name, tool, err := splitToolValue(f.Get("tool"))
	if err != nil {
		return q.addonFailed(err, from, form)
	}
	req := api.AddonGrantReq{Addon: name, Tool: tool, Computer: f.Get("computer")}
	leaves := f.Get("leaves") != "no"
	req.LeavesMachine = &leaves
	if s := strings.TrimSpace(f.Get("max_calls")); s != "" {
		n, perr := strconv.Atoi(s)
		if perr != nil {
			return q.addonFailed(badRequest("The call limit must be a whole number, such as 50."), from, form)
		}
		req.MaxCalls = n
	}
	if _, err := q.c.grantTool(f.Get("profile"), req); err != nil {
		return q.addonFailed(err, from, form)
	}
	return q.grantsDone(from, "granted")
}

func (h *Hub) webAddonRevoke(q *webReq) error {
	f := q.r.PostForm
	from, form := f.Get("from"), f1(f)
	if err := q.c.allow(ActAddonManage); err != nil {
		return q.addonFailed(err, from, form)
	}
	name, tool, err := splitToolValue(f.Get("tool"))
	if err != nil {
		return q.addonFailed(err, from, form)
	}
	if err := q.c.revokeTool(f.Get("profile"), name, tool); err != nil {
		return q.addonFailed(err, from, form)
	}
	return q.grantsDone(from, "revoked")
}
