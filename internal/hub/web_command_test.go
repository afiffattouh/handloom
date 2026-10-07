package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"handloom/internal/api"
)

func TestTheCommandCenterShowsTheRecordInWordsAndCharts(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, _ := e.owner()
	// A new hub: it says how to begin and invents nothing.
	body := e.req("GET", "/command", nil, c, nil).body
	for _, want := range []string{"Command center", "Needs you", "no device checks in this range", "no accepted tasks in this range", "Nothing to point out.", "Fleet"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty command center lacks %q", want)
		}
	}
	var j api.Job
	e.apiOK(e.humanAPI(), "", "POST", "/v1/jobs", api.JobNewReq{Title: "Fix", Lead: "lead", Repo: "/srv/app", Verify: "./check.sh", Device: "d1"}, &j)
	var t1 api.Task
	ag.lead("POST", "/v1/tasks", api.TaskCreateReq{Title: "first <b>task</b>", AssignedTo: "worker"}, &t1)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/claim", t1.ID), nil, nil)
	ag.worker("POST", fmt.Sprintf("/v1/tasks/%d/submit", t1.ID), api.SubmitReq{Evidence: []string{"commit:abc"}}, nil)
	e.apiOK(ag.device, "", "POST", fmt.Sprintf("/v1/tasks/%d/verify", t1.ID), api.TaskCheckReq{Agent: "worker", Command: "./check.sh", ExitCode: 0}, nil)
	ag.lead("POST", fmt.Sprintf("/v1/tasks/%d/accept", t1.ID), nil, nil)

	for _, path := range []string{"/command", "/command?range=24h", "/command?range=30d", "/command?machine=d1", "/command/fragment?range=7d"} {
		r := e.req("GET", path, nil, c, nil)
		if r.status != 200 {
			t.Fatalf("%s: %d", path, r.status)
		}
		for _, want := range []string{"1 of 1 checks", "100", "worker", "lead", `<svg class="chart"`, "Profiles compared"} {
			if !strings.Contains(r.body, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if strings.Contains(r.body, "<b>task</b>") || strings.Contains(r.body, "style=") {
			t.Errorf("%s: unescaped text or an inline style", path)
		}
	}
	// A machine nobody has joined shows an empty fleet, not an error.
	if r := e.req("GET", "/command?machine=nowhere", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "No agents on this machine") {
		t.Fatalf("unknown machine: %d", r.status)
	}
	// Viewers read it too.
	vc, _ := e.member("vera", "viewer")
	if r := e.req("GET", "/command", nil, vc, nil); r.status != 200 || strings.Contains(r.body, "New job") {
		t.Fatalf("viewer: %d", r.status)
	}
	// The same numbers are an API.
	var m Metrics
	e.apiOK(e.admin, "", "GET", "/v1/metrics", nil, &m)
	if m.Range != "7d" || m.Checks.N != 1 {
		t.Fatalf("metrics: %+v", m.Checks)
	}
}

func TestTheTerminalViewIsAskedForReadAndForgotten(t *testing.T) {
	e := newWebEnv(t, Options{})
	ag := e.agents()
	c, tok := e.owner()
	hdr := map[string]string{"X-CSRF-Token": tok}
	// The agent needs a terminal for its screen to be readable.
	e.apiOK(ag.device, "", "POST", "/v1/agents", api.RegisterReq{Name: "tty", Kind: "claude", WakeTarget: "tmux:/tmp/sock:%1"}, nil)

	page := e.req("GET", "/agents/tty", nil, c, nil)
	if page.status != 200 || !strings.Contains(page.body, `data-tail="/agents/tty/tail"`) || strings.Contains(page.body, "style=") {
		t.Fatalf("agent page: %d", page.status)
	}
	if r := e.req("GET", "/agents/nobody", nil, c, nil); r.status != 404 {
		t.Fatalf("unknown agent: %d", r.status)
	}
	if r := e.req("GET", "/agents/tty/tail", nil, c, nil); r.status != 200 || !strings.Contains(r.body, "Waiting for the machine") {
		t.Fatalf("before anyone asks: %d %s", r.status, r.body)
	}
	var wanted []string
	e.apiOK(ag.device, "", "GET", "/v1/device/tails", nil, &wanted)
	if len(wanted) != 0 {
		t.Fatalf("nobody is looking, yet %v are wanted", wanted)
	}
	// The device may not post a screen nobody asked for.
	if code := e.status(ag.device, "POST", "/v1/device/tails/tty", api.TailReq{Text: "x"}); code != 409 {
		t.Fatalf("unrequested screen: %d", code)
	}

	if r := e.req("POST", "/agents/tty/watch", nil, c, hdr); r.status != 204 {
		t.Fatalf("watch: %d %s", r.status, r.body)
	}
	e.apiOK(ag.device, "", "GET", "/v1/device/tails", nil, &wanted)
	if len(wanted) != 1 || wanted[0] != "tty" {
		t.Fatalf("wanted: %v", wanted)
	}
	secret := "SCREEN-MARKER-9137"
	e.apiOK(ag.device, "", "POST", "/v1/device/tails/tty", api.TailReq{Text: "line one\n" + secret + "\n"}, nil)
	if r := e.req("GET", "/agents/tty/tail", nil, c, nil); r.status != 200 || !strings.Contains(r.body, secret) {
		t.Fatalf("tail: %d %s", r.status, r.body)
	}
	// Memory only: the marker is nowhere in the database.
	for _, q := range []string{`SELECT count(*) FROM audit WHERE payload LIKE ?`, `SELECT count(*) FROM event WHERE payload LIKE ?`, `SELECT count(*) FROM message WHERE body LIKE ?`} {
		var n int
		if err := e.hub.db.QueryRow(q, "%"+secret+"%").Scan(&n); err != nil || n != 0 {
			t.Fatalf("the screen reached the database (%s): %d %v", q, n, err)
		}
	}
	// A screen is forgotten after a minute, and wanting ends after 45 seconds.
	e.clock.advance(61 * time.Second)
	if r := e.req("GET", "/agents/tty/tail", nil, c, nil); !strings.Contains(r.body, "Waiting for the machine") {
		t.Fatalf("an old screen is still served: %s", r.body)
	}
	e.apiOK(ag.device, "", "GET", "/v1/device/tails", nil, &wanted)
	if len(wanted) != 0 {
		t.Fatalf("wanting never ends: %v", wanted)
	}

	// A viewer may not look at a terminal, and a lead's device may not forge one for another device's agent.
	vc, vtok := e.member("vera", "viewer")
	if r := e.req("POST", "/agents/tty/watch", nil, vc, map[string]string{"X-CSRF-Token": vtok}); r.status != 403 {
		t.Fatalf("viewer watch: %d", r.status)
	}
	if r := e.req("GET", "/agents/tty/tail", nil, vc, nil); r.status != 403 {
		t.Fatalf("viewer tail: %d", r.status)
	}
	if r := e.req("POST", "/agents/tty/watch", nil, c, nil); r.status == 204 {
		t.Fatal("watch without a CSRF token")
	}
}

// status makes an API call and returns only the status code.
func (e *webEnv) status(token, method, path string, body any) int {
	e.t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
