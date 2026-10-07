package cli

import (
	"fmt"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
)

func profileLine(p api.ProfileInfo) string {
	skills := ""
	if len(p.Skills) > 0 {
		skills = "  skills: " + strings.Join(p.Skills, ", ")
	}
	return fmt.Sprintf("%-18s v%-3d %-7s %-6s tools: %s%s", p.Name, p.Version, p.Kind, p.Runtime, strings.Join(p.Tools, ","), skills)
}

// profiles lists the library.
func (e *env) profiles(args []string) error {
	fs := e.flags("profiles")
	if _, err := fs.need(args, 0, 0, "profiles"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var list []api.ProfileInfo
	if err := c.Get("/v1/profiles", &list); err != nil {
		return err
	}
	e.print(list, func() {
		if len(list) == 0 {
			fmt.Fprintln(e.out, "No profiles. Make one: handloom profile new <directory>")
		}
		for _, p := range list {
			fmt.Fprintln(e.out, profileLine(p))
		}
	})
	return nil
}

// profile: new (from a directory), check, show, export, versions.
func (e *env) profile(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: handloom profile new <dir> | add <starter> --kind K --runtime R [--name N] [--adapt] | check <dir> | show <name> [--version N] | export <name> <dir> | versions <name>")
	}
	sub := args[0]
	fs := e.flags("profile " + sub)
	version := fs.Int("version", 0, "a version (default: the newest)")
	kind := fs.String("kind", "", "add: the agent CLI, claude or codex")
	runtime := fs.String("runtime", "", "add: where the model runs, cloud or local")
	model := fs.String("model", "", "add: a model name (default: the CLI's own)")
	asName := fs.String("name", "", "add: a name for the profile (default: the starter's)")
	adapt := fs.Bool("adapt", false, "add: leave out what the CLI cannot do (Codex has no skills or denied commands) instead of refusing")
	pos, err := fs.parse(args[1:])
	if err != nil {
		return err
	}
	need := func(n int, synopsis string) error {
		if len(pos) != n {
			return usageErr("usage: handloom profile %s", synopsis)
		}
		return nil
	}
	switch sub {
	case "add":
		if err := need(1, "add <starter> --kind claude|codex --runtime cloud|local [--model M] [--name N] [--adapt]"); err != nil {
			return err
		}
		c, err := conn()
		if err != nil {
			return err
		}
		var resp api.StarterAddResp
		if err := c.Post("/v1/starters/"+pos[0]+"/add", api.StarterAddReq{Name: *asName, Kind: *kind, Runtime: *runtime, Model: *model, Adapt: *adapt}, &resp); err != nil {
			return err
		}
		e.print(resp, func() {
			fmt.Fprintln(e.out, "Added "+profileLine(resp.Profile))
			for _, a := range resp.Adapted {
				fmt.Fprintln(e.out, "  "+a)
			}
			for _, w := range resp.Warnings {
				fmt.Fprintln(e.out, "  worth a look: "+w)
			}
		})
		return nil
	case "check":
		if err := need(1, "check <dir>"); err != nil {
			return err
		}
		name, spec, err := profile.ReadDir(pos[0])
		if err != nil {
			return err
		}
		bad := profile.Normalize(spec)
		if !profile.ValidName(name) {
			bad = append(bad, fmt.Sprintf("bad profile name %q", name))
		}
		if len(bad) > 0 {
			for _, b := range bad {
				fmt.Fprintln(e.out, "problem: "+b)
			}
			return fmt.Errorf("%d problem(s) in %s", len(bad), pos[0])
		}
		fmt.Fprintf(e.out, "%s is a valid profile (hash %.12s).\nWhat %s will and will not enforce:\n", name, profile.Hash(spec), map[string]string{"claude": "Claude Code", "codex": "Codex"}[spec.Kind])
		for _, line := range profile.Enforcement(spec) {
			fmt.Fprintln(e.out, "  "+line)
		}
		return nil
	case "new":
		if err := need(1, "new <dir>"); err != nil {
			return err
		}
		name, spec, err := profile.ReadDir(pos[0])
		if err != nil {
			return err
		}
		c, err := conn()
		if err != nil {
			return err
		}
		var p api.ProfileFull
		if err := c.Post("/v1/profiles", api.ProfileReq{Name: name, Spec: *spec}, &p); err != nil {
			return err
		}
		e.print(p, func() { fmt.Fprintf(e.out, "%s: version %d, hash %.12s\n", p.Name, p.Version, p.Hash) })
		return nil
	case "show", "export":
		n := 1
		if sub == "export" {
			n = 2
		}
		if err := need(n, sub+" <name>"+map[bool]string{true: " <dir>", false: ""}[sub == "export"]); err != nil {
			return err
		}
		c, err := conn()
		if err != nil {
			return err
		}
		var p api.ProfileFull
		path := "/v1/profiles/" + pos[0]
		if *version > 0 {
			path += "?version=" + strconv.Itoa(*version)
		}
		if err := c.Get(path, &p); err != nil {
			return err
		}
		if sub == "export" {
			if err := profile.WriteDir(pos[1], p.Name, &p.Spec); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "Wrote %s version %d to %s\n", p.Name, p.Version, pos[1])
			return nil
		}
		e.print(p, func() {
			fmt.Fprintln(e.out, profileLine(p.ProfileInfo))
			if p.Spec.Description != "" {
				fmt.Fprintln(e.out, p.Spec.Description)
			}
			if len(p.Spec.Tools.DenyCommands) > 0 {
				fmt.Fprintln(e.out, "denied commands: "+strings.Join(p.Spec.Tools.DenyCommands, ", "))
			}
			if p.Spec.Prompt != "" {
				fmt.Fprintln(e.out, "\n"+p.Spec.Prompt)
			}
			fmt.Fprintf(e.out, "\nhash %s\n", p.Hash)
		})
		return nil
	case "versions":
		if err := need(1, "versions <name>"); err != nil {
			return err
		}
		c, err := conn()
		if err != nil {
			return err
		}
		var vs []api.ProfileInfo
		if err := c.Get("/v1/profiles/"+pos[0]+"/versions", &vs); err != nil {
			return err
		}
		e.print(vs, func() {
			for _, p := range vs {
				fmt.Fprintf(e.out, "v%-3d %.12s  %s by %s\n", p.Version, p.Hash, p.CreatedAt.Local().Format("2006-01-02 15:04"), p.CreatedBy)
			}
		})
		return nil
	}
	return usageErr("unknown profile verb %q", sub)
}

