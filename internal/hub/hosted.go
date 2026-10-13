package hub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Hosted projects (D106). A person may let the hub itself hold a project's code:
// a bare git repository at <data>/git/<name>.git, served over git's smart HTTP
// protocol. Machines reach it with their device credential (the one `link join`
// gave them) as the password; nobody needs GitHub, an SSH key or a token in an
// address. This is the one case where the hub stores code, so it is opt-in per
// project, refused for confidential jobs, and limited by a hook in the
// repository: machines may only create or advance branches named handloom/...
// and never delete or rewrite them. The main branch moves only when a person
// asks the hub to merge a published job (mergeHosted), which writes the branch
// pointer itself, so the hook, which guards pushes from outside, is not in the way.

const (
	hostedIdentityName  = "Handloom"
	hostedIdentityEmail = "handloom@localhost"
	gitTimeout          = 2 * time.Minute
	noGitText           = "hosting needs git on the hub: install git where the hub runs (the Handloom container image already has it), then try again"
)

// preReceiveHook is written into every hosted repository. POSIX sh and git only.
// It reads "old new ref" lines and refuses, with a sentence, anything but a create
// or fast-forward of a branch under handloom/.
const preReceiveHook = `#!/bin/sh
# Written by Handloom (D106). This repository takes branches named handloom/... only,
# and only moves them forward. The main branch changes when the owner merges a job.
bad=0
while read old new ref; do
  case "$ref" in
    refs/heads/handloom/?*) ;;
    refs/heads/*)
      echo "Handloom: ${ref#refs/heads/} cannot be pushed to. This project's main work is merged by its owner: push a branch named handloom/<something> (publishing a job does that), then ask for a merge." >&2
      bad=1; continue ;;
    *)
      echo "Handloom: only branches named handloom/... can be pushed here; $ref is not accepted." >&2
      bad=1; continue ;;
  esac
  case "$new" in
    *[!0]*) ;;
    *)
      echo "Handloom: ${ref#refs/heads/} cannot be deleted from the hub." >&2
      bad=1; continue ;;
  esac
  case "$old" in
    *[!0]*)
      if ! git merge-base --is-ancestor "$old" "$new" 2>/dev/null; then
        echo "Handloom: ${ref#refs/heads/} already has other work; the push would overwrite it, so it was refused. Use a new branch name." >&2
        bad=1
      fi ;;
  esac
done
exit $bad
`

// ---- where things are ----

func (h *Hub) gitRoot() string {
	if h.opt.DataDir == "" {
		return ""
	}
	abs, err := filepath.Abs(filepath.Join(h.opt.DataDir, "git"))
	if err != nil {
		return filepath.Join(h.opt.DataDir, "git")
	}
	return abs
}

func (h *Hub) repoPath(name string) string { return filepath.Join(h.gitRoot(), name+".git") }

func gitBinary() (string, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", &apiError{503, "unavailable", noGitText}
	}
	return bin, nil
}

// checkGitServes makes sure git is there and can serve repositories (some systems ship git
// http-backend in a separate package), so a project is never created that cannot be reached.
func (h *Hub) checkGitServes() error {
	bin, err := gitBinary()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, "--exec-path").Output()
	if err != nil {
		return &apiError{503, "unavailable", "hosting needs a working git on the hub: " + err.Error()}
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")); err != nil {
		return &apiError{503, "unavailable", "hosting needs git's web serving part (git http-backend), which this git does not have: install the full git package (on Alpine: git-daemon) on the hub"}
	}
	return nil
}

