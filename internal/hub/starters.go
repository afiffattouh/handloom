package hub

import (
	"sort"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/starters"
)

// The starter library: ready-made profiles and skills that ship with Handloom.
// Adding one makes an ordinary profile (through saveProfile, so it is hashed,
// versioned and audited like any other); the library itself is read-only.

func (c *call) starterInfos() ([]api.StarterInfo, error) {
	all, err := starters.All()
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	rows, err := c.tx.Query(`SELECT DISTINCT name FROM profile`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		have[n] = true
	}
	rows.Close()
	out := make([]api.StarterInfo, 0, len(all))
	for _, st := range all {
		tpl, err := starters.Template(st.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, api.StarterInfo{Name: st.Name, Title: st.Title, Group: st.Group, Summary: st.Summary, Core: st.Core, Skills: st.Skills,
			Tools: tpl.Tools.Allow, RecommendRuntime: st.RecommendRuntime, Note: st.Note, Added: have[st.Name]})
	}
	return out, nil
}

func starterList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	return c.starterInfos()
}

func starterGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	name := c.r.PathValue("name")
	infos, err := c.starterInfos()
	if err != nil {
		return nil, err
	}
	for _, in := range infos {
		if in.Name == name {
			spec, err := starters.Template(name)
			if err != nil {
				return nil, err
			}
			view := *spec
			return api.StarterFull{StarterInfo: in, Spec: view, WhatItCanDo: profile.Summary(spec)}, nil
		}
	}
	return nil, notFound("no starter %q", name)
}

// addStarter is the work of adding a starter, for the API and the web page.
func (c *call) addStarter(starter string, req api.StarterAddReq) (*api.StarterAddResp, []string, error) {
	if starters.Get(starter) == nil {
		return nil, nil, notFound("no starter %q", starter)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = starter
	}
	if !profile.ValidName(name) {
		return nil, nil, badRequest("bad profile name %q: use letters, digits, '.', '_' or '-', at most 40 characters", name)
	}
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM profile WHERE name = ?`, name).Scan(&n); err != nil {
		return nil, nil, err
	}
	if n > 0 {
		return nil, nil, conflict("you already have a profile called %q: open it, or add this one under another name", name)
	}
	res, err := starters.Build(starter, starters.Choice{Kind: req.Kind, Runtime: req.Runtime, Model: req.Model, Adapt: req.Adapt})
	if err != nil {
		return nil, nil, err
	}
	if len(res.Problems) > 0 {
		return nil, res.Problems, nil
	}
	row, err := c.saveProfile(name, *res.Spec)
	if err != nil {
		return nil, nil, err
	}
	if err := c.audit("profile.starter", "profile:"+name, map[string]any{"starter": starter, "adapted": res.Adapted}); err != nil {
		return nil, nil, err
	}
	return &api.StarterAddResp{Profile: row.info(), Adapted: res.Adapted, Warnings: res.Warnings}, nil, nil
}

func starterAdd(c *call) (any, error) {
	if err := c.ownerOnly("adding profiles"); err != nil {
		return nil, err
	}
	var req api.StarterAddReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	resp, problems, err := c.addStarter(c.r.PathValue("name"), req)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, badRequest("it cannot be added as chosen: %s", strings.Join(problems, "; "))
	}
	return resp, nil
}

func groupStarters(in []api.StarterInfo) []starterGroup {
	by := map[string]*starterGroup{}
	var order []string
	for _, s := range in {
		g := by[s.Group]
		if g == nil {
			g = &starterGroup{Name: s.Group}
			by[s.Group] = g
			order = append(order, s.Group)
		}
		g.Items = append(g.Items, s)
	}
	out := make([]starterGroup, 0, len(order))
	for _, n := range order {
		out = append(out, *by[n])
	}
	return out
}

type starterGroup struct {
	Name  string
	Items []api.StarterInfo
}

func sortedSkillNames() []starters.Skill { return starters.Skills() }

var _ = sort.Strings