// starters lists the starter library, or shows one starter.
func (e *env) starters(args []string) error {
	fs := e.flags("starters")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		var s api.StarterFull
		if err := c.Get("/v1/starters/"+pos[0], &s); err != nil {
			return err
		}
		e.print(s, func() {
			fmt.Fprintf(e.out, "%s (%s)\n%s\n\n%s\n\ntools: %s\n", s.Title, s.Group, s.Summary, s.WhatItCanDo, strings.Join(s.Tools, ", "))
			if len(s.Skills) > 0 {
				fmt.Fprintf(e.out, "skills: %s\n", strings.Join(s.Skills, ", "))
			}
			if s.Note != "" {
				fmt.Fprintf(e.out, "note: %s\n", s.Note)
			}
			fmt.Fprintf(e.out, "\nInstructions:\n%s\n\nAdd it: handloom profile add %s --kind claude --runtime cloud\n", s.Spec.Prompt, s.Name)
		})
		return nil
	}
	if len(pos) > 1 {
		return usageErr("usage: handloom starters [name]")
	}
	var list []api.StarterInfo
	if err := c.Get("/v1/starters", &list); err != nil {
		return err
	}
	e.print(list, func() {
		group := ""
		for _, s := range list {
			if s.Group != group {
				group = s.Group
				fmt.Fprintf(e.out, "\n%s\n", group)
			}
			mark := " "
			if s.Added {
				mark = "*"
			}
			fmt.Fprintf(e.out, " %s %-24s %s\n", mark, s.Name, s.Summary)
		}
		fmt.Fprintln(e.out, "\n* already in your profiles. Show one: handloom starters <name>. Add one: handloom profile add <name> --kind claude --runtime cloud")
	})
	return nil
}