// hostedBase is the address machines use to reach this hub: the configured public URL,
// else the address of the request that is creating the project.
func (h *Hub) hostedBase(r *http.Request) (string, error) {
	if h.opt.BaseURL != "" {
		u, err := url.Parse(strings.TrimRight(h.opt.BaseURL, "/"))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", badRequest("the hub's public address (HANDLOOM_BASE_URL) is not a usable http:// or https:// address")
		}
		return u.Scheme + "://" + u.Host + u.Path, nil
	}
	if r == nil || r.Host == "" {
		return "", badRequest("the hub does not know its own public address: set HANDLOOM_BASE_URL")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]{1,260}$`).MatchString(r.Host) {
		return "", badRequest("the address this request came to is not usable; set HANDLOOM_BASE_URL on the hub")
	}
	scheme := "http"
	if r.TLS != nil || (h.opt.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) {
		scheme = "https"
	}
	return scheme + "://" + r.Host, nil
}

// hostedRemote is the address of a hosted project's repository. It never has a credential in it.
func hostedRemote(base, name string) string {
	return strings.TrimRight(base, "/") + "/git/" + name + ".git"
}

// ---- running git on the hub ----

func gitEnv(home string, extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=" + hostedIdentityName, "GIT_AUTHOR_EMAIL=" + hostedIdentityEmail,
		"GIT_COMMITTER_NAME=" + hostedIdentityName, "GIT_COMMITTER_EMAIL=" + hostedIdentityEmail,
	}
	return append(env, extra...)
}

// runGit runs git with a fixed environment (the hub user's own git settings are never read) and
// returns its standard output; on failure the error is git's own words.
func (h *Hub) runGit(dir, stdin string, args ...string) (string, error) {
	bin, err := gitBinary()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(h.gitRoot())
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

// repoSize is the disk a hosted repository uses, or 0.
func (h *Hub) repoSize(name string) int64 {
	var n int64
	filepath.WalkDir(h.repoPath(name), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// ---- creating a hosted project ----

func codebaseHost(c *call) (any, error) {
	if err := c.allow(ActProjectManage); err != nil {
		return nil, err
	}
	var req api.CodebaseHostReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.hostCodebase(req.Name)
}

// hostCodebase makes a bare repository on the hub with a first commit on main (a README) and
// the hook that guards it, and records the project. Nothing is left on disk if it fails.
func (c *call) hostCodebase(name string) (api.Codebase, error) {
	name = strings.TrimSpace(name)
	if err := checkCodebaseName(name); err != nil {
		return api.Codebase{}, err
	}
	if err := c.h.checkGitServes(); err != nil {
		return api.Codebase{}, err
	}
	h := c.h
	if h.gitRoot() == "" {
		return api.Codebase{}, &apiError{503, "unavailable", "this hub has no data folder to keep code in, so it cannot host projects"}
	}
	base, err := h.hostedBase(c.r)
	if err != nil {
		return api.Codebase{}, err
	}
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM codebase WHERE name = ?`, name).Scan(&n); err != nil {
		return api.Codebase{}, err
	}
	if n > 0 {
		return api.Codebase{}, conflict("there is already a project called %q", name)
	}
	repo := h.repoPath(name)
	if err := os.MkdirAll(h.gitRoot(), 0o700); err != nil {
		return api.Codebase{}, fmt.Errorf("could not make the folder for hosted code: %w", err)
	}
	os.RemoveAll(repo) // left over from a creation that did not finish: there is no project of this name
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(repo)
		}
	}()
	if err := h.initHostedRepo(repo, name); err != nil {
		return api.Codebase{}, &apiError{500, "internal", "could not set up the repository on the hub: " + err.Error()}
	}
	remote := hostedRemote(base, name)
	if _, err := c.tx.Exec(`INSERT INTO codebase(name, remote, base_branch, hosted, created_by, created_at) VALUES (?, ?, 'main', 1, ?, ?)`,
		name, remote, c.p.actor(), store.Millis(c.now)); err != nil {
		return api.Codebase{}, conflict("there is already a project called %q", name)
	}
	if err := c.audit("project.create", "project:"+name, map[string]any{"hosted": true, "remote": remote}); err != nil {
		return api.Codebase{}, err
	}
	r, err := c.codebaseByName(name)
	if err != nil {
		return api.Codebase{}, err
	}
	ok = true
	v, err := c.codebaseView(r)
	v.SizeBytes = h.repoSize(name)
	return v, err
}

