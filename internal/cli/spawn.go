package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/drivers"
	"handloom/internal/profile"
)

// spawn asks for an agent to be started. A lead starts agents on its own
// device in its own job; a human names the device and, optionally, the job.
func (e *env) spawn(args []string) error {
	fs := e.flags("spawn")
	kind := fs.String("kind", "", "agent kind to start (default claude, or the profile's)")
	prof := fs.String("profile", "", "profile: what the agent may do and know (name or name@version)")
	model := fs.String("model", "", "model name for the agent CLI")
	device := fs.String("device", "", "device to start it on (humans)")
	job := fs.Int64("job", 0, "job it joins (humans; a lead's agents join the lead's job)")
	project := fs.String("project", "", "project (humans)")
	wait := fs.Duration("wait", 90*time.Second, "wait this long for it to start (0: do not wait)")
	pos, err := fs.need(args, 1, 1, "spawn <name> [--profile P] [--kind claude] [--model M] [--device D] [--job N] [--wait 90s]")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var s api.Spawn
	if err := c.Post("/v1/spawns", api.SpawnReq{Name: pos[0], Kind: *kind, Profile: *prof, Model: *model, Device: *device, Job: *job, Project: *project}, &s); err != nil {
		return err
	}
	deadline := time.Now().Add(*wait)
	for *wait > 0 && (s.Status == api.SpawnPending || s.Status == api.SpawnLaunching) && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if err := c.Get(fmt.Sprintf("/v1/spawns/%d", s.ID), &s); err != nil {
			return err
		}
	}
	e.print(s, func() {
		switch s.Status {
		case api.SpawnStarted:
			fmt.Fprintf(e.out, "%s is running on %s (pane %s). Give it work: handloom task create \"...\" --assign %s\n", s.Name, s.Device, s.Pane, s.Name)
		case api.SpawnFailed:
			fmt.Fprintf(e.out, "%s did not start: %s\n", s.Name, s.Error)
		default:
			fmt.Fprintf(e.out, "%s is %s on %s (spawn #%d).\n", s.Name, s.Status, s.Device, s.ID)
		}
	})
	if s.Status == api.SpawnFailed {
		return fmt.Errorf("spawn failed")
	}
	return nil
}

