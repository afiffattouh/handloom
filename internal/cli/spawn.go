package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"handloom/internal/api"
	"handloom/internal/drivers"
)

// spawn asks for an agent to be started. A lead starts agents on its own
// device in its own job; a human names the device and, optionally, the job.
func (e *env) spawn(args []string) error {
	fs := e.flags("spawn")
	kind := fs.String("kind", "claude", "agent kind to start")
	model := fs.String("model", "", "model name for the agent CLI")
	device := fs.String("device", "", "device to start it on (humans)")
	job := fs.Int64("job", 0, "job it joins (humans; a lead's agents join the lead's job)")
	project := fs.String("project", "", "project (humans)")
	wait := fs.Duration("wait", 90*time.Second, "wait this long for it to start (0: do not wait)")
	pos, err := fs.need(args, 1, 1, "spawn <name> [--kind claude] [--model M] [--device D] [--job N] [--wait 90s]")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var s api.Spawn
	if err := c.Post("/v1/spawns", api.SpawnReq{Name: pos[0], Kind: *kind, Model: *model, Device: *device, Job: *job, Project: *project}, &s); err != nil {
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
	switch s.Kind {
	case "claude":
		argv = claudeArgv(s.Model)
		if err := trustClaudeDir(dir); err != nil {
			fmt.Fprintf(e.err, "handloom: could not pre-approve %s for Claude Code (%v); it may ask\n", dir, err)
		}
	default:
		return fail(fmt.Errorf("cannot start %q agents yet", s.Kind))
	}
	if err := e.installAdapter(s.Kind, spec, dir, s.Name, s.Project, true); err != nil {
		return fail(err)
	}
	if err := c.Post(fmt.Sprintf("/v1/spawns/%d/report", id), api.SpawnReport{Status: api.SpawnStarted, Pane: drivers.Detect()}, nil); err != nil {
		return fail(err)
	}
	return e.run(append([]string{s.Name, "--kind", s.Kind, "--"}, argv...))
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
