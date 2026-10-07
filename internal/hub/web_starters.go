package hub

import (
	"errors"
	"net/http"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/starters"
)

type startersView struct {
	Groups  []starterGroup
	Skills  []starters.Skill
	CanEdit bool
	Count   int
}

func (h *Hub) webStarters(q *webReq) error {
	in, err := q.c.starterInfos()
	if err != nil {
		return err
	}
	q.page(200, "starters", pageData{Title: "Starter library", Extra: &startersView{Groups: groupStarters(in), Skills: starters.Skills(), CanEdit: q.isOwner(), Count: len(in)}})
	return nil
}

type starterView struct {
	Info     api.StarterInfo
	Summary  string
	Prompt   string
	Skills   []starters.Skill
	Enforce  []string
	Write    []string
	Deny     []string
	CanEdit  bool
	Form     map[string]string
	Problems []string
	CanAdapt bool
	Adapted  []string
	Kinds    []kindOpt
}

func (q *webReq) starterPage(status int, name string, form map[string]string, problems []string, canAdapt bool) error {
	infos, err := q.c.starterInfos()
	if err != nil {
		return err
	}
	var info *api.StarterInfo
	for i := range infos {
		if infos[i].Name == name {
			info = &infos[i]
		}
	}
	if info == nil {
		q.page(404, "message", pageData{Title: "Not found", Error: "No such starter."})
		return errHandled
	}
	spec, err := starters.Template(name)
	if err != nil {
		return err
	}
	v := &starterView{Info: *info, Summary: profile.Summary(spec), Prompt: spec.Prompt, Write: spec.Write, Deny: spec.Tools.DenyCommands,
		CanEdit: q.isOwner(), Form: form, Problems: problems, CanAdapt: canAdapt, Kinds: kindOptions()}
	for _, sk := range info.Skills {
		if s, ok := starters.GetSkill(sk); ok {
			v.Skills = append(v.Skills, s)
		}
	}
	claude := *spec
	claude.Kind = "claude"
	v.Enforce = profile.Enforcement(&claude)
	q.page(status, "starter", pageData{Title: info.Title, Extra: v, Narrow: true})
	return nil
}

func (h *Hub) webStarter(q *webReq) error {
	form := map[string]string{"name": q.r.PathValue("name")}
	if s := starters.Get(q.r.PathValue("name")); s != nil && s.RecommendRuntime != "" {
		form["recommend"] = s.RecommendRuntime
	}
	return q.starterPage(200, q.r.PathValue("name"), form, nil, false)
}

func (h *Hub) webStarterAdd(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner adds profiles.")
	}
	starter := q.r.PathValue("name")
	if err := q.stepUp(); err != nil {
		return q.starterError(starter, err, nil)
	}
	f := q.r.PostForm
	req := api.StarterAddReq{Name: strings.TrimSpace(f.Get("name")), Kind: f.Get("kind"), Runtime: f.Get("runtime"), Model: strings.TrimSpace(f.Get("model")), Adapt: f.Get("adapt") == "1"}
	form := map[string]string{"name": req.Name, "kind": req.Kind, "runtime": req.Runtime, "model": req.Model}
	resp, problems, err := q.c.addStarter(starter, req)
	if err != nil {
		return q.starterError(starter, err, form)
	}
	if len(problems) > 0 {
		// Codex cannot do some things a starter asks for: say which, and offer to leave them out.
		adaptable := req.Kind != "" && !req.Adapt
		return q.starterPage(400, starter, form, problems, adaptable)
	}
	_ = resp
	http.Redirect(q.w, q.r, "/profiles/"+firstNonEmpty(req.Name, starter)+"?done=added", http.StatusSeeOther)
	return nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (q *webReq) starterError(name string, err error, form map[string]string) error {
	var ae *apiError
	status, msg := 500, "Something went wrong."
	if errors.As(err, &ae) {
		status, msg = ae.status, ae.msg
	}
	if form == nil {
		form = map[string]string{"name": name}
	}
	return q.starterPage(status, name, form, []string{msg}, false)
}

// kindOpt is one agent CLI in a select, with what makes it different.
type kindOpt struct{ Value, Label string }

func kindOptions() []kindOpt {
	desc := map[string]string{
		"claude":   "Claude Code: cloud models, refuses the shell commands you name, loads skills",
		"codex":    "Codex: cloud models, sandboxed files and network, cannot refuse commands",
		"omp":      "OMP: any model, including your local one; cannot refuse commands",
		"pi":       "Pi: any model, including your local one; no web tool",
		"opencode": "OpenCode: any model; refuses the shell commands you name",
	}
	var out []kindOpt
	for _, k := range profile.Kinds {
		out = append(out, kindOpt{k, desc[k]})
	}
	return out
}
