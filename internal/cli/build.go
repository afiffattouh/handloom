package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"handloom/internal/api"
	"handloom/internal/playbook"
)

func (e *env) build(args []string) error {
	fs := e.flags("build")
	recipe := fs.String("playbook", playbook.MVP, "delivery playbook")
	project := fs.String("project", "", "existing project")
	repo := fs.String("repo", "", "repository path on the selected machine")
	device := fs.String("device", "", "machine holding the repository")
	verify := fs.String("verify", "", "project verification command")
	base := fs.String("base", "", "base branch")
	title := fs.String("title", "", "short product title")
	file := fs.String("brief-file", "", "read the product brief from a text file")
	request := fs.String("request-id", "", "reuse a launch identifier to retry safely")
	dry := fs.Bool("dry-run", false, "show the plan without creating a job")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	brief := strings.Join(pos, " ")
	if *file != "" {
		if brief != "" {
			return usageErr("provide a brief as words or --brief-file, not both")
		}
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, 16001))
		f.Close()
		if err != nil {
			return err
		}
		brief = string(b)
	}
	compiled, err := playbook.Compile(*recipe, brief)
	if err != nil {
		return err
	}
	if *dry {
		e.print(compiled, func() {
			fmt.Fprintf(e.out, "%s: %d stages; ends at release readiness, not deployment\n", *recipe, len(compiled.Steps))
			for _, s := range compiled.Steps {
				fmt.Fprintf(e.out, "%s: %s (%s)\n", s.Key, s.Title, s.Profile)
			}
		})
		return nil
	}
	if *title == "" {
		r := []rune(strings.TrimSpace(brief))
		if len(r) > 80 {
			r = r[:80]
		}
		*title = string(r)
	}
	if *request == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		*request = hex.EncodeToString(b)
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	if *project != "" {
		if _, err := e.ensureClone(c, *project, *device); err != nil {
			return err
		}
	}
	fmt.Fprintf(e.err, "Build request: %s (reuse --request-id if the response is lost)\n", *request)
	var j api.Job
	err = c.Post("/v1/products", api.ProductBuildReq{RequestID: *request, Playbook: *recipe, Title: *title, Brief: brief, Project: *project, Repo: *repo, Device: *device, Verify: *verify, Base: *base}, &j)
	if err != nil {
		return err
	}
	e.print(j, func() {
		fmt.Fprintf(e.out, "Started MVP build #%d: %s\n%d managed stages. Track with: handloom job plan %d\nThe final stage prepares release readiness; it does not deploy.\n", j.ID, j.Title, j.Tasks.Open, j.ID)
	})
	return nil
}

func (e *env) playbook(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(e.out, "%s — product brief, UX/UI, architecture, backend/frontend, QA, security, review, release readiness\n", playbook.MVP)
		return nil
	}
	if args[0] != "install" {
		return usageErr("usage: handloom playbook [install mvp-v1 --kind K --runtime cloud|local]")
	}
	fs := e.flags("playbook install")
	kind := fs.String("kind", "", "agent CLI")
	runtime := fs.String("runtime", "", "cloud or local")
	model := fs.String("model", "", "model override")
	adapt := fs.Bool("adapt", false, "adapt unsupported profile capabilities for this CLI")
	pos, err := fs.need(args[1:], 1, 1, "playbook install mvp-v1 --kind K --runtime R")
	if err != nil {
		return err
	}
	if pos[0] != playbook.MVP {
		return usageErr("unknown playbook %q", pos[0])
	}
	if *kind == "" || *runtime == "" {
		return usageErr("choose --kind and --runtime for the playbook profiles")
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	var result api.PlaybookInstallResp
	if err := c.Post("/v1/playbooks/"+pos[0]+"/install", api.StarterAddReq{Kind: *kind, Runtime: *runtime, Model: *model, Adapt: *adapt}, &result); err != nil {
		return err
	}
	e.print(result, func() {
		for _, warning := range result.Warnings {
			fmt.Fprintln(e.out, "Note: "+warning)
		}
		fmt.Fprintln(e.out, "MVP profiles ready (existing profiles preserved):")
		for _, p := range result.Profiles {
			fmt.Fprintf(e.out, "%s@%d: %s / %s\n", p.Name, p.Version, p.Kind, p.Runtime)
		}
	})
	return nil
}
