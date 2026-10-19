package hub

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"handloom/internal/api"
)

// Adding the kit's checks to a project the hub holds. A hosted project cannot take a push to its
// main branch, so the hub does it: it writes the kit into a commit on a side ref that machines
// cannot touch (refs/handloom/checks: the repository's hook accepts only handloom/... branches),
// shows what is in it, and moves the main branch only when a person asks. The state lives in git:
// no side ref and no `check` on main is "none", the ref is "ready", `check` on main is "has".

const checksRef = "refs/handloom/checks"

func codebaseChecksGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	return c.checksState(c.r.PathValue("name"))
}

func codebaseChecks(c *call) (any, error) {
	var req api.ChecksReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	name := c.r.PathValue("name")
	switch req.Action {
	case "prepare":
		if err := c.allow(ActProjectManage); err != nil {
			return nil, err
		}
		return c.prepareChecks(name, req.Preset)
	case "apply":
		// Putting the checks on the main branch is the same step as merging a job: only a person.
		if err := c.allow(ActProjectMerge); err != nil {
			return nil, err
		}
		return c.applyChecks(name)
	case "discard":
		if err := c.allow(ActProjectManage); err != nil {
			return nil, err
		}
		return c.discardChecks(name)
	}
	return nil, badRequest("action must be prepare, apply or discard")
}

// hostedRepo finds a hosted project and its repository; it says why in words when it cannot.
func (c *call) hostedRepo(name string) (cb *cbRow, repo, into string, err error) {
	cb, err = c.codebaseByName(name)
	if err != nil {
		return
	}
	if !cb.hosted {
		return nil, "", "", conflict("project %q is not held by Handloom, so the hub cannot add files to it: run `handloom kit init` in a copy of it, commit, and push or open a pull request", name)
	}
	if _, err = gitBinary(); err != nil {
		return
	}
	repo = c.h.repoPath(name)
	if st, e := os.Stat(repo); e != nil || !st.IsDir() {
		return nil, "", "", conflict("the repository of %s is missing on the hub's disk; nothing was changed", name)
	}
	into = cb.base
	if into == "" {
		into = "main"
	}
	return
}

func (h *Hub) repoGit(repo string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		return h.runGit(repo, "", append([]string{"--git-dir", repo}, args...)...)
	}
}

var tomlPresetRE = regexp.MustCompile(`(?m)^preset = "([^"]*)"`)

// checksState reads the state from git. Reading takes no lock: refs change in one step.
func (c *call) checksState(name string) (api.ChecksState, error) {
	out := api.ChecksState{Project: name, State: "none"}
	_, repo, into, err := c.hostedRepo(name)
	if err != nil {
		return out, err
	}
	git := c.h.repoGit(repo)
	if _, err := git("cat-file", "-e", "refs/heads/"+into+":check"); err == nil {
		out.State = "has"
		return out, nil
	}
	tip, err := git("rev-parse", "--verify", "-q", checksRef+"^{commit}")
	if err != nil {
		return out, nil
	}
	tip = strings.TrimSpace(tip)
	out.State = "ready"
	files, _ := git("diff", "--name-only", "refs/heads/"+into, tip)
	for _, f := range strings.Fields(files) {
		out.Files = append(out.Files, f)
	}
	if !hasString(out.Files, ".claude/settings.json") {
		out.Notes = append(out.Notes, "the project already has .claude/settings.json, so the two Claude Code hooks were not added; the gate does not depend on them")
	}
	if t, err := git("show", tip+":factory.toml"); err == nil {
		out.Toml = t
		if m := tomlPresetRE.FindStringSubmatch(t); m != nil {
			out.Preset = m[1]
		}
	}
	return out, nil
}

