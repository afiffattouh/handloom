package hub

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// ---- profiles ----

type profileView struct {
	Info      api.ProfileInfo
	Spec      profile.Spec
	Enforce   []string
	Versions  []api.ProfileInfo
	Skills    []skillView
	Deny      string
	Tool      map[string]bool
	CanEdit   bool
	IsNew     bool
	Hash      string
	Name      string
	Fresh     bool
	KnownKind string
}

type skillView struct {
	Name   string
	Text   string
	Others []string // other files in the skill, kept as they are
}

func (q *webReq) profilesPage(status int, errMsg string) error {
	rows, err := profileList(q.c)
	if err != nil {
		return err
	}
	q.page(status, "profiles", pageData{Title: "Profiles", Error: errMsg,
		Notice: map[string]string{"saved": "Profile saved."}[q.r.URL.Query().Get("done")],
		Extra:  map[string]any{"List": rows, "CanEdit": q.isOwner()}})
	return nil
}

func (h *Hub) webProfiles(q *webReq) error { return q.profilesPage(200, "") }

func skillViews(s *profile.Spec) []skillView {
	by := map[string]*skillView{}
	var names []string
	for _, f := range s.Skills {
		n := strings.SplitN(f.Path, "/", 2)[0]
		v := by[n]
		if v == nil {
			v = &skillView{Name: n}
			by[n] = v
			names = append(names, n)
		}
		if f.Path == n+"/SKILL.md" {
			v.Text = f.Content
		} else {
			v.Others = append(v.Others, strings.TrimPrefix(f.Path, n+"/"))
		}
	}
	sort.Strings(names)
	out := make([]skillView, 0, len(names))
	for _, n := range names {
		out = append(out, *by[n])
	}
	return out
}

func (q *webReq) profileForm(status int, errMsg string, v *profileView) error {
	v.CanEdit = q.isOwner()
	v.Tool = map[string]bool{}
	for _, t := range v.Spec.Tools.Allow {
		v.Tool[t] = true
	}
	v.Deny = strings.Join(v.Spec.Tools.DenyCommands, "\n")
	v.Skills = skillViews(&v.Spec)
	if !v.IsNew {
		v.Enforce = profile.Enforcement(&v.Spec)
	}
	title := "New profile"
	if !v.IsNew {
		title = v.Name
	}
	q.page(status, "profile", pageData{Title: title, Error: errMsg, Extra: v,
		Notice: map[string]string{"saved": "Saved. New agents started from this profile use this version; running ones keep theirs."}[q.r.URL.Query().Get("done")]})
	return nil
}

func (h *Hub) webProfileNewForm(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner writes profiles.")
	}
	return q.profileForm(200, "", &profileView{IsNew: true, Spec: profile.Spec{Kind: "claude", Runtime: profile.Cloud, Tools: profile.Tools{Allow: []string{"read"}}}})
}

func (h *Hub) webProfileShow(q *webReq) error {
	version, _ := strconv.Atoi(q.r.URL.Query().Get("version"))
	p, err := q.c.profileVersion(q.r.PathValue("name"), version)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			q.page(ae.status, "message", pageData{Title: "Not found", Error: ae.msg})
			return errHandled
		}
		return err
	}
	vs, err := profileVersions(q.c)
	if err != nil {
		return err
	}
	return q.profileForm(200, "", &profileView{Info: p.info(), Spec: p.spec, Versions: vs.([]api.ProfileInfo), Name: p.name, Hash: p.hash})
}

// specFromForm builds a profile from the form. Existing skills keep their
// other files; a skill whose text is emptied is removed.
func specFromForm(q *webReq, old *profile.Spec) profile.Spec {
	f := q.r.PostForm
	s := profile.Spec{Description: strings.TrimSpace(f.Get("description")), Kind: "claude",
		Runtime: f.Get("runtime"), Model: strings.TrimSpace(f.Get("model")), Prompt: strings.TrimSpace(strings.ReplaceAll(f.Get("prompt"), "\r\n", "\n"))}
	s.Tools.Allow = f["tool"]
	for _, line := range strings.Split(strings.ReplaceAll(f.Get("deny"), "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			s.Tools.DenyCommands = append(s.Tools.DenyCommands, line)
		}
	}
	keep := map[string][]profile.File{}
	if old != nil {
		for _, file := range old.Skills {
			n := strings.SplitN(file.Path, "/", 2)[0]
			if file.Path != n+"/SKILL.md" {
				keep[n] = append(keep[n], file)
			}
		}
	}
	names, texts := f["skill_name"], f["skill_text"]
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || i >= len(texts) || strings.TrimSpace(texts[i]) == "" {
			continue
		}
		s.Skills = append(s.Skills, profile.File{Path: n + "/SKILL.md", Content: strings.ReplaceAll(texts[i], "\r\n", "\n")})
		s.Skills = append(s.Skills, keep[n]...)
	}
	return s
}

