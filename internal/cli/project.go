package cli

import (
	"fmt"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

// Projects (D104): a name and a shared git remote that any machine can get on
// demand. Cloning and creating run on a machine under its own git and gh login.

var (
	cloneWait   = 5 * time.Minute // how long `project clone` and `job new --project` wait for a machine to fetch a copy
	createWait  = 2 * time.Minute // how long `project new --create` waits
	projectPoll = time.Second
)

func (e *env) project(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: handloom project new|list|show|clone|check|checks (see `handloom help`)")
	}
	sub := args[0]
	fs := e.flags("project " + sub)
	remote := fs.String("remote", "", "new: the address of a repository that exists (https://..., ssh://... or git@host:owner/name; no password or token in it)")
	create := fs.Bool("create", false, "new: make the repository with gh on --device, under that machine's login")
	owner := fs.String("owner", "", "new --create: the GitHub user or organisation (default: the account gh is logged in as)")
	public := fs.Bool("public", false, "new --create: make it public (private otherwise)")
	device := fs.String("device", "", "the machine that creates the repository or fetches the copy")
	base := fs.String("base", "", "new: the main branch (default: the repository's own)")
	preset := fs.String("preset", "", "checks: the kind of project, generic (default) or web")
	apply := fs.Bool("apply", false, "checks: put the prepared checks on the main branch")
	discard := fs.Bool("discard", false, "checks: throw the prepared checks away")
	update := fs.Bool("update", false, "checks: refresh the kit's own files in a project that already has the checks")
	pos, err := fs.parse(args[1:])
	if err != nil {
		return err
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	switch {
	case sub == "add" && len(pos) == 1:
		if err := c.Post("/v1/admin/projects", api.NameReq{Name: pos[0]}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Tenancy project %s added.\n", pos[0])
	case sub == "tenants" && len(pos) == 0:
		var projects []api.Project
		if err := c.Get("/v1/projects", &projects); err != nil {
			return err
		}
		e.print(projects, func() {
			for _, p := range projects {
				fmt.Fprintln(e.out, p.Name)
			}
		})
	case sub == "list" && len(pos) == 0:
		var cbs []api.Codebase
		if err := c.Get("/v1/codebases", &cbs); err != nil {
			return err
		}
		e.print(cbs, func() {
			if len(cbs) == 0 {
				fmt.Fprintln(e.out, "No projects yet. Make one the hub holds with: handloom project new NAME   (or attach a repository: --remote URL; or make one on GitHub: --create)")
			}
			for _, p := range cbs {
				fmt.Fprintln(e.out, projectLine(p))
			}
		})
	case sub == "check" && (len(pos) == 1 || len(pos) == 2):
		// handloom project check NAME [COMMAND]   show it, or set it; an empty string "" clears it
		var p api.Codebase
		if len(pos) == 1 {
			if err := c.Get("/v1/codebases/"+pos[0], &p); err != nil {
				return err
			}
		} else if err := c.Post("/v1/codebases/"+pos[0]+"/check", api.CodebaseCheckReq{Check: pos[1]}, &p); err != nil {
			return err
		}
		e.print(p, func() {
			if p.Check == "" {
				fmt.Fprintf(e.out, "%s has no check of its own: a job asks for one. Set it with: handloom project check %s \"./check\"\n", p.Name, p.Name)
				return
			}
			fmt.Fprintf(e.out, "%s: jobs are checked with: %s\n", p.Name, p.Check)
		})
	case sub == "checks" && len(pos) == 1:
		// handloom project checks NAME [--preset web|--update]   prepare the kit's checks for a project the hub holds, or show them
		// handloom project checks NAME --apply          put them on the main branch (a person); --discard throws them away
		var st api.ChecksState
		var err error
		switch {
		case *apply && *discard:
			return usageErr("--apply and --discard cannot be used together")
		case *apply:
			err = c.Post("/v1/codebases/"+pos[0]+"/checks", api.ChecksReq{Action: "apply"}, &st)
		case *discard:
			err = c.Post("/v1/codebases/"+pos[0]+"/checks", api.ChecksReq{Action: "discard"}, &st)
		default:
			if err = c.Get("/v1/codebases/"+pos[0]+"/checks", &st); err == nil && (st.State == "none" || (st.State == "has" && *update)) {
				err = c.Post("/v1/codebases/"+pos[0]+"/checks", api.ChecksReq{Action: "prepare", Preset: *preset}, &st)
			}
		}
		if err != nil {
			return err
		}
		e.print(st, func() {
			switch {
			case st.Applied:
				fmt.Fprintf(e.out, "The checks are on the main branch of %s. Jobs started from it are checked with ./check.\n", st.Project)
			case st.State == "has":
				fmt.Fprintf(e.out, "%s has the checks: ./check is on its main branch.\n", st.Project)
				if st.Outdated {
					fmt.Fprintf(e.out, "Its kit is older than this hub's. Refresh it with: handloom project checks %s --update\n", st.Project)
				}
			case st.State == "ready":
				what := "add"
				if st.Update {
					what = "refresh"
				}
				fmt.Fprintf(e.out, "Checks are prepared for %s (not on the main branch yet). They "+what+" %d files:\n  %s\n\nWhat ./check will run (factory.toml):\n%s\n", st.Project, len(st.Files), strings.Join(st.Files, "\n  "), st.Toml)
				for _, n := range st.Notes {
					fmt.Fprintln(e.out, "note: "+n)
				}
				fmt.Fprintf(e.out, "Put them on the main branch with: handloom project checks %s --apply   (or throw them away: --discard)\n", st.Project)
			default:
				fmt.Fprintf(e.out, "%s has no prepared checks.\n", st.Project)
			}
		})
	case sub == "show" && len(pos) == 1:
		var p api.Codebase
		if err := c.Get("/v1/codebases/"+pos[0], &p); err != nil {
			return err
		}
		e.print(p, func() {
			fmt.Fprintln(e.out, projectLine(p))
			if p.Hosted {
				fmt.Fprintf(e.out, "  held by Handloom: %s on the hub's disk (include the git folder of the data directory in backups)\n", sizeWords(p.SizeBytes))
			}
			for _, k := range p.Clones {
				line := fmt.Sprintf("  %s: %s", k.Device, k.Status)
				if k.Path != "" {
					line += " at " + k.Path
				}
				if k.Detail != "" {
					line += " (" + k.Detail + ")"
				}
				fmt.Fprintln(e.out, line)
			}
		})
	case sub == "new" && len(pos) == 1:
		if *remote != "" && *create {
			return usageErr("usage: handloom project new NAME [--device D]   or   handloom project new NAME --remote URL [--base B] [--device D]   or   handloom project new NAME --create [--owner O] [--public] [--device D]")
		}
		if *remote == "" && !*create {
			// No address and no GitHub: the hub holds the code itself (D106).
			if *owner != "" || *public || *base != "" {
				return usageErr("--owner and --public go with --create, and --base with --remote; a project the hub holds takes only a name")
			}
			var p api.Codebase
			if err := c.Post("/v1/codebases/host", api.CodebaseHostReq{Name: pos[0]}, &p); err != nil {
				return err
			}
			e.print(p, func() {
				fmt.Fprintf(e.out, "Created project %s on the hub (the hub holds its code; no GitHub needed): %s\n", p.Name, p.Remote)
				fmt.Fprintln(e.out, "Machines get their own copy with: handloom project clone "+p.Name+" --device NAME. Keep the hub's git folder in your backups.")
			})
			if *device != "" {
				path, err := e.ensureClone(c, pos[0], *device)
				if err != nil {
					return err
				}
				fmt.Fprintf(e.out, "%s has its copy at %s.\n", *device, path)
			}
			return nil
		}
		if *create {
			private := !*public
			var q api.CodebaseRequest
			if err := c.Post("/v1/codebases/create", api.CodebaseCreateReq{Name: pos[0], Owner: *owner, Private: &private, Base: *base, Device: *device}, &q); err != nil {
				return err
			}
			fmt.Fprintf(e.err, "Asking %s to create the repository with gh ...\n", q.Device)
			q, err = e.waitRequest(c, q, createWait)
			if err != nil {
				return err
			}
			var p api.Codebase
			if err := c.Get("/v1/codebases/"+pos[0], &p); err != nil {
				return err
			}
			e.print(p, func() {
				fmt.Fprintf(e.out, "Created project %s: %s\n", p.Name, p.Remote)
				fmt.Fprintf(e.out, "%s has its copy at %s (main branch %s).\n", q.Device, q.Path, p.Base)
				if q.Detail != "" {
					fmt.Fprintln(e.out, q.Detail)
				}
			})
			return nil
		}
		if *owner != "" || *public {
			return usageErr("--owner and --public go with --create")
		}
		var p api.Codebase
		if err := c.Post("/v1/codebases", api.CodebaseReq{Name: pos[0], Remote: *remote, Base: *base}, &p); err != nil {
			return err
		}
		e.print(p, func() { fmt.Fprintf(e.out, "Attached project %s: %s\n", p.Name, p.Remote) })
		if *device != "" {
			path, err := e.ensureClone(c, pos[0], *device)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s has its copy at %s.\n", *device, path)
		}
	case sub == "clone" && len(pos) == 1:
		path, err := e.ensureClone(c, pos[0], *device)
		if err != nil {
			return err
		}
		e.print(map[string]string{"project": pos[0], "device": *device, "path": path}, func() {
			fmt.Fprintf(e.out, "Project %s is ready on %s at %s.\n", pos[0], *device, path)
		})
	default:
		return usageErr("usage: handloom project new NAME [--remote URL | --create] | list | show NAME | clone NAME --device D")
	}
	return nil
}

func projectLine(p api.Codebase) string {
	remote := p.Remote
	if remote == "" {
		remote = "(no repository yet)"
	}
	var have []string
	for _, k := range p.Clones {
		have = append(have, k.Device+" "+k.Status)
	}
	machines := "on no machine yet"
	if len(have) > 0 {
		machines = "on " + strings.Join(have, ", ")
	}
	if p.Hosted {
		remote += " (held by Handloom)"
	}
	return fmt.Sprintf("%-20s %s  %s", p.Name, remote, machines)
}

func sizeWords(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// ensureClone has the machine fetch its own copy of the project, unless it has one, and waits.
func (e *env) ensureClone(c *client.Client, name, device string) (string, error) {
	var q api.CodebaseRequest
	if err := c.Post("/v1/codebases/"+name+"/clone", api.CodebaseCloneReq{Device: device}, &q); err != nil {
		return "", err
	}
	if q.Status == "ready" {
		return q.Path, nil
	}
	fmt.Fprintf(e.err, "Getting project %s onto %s ...\n", name, q.Device)
	q, err := e.waitRequest(c, q, cloneWait)
	if err != nil {
		return "", err
	}
	return q.Path, nil
}

// waitRequest polls the project until the machine has reported on request q.
func (e *env) waitRequest(c *client.Client, q api.CodebaseRequest, limit time.Duration) (api.CodebaseRequest, error) {
	deadline := time.Now().Add(limit)
	for {
		var p api.Codebase
		if err := c.Get("/v1/codebases/"+q.Codebase, &p); err != nil {
			return q, err
		}
		for _, k := range p.Clones {
			if k.Device != q.Device {
				continue
			}
			switch k.Status {
			case "ready":
				q.Status, q.Path, q.Detail = "ready", k.Path, k.Detail
				return q, nil
			case "failed":
				return q, fmt.Errorf("%s could not do it: %s", q.Device, k.Detail)
			}
		}
		if !time.Now().Before(deadline) {
			return q, fmt.Errorf("%s has not answered in %d seconds: is its link running (handloom doctor there)? Check later with: handloom project show %s", q.Device, int(limit.Seconds()), q.Codebase)
		}
		time.Sleep(projectPoll)
	}
}
