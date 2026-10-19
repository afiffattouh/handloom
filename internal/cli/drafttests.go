package cli

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"handloom/internal/api"
	"handloom/internal/client"
)

// `handloom job draft-tests SLUG`: a helper other than the one who will build the work drafts acceptance
// tests from the spec's Risks, for the owner to read before sealing. The drafts are written beside the
// spec (specs/<slug>/drafts/), never into tests/acceptance/, which only a person may change.

var draftSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// specPlaceholders are the template's own words: a spec that still has them has not been written yet.
var specPlaceholders = []string{"<short title>", "<What should be true", "- path/to/file", "command that fails today", "- <risk, and the acceptance test"}

// risksOf is the text under "## Risks" in a spec, without blank lines and comments in angle brackets.
func risksOf(spec string) string {
	var out []string
	in := false
	for _, line := range strings.Split(spec, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.EqualFold(strings.TrimSpace(line[3:]), "risks")
			continue
		}
		if in && strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func draftBrief(slug string) string {
	return fmt.Sprintf(`Draft acceptance tests for the task "%[1]s". You are not building the feature; a different helper does that later.

1. Read specs/%[1]s.md on this branch: the Goal, and above all the "Risks" list.
2. For each risk write a test that FAILS today and would pass only once that risk is handled (hostile input such as ../x, empty or huge values, data shared with a caller, boundaries). Add one test for the Goal itself.
3. Put the files in specs/%[1]s/drafts/, in the project's own test style (look at its existing tests). Start each file with a comment saying which risk it covers and how to run it.
4. Do not implement the feature. Do not change anything else. tests/acceptance/, factory.toml, .factory/ and ./check belong to a person: never write there.
5. If the spec's Risks are too vague to test, write BLOCKED.md saying what is missing instead of guessing.

A person reads your drafts before any of them is used.`, slug)
}

func (e *env) jobDraftTests(c *client.Client, slug, project, device, profile, tenant string) error {
	if !draftSlug.MatchString(slug) {
		return usageErr("the task name is lowercase letters, digits, dot, dash or underscore, such as parser-fix")
	}
	if project == "" || profile == "" {
		return usageErr("usage: handloom job draft-tests <task> --project NAME --worker-profile PROFILE [--device D]   (use a different profile from the one that will build the work)")
	}
	path, err := e.ensureClone(c, project, device)
	if err != nil {
		return err
	}
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).Output()
		return string(out), err
	}
	branch := "task/" + slug
	if _, err := git("rev-parse", "--verify", "-q", branch+"^{commit}"); err != nil {
		return fmt.Errorf("%s has no branch %s on this computer (%s). Start the task there first: handloom kit task new %s, fill in specs/%s.md, and commit it on that branch. If this computer does not hold the project's copy, run this command on the one that does", project, branch, path, slug, slug)
	}
	spec, err := git("show", branch+":specs/"+slug+".md")
	if err != nil {
		return fmt.Errorf("specs/%s.md is not committed on %s in %s: write the spec and commit it there first", slug, branch, path)
	}
	var left []string
	for _, p := range specPlaceholders {
		if strings.Contains(spec, p) {
			left = append(left, p)
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("specs/%s.md still has the template's placeholders (%s). Fill in the Goal, Files, Risks and Checks first", slug, strings.Join(left, "; "))
	}
	risks := risksOf(spec)
	if risks == "" || strings.EqualFold(strings.Trim(strings.TrimSpace(risks), "-*. "), "none") {
		return fmt.Errorf("specs/%s.md lists no risks, so there is nothing to draft tests from. List what could go wrong (hostile input, shared data, boundaries), or write none only if the change takes no outside input and stores nothing", slug)
	}
	var j api.Job
	if err := c.Post("/v1/jobs", api.JobNewReq{Title: "Draft acceptance tests for " + slug, Body: draftBrief(slug), Project: tenant, Codebase: project, Device: device,
		Base: branch, WorkerProfile: profile}, &j); err != nil {
		return err
	}
	e.print(j, func() {
		fmt.Fprintln(e.out, "Started "+jobLine(j))
		fmt.Fprintf(e.out, "A helper started from %s drafts the tests into specs/%s/drafts/. Read them on the job's page, then move the ones you want into tests/acceptance/ and seal: handloom kit task seal %s\n", profile, slug, slug)
	})
	return nil
}
