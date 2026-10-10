package hub

import (
	"fmt"
	"strings"
	"testing"

	"handloom/internal/api"
)

func (e *env) agent(name string) caller {
	e.t.Helper()
	e.ok(caller{e.d1, ""}, "POST", "/v1/agents", api.RegisterReq{Name: name, Kind: "shell"}, nil)
	return caller{e.d1, name}
}

func (e *env) newJob(title, lead string) api.Job {
	e.t.Helper()
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: title, Lead: lead}, &j)
	return j
}

func (e *env) inboxBodies(c caller) string {
	var b strings.Builder
	for _, m := range e.inbox(c) {
		b.WriteString(m.Body + "\n")
	}
	return b.String()
}

// Two jobs in one project, each with its own lead: work in a job is judged by
// that job's lead and nobody else.
func TestTwoJobsTwoLeads(t *testing.T) {
	e := newEnv(t)
	lead1, lead2 := e.agent("lead1"), e.agent("lead2")
	w1 := e.agent("w1")
	e.agent("w2")
	j1, j2 := e.newJob("Scan competitors", "lead1"), e.newJob("Draft the proposal", "lead2")
	if j1.Lead != "lead1" || j2.Lead != "lead2" || j1.ID == j2.ID {
		t.Fatalf("jobs: %+v %+v", j1, j2)
	}
	if !strings.Contains(e.inboxBodies(lead1), "lead of job #") {
		t.Fatal("the new lead was not told about its job")
	}
	e.inbox(lead2)

	// The project-level lead is a different thing and still exists once.
	var agents []api.Agent
	e.ok(e.afif(), "GET", "/v1/agents", nil, &agents)
	leads := 0
	for _, a := range agents {
		if a.Role == api.RoleLead {
			leads++
		}
	}
	if leads != 3 {
		t.Fatalf("%d leads, want the project lead and two job leads", leads)
	}
	e.fail(409, e.afif(), "POST", "/v1/agents/w1/role", api.RoleReq{Role: "lead"}) // second project lead

	// A lead's tasks go into its own job, and cannot name another.
	var t1 api.Task
	e.ok(lead1, "POST", "/v1/tasks", api.TaskCreateReq{Title: "fetch pages", AssignedTo: "w1"}, &t1)
	if t1.Job == nil || *t1.Job != j1.ID {
		t.Fatalf("task job: %+v", t1)
	}
	e.fail(403, lead1, "POST", "/v1/tasks", api.TaskCreateReq{Title: "x", Job: j2.ID})
	var t2 api.Task
	e.ok(lead2, "POST", "/v1/tasks", api.TaskCreateReq{Title: "outline", AssignedTo: "w2"}, &t2)
	e.fail(409, lead1, "POST", "/v1/tasks", api.TaskCreateReq{Title: "needs other", DependsOn: []int64{t2.ID}}) // dependency across jobs

	// A submit in job 1 reaches lead1 only.
	e.inbox(w1)
	e.inbox(lead1)
	e.inbox(lead2)
	e.inbox(e.lead())
	e.ok(w1, "POST", taskPath(t1.ID, "claim"), nil, nil)
	e.ok(w1, "POST", taskPath(t1.ID, "submit"), api.SubmitReq{Evidence: []string{"file:a"}, Note: "done"}, nil)
	if got := e.inboxBodies(lead1); !strings.Contains(got, "submitted by w1") {
		t.Fatalf("lead1 inbox: %q", got)
	}
	for name, c := range map[string]caller{"lead2": lead2, "project lead": e.lead()} {
		if got := e.inboxBodies(c); strings.Contains(got, "submitted") {
			t.Fatalf("%s heard about job 1: %q", name, got)
		}
	}
	// role:lead from a worker means the lead it reports to.
	e.ok(w1, "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "question about my task"}, nil)
	if !strings.Contains(e.inboxBodies(lead1), "question about my task") {
		t.Fatal("role:lead did not reach the job's lead")
	}
	if strings.Contains(e.inboxBodies(lead2)+e.inboxBodies(e.lead()), "question about my task") {
		t.Fatal("role:lead reached another job's lead")
	}
	// A human has to say which lead.
	e.fail(400, e.afif(), "POST", "/v1/messages", api.SendReq{To: "role:lead", Body: "hello"})
	// task: addressing goes to the owner and that job's lead.
	e.ok(e.afif(), "POST", "/v1/messages", api.SendReq{To: fmt.Sprintf("task:%d", t1.ID), Body: "about task 1"}, nil)
	if !strings.Contains(e.inboxBodies(lead1), "about task 1") || strings.Contains(e.inboxBodies(lead2), "about task 1") {
		t.Fatal("task: addressing used the wrong lead")
	}
}

func TestJobsAreForHumans(t *testing.T) {
	e := newEnv(t)
	lead := e.agent("lead1")
	e.fail(403, e.lead(), "POST", "/v1/jobs", api.JobNewReq{Title: "a lead starts a job"})
	e.fail(403, e.worker(), "POST", "/v1/jobs", api.JobNewReq{Title: "a worker starts a job"})
	e.fail(403, e.observer(), "POST", "/v1/jobs", api.JobNewReq{Title: "an observer starts a job"})
	e.fail(400, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "  "})
	e.fail(404, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "x", Lead: "nobody"})
	j := e.newJob("A job", "lead1")
	e.fail(403, lead, "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), nil)
	e.fail(403, e.worker(), "POST", "/v1/agents/worker/job", api.AgentJobReq{Job: j.ID})
	// Everybody can read.
	for _, c := range []caller{e.lead(), e.worker(), e.observer(), e.afif()} {
		e.ok(c, "GET", fmt.Sprintf("/v1/jobs/%d", j.ID), nil, nil)
		e.ok(c, "GET", "/v1/jobs", nil, nil)
	}
	for _, a := range []string{"job.new", "agent.job"} {
		if !e.audited(a, "") {
			t.Errorf("audit has no %s", a)
		}
	}
}