func (c *call) prepareChecks(name, preset string) (api.ChecksState, error) {
	out := api.ChecksState{Project: name, State: "none"}
	_, repo, into, err := c.hostedRepo(name)
	if err != nil {
		return out, err
	}
	if preset == "" {
		preset = "generic"
	}
	if preset != "generic" && preset != "web" {
		return out, badRequest("%q is not a kind of project the kit knows: use generic or web", preset)
	}
	h := c.h
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	git := h.repoGit(repo)
	if _, err := git("rev-parse", "--verify", "-q", checksRef); err == nil {
		return out, conflict("checks are already prepared for %s: put them on %s or discard them first", name, into)
	}
	baseSHA, err := git("rev-parse", "--verify", "-q", "refs/heads/"+into+"^{commit}")
	if err != nil {
		return out, conflict("the project has no branch %s to add the checks to", into)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	for _, f := range []string{"check", "factory.toml"} {
		if _, err := git("cat-file", "-e", baseSHA+":"+f); err == nil {
			return out, conflict("%s already has %s on %s: it has checks of its own, and the hub does not overwrite them", name, f, into)
		}
	}
	work, err := os.MkdirTemp(h.gitRoot(), ".checks-")
	if err != nil {
		return out, fmt.Errorf("could not make a working folder: %w", err)
	}
	defer func() {
		git("worktree", "remove", "--force", work)
		os.RemoveAll(work)
		git("worktree", "prune")
	}()
	if _, err := git("worktree", "add", "-q", "--detach", work, baseSHA); err != nil {
		return out, &apiError{500, "internal", "could not prepare the checks: " + err.Error()}
	}
	created, notes, err := seedKit(work, name, preset)
	if err != nil {
		return out, err
	}
	if len(created) == 0 {
		return out, conflict("nothing to add: the project already has every file the kit would write")
	}
	wgit := func(args ...string) (string, error) { return h.runGit(work, "", args...) }
	// The folder holds the project's files, so git sees the kit's files as new; add only those.
	if _, err := wgit("add", "-A"); err != nil {
		return out, &apiError{500, "internal", "could not record the checks: " + err.Error()}
	}
	if _, err := wgit("-c", "commit.gpgsign=false", "commit", "-q", "-m", "Add the checks (Handloom kit)"); err != nil {
		return out, &apiError{500, "internal", "could not record the checks: " + err.Error()}
	}
	head, err := wgit("rev-parse", "HEAD")
	if err != nil {
		return out, err
	}
	// An empty old value means: only if the ref does not exist yet.
	if _, err := git("update-ref", checksRef, strings.TrimSpace(head), ""); err != nil {
		return out, conflict("checks were prepared by someone else a moment ago; reload and look")
	}
	if err := c.record(0, 0, "project.checks_prepare", "project:"+name, map[string]any{"preset": preset, "files": len(created)}); err != nil {
		return out, err
	}
	st, err := c.checksState(name)
	st.Notes = notes
	return st, err
}

func (c *call) applyChecks(name string) (api.ChecksState, error) {
	out := api.ChecksState{Project: name, State: "none"}
	cb, repo, into, err := c.hostedRepo(name)
	if err != nil {
		return out, err
	}
	h := c.h
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	git := h.repoGit(repo)
	tip, err := git("rev-parse", "--verify", "-q", checksRef+"^{commit}")
	if err != nil {
		return out, conflict("no checks are prepared for %s: prepare them first", name)
	}
	tip = strings.TrimSpace(tip)
	baseSHA, err := git("rev-parse", "--verify", "-q", "refs/heads/"+into+"^{commit}")
	if err != nil {
		return out, conflict("the project has no branch %s to add the checks to", into)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	parent, err := git("rev-parse", "--verify", "-q", tip+"^")
	if err != nil || strings.TrimSpace(parent) != baseSHA {
		return out, conflict("%s changed after the checks were prepared, so they may not fit it now. Discard them and prepare them again. Nothing was changed", into)
	}
	// Moves the main branch only if it is still where the checks were made from; no hook runs.
	if _, err := git("update-ref", "-m", "handloom: add the checks", "refs/heads/"+into, tip, baseSHA); err != nil {
		return out, conflict("%s changed while the checks were being added; prepare them again. Nothing was changed", into)
	}
	git("update-ref", "-d", checksRef)
	// The project's own check becomes ./check unless someone chose another.
	setCheck := ""
	if cb.check == "" {
		setCheck = "./check"
		if _, err := c.setCodebaseCheck(name, setCheck); err != nil {
			return out, err
		}
	}
	if err := c.record(0, 0, "project.checks_apply", "project:"+name, map[string]any{"into": into, "commit": tip, "check": setCheck}); err != nil {
		return out, err
	}
	out.State, out.Applied = "has", true
	return out, nil
}

func (c *call) discardChecks(name string) (api.ChecksState, error) {
	out := api.ChecksState{Project: name, State: "none"}
	_, repo, _, err := c.hostedRepo(name)
	if err != nil {
		return out, err
	}
	c.h.gitMu.Lock()
	defer c.h.gitMu.Unlock()
	git := c.h.repoGit(repo)
	if _, err := git("rev-parse", "--verify", "-q", checksRef); err != nil {
		return out, nil // nothing to discard
	}
	if _, err := git("update-ref", "-d", checksRef); err != nil {
		return out, err
	}
	if err := c.record(0, 0, "project.checks_discard", "project:"+name, nil); err != nil {
		return out, err
	}
	return out, nil
}