func (h *Hub) webProfileSave(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner writes profiles.")
	}
	if err := q.stepUp(); err != nil {
		return q.profileError(err, nil)
	}
	name := strings.TrimSpace(q.r.PostForm.Get("name"))
	var old *profile.Spec
	isNew := q.r.PathValue("name") == ""
	if !isNew {
		name = q.r.PathValue("name")
		if cur, err := q.c.profileVersion(name, 0); err == nil {
			old = &cur.spec
		}
	}
	spec := specFromForm(q, old)
	if _, err := q.c.saveProfile(name, spec); err != nil {
		return q.profileError(err, &profileView{IsNew: isNew, Name: name, Spec: spec})
	}
	http.Redirect(q.w, q.r, "/profiles/"+name+"?done=saved", http.StatusSeeOther)
	return nil
}

func (q *webReq) profileError(err error, v *profileView) error {
	var ae *apiError
	status, msg := 500, "Something went wrong."
	if errors.As(err, &ae) {
		status, msg = ae.status, ae.msg
	}
	if v == nil {
		return q.profilesPage(status, msg)
	}
	if !v.IsNew {
		v.Info.Name = v.Name
	}
	return q.profileForm(status, msg, v)
}

// ---- agents and spawns ----

type agentsView struct {
	Agents   []agentView
	Spawns   []api.Spawn
	Profiles []api.ProfileInfo
	Devices  []string
	CanSpawn bool
	Form     map[string]string
}

func (q *webReq) agentsPage(status int, errMsg string, form map[string]string) error {
	d, err := q.c.digest()
	if err != nil {
		return err
	}
	v := &agentsView{CanSpawn: q.human.Role != store.RoleViewer, Form: form}
	for _, a := range d.Agents {
		v.Agents = append(v.Agents, agentView{a, stateGlyph(a.State), ago(q.now, a.StateAt)})
	}
	sp, err := spawnList(q.c)
	if err != nil {
		return err
	}
	all := sp.([]api.Spawn)
	for i := len(all) - 1; i >= 0 && len(v.Spawns) < 8; i-- {
		v.Spawns = append(v.Spawns, all[i])
	}
	pl, err := profileList(q.c)
	if err != nil {
		return err
	}
	v.Profiles = pl.([]api.ProfileInfo)
	ds, err := store.ListDevices(q.c.tx)
	if err != nil {
		return err
	}
	for _, dv := range ds {
		if dv.Joined && !dv.Revoked.Valid {
			v.Devices = append(v.Devices, dv.Name)
		}
	}
	q.page(status, "agents", pageData{Title: "Agents", Error: errMsg, Extra: v,
		Notice: map[string]string{"spawn": "Requested. The device's link starts it within a few seconds; it shows below."}[q.r.URL.Query().Get("done")]})
	return nil
}

func (h *Hub) webAgents(q *webReq) error { return q.agentsPage(200, "", nil) }

func (h *Hub) webSpawn(q *webReq) error {
	if err := q.c.allow(ActSpawn); err != nil {
		return q.refuse("You cannot start agents.")
	}
	f := q.r.PostForm
	req := api.SpawnReq{Name: strings.TrimSpace(f.Get("name")), Profile: f.Get("profile"), Device: f.Get("device"), Model: strings.TrimSpace(f.Get("model"))}
	if j := strings.TrimSpace(f.Get("job")); j != "" {
		n, err := strconv.ParseInt(strings.TrimPrefix(j, "#"), 10, 64)
		if err != nil {
			return q.agentsPage(400, "The job must be a number.", f1(f))
		}
		req.Job = n
	}
	// spawnNew reads its request from the body; the web form calls the same code through a small adapter.
	if _, err := q.c.spawnFromRequest(req); err != nil {
		var ae *apiError
		status, msg := 500, "Something went wrong."
		if errors.As(err, &ae) {
			status, msg = ae.status, ae.msg
		}
		return q.agentsPage(status, msg, f1(f))
	}
	http.Redirect(q.w, q.r, "/agents?done=spawn", http.StatusSeeOther)
	return nil
}

func f1(f map[string][]string) map[string]string {
	out := map[string]string{}
	for k, v := range f {
		if len(v) > 0 && k != "csrf" && k != "current_password" {
			out[k] = v[0]
		}
	}
	return out
}

var _ = fmt.Sprint