func (e *env) spawns(args []string) error {
	fs := e.flags("spawns")
	if _, err := fs.need(args, 0, 0, "spawns"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var list []api.Spawn
	if err := c.Get("/v1/spawns", &list); err != nil {
		return err
	}
	e.print(list, func() {
		if len(list) == 0 {
			fmt.Fprintln(e.out, "No spawns.")
		}
		for _, s := range list {
			job := ""
			if s.Job != nil {
				job = fmt.Sprintf(" job #%d", *s.Job)
			}
			if s.Profile != "" {
				job += " as " + s.Profile
			}
			extra := ""
			if s.Error != "" {
				extra = ": " + s.Error
			}
			fmt.Fprintf(e.out, "#%-3d %-10s %-14s %s on %s%s%s\n", s.ID, s.Status, s.Name, s.Kind, s.Device, job, extra)
		}
	})
	return nil
}

// claudeArgv is what a spawned Claude Code runs: no approval prompts (an
// agent nobody watches must never stop at one), only the tools listed, the
// project's own settings and nothing from the user's global ones.
func claudeArgv(model string) []string {
	argv := []string{"claude", "--setting-sources", "project,local", "--permission-mode", "dontAsk",
		"--allowedTools", "Read", "Glob", "Grep", "Write", "Edit", "Bash(handloom:*)"}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return argv
}

// spawnFailPause is how long a failed spawn's terminal keeps its message up.
var spawnFailPause = 20 * time.Second

// spawnExec runs inside the terminal the link opened for a spawn. It installs
// the adapter the normal way, tells the hub the agent is up, and becomes the
// agent CLI through `run`, so the agent is registered with this terminal.
func (e *env) spawnExec(args []string) error {
	if len(args) != 1 {
		return usageErr("usage: handloom spawn-exec <id>   (started by the link)")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return usageErr("bad spawn id %q", args[0])
	}
	c, err := conn()
	if err != nil {
		return err
	}
	fail := func(err error) error {
		fmt.Fprintf(e.err, "handloom: spawn %d failed: %v\n", id, err)
		c.Post(fmt.Sprintf("/v1/spawns/%d/report", id), api.SpawnReport{Status: api.SpawnFailed, Error: err.Error()}, nil)
		time.Sleep(spawnFailPause) // leave the message on the screen
		return err
	}
	var all []api.Spawn
	if err := c.Get("/v1/device/spawns", &all); err != nil {
		return fail(err)
	}
	var s *api.Spawn
	for i := range all {
		if all[i].ID == id {
			s = &all[i]
		}
	}
	if s == nil || s.Status != api.SpawnLaunching {
		return fail(fmt.Errorf("spawn %d is not waiting to be started here", id))
	}
	dir, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	spec := adapterSpecs[s.Kind]
	if spec == nil {
		return fail(fmt.Errorf("no adapter for %q", s.Kind))
	}
	var argv []string
	var extra string
	switch s.Kind {
	case "claude":
		prof, err := fetchSpawnProfile(c, s)
		if err != nil {
			return fail(err)
		}
		if prof == nil {
			argv = claudeArgv(s.Model)
		} else {
			argv = profile.ClaudeArgv(&prof.Spec, s.Model)
			if err := profile.InstallSkills(dir, &prof.Spec); err != nil {
				return fail(err)
			}
			if prof.Spec.Prompt != "" {
				extra = fmt.Sprintf("## Your profile: %s (version %d)\n\n%s", prof.Name, prof.Version, prof.Spec.Prompt)
			}
		}
		if err := trustClaudeDir(dir); err != nil {
			fmt.Fprintf(e.err, "handloom: could not pre-approve %s for Claude Code (%v); it may ask\n", dir, err)
		}
	case "codex":
		prof, err := fetchSpawnProfile(c, s)
		if err != nil {
			return fail(err)
		}
		bin, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		ps := &profile.Spec{Kind: "codex", Tools: profile.Tools{Allow: []string{"edit", "read", "shell"}}}
		if prof != nil {
			ps = &prof.Spec
			if ps.Prompt != "" {
				extra = fmt.Sprintf("## Your profile: %s (version %d)\n\n%s", prof.Name, prof.Version, ps.Prompt)
			}
		}
		argv = profile.CodexArgv(ps, s.Model, bin, client.Home(), s.Name)
		if err := trustCodexDir(dir); err != nil {
			fmt.Fprintf(e.err, "handloom: could not pre-approve %s for Codex (%v); it may ask\n", dir, err)
		}
	case "omp", "pi", "opencode":
		prof, err := fetchSpawnProfile(c, s)
		if err != nil {
			return fail(err)
		}
		ps := &profile.Spec{Kind: s.Kind, Tools: profile.Tools{Allow: []string{"edit", "read", "shell"}}}
		if prof != nil {
			ps = &prof.Spec
			if ps.Prompt != "" {
				extra = fmt.Sprintf("## Your profile: %s (version %d)\n\n%s", prof.Name, prof.Version, ps.Prompt)
			}
		}
		switch s.Kind {
		case "omp":
			argv = profile.OmpArgv(ps, s.Model, s.Role, filepath.Join(dir, ".handloom", "omp-extension.ts"))
		case "pi":
			argv = profile.PiArgv(ps, s.Model)
		case "opencode":
			argv = profile.OpenCodeArgv(ps, s.Model)
			cfg, err := profile.OpenCodeConfig(ps, s.Model)
			if err != nil {
				return fail(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "opencode.json"), append(cfg, '\n'), 0o644); err != nil {
				return fail(err)
			}
		}
	default:
		return fail(fmt.Errorf("cannot start %q agents yet", s.Kind))
	}
	if s.Kind != "claude" {
		if sk := inlineSkillsOf(c, s); sk != "" {
			extra += "\n\n## Skills\n\nApply these when the situation they describe comes up.\n\n" + sk
		}
	}
	if err := e.installAdapter(s.Kind, spec, dir, s.Name, s.Project, true, extra); err != nil {
		return fail(err)
	}
	if s.Repo != "" {
		if err := writeScope(dir, s); err != nil {
			return fail(err)
		}
	}
	if err := c.Post(fmt.Sprintf("/v1/spawns/%d/report", id), api.SpawnReport{Status: api.SpawnStarted, Pane: drivers.Detect()}, nil); err != nil {
		return fail(err)
	}
	if s.Role == api.RoleLead {
		// The hub has just made it the job's lead: its instructions should say so.
		if err := e.installAdapter(s.Kind, spec, dir, s.Name, s.Project, true, extra); err != nil {
			return fail(err)
		}
	}
	// Commits made by this agent say so.
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", s.Name}, {"GIT_COMMITTER_NAME", s.Name},
		{"GIT_AUTHOR_EMAIL", s.Name + "@handloom.local"}, {"GIT_COMMITTER_EMAIL", s.Name + "@handloom.local"}} {
		os.Setenv(kv[0], kv[1])
	}
	return e.run(append([]string{s.Name, "--kind", s.Kind, "--"}, argv...))
}

