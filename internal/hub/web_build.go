package hub

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"handloom/internal/api"
	"handloom/internal/playbook"
	"handloom/internal/store"
)

type buildFormView struct {
	Form                 map[string]string
	Projects             []projectOption
	Missing              []string
	CanInstall, CanBuild bool
	Warnings             []string
}

func (h *Hub) webBuildForm(q *webReq) error {
	return q.buildForm(200, "", map[string]string{}, nil)
}

func (q *webReq) buildForm(status int, message string, form map[string]string, warnings []string) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot start product builds.")
	}
	if form["request_id"] == "" {
		form["request_id"] = randomID()
	}
	if form["kind"] == "" {
		form["kind"] = "claude"
	}
	v := &buildFormView{Form: form, CanInstall: q.human.Role == store.RoleOwner, Warnings: warnings}
	profiles, err := profileList(q.c)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, p := range profiles.([]api.ProfileInfo) {
		have[p.Name] = true
	}
	for name := range playbook.ProfileStarters {
		if !have[name] {
			v.Missing = append(v.Missing, name)
		}
	}
	sort.Strings(v.Missing)
	devices, err := q.joinedDevices()
	if err != nil {
		return err
	}
	joined := map[string]bool{}
	for _, d := range devices {
		joined[d] = true
	}
	projects, err := q.c.codebases()
	if err != nil {
		return err
	}
	for _, p := range projects {
		if strings.TrimSpace(p.Check) == "" {
			continue
		}
		for _, copy := range p.Clones {
			if copy.Status == "ready" && joined[copy.Device] {
				v.Projects = append(v.Projects, projectOption{Value: p.Name + "|" + copy.Device, Label: p.Name + " · " + copy.Device})
			}
		}
	}
	if form["project"] == "" && len(v.Projects) == 1 {
		form["project"] = v.Projects[0].Value
	}
	v.CanBuild = len(v.Projects) > 0 && len(v.Missing) == 0
	q.page(status, "build", pageData{Title: "Build a product", Error: message, Extra: v})
	return nil
}

func (h *Hub) webBuildCreate(q *webReq) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot start product builds.")
	}
	f := q.r.PostForm
	name, device, ok := strings.Cut(f.Get("project"), "|")
	if !ok || name == "" || device == "" {
		return q.buildForm(400, "Choose a prepared project. Add its repository and check command in Projects if none is available.", f1(f), nil)
	}
	brief := strings.TrimSpace(strings.ReplaceAll(f.Get("brief"), "\r\n", "\n"))
	title := strings.TrimSpace(f.Get("title"))
	if title == "" {
		title = strings.SplitN(brief, "\n", 2)[0]
		if r := []rune(title); len(r) > 48 {
			title = string(r[:48]) + "…"
		}
	}
	result, err := q.c.buildProduct(api.ProductBuildReq{RequestID: f.Get("request_id"), Playbook: playbook.MVP, Title: title, Brief: brief, Project: name, Device: device})
	if err != nil {
		return q.buildFailed(err)
	}
	http.Redirect(q.w, q.r, fmt.Sprintf("/jobs/%d?done=started", result.(api.Job).ID), http.StatusSeeOther)
	return nil
}

func (h *Hub) webBuildProfiles(q *webReq) error {
	result, err := q.c.installMVPProfiles(api.StarterAddReq{Kind: q.r.PostForm.Get("kind"), Runtime: "cloud", Adapt: q.r.PostForm.Get("adapt") == "1"})
	if err != nil {
		return q.buildFailed(err)
	}
	return q.buildForm(200, "", f1(q.r.PostForm), result.(api.PlaybookInstallResp).Warnings)
}

// Form failures must roll back partial jobs/profiles before reading the page.
// Preserve both the brief and operation ID so retrying cannot create another run.
func (q *webReq) buildFailed(err error) error {
	var ae *apiError
	status, message := 500, "The build could not be started. Your brief is kept below; try again."
	if errors.As(err, &ae) {
		status, message = ae.status, ae.msg
		if ae.status == 409 && strings.Contains(ae.msg, "request_id") {
			message = "This form already started a build with different details. Open Build a product from the menu to start another."
		}
	}
	q.c.tx.Rollback()
	if ae != nil && ae.status == 403 {
		q.c.h.auditDenied(q.c.p, q.r, ae)
	}
	tx, txErr := q.c.h.db.Begin()
	if txErr != nil {
		return txErr
	}
	defer tx.Rollback()
	q.c.tx = tx
	if renderErr := q.buildForm(status, message, f1(q.r.PostForm), nil); renderErr != nil {
		return renderErr
	}
	return errHandled
}
