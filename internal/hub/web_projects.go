package hub

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// The Projects page (D104): a project is a name and a shared git remote. The
// forms ask a machine to fetch its own copy or to create the repository; the
// machine does it under its own git and gh login.

type projectView struct {
	api.Codebase
	RemoteLink bool     // an https address: shown as a link; anything else stays plain text
	Size       string   // hosted: disk used on the hub
	CloneTo    []string // machines that do not have a copy (or whose attempt failed)
}

type projectsView struct {
	Projects  []projectView
	Devices   []string
	CanManage bool
	Form      map[string]string
	Which     string // the form that was refused, to show what was typed
	PrivateOn bool
}

func (q *webReq) joinedDevices() ([]string, error) {
	ds, err := store.ListDevices(q.c.tx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range ds {
		if d.Joined && !d.Revoked.Valid {
			out = append(out, d.Name)
		}
	}
	return out, nil
}

func (q *webReq) projectsPage(status int, errMsg, which string, form map[string]string) error {
	cbs, err := q.c.codebases()
	if err != nil {
		return err
	}
	devs, err := q.joinedDevices()
	if err != nil {
		return err
	}
	v := &projectsView{Devices: devs, CanManage: q.c.allow(ActProjectManage) == nil, Form: form, Which: which, PrivateOn: which != "create" || form["c_private"] == "1"}
	if v.Form == nil {
		v.Form = map[string]string{}
	}
	if v.Form["c_device"] == "" && len(devs) == 1 {
		v.Form["c_device"] = devs[0]
	}
	for _, cb := range cbs {
		pv := projectView{Codebase: cb, RemoteLink: strings.HasPrefix(cb.Remote, "https://") && !cb.Hosted}
		if cb.Hosted {
			pv.Size = humanBytes(q.c.h.repoSize(cb.Name))
		}
		have := map[string]string{}
		for _, k := range cb.Clones {
			have[k.Device] = k.Status
		}
		if cb.Remote != "" {
			for _, d := range devs {
				if s := have[d]; s == "" || s == "failed" {
					pv.CloneTo = append(pv.CloneTo, d)
				}
			}
		}
		v.Projects = append(v.Projects, pv)
	}
	notice := map[string]string{
		"hosted":   "Project created on this hub. Have a machine fetch its copy with Clone, or start a job from it.",
		"attached": "Project added.",
		"creating": "Asked the machine to create the repository. It shows here, with its address, when the machine has done it (usually within a minute): reload this page.",
		"cloning":  "Asked the machine to fetch its own copy. It shows as ready here when it is done (usually within a minute): reload this page.",
		"ready":    "That machine already has its copy.",
	}[q.r.URL.Query().Get("done")]
	q.page(status, "projects", pageData{Title: "Projects", Error: errMsg, Notice: notice, Extra: v})
	return nil
}

func (h *Hub) webProjects(q *webReq) error { return q.projectsPage(200, "", "", nil) }

// projectFailed shows the page again with the refusal in words and what was typed. The failed
// statements are abandoned with the transaction (the hub has a single connection).
func (q *webReq) projectFailed(err error, which string, form map[string]string) error {
	var ae *apiError
	status, msg := 500, "Something went wrong."
	if errors.As(err, &ae) {
		status, msg = ae.status, ae.msg
	}
	q.c.tx.Rollback()
	if ae != nil && ae.status == 403 {
		q.c.h.auditDenied(q.c.p, q.r, ae)
	}
	tx, terr := q.c.h.db.Begin()
	if terr != nil {
		return terr
	}
	defer tx.Rollback()
	q.c.tx = tx
	q.projectsPage(status, msg, which, form)
	return errHandled
}

// humanBytes is a size for people: 3.2 MB.
func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

func (h *Hub) webProjectHost(q *webReq) error {
	f := q.r.PostForm
	if err := q.c.allow(ActProjectManage); err != nil {
		return q.projectFailed(err, "host", f1(f))
	}
	if _, err := q.c.hostCodebase(f.Get("h_name")); err != nil {
		return q.projectFailed(err, "host", f1(f))
	}
	http.Redirect(q.w, q.r, "/projects?done=hosted", http.StatusSeeOther)
	return nil
}

func (h *Hub) webProjectAttach(q *webReq) error {
	f := q.r.PostForm
	if err := q.c.allow(ActProjectManage); err != nil {
		return q.projectFailed(err, "attach", f1(f))
	}
	if _, err := q.c.attachCodebase(api.CodebaseReq{Name: f.Get("a_name"), Remote: f.Get("a_remote"), Base: f.Get("a_base")}); err != nil {
		return q.projectFailed(err, "attach", f1(f))
	}
	http.Redirect(q.w, q.r, "/projects?done=attached", http.StatusSeeOther)
	return nil
}

func (h *Hub) webProjectCreate(q *webReq) error {
	f := q.r.PostForm
	if err := q.c.allow(ActProjectManage); err != nil {
		return q.projectFailed(err, "create", f1(f))
	}
	private := f.Get("c_private") == "1"
	if _, err := q.c.createCodebase(api.CodebaseCreateReq{Name: f.Get("c_name"), Owner: f.Get("c_owner"), Private: &private, Device: f.Get("c_device")}); err != nil {
		return q.projectFailed(err, "create", f1(f))
	}
	http.Redirect(q.w, q.r, "/projects?done=creating", http.StatusSeeOther)
	return nil
}

func (h *Hub) webProjectClone(q *webReq) error {
	f := q.r.PostForm
	if err := q.c.allow(ActProjectManage); err != nil {
		return q.projectFailed(err, "clone", f1(f))
	}
	req, err := q.c.cloneCodebase(q.r.PathValue("name"), f.Get("device"))
	if err != nil {
		return q.projectFailed(err, "clone", f1(f))
	}
	done := "cloning"
	if req.Status == "ready" {
		done = "ready"
	}
	http.Redirect(q.w, q.r, "/projects?done="+done, http.StatusSeeOther)
	return nil
}
