package hub

import (
	"net/url"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/plan"
)

func preparedBuildProject(t *testing.T, e *webEnv) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO codebase(name,remote,base_branch,created_by,created_at,check_cmd) VALUES ('clinic','https://github.com/example/clinic','main','human:owner',0,'./check')`,
		`INSERT INTO codebase_clone(codebase_id,device_id,path,status,detail,created_at,updated_at) SELECT c.id,d.id,'/srv/clinic','ready','',0,0 FROM codebase c, device d WHERE c.name='clinic' AND d.name='d1'`,
	} {
		if _, err := e.hub.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProductBuildWebJourney(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	c, csrf := e.owner()
	r := e.req("GET", "/build", nil, c, nil)
	for _, want := range []string{"Build a product", "Setup needed", "No project is prepared yet", "disabled aria-describedby", "Deployment is not automatic"} {
		if r.status != 200 || !strings.Contains(r.body, want) {
			t.Fatalf("empty build page missing %q: %d", want, r.status)
		}
	}
	preparedBuildProject(t, e)
	f := url.Values{"csrf": {csrf}, "request_id": {"web-build-1"}, "brief": {"A clinic booking tool <script>alert(1)</script>"}, "project": {"clinic|d1"}, "kind": {"claude"}}
	// Failure after inserting a job/lead must roll back, preserving the brief and nonce.
	r = e.req("POST", "/build", f, c, nil)
	if r.status != 404 || !strings.Contains(r.body, "A clinic booking tool &lt;script&gt;") || !strings.Contains(r.body, `value="web-build-1"`) {
		t.Fatalf("failed launch: %d", r.status)
	}
	var n int
	e.hub.db.QueryRow(`SELECT count(*) FROM task`).Scan(&n)
	if n != 0 {
		t.Fatal("failed launch left jobs/tasks")
	}
	r = e.req("POST", "/build/profiles", f, c, nil)
	if r.status != 200 || strings.Contains(r.body, "Setup needed") || !strings.Contains(r.body, "Team setup changes") || !strings.Contains(r.body, "A clinic booking tool &lt;script&gt;") {
		t.Fatalf("install: %d %s", r.status, r.body)
	}
	r = e.req("POST", "/build", f, c, nil)
	if r.status != 303 {
		t.Fatalf("launch: %d %s", r.status, r.body)
	}
	location := r.header.Get("Location")
	again := e.req("POST", "/build", f, c, nil)
	if again.status != 303 || again.header.Get("Location") != location {
		t.Fatal("duplicate launch was not idempotent")
	}
	e.hub.db.QueryRow(`SELECT count(*) FROM task WHERE kind='job'`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate jobs: %d", n)
	}
	page := e.req("GET", location, nil, c, nil)
	for _, want := range []string{"Build stages", "Your brief", "Stage evidence and technical details", "data-build-progress", "A clinic booking tool &lt;script&gt;"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("build page missing %q", want)
		}
	}
	for _, bad := range []string{"Do not create, assign, or spawn", "Try again with another profile", "<script>alert(1)</script>"} {
		if strings.Contains(page.body, bad) {
			t.Errorf("build page exposes %q", bad)
		}
	}
	// Edited duplicate requests are refused without losing form values.
	f.Set("brief", "A changed product")
	r = e.req("POST", "/build", f, c, nil)
	if r.status != 409 || !strings.Contains(r.body, "A changed product") {
		t.Fatalf("changed duplicate: %d", r.status)
	}
}

func TestProductBuildWebPermissions(t *testing.T) {
	e := newWebEnv(t, Options{})
	e.agents()
	c, _ := e.owner()
	preparedBuildProject(t, e)
	viewer, vcsrf := e.member("read-only", "viewer")
	for _, path := range []string{"/build", "/build/profiles"} {
		if r := e.req("POST", path, url.Values{"csrf": {vcsrf}, "kind": {"claude"}}, viewer, nil); r.status != 403 {
			t.Fatalf("viewer %s: %d", path, r.status)
		}
	}
	if r := e.req("GET", "/build", nil, viewer, nil); r.status != 403 {
		t.Fatalf("viewer form: %d", r.status)
	}
	member, mcsrf := e.member("builder", "member")
	if r := e.req("POST", "/build/profiles", url.Values{"csrf": {mcsrf}, "kind": {"claude"}}, member, nil); r.status != 403 {
		t.Fatalf("member installed profiles: %d", r.status)
	}
	if r := e.req("GET", "/build", nil, member, nil); strings.Contains(r.body, `value="clinic|d1"`) {
		t.Fatal("member sees a machine they cannot use")
	}
	for _, path := range []string{"/build", "/build/profiles"} {
		if r := e.req("POST", path, url.Values{"kind": {"claude"}}, c, nil); r.status != 403 {
			t.Fatalf("no CSRF %s: %d", path, r.status)
		}
	}
	e.hub.db.Exec(`UPDATE device SET revoked_at = 1 WHERE name = 'd1'`)
	if r := e.req("GET", "/build", nil, c, nil); strings.Contains(r.body, `value="clinic|d1"`) {
		t.Fatal("revoked device offered")
	}
}

func TestBuildProgressDoesNotOverstateReadiness(t *testing.T) {
	v := &jobView2{Open: true, Job: api.Job{Status: api.StatusOpen}, Plan: &plan.Status{Playbook: "mvp-v1", Steps: []plan.StepStatus{{Task: 2, Title: "Release", Status: api.StatusDone}}}, Tasks: []taskView{{Task: api.Task{ID: 2, Merge: &api.TaskMerge{Status: "pending"}}}}}
	for _, state := range []string{"pending", "failed", "conflict", "merged"} {
		t.Run(state, func(t *testing.T) {
			v.Tasks[0].Merge.Status = state
			b := buildProgress(v)
			if b.Complete != (state == "merged") {
				t.Fatalf("wrong completion for %s: %+v", state, b)
			}
		})
	}
	v.Plan.Steps[0].Status = api.StatusCancelled
	b := buildProgress(v)
	if b.Complete || b.State != "Needs attention" {
		t.Fatalf("cancelled stage became complete: %+v", b)
	}
	v.Job.Status = api.StatusCancelled
	if b = buildProgress(v); b.State != "Build stopped" || b.Complete {
		t.Fatalf("cancelled build: %+v", b)
	}
}