func TestJobRootIsNotATask(t *testing.T) {
	e := newEnv(t)
	e.agent("lead1")
	j := e.newJob("A job", "lead1")
	var tasks []api.Task
	e.ok(e.afif(), "GET", "/v1/tasks", nil, &tasks)
	if len(tasks) != 0 {
		t.Fatalf("the job root is on the task board: %+v", tasks)
	}
	e.fail(409, e.worker(), "POST", taskPath(j.ID, "claim"), nil)
	// The lead of a closed job cannot add tasks to it.
	lead := caller{e.d1, "lead1"}
	var task api.Task
	e.ok(lead, "POST", "/v1/tasks", api.TaskCreateReq{Title: "one"}, &task)
	e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), nil) // unfinished task
	var got api.Job
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), api.JobCloseReq{Cancel: true}, &got)
	if got.Status != api.StatusCancelled || got.Tasks.Cancelled != 1 {
		t.Fatalf("cancelled job: %+v", got)
	}
	e.fail(409, lead, "POST", "/v1/tasks", api.TaskCreateReq{Title: "late"})
	e.fail(409, e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), nil) // already closed
}

func TestJobFinishesWhenTasksAreDone(t *testing.T) {
	e := newEnv(t)
	lead := e.agent("lead1")
	e.agent("w1")
	j := e.newJob("A job", "lead1")
	var task api.Task
	e.ok(lead, "POST", "/v1/tasks", api.TaskCreateReq{Title: "one", AssignedTo: "w1"}, &task)
	w1 := caller{e.d1, "w1"}
	e.ok(w1, "POST", taskPath(task.ID, "claim"), nil, nil)
	e.ok(w1, "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"file:x"}, Note: "done"}, nil)
	e.ok(lead, "POST", taskPath(task.ID, "accept"), nil, nil)
	var got api.Job
	e.ok(e.afif(), "POST", fmt.Sprintf("/v1/jobs/%d/close", j.ID), nil, &got)
	if got.Status != api.StatusDone || got.Tasks.Done != 1 {
		t.Fatalf("job: %+v", got)
	}
}

func TestMovingAgentsBetweenJobs(t *testing.T) {
	e := newEnv(t)
	e.agent("lead1")
	e.agent("lead2")
	w := e.agent("w")
	j1, j2 := e.newJob("one", "lead1"), e.newJob("two", "")
	// Job two has no lead yet: its tasks are judged by the project lead.
	var task api.Task
	e.ok(e.afif(), "POST", "/v1/tasks", api.TaskCreateReq{Title: "t", Job: j2.ID, AssignedTo: "w"}, &task)
	e.inbox(e.lead())
	e.ok(w, "POST", taskPath(task.ID, "claim"), nil, nil)
	e.ok(w, "POST", taskPath(task.ID, "submit"), api.SubmitReq{Evidence: []string{"file:x"}, Note: "done"}, nil)
	if !strings.Contains(e.inboxBodies(e.lead()), "submitted by w") {
		t.Fatal("a job without a lead should fall back to the project lead")
	}
	// Giving it a lead moves the judging.
	e.ok(e.afif(), "POST", "/v1/agents/lead2/job", api.AgentJobReq{Job: j2.ID}, nil)
	e.ok(e.afif(), "POST", "/v1/agents/lead2/role", api.RoleReq{Role: "lead"}, nil)
	e.fail(409, e.afif(), "POST", "/v1/agents/lead1/job", api.AgentJobReq{Job: j2.ID}) // would make two leads of job 2 (lead1 is a lead)
	e.fail(404, e.afif(), "POST", "/v1/agents/lead1/job", api.AgentJobReq{Job: 999})
	// A job lead cannot just leave its job: it would become a second project lead.
	e.fail(409, e.afif(), "POST", "/v1/agents/lead2/job", api.AgentJobReq{Job: 0})
	e.ok(e.afif(), "POST", "/v1/agents/lead2/role", api.RoleReq{Role: "worker"}, nil)
	e.ok(e.afif(), "POST", "/v1/agents/lead2/job", api.AgentJobReq{Job: 0}, nil)
	_ = j1
}

func TestConfidentialJobsNeedAPrivateHub(t *testing.T) {
	e := newEnv(t)
	e.agent("lead1")
	e.fail(400, e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "client work", Lead: "lead1", Confidential: true})
	e.hub.opt.AllowConfidential = true
	var j api.Job
	e.ok(e.afif(), "POST", "/v1/jobs", api.JobNewReq{Title: "client work", Lead: "lead1", Confidential: true}, &j)
	if !j.Confidential {
		t.Fatalf("job: %+v", j)
	}
	// Tasks of a confidential job inherit the mark.
	var task api.Task
	e.ok(caller{e.d1, "lead1"}, "POST", "/v1/tasks", api.TaskCreateReq{Title: "t"}, &task)
	var n int
	e.hub.db.QueryRow(`SELECT confidential FROM task WHERE id = ?`, task.ID).Scan(&n)
	if n != 1 {
		t.Fatal("task of a confidential job is not marked confidential")
	}
}
