package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// A submitted task's result: the machine that holds the work reports which files changed and the diff, and
// a person reads it on the job page. The hub keeps a bounded copy (the files themselves stay on the machine);
// web pages among the changes are kept in full when small, so a person can open them.

const (
	maxResultDiff    = 200 << 10
	maxResultFiles   = 200
	maxResultPages   = 8
	maxResultPageLen = 64 << 10
)

func taskResultPost(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device reports a result")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.TaskResultReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	a, err := c.agentByName(req.Agent)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not on this device", req.Agent)
	}
	t, err := c.task(id)
	if err != nil {
		return nil, err
	}
	if t.projectID != a.projectID || !t.owner.Valid || t.owner.Int64 != a.id {
		return nil, forbidden("task %d is not %s's", id, a.name)
	}
	if t.status != api.StatusSubmitted && t.status != api.StatusDone {
		return nil, conflict("task %d is %s; only submitted work has a result", id, t.status)
	}
	if len(req.Files) > maxResultFiles {
		req.Files, req.Truncated = req.Files[:maxResultFiles], true
	}
	for i := range req.Files {
		f := &req.Files[i]
		f.Path = cutString(f.Path, 300)
		if f.Status != "added" && f.Status != "deleted" {
			f.Status = "modified"
		}
	}
	if len(req.Diff) > maxResultDiff {
		req.Diff, req.Truncated = req.Diff[:maxResultDiff], true
	}
	pages := map[string]string{}
	var names []string
	for p := range req.Pages {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		if len(pages) >= maxResultPages || len(req.Pages[p]) > maxResultPageLen || !isPagePath(p) {
			continue
		}
		pages[p] = req.Pages[p]
	}
	files, _ := json.Marshal(req.Files)
	pg, _ := json.Marshal(pages)
	trunc := 0
	if req.Truncated {
		trunc = 1
	}
	if _, err := c.tx.Exec(`INSERT INTO task_result(task_id, agent, files, diff, pages, truncated, at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET agent = excluded.agent, files = excluded.files, diff = excluded.diff, pages = excluded.pages,
		truncated = excluded.truncated, at = excluded.at`, id, a.name, string(files), req.Diff, string(pg), trunc, store.Millis(c.now)); err != nil {
		return nil, err
	}
	return nil, nil
}

func cutString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

var pagePathRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// isPagePath is a relative path to an .html file with no dot segments.
func isPagePath(p string) bool {
	if !pagePathRE.MatchString(p) || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || strings.HasPrefix(seg, "..") {
			return false
		}
	}
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".html") || strings.HasSuffix(l, ".htm")
}

// result reads a task's stored result; nil when there is none.
func (c *call) result(taskID int64) (*api.TaskResult, map[string]string, error) {
	var files, diff, pages string
	var trunc int
	var at int64
	r := &api.TaskResult{}
	err := c.tx.QueryRow(`SELECT agent, files, diff, pages, truncated, at FROM task_result WHERE task_id = ?`, taskID).
		Scan(&r.Agent, &files, &diff, &pages, &trunc, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	r.Diff, r.Truncated, r.At = diff, trunc != 0, store.Time(at)
	json.Unmarshal([]byte(files), &r.Files)
	if r.Files == nil {
		r.Files = []api.ResultFile{}
	}
	pm := map[string]string{}
	json.Unmarshal([]byte(pages), &pm)
	for p := range pm {
		r.Pages = append(r.Pages, p)
	}
	sort.Strings(r.Pages)
	return r, pm, nil
}

func taskResultGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	t, err := c.pathTask()
	if err != nil {
		return nil, err
	}
	r, _, err := c.result(t.id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, notFound("task %d has no result yet: nothing was submitted, or the machine has not reported it", t.id)
	}
	return r, nil
}

// webResultPage serves one changed web page of a task's result, so a person can see how it looks. The page
// is the agent's work, not the hub's: it is served with a policy that gives it no scripts, no network and no
// access to this site, and links between the task's pages work through a base address.
func (h *Hub) webResultPage(q *webReq) error {
	t, err := q.c.pathTask()
	if err != nil {
		return err
	}
	if jid, _ := strconv.ParseInt(q.r.PathValue("job"), 10, 64); !t.jobID.Valid || t.jobID.Int64 != jid {
		q.page(http.StatusNotFound, "message", pageData{Title: "Not found", Error: "No such task in this job."})
		return errHandled
	}
	_, pages, err := q.c.result(t.id)
	if err != nil {
		return err
	}
	rel := q.r.PathValue("path")
	body, ok := pages[rel]
	if !ok {
		q.page(http.StatusNotFound, "message", pageData{Title: "Not found", Error: "No such page in this result."})
		return errHandled
	}
	base := fmt.Sprintf("/jobs/%s/tasks/%d/page/", q.r.PathValue("job"), t.id)
	if dir := path.Dir(rel); dir != "." {
		base += dir + "/"
	}
	tag := `<base href="` + base + `">`
	if loc := headRE.FindStringIndex(body); loc != nil {
		body = body[:loc[1]] + tag + body[loc[1]:]
	} else {
		body = tag + body
	}
	hd := q.w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'self'; form-action 'none'")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")
	q.w.Write([]byte(body))
	return nil
}

var headRE = regexp.MustCompile(`(?i)<head[^>]*>`)
