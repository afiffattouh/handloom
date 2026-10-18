package hub

import (
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"handloom/internal/api"
	"handloom/internal/store"
)

// A project (D104) is a name and a shared git remote that any machine can get
// on demand. Inside the program it is called a codebase, because the hub's
// table "project" is the tenancy. The hub keeps the name, the remote's
// address and, per machine, where the clone is and how it went. Cloning and
// creating a repository on GitHub (or any other host) happen on a machine,
// under that machine's own git and gh login: the code and those credentials
// never reach the hub. The one exception is a hosted project (D106, hosted.go),
// which a person chooses on purpose: then the hub itself keeps the repository,
// on its disk next to the database, and the machines use their device
// credential to reach it.

var (
	codebaseNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	ownerRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	sshUserRE      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostRE         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	scpRE          = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31})@([A-Za-z0-9][A-Za-z0-9.-]{0,252}):([A-Za-z0-9._~/-]+)$`)
)

func checkCodebaseName(name string) error {
	if !codebaseNameRE.MatchString(name) {
		return badRequest("bad project name %q: use lowercase letters, digits, '.', '_' or '-', at most 40 characters, starting with a letter or digit", name)
	}
	return nil
}

// checkRemote accepts https://host/path, ssh://[user@]host/path and git@host:path,
// and refuses everything that could carry a login or point at the hub's own disk:
// a user name and password or token in the address, a local path, a query, spaces
// and control characters. file:// is accepted only when the hub allows it (tests).
func checkRemote(remote string, allowFile bool) error {
	bad := func(why string) error {
		return badRequest("the repository address is not accepted: %s. Use https://host/owner/name, ssh://git@host/owner/name or git@host:owner/name, with no password or token in it (each machine uses its own login)", why)
	}
	if remote == "" {
		return bad("it is empty")
	}
	if len(remote) > 300 {
		return bad("it is longer than 300 characters")
	}
	for _, r := range remote {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' {
			return bad("it has a space or a control character")
		}
	}
	if strings.HasPrefix(remote, "-") {
		return bad("it starts with a dash")
	}
	if m := scpRE.FindStringSubmatch(remote); m != nil {
		if strings.Contains(m[3], "..") {
			return bad("it has '..' in the path")
		}
		return nil
	}
	if !strings.Contains(remote, "://") {
		if strings.HasPrefix(remote, "/") || strings.HasPrefix(remote, ".") || strings.HasPrefix(remote, "~") {
			return bad("a path on a disk is not a shared address")
		}
		return bad("it is not an https://, ssh:// or git@host:path address")
	}
	u, err := url.Parse(remote)
	if err != nil {
		return bad("it is not a valid address")
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.Contains(remote, "?") || strings.Contains(remote, "#") {
		return bad("it has a query or fragment, which could carry a token")
	}
	if strings.Contains(u.Path, "..") {
		return bad("it has '..' in the path")
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			return bad("it has a user name, password or token in it")
		}
	case "ssh":
		if u.User != nil {
			if _, hasPw := u.User.Password(); hasPw || !sshUserRE.MatchString(u.User.Username()) {
				return bad("it has a password or token in it")
			}
		}
	case "file":
		if !allowFile {
			return bad("file:// addresses are not allowed on this hub")
		}
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return bad("a file:// address must be file:///absolute/path")
		}
		return nil
	case "http":
		return bad("http:// is not accepted, use https://")
	default:
		return bad("the scheme " + u.Scheme + ":// is not accepted")
	}
	if !hostRE.MatchString(u.Hostname()) {
		return bad("the host name is not valid")
	}
	if strings.Trim(u.Path, "/") == "" {
		return bad("it has no path (owner/name)")
	}
	return nil
}

type cbRow struct {
	id                 int64
	name, remote, base string
	createdBy          string
	created            int64
	hosted             bool
	check              string
}

func (c *call) codebaseByName(name string) (*cbRow, error) {
	r := &cbRow{}
	err := c.tx.QueryRow(`SELECT id, name, remote, base_branch, created_by, created_at, hosted, check_cmd FROM codebase WHERE name = ?`, name).
		Scan(&r.id, &r.name, &r.remote, &r.base, &r.createdBy, &r.created, &r.hosted, &r.check)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no project %q: see the list with: handloom project list", name)
	}
	return r, err
}

func (c *call) codebaseView(r *cbRow) (api.Codebase, error) {
	out := api.Codebase{Name: r.name, Remote: r.remote, Base: r.base, Check: r.check, CreatedBy: r.createdBy, CreatedAt: store.Time(r.created), Hosted: r.hosted, Clones: []api.CodebaseClone{}}
	// A member sees the copies on their own machines only (D108).
	where, wargs := c.deviceScope().Clause("d")
	rows, err := c.tx.Query(`SELECT d.name, k.path, k.status, k.detail, k.updated_at FROM codebase_clone k JOIN device d ON d.id = k.device_id
		WHERE k.codebase_id = ? AND `+where+` ORDER BY d.name`, append([]any{r.id}, wargs...)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k api.CodebaseClone
		var at int64
		if err := rows.Scan(&k.Device, &k.Path, &k.Status, &k.Detail, &at); err != nil {
			return out, err
		}
		k.At = store.Time(at)
		out.Clones = append(out.Clones, k)
	}
	return out, rows.Err()
}

// ---- the API ----

func codebaseList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	return c.codebases()
}

func (c *call) codebases() ([]api.Codebase, error) {
	rows, err := c.tx.Query(`SELECT id, name, remote, base_branch, created_by, created_at, hosted, check_cmd FROM codebase ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var rs []*cbRow
	for rows.Next() {
		r := &cbRow{}
		if err := rows.Scan(&r.id, &r.name, &r.remote, &r.base, &r.createdBy, &r.created, &r.hosted, &r.check); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	out := []api.Codebase{}
	for _, r := range rs {
		v, err := c.codebaseView(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func codebaseGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	r, err := c.codebaseByName(c.r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	v, err := c.codebaseView(r)
	if err == nil && r.hosted {
		v.SizeBytes = c.h.repoSize(r.name)
	}
	return v, err
}

func codebaseCheck(c *call) (any, error) {
	if err := c.allow(ActProjectManage); err != nil {
		return nil, err
	}
	var req api.CodebaseCheckReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.setCodebaseCheck(c.r.PathValue("name"), req.Check)
}

// setCodebaseCheck is the work of setting a project's check, for the API and the web form. The command runs
// on the owner's machines, so only someone who manages projects may set it, and a job started by an AI app
// can use it but not change it.
func (c *call) setCodebaseCheck(name, check string) (api.Codebase, error) {
	check = strings.TrimSpace(check)
	if check != "" {
		if err := checkRepoSpec("/x", check); err != nil {
			return api.Codebase{}, err
		}
	}
	r, err := c.codebaseByName(name)
	if err != nil {
		return api.Codebase{}, err
	}
	if _, err := c.tx.Exec(`UPDATE codebase SET check_cmd = ? WHERE id = ?`, check, r.id); err != nil {
		return api.Codebase{}, err
	}
	if err := c.record(0, 0, "project.check", "project:"+name, map[string]any{"check": check}); err != nil {
		return api.Codebase{}, err
	}
	r.check = check
	return c.codebaseView(r)
}

func codebaseAttach(c *call) (any, error) {
	if err := c.allow(ActProjectManage); err != nil {
		return nil, err
	}
	var req api.CodebaseReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.attachCodebase(req)
}

// attachCodebase is the work of "attach an existing repository", for the API and the web form.
func (c *call) attachCodebase(req api.CodebaseReq) (api.Codebase, error) {
	req.Name, req.Remote, req.Base = strings.TrimSpace(req.Name), strings.TrimSpace(req.Remote), strings.TrimSpace(req.Base)
	if err := checkCodebaseName(req.Name); err != nil {
		return api.Codebase{}, err
	}
	if err := checkRemote(req.Remote, c.h.opt.AllowFileRemotes); err != nil {
		return api.Codebase{}, err
	}
	if err := checkBase(req.Base); err != nil {
		return api.Codebase{}, err
	}
	if _, err := c.tx.Exec(`INSERT INTO codebase(name, remote, base_branch, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		req.Name, req.Remote, req.Base, c.p.actor(), store.Millis(c.now)); err != nil {
		return api.Codebase{}, conflict("there is already a project called %q", req.Name)
	}
	if err := c.audit("project.attach", "project:"+req.Name, map[string]any{"remote": req.Remote, "base": req.Base}); err != nil {
		return api.Codebase{}, err
	}
	r, err := c.codebaseByName(req.Name)
	if err != nil {
		return api.Codebase{}, err
	}
	return c.codebaseView(r)
}

func checkBase(base string) error {
	if base == "" {
		return nil
	}
	if len(base) > 200 || strings.HasPrefix(base, "-") || strings.HasPrefix(base, "/") || strings.Contains(base, "..") || !baseRE.MatchString(base) {
		return badRequest("the base must be a branch name such as main")
	}
	return nil
}

func codebaseCreate(c *call) (any, error) {
	if err := c.allow(ActProjectManage); err != nil {
		return nil, err
	}
	var req api.CodebaseCreateReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.createCodebase(req)
}

// createCodebase queues the creation of a repository on a machine. The project is
// recorded with an empty address that the machine's report fills in. A project whose
// creation failed (still no address, nothing waiting) can be asked for again.
func (c *call) createCodebase(req api.CodebaseCreateReq) (api.CodebaseRequest, error) {
	req.Name, req.Owner, req.Base = strings.TrimSpace(req.Name), strings.TrimSpace(req.Owner), strings.TrimSpace(req.Base)
	if err := checkCodebaseName(req.Name); err != nil {
		return api.CodebaseRequest{}, err
	}
	if req.Owner != "" && !ownerRE.MatchString(req.Owner) {
		return api.CodebaseRequest{}, badRequest("bad owner %q: a GitHub user or organisation name, or leave it empty for the account the machine is logged in as", req.Owner)
	}
	if err := checkBase(req.Base); err != nil {
		return api.CodebaseRequest{}, err
	}
	private := req.Private == nil || *req.Private
	dev, err := c.spawnDevice(req.Device)
	if err != nil {
		return api.CodebaseRequest{}, err
	}
	var id int64
	err = c.tx.QueryRow(`SELECT id FROM codebase WHERE name = ?`, req.Name).Scan(&id)
	switch {
	case err == nil:
		var remote string
		var waiting int
		c.tx.QueryRow(`SELECT remote FROM codebase WHERE id = ?`, id).Scan(&remote)
		c.tx.QueryRow(`SELECT count(*) FROM codebase_request WHERE codebase_id = ? AND status = 'pending'`, id).Scan(&waiting)
		if remote != "" || waiting > 0 {
			return api.CodebaseRequest{}, conflict("there is already a project called %q", req.Name)
		}
	case errors.Is(err, sql.ErrNoRows):
		res, err := c.tx.Exec(`INSERT INTO codebase(name, remote, base_branch, created_by, created_at) VALUES (?, '', ?, ?, ?)`,
			req.Name, req.Base, c.p.actor(), store.Millis(c.now))
		if err != nil {
			return api.CodebaseRequest{}, err
		}
		id, _ = res.LastInsertId()
	default:
		return api.CodebaseRequest{}, err
	}
	rid, err := c.queueCodebaseRequest(id, dev, "create", req.Owner, private)
	if err != nil {
		return api.CodebaseRequest{}, err
	}
	if err := c.audit("project.create", "project:"+req.Name, map[string]any{"owner": req.Owner, "private": private, "device": req.Device}); err != nil {
		return api.CodebaseRequest{}, err
	}
	return c.oneCodebaseRequest(rid)
}

func (c *call) queueCodebaseRequest(codebase, device int64, kind, owner string, private bool) (int64, error) {
	ms := store.Millis(c.now)
	res, err := c.tx.Exec(`INSERT INTO codebase_request(codebase_id, device_id, kind, owner, private, requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		codebase, device, kind, owner, private, c.p.actor(), ms)
	if err != nil {
		return 0, err
	}
	if _, err := c.tx.Exec(`INSERT INTO codebase_clone(codebase_id, device_id, status, created_at, updated_at) VALUES (?, ?, 'pending', ?, ?)
		ON CONFLICT(codebase_id, device_id) DO UPDATE SET status = 'pending', detail = '', updated_at = excluded.updated_at`, codebase, device, ms, ms); err != nil {
		return 0, err
	}
	// Wake that machine's link now rather than at its next tick.
	if err := c.emit(0, 0, "project.requested", map[string]any{"device_id": device}); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func codebaseClone(c *call) (any, error) {
	if err := c.allow(ActProjectManage); err != nil {
		return nil, err
	}
	var req api.CodebaseCloneReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.cloneCodebase(c.r.PathValue("name"), req.Device)
}

// cloneCodebase asks a machine to get its own copy. A copy that is ready is
// answered with its path; one that is on its way is a conflict.
func (c *call) cloneCodebase(name, device string) (api.CodebaseRequest, error) {
	r, err := c.codebaseByName(name)
	if err != nil {
		return api.CodebaseRequest{}, err
	}
	dev, err := c.spawnDevice(device)
	if err != nil {
		return api.CodebaseRequest{}, err
	}
	var devName string
	c.tx.QueryRow(`SELECT name FROM device WHERE id = ?`, dev).Scan(&devName)
	if r.remote == "" {
		return api.CodebaseRequest{}, conflict("project %q has no repository yet: its creation has not finished (or failed); see: handloom project show %s", name, name)
	}
	var status, path string
	err = c.tx.QueryRow(`SELECT status, path FROM codebase_clone WHERE codebase_id = ? AND device_id = ?`, r.id, dev).Scan(&status, &path)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return api.CodebaseRequest{}, err
	}
	var waiting int
	if err := c.tx.QueryRow(`SELECT count(*) FROM codebase_request WHERE codebase_id = ? AND device_id = ? AND status = 'pending'`, r.id, dev).Scan(&waiting); err != nil {
		return api.CodebaseRequest{}, err
	}
	if waiting > 0 {
		return api.CodebaseRequest{}, conflict("project %q is already being fetched to %s; wait for it to finish", name, devName)
	}
	if status == "ready" {
		return api.CodebaseRequest{Codebase: name, Device: devName, Kind: "clone", Status: "ready", Path: path, At: c.now.UTC()}, nil
	}
	rid, err := c.queueCodebaseRequest(r.id, dev, "clone", "", true)
	if err != nil {
		return api.CodebaseRequest{}, err
	}
	if err := c.audit("project.clone", "project:"+name, map[string]any{"device": devName}); err != nil {
		return api.CodebaseRequest{}, err
	}
	return c.oneCodebaseRequest(rid)
}

func (c *call) oneCodebaseRequest(id int64) (api.CodebaseRequest, error) {
	var q api.CodebaseRequest
	var at int64
	var done sql.NullInt64
	err := c.tx.QueryRow(`SELECT q.id, cb.name, d.name, q.kind, q.status, q.detail, q.requested_by, q.created_at, q.done_at,
		COALESCE((SELECT k.path FROM codebase_clone k WHERE k.codebase_id = q.codebase_id AND k.device_id = q.device_id), '')
		FROM codebase_request q JOIN codebase cb ON cb.id = q.codebase_id JOIN device d ON d.id = q.device_id WHERE q.id = ?`, id).
		Scan(&q.ID, &q.Codebase, &q.Device, &q.Kind, &q.Status, &q.Detail, &q.RequestedBy, &at, &done, &q.Path)
	if err != nil {
		return q, err
	}
	q.At = store.Time(at)
	q.DoneAt = store.TimePtr(done)
	return q, nil
}

// deviceCodebaseRequests is what a link asks for: the clones and creations waiting for its device.
func deviceCodebaseRequests(c *call) (any, error) {
	if c.p.kind != kindDevice || c.p.agentName != "" {
		return nil, forbidden("only a machine's link may list its project requests")
	}
	rows, err := c.tx.Query(`SELECT q.id, cb.name, q.kind, cb.remote, cb.base_branch, q.owner, q.private, cb.hosted FROM codebase_request q JOIN codebase cb ON cb.id = q.codebase_id
		WHERE q.device_id = ? AND q.status = 'pending' ORDER BY q.id LIMIT 5`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.PendingCodebaseRequest{}
	for rows.Next() {
		var p api.PendingCodebaseRequest
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Remote, &p.Base, &p.Owner, &p.Private, &p.Hosted); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// codebaseReport is the device's account of a request. Only its own device, and only once.
func codebaseReport(c *call) (any, error) {
	if c.p.kind != kindDevice || c.p.agentName != "" {
		return nil, forbidden("only the link of the machine that does the work reports on it, not an agent")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.CodebaseReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Status != "done" && req.Status != "failed" {
		return nil, badRequest("status must be done or failed")
	}
	var cb, dev int64
	var kind, status, name string
	err = c.tx.QueryRow(`SELECT q.codebase_id, q.device_id, q.kind, q.status, cb.name FROM codebase_request q JOIN codebase cb ON cb.id = q.codebase_id WHERE q.id = ?`, id).
		Scan(&cb, &dev, &kind, &status, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no project request %d", id)
	}
	if err != nil {
		return nil, err
	}
	if dev != c.p.deviceID {
		return nil, forbidden("project request %d belongs to another machine", id)
	}
	if status != "pending" {
		return nil, conflict("project request %d is already %s", id, status)
	}
	if len(req.Detail) > 2000 {
		req.Detail = req.Detail[:2000]
	}
	req.Path, req.Remote, req.Base = strings.TrimSpace(req.Path), strings.TrimSpace(req.Remote), strings.TrimSpace(req.Base)
	if req.Status == "done" {
		if req.Path == "" || !strings.HasPrefix(req.Path, "/") || len(req.Path) > 400 || strings.ContainsAny(req.Path, "\x00\n\r") {
			return nil, badRequest("a finished request must report the absolute path of the clone")
		}
		if kind == "create" {
			if err := checkRemote(req.Remote, c.h.opt.AllowFileRemotes); err != nil {
				return nil, err
			}
		}
		if err := checkBase(req.Base); err != nil {
			return nil, err
		}
	}
	ms := store.Millis(c.now)
	if _, err := c.tx.Exec(`UPDATE codebase_request SET status = ?, detail = ?, done_at = ? WHERE id = ?`, req.Status, req.Detail, ms, id); err != nil {
		return nil, err
	}
	cloneStatus := "ready"
	if req.Status == "failed" {
		cloneStatus = "failed"
	}
	path := req.Path
	if req.Status == "failed" {
		path = ""
	}
	if _, err := c.tx.Exec(`UPDATE codebase_clone SET status = ?, path = ?, detail = ?, updated_at = ? WHERE codebase_id = ? AND device_id = ?`,
		cloneStatus, path, req.Detail, ms, cb, dev); err != nil {
		return nil, err
	}
	if req.Status == "done" {
		if kind == "create" {
			if _, err := c.tx.Exec(`UPDATE codebase SET remote = ? WHERE id = ? AND remote = ''`, req.Remote, cb); err != nil {
				return nil, err
			}
		}
		if req.Base != "" {
			if _, err := c.tx.Exec(`UPDATE codebase SET base_branch = ? WHERE id = ? AND base_branch = ''`, req.Base, cb); err != nil {
				return nil, err
			}
		}
	}
	action := "project." + kind + "_done"
	if req.Status == "failed" {
		action = "project." + kind + "_failed"
	}
	return nil, c.audit(action, "project:"+name, map[string]any{"path": path, "remote": req.Remote, "base": req.Base})
}

// codebaseFor resolves a project on a machine for a job: the ready clone's path and the project's base branch.
func (c *call) codebaseFor(name string, deviceID int64) (id int64, repo, base string, err error) {
	r, err := c.codebaseByName(name)
	if err != nil {
		return 0, "", "", err
	}
	var devName, status, path string
	c.tx.QueryRow(`SELECT name FROM device WHERE id = ?`, deviceID).Scan(&devName)
	e := c.tx.QueryRow(`SELECT status, path FROM codebase_clone WHERE codebase_id = ? AND device_id = ?`, r.id, deviceID).Scan(&status, &path)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return 0, "", "", e
	}
	if status != "ready" || path == "" {
		return 0, "", "", conflict("project %q is not on %s yet: run: handloom project clone %s --device %s", name, devName, name, devName)
	}
	return r.id, path, r.base, nil
}

// codebaseName is the project a job was started from, or "".
func (c *call) codebaseName(task int64) string {
	var n string
	c.tx.QueryRow(`SELECT cb.name FROM task t JOIN codebase cb ON cb.id = t.codebase_id WHERE t.id = ?`, task).Scan(&n)
	return n
}