// initHostedRepo makes the bare repository, the first commit and the hook. The first commit is
// built with plumbing commands, so no working folder is needed.
func (h *Hub) initHostedRepo(repo, name string) error {
	if _, err := h.runGit("", "", "init", "-q", "--bare", repo); err != nil {
		return err
	}
	git := func(stdin string, args ...string) (string, error) {
		return h.runGit(repo, stdin, append([]string{"--git-dir", repo}, args...)...)
	}
	for _, kv := range [][2]string{{"receive.denyDeletes", "true"}, {"receive.denyNonFastForwards", "true"}, {"uploadpack.allowAnySHA1InWant", "false"}} {
		if _, err := git("", "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := git("", "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	blob, err := git("# "+name+"\n", "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	tree, err := git(fmt.Sprintf("100644 blob %s\tREADME.md\n", strings.TrimSpace(blob)), "mktree")
	if err != nil {
		return err
	}
	commit, err := git("", "commit-tree", strings.TrimSpace(tree), "-m", "Start the project")
	if err != nil {
		return err
	}
	if _, err := git("", "update-ref", "refs/heads/main", strings.TrimSpace(commit)); err != nil {
		return err
	}
	hooks := filepath.Join(repo, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte(preReceiveHook), 0o755)
}

// ---- serving git ----

// gitPaths are the only things git's smart protocol asks for.
var gitPaths = map[string]string{"/info/refs": "GET", "/git-upload-pack": "POST", "/git-receive-pack": "POST"}

type gitWriter struct{ h *Hub }

func (g gitWriter) Write(p []byte) (int, error) {
	g.h.opt.Log.Printf("git: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}

func plainText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintln(w, msg)
}

// handleGit serves /git/<name>.git/... with git http-backend. The password of HTTP Basic
// authentication is a device credential; the user name is ignored. Only a machine's
// credential works: not a person's token, an AI app's token, an agent's token or the
// admin token. The request is streamed to git; no size limit applies.
func (h *Hub) handleGit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rest := strings.TrimPrefix(r.URL.Path, "/git/")
	seg, sub, _ := strings.Cut(rest, "/")
	sub = "/" + sub
	name, isGit := strings.CutSuffix(seg, ".git")
	method, known := gitPaths[sub]
	if !isGit || !known || !codebaseNameRE.MatchString(name) {
		plainText(w, 404, "Not found.")
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		plainText(w, 405, "That is not how git talks to this address.")
		return
	}
	ip := h.clientIP(r)
	_, pass, hasAuth := r.BasicAuth()
	deny := func(status int, msg string) {
		if status == 401 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Handloom", charset="UTF-8"`)
		}
		plainText(w, status, msg)
	}
	if !hasAuth || pass == "" {
		deny(401, "Handloom needs this machine's credential. On a machine that joined the hub, git gets it by itself; if this is a clone made by hand, fetch the project with: handloom project clone NAME --device THIS-MACHINE")
		return
	}
	// Refusals are rate limited per address, so a stolen guess cannot be tried quickly.
	refused := func(status int, msg string) {
		if !h.allowRate("gitfail:"+ip, 30, 10, h.opt.Now()) {
			deny(429, "Too many refused attempts. Wait a minute.")
			return
		}
		deny(status, msg)
	}
	if !strings.HasPrefix(pass, store.PrefixDevice) {
		refused(403, "This address takes a machine's credential, the one `handloom link join` saved. A person's token, an AI app's token and the admin token do not work here.")
		return
	}
	if api.AgentFrom(r.Header) != "" {
		refused(403, "An agent cannot use this address itself; the machine's git does, through the link.")
		return
	}
	device, hosted, err := h.gitCaller(r.Context(), pass, name)
	if err != nil {
		h.opt.Log.Printf("git sign-in: %v", err)
		plainText(w, 500, "Something went wrong on the hub. It has been logged.")
		return
	}
	switch {
	case device == "":
		refused(401, "That credential is not accepted (is this machine still joined to the hub? it may have been revoked).")
		return
	case !hosted:
		plainText(w, 404, "Not found.")
		return
	}
	bin, err := gitBinary()
	if err != nil {
		plainText(w, 503, noGitText)
		return
	}
	root := h.gitRoot()
	if root == "" {
		plainText(w, 503, "This hub has no data folder to keep code in.")
		return
	}
	// A push or a clone can outlast the server's read and write limits.
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{})
	rc.SetWriteDeadline(time.Time{})
	// The credential is the caller's; git and its hook never see it.
	r2 := r.Clone(r.Context())
	r2.Header.Del("Authorization")
	handler := &cgi.Handler{
		Path: bin, Root: "/git", Dir: root,
		Args: []string{"-c", "http.receivepack=true", "http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + device,
			"HOME=" + root, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		},
		InheritEnv: []string{"PATH"},
		Stderr:     gitWriter{h},
	}
	handler.ServeHTTP(w, r2)
}

// gitCaller finds the live device a credential belongs to ("" when there is none) and whether the
// project is a hosted one. It uses its own short transaction: the hub has one database connection
// and a push must not hold it.
func (h *Hub) gitCaller(ctx context.Context, cred, project string) (device string, hosted bool, err error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	hash := store.HashToken(cred)
	var name string
	var revoked sql.NullInt64
	var stored string
	if err := tx.QueryRow(`SELECT name, revoked_at, credential_hash FROM device WHERE credential_hash = ?`, hash).Scan(&name, &revoked, &stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if revoked.Valid || subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
		return "", false, nil
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM codebase WHERE name = ? AND hosted = 1`, project).Scan(&n); err != nil {
		return "", false, err
	}
	return name, n > 0, nil
}

// ---- merging ----

func codebaseMerge(c *call) (any, error) {
	if err := c.allow(ActProjectMerge); err != nil {
		return nil, err
	}
	var req api.CodebaseMergeReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.mergeHosted(c.r.PathValue("name"), req)
}

var mergeBranchRE = regexp.MustCompile(`^handloom/[A-Za-z0-9][A-Za-z0-9._/-]{0,120}$`)

// oneLine makes a job's title safe to put in a commit message.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = strings.TrimSpace(s[:max])
	}
	return s
}

// mergeFailed audits a merge that did not happen and answers in words. Nothing else is written:
// the error is kept so that the audit row is.
func (c *call) mergeFailed(project string, job int64, branch, why string) error {
	if err := c.audit("project.merge_failed", "project:"+project, map[string]any{"job": job, "branch": branch, "reason": why}); err != nil {
		return err
	}
	return keepErr{&apiError{409, "conflict", why}}
}

// mergeHosted puts a job's published branch into the base branch of a hosted project. The merge
// is made in a temporary working folder of the repository (a fast-forward when the branch simply
// continues main, else a merge commit "Merge job N: TITLE"), and main is moved last, in one step
// that only succeeds if main is still where the merge started. Any failure leaves main untouched.
func (c *call) mergeHosted(name string, req api.CodebaseMergeReq) (api.CodebaseMerge, error) {
	var out api.CodebaseMerge
	cb, err := c.codebaseByName(name)
	if err != nil {
		return out, err
	}
	if !cb.hosted {
		return out, conflict("project %q is not held by Handloom, so Handloom does not merge for it: merge its pull request where the code lives", name)
	}
	t, err := c.job(req.Job)
	if err != nil {
		return out, err
	}
	var jobProject sql.NullInt64
	c.tx.QueryRow(`SELECT codebase_id FROM task WHERE id = ?`, t.id).Scan(&jobProject)
	if !jobProject.Valid || jobProject.Int64 != cb.id {
		return out, badRequest("job %d does not belong to project %s", t.id, name)
	}
	req.Branch = strings.TrimSpace(req.Branch)
	if req.Branch != "" && (!mergeBranchRE.MatchString(req.Branch) || strings.Contains(req.Branch, "..") || strings.HasSuffix(req.Branch, "/") || strings.HasSuffix(req.Branch, ".lock")) {
		return out, badRequest("%q is not a branch published to this project (those are named handloom/...)", req.Branch)
	}
	// Which publish: the named branch, else the job's latest finished one.
	var pubID int64
	var branch string
	q := `SELECT id, branch FROM publish WHERE job_id = ? AND status = 'done'`
	args := []any{t.id}
	if req.Branch != "" {
		q += ` AND branch = ?`
		args = append(args, req.Branch)
	}
	if err := c.tx.QueryRow(q+` ORDER BY id DESC LIMIT 1`, args...).Scan(&pubID, &branch); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if req.Branch != "" {
			return out, badRequest("job %d did not publish a branch called %s; see: handloom job show %d", t.id, req.Branch, t.id)
		}
		return out, conflict("job %d has not been published to this project yet; publish it first: handloom job publish %d", t.id, t.id)
	}
	if t.confidential {
		return out, conflict("job %d is confidential, and confidential work must not sit on the hub", t.id)
	}
	if _, err := gitBinary(); err != nil {
		return out, err
	}
	repo := c.h.repoPath(name)
	if st, err := os.Stat(repo); err != nil || !st.IsDir() {
		return out, c.mergeFailed(name, t.id, branch, "the repository of this project is missing on the hub's disk (was the git folder left out of a restore?); nothing was changed")
	}
	into := cb.base
	if into == "" {
		into = "main"
	}
	h := c.h
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	git := func(args ...string) (string, error) {
		return h.runGit(repo, "", append([]string{"--git-dir", repo}, args...)...)
	}
	branchSHA, err := git("rev-parse", "--verify", "-q", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return out, c.mergeFailed(name, t.id, branch, fmt.Sprintf("the branch %s is not in the repository on the hub (it was published, but is gone); publish job %d again. Nothing was changed", branch, t.id))
	}
	baseSHA, err := git("rev-parse", "--verify", "-q", "refs/heads/"+into+"^{commit}")
	if err != nil {
		return out, c.mergeFailed(name, t.id, branch, fmt.Sprintf("the project has no branch %s to merge into. Nothing was changed", into))
	}
	branchSHA, baseSHA = strings.TrimSpace(branchSHA), strings.TrimSpace(baseSHA)
	out = api.CodebaseMerge{Project: name, Job: t.id, Branch: branch, Into: into}

	newSHA, how := "", ""
	switch {
	case branchSHA == baseSHA, isAncestor(git, branchSHA, baseSHA):
		newSHA, how = baseSHA, "already merged"
	case isAncestor(git, baseSHA, branchSHA):
		newSHA, how = branchSHA, "fast-forward"
	default:
		work, err := os.MkdirTemp(h.gitRoot(), ".merge-")
		if err != nil {
			return out, fmt.Errorf("could not make a working folder: %w", err)
		}
		defer func() {
			git("worktree", "remove", "--force", work)
			os.RemoveAll(work)
			git("worktree", "prune")
		}()
		if _, err := git("worktree", "add", "-q", "--detach", work, baseSHA); err != nil {
			return out, c.mergeFailed(name, t.id, branch, "could not prepare the merge: "+err.Error()+". Nothing was changed")
		}
		wgit := func(args ...string) (string, error) { return h.runGit(work, "", args...) }
		msg := fmt.Sprintf("Merge job %d: %s", t.id, oneLine(t.title, 100))
		if _, err := wgit("-c", "commit.gpgsign=false", "merge", "--no-ff", "-q", "-m", msg, branchSHA); err != nil {
			files, _ := wgit("diff", "--name-only", "--diff-filter=U", "-z")
			var names []string
			for _, f := range strings.Split(files, "\x00") {
				if f != "" {
					names = append(names, f)
				}
			}
			why := fmt.Sprintf("job %d cannot be merged into %s: %s", t.id, into, err.Error())
			if len(names) > 0 {
				shown := names
				if len(shown) > 10 {
					shown = shown[:10]
				}
				why = fmt.Sprintf("job %d conflicts with the current %s in: %s%s. Nothing was changed. The job's work needs to be brought up to date with %s (start a new job from the project, or merge %s into the job's branch by hand) and published again",
					t.id, into, strings.Join(shown, ", "), map[bool]string{true: " and more", false: ""}[len(names) > len(shown)], into, into)
			}
			return out, c.mergeFailed(name, t.id, branch, why)
		}
		head, err := wgit("rev-parse", "HEAD")
		if err != nil {
			return out, c.mergeFailed(name, t.id, branch, "the merge was made but its result could not be read: "+err.Error()+". Nothing was changed")
		}
		newSHA, how = strings.TrimSpace(head), "merge commit"
		if !isAncestor(git, baseSHA, newSHA) || !isAncestor(git, branchSHA, newSHA) {
			return out, c.mergeFailed(name, t.id, branch, "the merge result does not contain both sides, so it was not used. Nothing was changed")
		}
	}
	if how != "already merged" {
		// Moves main only if it is still where the merge started; no hook runs (it guards pushes from machines).
		if _, err := git("update-ref", "-m", fmt.Sprintf("handloom: merge job %d", t.id), "refs/heads/"+into, newSHA, baseSHA); err != nil {
			return out, c.mergeFailed(name, t.id, branch, fmt.Sprintf("%s changed while the merge was being made; try again. Nothing was changed", into))
		}
	}
	out.Commit, out.How = newSHA, how
	if _, err := c.tx.Exec(`UPDATE publish SET merged_into = ?, merged_at = ?, merged_commit = ? WHERE id = ?`, into, store.Millis(c.now), newSHA, pubID); err != nil {
		return out, err
	}
	if err := c.record(t.projectID, 0, "project.merge", "project:"+name, map[string]any{"job": t.id, "branch": branch, "into": into, "commit": newSHA, "how": how}); err != nil {
		return out, err
	}
	return out, nil
}

func isAncestor(git func(...string) (string, error), a, b string) bool {
	_, err := git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// hostedFor says whether a job belongs to a hosted project, and which.
func (c *call) hostedFor(job int64) (name string, hosted bool) {
	err := c.tx.QueryRow(`SELECT cb.name FROM task t JOIN codebase cb ON cb.id = t.codebase_id WHERE t.id = ? AND cb.hosted = 1`, job).Scan(&name)
	return name, err == nil
}
