package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"handloom/internal/api"
	"handloom/internal/playbook"
	"handloom/internal/profile"
	"handloom/internal/starters"
)

func playbookInstall(c *call) (any, error) {
	if err := c.ownerOnly("installing playbook profiles"); err != nil {
		return nil, err
	}
	if c.r.PathValue("name") != playbook.MVP {
		return nil, notFound("unknown playbook")
	}
	var req api.StarterAddReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	names := []string{}
	for name := range playbook.ProfileStarters {
		names = append(names, name)
	}
	sort.Strings(names)
	result := api.PlaybookInstallResp{Profiles: []api.ProfileInfo{}, Warnings: []string{}}
	for _, name := range names {
		// Existing profiles are intentional workspace configuration; never overwrite.
		var n int
		if err := c.tx.QueryRow(`SELECT count(*) FROM profile WHERE name = ?`, name).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			p, err := c.profileVersion(name, 0)
			if err != nil {
				return nil, err
			}
			result.Profiles = append(result.Profiles, p.info())
			continue
		}
		built, err := starters.Build(playbook.ProfileStarters[name], starters.Choice{Kind: req.Kind, Runtime: req.Runtime, Model: req.Model, Adapt: req.Adapt})
		if err != nil {
			return nil, err
		}
		if len(built.Problems) > 0 {
			return nil, badRequest("%s: %s", name, strings.Join(built.Problems, "; "))
		}
		for _, warning := range built.Warnings {
			result.Warnings = append(result.Warnings, name+": "+warning)
		}
		if len(built.Adapted) > 0 {
			result.Warnings = append(result.Warnings, name+": adapted capabilities: "+strings.Join(built.Adapted, ", "))
		}
		// Documentation producers need to commit their scoped artifacts for
		// integration. Their standalone starters intentionally omit a shell.
		if name == "mvp-product" || name == "mvp-design" {
			built.Spec.Tools.Allow = append(built.Spec.Tools.Allow, "shell")
			built.Spec.Tools.DenyCommands = append(built.Spec.Tools.DenyCommands, "rm", "sudo", "git push", "git reset --hard", "git clean")
			built.Spec.Prompt += "\nFor this MVP playbook, make routine decisions within the original brief and record assumptions. Commit your scoped documentation/design artifacts before submitting; do not edit application code. Ask the lead only when a consequential ambiguity prevents useful progress."
			result.Warnings = append(result.Warnings, name+": shell enabled to commit scoped documentation/design artifacts; destructive commands and pushing are denied")
		}
		if req.Adapt {
			if changes := profile.Adapt(built.Spec); len(changes) > 0 {
				result.Warnings = append(result.Warnings, name+": adapted playbook capabilities: "+strings.Join(changes, ", "))
			}
		}
		if name == playbook.LeadProfile {
			built.Spec.Prompt = playbook.LeadInstructions
			// The ordinary lead skill tells agents to spawn/assign manually.
			kept := built.Spec.Skills[:0]
			for _, skill := range built.Spec.Skills {
				if !strings.HasPrefix(skill.Path, "plan-a-job/") {
					kept = append(kept, skill)
				}
			}
			built.Spec.Skills = kept
		}
		p, err := c.saveProfile(name, *built.Spec)
		if err != nil {
			return nil, err
		}
		result.Profiles = append(result.Profiles, p.info())
	}
	return result, nil
}

func productBuild(c *call) (any, error) {
	if err := c.humanOnly("starting a product build"); err != nil {
		return nil, err
	}
	var req api.ProductBuildReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Playbook == "" {
		req.Playbook = playbook.MVP
	}
	if req.RequestID == "" || len(req.RequestID) > 128 {
		return nil, badRequest("request_id must contain 1 to 128 bytes; reuse it when retrying")
	}
	p, err := playbook.Compile(req.Playbook, req.Brief)
	if err != nil {
		return nil, badRequest("%s", err)
	}
	raw := string(marshal(req))
	var old string
	var id int64
	err = c.tx.QueryRow(`SELECT request,job_id FROM product_run WHERE actor = ? AND request_id = ?`, c.p.actor(), req.RequestID).Scan(&old, &id)
	if err == nil {
		if old != raw {
			return nil, conflict("request_id already belongs to a different product request")
		}
		j, err := c.job(id)
		if err != nil {
			return nil, err
		}
		return c.jobView(j)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if req.Repo == "" && req.Project == "" {
		return nil, badRequest("choose an existing project or repository for the product build")
	}
	body := fmt.Sprintf("Playbook: %s\n\nOriginal product brief:\n%s\n\n%s", req.Playbook, strings.TrimSpace(req.Brief), playbook.LeadInstructions)
	result, err := c.createJob(api.JobNewReq{Title: req.Title, Body: body, Repo: req.Repo, Codebase: req.Project, Device: req.Device, Verify: req.Verify, Base: req.Base, LeadProfile: playbook.LeadProfile})
	if err != nil {
		return nil, err
	}
	j := result.(api.Job)
	if j.Verify == "" {
		return nil, badRequest("the product needs a verification command; set the project's check or pass --verify")
	}
	if _, err := c.submitJobPlan(j.ID, p); err != nil {
		return nil, err
	}
	if _, err := c.tx.Exec(`INSERT INTO product_run(actor,request_id,request,job_id) VALUES (?,?,?,?)`, c.p.actor(), req.RequestID, raw, j.ID); err != nil {
		return nil, err
	}
	if err := c.record(0, 0, "product.build", fmt.Sprintf("job:%d", j.ID), map[string]any{"playbook": req.Playbook}); err != nil {
		return nil, err
	}
	t, err := c.job(j.ID)
	if err != nil {
		return nil, err
	}
	return c.jobView(t)
}