// scope is what the link and the agent's work directory remember about a repo
// job: the commit the worktree was cut from, so what the agent changed can be
// told from what was already there.
type scope struct {
	Base string `json:"base"`
	Repo string `json:"repo"`
	Job  int64  `json:"job"`
}

func writeScope(dir string, s *api.Spawn) error {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("%s is not a git worktree: %v", dir, err)
	}
	sc := scope{Base: strings.TrimSpace(string(out)), Repo: s.Repo}
	if s.Job != nil {
		sc.Job = *s.Job
	}
	b, _ := json.MarshalIndent(sc, "", "  ")
	if err := os.MkdirAll(filepath.Join(dir, ".handloom"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".handloom", "scope.json"), append(b, '\n'), 0o600)
}

// fetchSpawnProfile gets the profile a spawn is pinned to, or nil when it has
// none. The hub's answer is checked: valid, and hashing to what it claims.
func fetchSpawnProfile(c *client.Client, s *api.Spawn) (*api.ProfileFull, error) {
	if s.Profile == "" {
		return nil, nil
	}
	var p api.ProfileFull
	if err := c.Get(fmt.Sprintf("/v1/device/spawns/%d/profile", s.ID), &p); err != nil {
		return nil, fmt.Errorf("profile %s: %w", s.Profile, err)
	}
	if bad := profile.Normalize(&p.Spec); len(bad) > 0 {
		return nil, fmt.Errorf("profile %s is not acceptable: %s", s.Profile, strings.Join(bad, "; "))
	}
	if got := profile.Hash(&p.Spec); got != p.Hash {
		return nil, fmt.Errorf("profile %s does not match its hash (hub says %.12s, content is %.12s)", s.Profile, p.Hash, got)
	}
	if want := fmt.Sprintf("%s@%d", p.Name, p.Version); want != s.Profile {
		return nil, fmt.Errorf("the hub sent profile %s for a spawn pinned to %s", want, s.Profile)
	}
	return &p, nil
}

// trustClaudeDir tells Claude Code that dir is a folder its user trusts, so a
// fresh working directory does not stop at the "trust this folder" question
// (which would leave the agent waiting for a key nobody will press). The link
// made the directory itself. It edits ~/.claude.json in place and does
// nothing if Claude Code has never run on this machine.
func trustClaudeDir(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	projects := map[string]map[string]json.RawMessage{}
	if p, ok := doc["projects"]; ok {
		if err := json.Unmarshal(p, &projects); err != nil {
			return err
		}
	}
	proj := projects[dir]
	if proj == nil {
		proj = map[string]json.RawMessage{}
	}
	proj["hasTrustDialogAccepted"] = json.RawMessage("true")
	projects[dir] = proj
	doc["projects"], _ = json.Marshal(projects)
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp := path + ".handloom-tmp"
	if err := os.WriteFile(tmp, out, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// trustCodexDir answers Codex's "trust this folder?" question in advance for
// the directory the link made, the way Codex itself records the answer: a
// [projects."<dir>"] section in config.toml. Without it a fresh work directory
// stops Codex at that question, and the first thing typed into it (the
// link's nudge) would answer it by accident. Nothing else in the file is touched.
func trustCodexDir(dir string) error {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = filepath.Join(h, ".codex")
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return nil // Codex has never run here
	}
	path := filepath.Join(home, "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	header := "[projects." + profile.TOMLString(dir) + "]"
	if strings.Contains(string(raw), header) {
		return nil
	}
	text := string(raw)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n" + header + "\ntrust_level = \"trusted\"\n"
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".handloom-tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// inlineSkillsOf is the text of the spawn's profile skills, for CLIs that do not load SKILL.md folders.
func inlineSkillsOf(c *client.Client, s *api.Spawn) string {
	prof, err := fetchSpawnProfile(c, s)
	if err != nil || prof == nil {
		return ""
	}
	return profile.InlineSkills(&prof.Spec)
}
