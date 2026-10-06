package link

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/profile"
)

// The scope check. An agent of a repo job works in a git worktree the link
// made. When its profile lists paths it may change (write:), the link looks at
// what the agent actually changed before it lets a submit through, and
// refuses the submit if any file is outside. The link keeps its own copy of
// what to check (the commit the worktree was cut from, the patterns, the
// directory) outside the worktree, because the agent can edit everything
// inside it, including .handloom/scope.json. Agents the link did not start
// have no scope and are not checked.

type scopeRecord struct {
	Spawn  int64    `json:"spawn"`
	Job    int64    `json:"job"`
	Agent  string   `json:"agent"`
	Dir    string   `json:"dir"`
	Base   string   `json:"base"`
	Write  []string `json:"write"`
	Verify string   `json:"verify,omitempty"` // the job's verify command, run in Dir after a submit goes through
	// Autocommit: the agent's CLI cannot commit in its worktree (Codex's sandbox
	// keeps the repository's .git read-only), so the link commits what it
	// changed when it submits.
	Autocommit bool `json:"autocommit,omitempty"`
}

func (l *Link) scopeFile(agent string) string {
	return filepath.Join(client.Home(), "scopes", agent+".json")
}

func (l *Link) saveScope(rec scopeRecord) error {
	if err := os.MkdirAll(filepath.Dir(l.scopeFile(rec.Agent)), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	return os.WriteFile(l.scopeFile(rec.Agent), append(b, '\n'), 0o600)
}

func (l *Link) loadScope(agent string) *scopeRecord {
	if agent == "" || strings.ContainsAny(agent, `/\`) {
		return nil
	}
	b, err := os.ReadFile(l.scopeFile(agent))
	if err != nil {
		return nil
	}
	var rec scopeRecord
	if json.Unmarshal(b, &rec) != nil || rec.Dir == "" || rec.Base == "" {
		return nil
	}
	return &rec
}

// recordScope is called when the link has made a worktree for a spawn. The
// patterns come from the profile version the spawn is pinned to.
func (l *Link) recordScope(ctx context.Context, s api.Spawn, dir string) error {
	var write []string
	if s.Profile != "" {
		var p api.ProfileFull
		if err := l.hub.Do(ctx, "GET", fmt.Sprintf("/v1/device/spawns/%d/profile", s.ID), nil, &p); err != nil {
			return fmt.Errorf("profile %s: %w", s.Profile, err)
		}
		write = p.Spec.Write
	}
	if len(write) == 0 && s.Verify == "" && s.Kind != "codex" {
		os.Remove(l.scopeFile(s.Name)) // an earlier agent of the same name must not leave its limits behind
		return nil
	}
	base, err := l.opt.Git(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	rec := scopeRecord{Spawn: s.ID, Agent: s.Name, Dir: dir, Base: strings.TrimSpace(base), Write: write, Verify: s.Verify, Autocommit: s.Kind == "codex"}
	if s.Job != nil {
		rec.Job = *s.Job
	}
	return l.saveScope(rec)
}

// changedFiles lists what differs from the commit the worktree started at:
// tracked changes (committed or not, renames counted as a delete and an add)
// and new files git does not ignore. Files handloom and the agent CLIs wrote
// themselves are left out.
func (l *Link) changedFiles(ctx context.Context, rec *scopeRecord) ([]string, error) {
	git := func(args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append([]string{"-C", rec.Dir}, args...)...)
	}
	tracked, err := git("diff", "--name-only", "--no-renames", "-z", rec.Base)
	if err != nil {
		return nil, err
	}
	untracked, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range []string{tracked, untracked} {
		for _, p := range strings.Split(list, "\x00") {
			if p == "" || seen[p] || profile.AdapterPath(p) {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// submitGate sits in front of POST /v1/tasks/{id}/submit on the local socket.
func (l *Link) submitGate(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent := api.AgentFrom(r.Header)
		rec := l.loadScope(agent)
		if rec == nil {
			next.ServeHTTP(w, r)
			return
		}
		var taskID int64
		fmt.Sscan(r.PathValue("id"), &taskID)
		if rec.Autocommit {
			if err := l.autoCommit(r.Context(), rec, taskID); err != nil {
				l.opt.Log.Printf("commit for %s: %v", agent, err)
			}
		}
		if len(rec.Write) == 0 { // nothing to check, but the work may still be verified
			l.forwardAndVerify(w, r, next, rec, taskID)
			return
		}
		refuse := func(status int, msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(api.Error{Error: msg, Code: "scope"})
		}
		changed, err := l.changedFiles(r.Context(), rec)
		if err != nil {
			// Fail closed: a submit nobody could check is not let through.
			l.opt.Log.Printf("scope check for %s: %v", agent, err)
			refuse(http.StatusInternalServerError, "the link could not check which files you changed, so it did not forward the submit: "+reasonText(err.Error()))
			return
		}
		var bad []string
		for _, p := range changed {
			if !profile.MatchWrite(rec.Write, p) {
				bad = append(bad, p)
			}
		}
		if len(bad) == 0 {
			l.forwardAndVerify(w, r, next, rec, taskID)
			return
		}
		go func() {
			if err := l.hub.Do(context.Background(), "POST", "/v1/device/scope-refused", api.ScopeRefusal{Agent: agent, Task: taskID, Paths: bad}, nil); err != nil {
				l.opt.Log.Printf("report refused submit of %s: %v", agent, err)
			}
		}()
		shown := bad
		if len(shown) > 10 {
			shown = append(shown[:10:10], fmt.Sprintf("... and %d more", len(bad)-10))
		}
		refuse(http.StatusConflict, fmt.Sprintf("submit refused: you changed files your profile does not allow (allowed: %s): %s. Revert them (git checkout -- <path>, or delete the new file), then submit again.",
			strings.Join(rec.Write, ", "), strings.Join(shown, ", ")))
	}
}

// statusWriter remembers the status a handler answered with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// forwardAndVerify lets a submit through and, if the hub accepted it and the
// job has a verify command, checks the agent's work in the background.
func (l *Link) forwardAndVerify(w http.ResponseWriter, r *http.Request, next http.Handler, rec *scopeRecord, taskID int64) {
	sw := &statusWriter{ResponseWriter: w}
	next.ServeHTTP(sw, r)
	if sw.status == http.StatusOK && rec.Verify != "" && taskID > 0 {
		go l.runVerify(rec, taskID)
	}
}

// autoCommit commits what the agent changed in its worktree, in its own name,
// without running the repository's hooks (they would run here, as the link's
// user). The files handloom and the agent CLI wrote are left out.
func (l *Link) autoCommit(ctx context.Context, rec *scopeRecord, taskID int64) error {
	git := func(args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append([]string{"-C", rec.Dir}, args...)...)
	}
	args := []string{"add", "-A", "--", "."}
	for _, p := range []string{".handloom", ".claude", ".codex", ".pi", ".opencode", "CLAUDE.md", "AGENTS.md"} {
		args = append(args, ":(exclude)"+p)
	}
	if _, err := git(args...); err != nil {
		return err
	}
	if _, err := git("diff", "--cached", "--quiet"); err == nil {
		return nil // nothing to commit
	}
	name := rec.Agent
	_, err := git("-c", "user.name="+name, "-c", "user.email="+name+"@handloom.local",
		"commit", "-q", "--no-verify", "-m", fmt.Sprintf("%s: work for task #%d", name, taskID))
	return err
}
