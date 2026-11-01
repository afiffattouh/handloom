package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"handloom/internal/api"
	"handloom/internal/plan"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// Plans are immutable in this first protocol version. Repeating the same request
// returns its recorded tasks, even after a restart or a lost HTTP response.
func jobPlanSubmit(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req plan.Plan
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.submitJobPlan(id, req)
}

func (c *call) submitJobPlan(id int64, req plan.Plan) (any, error) {
	j, err := c.job(id)
	if err != nil {
		return nil, err
	}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if !a.jobID.Valid || a.jobID.Int64 != id {
			return nil, forbidden("only this job's lead may submit its plan")
		}
	}
	if req.MaxAttempts == 0 {
		req.MaxAttempts = 2
	}
	order, err := req.Order()
	if err != nil {
		return nil, badRequest("%s", err)
	}
	request := string(marshal(req))
	var old string
	err = c.tx.QueryRow(`SELECT request FROM job_plan WHERE job_id = ?`, id).Scan(&old)
	if err == nil {
		if old != request {
			return nil, conflict("job %d already has a different plan; plans cannot be replaced", id)
		}
		return c.planStatus(id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if j.status != api.StatusOpen {
		return nil, conflict("job %d is not open", id)
	}
	var existing int
	if err := c.tx.QueryRow(`SELECT count(*) FROM task WHERE job_id = ?`, id).Scan(&existing); err != nil {
		return nil, err
	}
	if existing != 0 {
		return nil, conflict("submit a plan before creating this job's tasks")
	}
	var started int
	if err := c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE job_id = ?`, id).Scan(&started); err != nil {
		return nil, err
	}
	if started+len(req.Steps) > c.h.opt.MaxJobStarts {
		return nil, badRequest("plan needs more worker starts than this job's remaining limit of %d", c.h.opt.MaxJobStarts-started)
	}
	device := j.deviceID.Int64
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if req.Device != "" {
			return nil, forbidden("a lead cannot choose another machine")
		}
		device = a.deviceID
	} else if device == 0 || req.Device != "" {
		device, err = c.spawnDevice(req.Device)
		if err != nil {
			return nil, err
		}
	}
	if j.deviceID.Valid && j.deviceID.Int64 != device {
		return nil, conflict("the plan must use the job's machine")
	}
	if err := c.useDevice(device); err != nil {
		return nil, err
	}
	var joined int
	if err := c.tx.QueryRow(`SELECT count(*) FROM device WHERE id = ? AND credential_hash IS NOT NULL AND revoked_at IS NULL`, device).Scan(&joined); err != nil {
		return nil, err
	}
	if joined != 1 {
		return nil, conflict("the plan's machine is not available")
	}
	// Resolve all profiles before inserting anything, and retain exact versions.
	pinned := make([]string, len(req.Steps))
	for i, s := range req.Steps {
		name, version, err := parseProfileRef(s.Profile)
		if err != nil {
			return nil, err
		}
		p, err := c.profileVersion(name, version)
		if err != nil {
			return nil, err
		}
		if !spawnKinds[p.spec.Kind] {
			return nil, badRequest("step %s uses an unsupported agent kind", s.Key)
		}
		if j.confidential && p.spec.Runtime != profile.Local {
			return nil, badRequest("confidential step %s requires a local profile", s.Key)
		}
		pinned[i] = profileRef(p.name, p.version)
	}
	_, err = c.tx.Exec(`INSERT INTO job_plan(job_id, request, device_id, max_attempts, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`, id, request, device, req.MaxAttempts, c.p.actor(), store.Millis(c.now))
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, i := range order {
		s := req.Steps[i]
		deps := make([]int64, 0, len(s.DependsOn))
		for _, key := range s.DependsOn {
			deps = append(deps, ids[key])
		}
		job := id
		if c.p.kind == kindDevice {
			job = 0
		}
		t, err := c.createTask(api.TaskCreateReq{Title: s.Title, Body: s.Body, Project: j.project, Job: job, DependsOn: deps})
		if err != nil {
			return nil, err
		}
		ids[s.Key] = t.id
		if _, err := c.tx.Exec(`INSERT INTO plan_step(task_id, job_id, step_key, profile) VALUES (?, ?, ?, ?)`, t.id, id, s.Key, pinned[i]); err != nil {
			return nil, err
		}
	}
	if err := c.record(j.projectID, 0, "job.plan", j.target(), map[string]any{"steps": len(ids), "max_attempts": req.MaxAttempts}); err != nil {
		return nil, err
	}
	return c.planStatus(id)
}

func jobPlanGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	if _, err := c.job(id); err != nil {
		return nil, err
	}
	return c.planStatus(id)
}

func (c *call) planStatus(id int64) (plan.Status, error) {
	out := plan.Status{Job: id, Version: 1, Steps: []plan.StepStatus{}}
	var request string
	if err := c.tx.QueryRow(`SELECT max_attempts, request FROM job_plan WHERE job_id = ?`, id).Scan(&out.MaxAttempts, &request); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, notFound("job %d has no plan", id)
		}
		return out, err
	}
	var meta struct {
		Playbook string `json:"playbook"`
	}
	if err := json.Unmarshal([]byte(request), &meta); err != nil {
		return out, err
	}
	out.Playbook = meta.Playbook
	rows, err := c.tx.Query(`SELECT s.step_key, t.title, s.task_id, s.profile, s.attempts, s.worker, t.status, s.detail FROM plan_step s JOIN task t ON t.id = s.task_id WHERE s.job_id = ? ORDER BY s.task_id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var s plan.StepStatus
		if err := rows.Scan(&s.Key, &s.Title, &s.Task, &s.Profile, &s.Attempts, &s.Worker, &s.Status, &s.Detail); err != nil {
			return out, err
		}
		out.Steps = append(out.Steps, s)
	}
	return out, rows.Err()
}

type plannedStep struct {
	task, job, device int64
	profile, worker   string
	attempts, max     int
	spawn             sql.NullInt64
}

// dispatchPlans is called inside Sweep's transaction. The attempt counter,
// worker name, spawn intent and pending assignment are committed together.
func (c *call) dispatchPlans() error {
	rows, err := c.tx.Query(`SELECT s.task_id, s.job_id, p.device_id, s.profile, s.worker, s.attempts, p.max_attempts, s.spawn_id
 FROM plan_step s JOIN job_plan p ON p.job_id = s.job_id JOIN task t ON t.id = s.task_id JOIN task j ON j.id = s.job_id
 WHERE j.status = 'open' AND t.status = 'open' ORDER BY s.task_id`)
	if err != nil {
		return err
	}
	var steps []plannedStep
	for rows.Next() {
		var s plannedStep
		if err := rows.Scan(&s.task, &s.job, &s.device, &s.profile, &s.worker, &s.attempts, &s.max, &s.spawn); err != nil {
			rows.Close()
			return err
		}
		steps = append(steps, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range steps {
		if err := c.dispatchStep(s); err != nil {
			return err
		}
	}
	return nil
}

func (c *call) planDetail(task int64, detail string) error {
	_, err := c.tx.Exec(`UPDATE plan_step SET detail = ? WHERE task_id = ?`, detail, task)
	return err
}

func (c *call) dispatchStep(s plannedStep) error {
	t, err := c.task(s.task)
	if err != nil {
		return err
	}
	j, err := c.task(s.job)
	if err != nil {
		return err
	}
	deps, err := c.unfinishedDeps(t)
	if err != nil {
		return err
	}
	if len(deps) != 0 {
		return c.planDetail(t.id, "Waiting for dependencies to be accepted.")
	}
	// Acceptance queues an integration merge. Dependent work must not start
	// against a branch whose prerequisite is still being merged or has failed.
	if j.repo != "" {
		for _, dep := range t.deps {
			var status string
			err := c.tx.QueryRow(`SELECT status FROM merge WHERE task_id = ? ORDER BY id DESC LIMIT 1`, dep).Scan(&status)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if status != "merged" {
				return c.planDetail(t.id, "Waiting for dependency integration to pass.")
			}
		}
	}
	if s.spawn.Valid {
		spawn, err := c.spawn(s.spawn.Int64)
		if err != nil {
			return err
		}
		if spawn.status == api.SpawnPending || spawn.status == api.SpawnLaunching {
			return nil
		}
		a, err := c.agentByName(s.worker)
		if err != nil {
			return err
		}
		if a != nil && a.state != api.StateOffline {
			return nil
		}
		// A failed launch can still register late. The claim guard below fences
		// previous attempt names even while the replacement is registering.
	}
	if s.attempts >= s.max {
		return c.planDetail(t.id, "Worker attempt limit reached; inspect the last attempt before continuing.")
	}
	var deviceName string
	err = c.tx.QueryRow(`SELECT name FROM device WHERE id = ? AND credential_hash IS NOT NULL AND revoked_at IS NULL`, s.device).Scan(&deviceName)
	if errors.Is(err, sql.ErrNoRows) {
		return c.planDetail(t.id, "The assigned machine is unavailable.")
	}
	if err != nil {
		return err
	}
	n, err := c.liveInJob(sql.NullInt64{Int64: s.job, Valid: true}, j.projectID)
	if err != nil {
		return err
	}
	if n >= c.h.opt.MaxSpawns {
		return c.planDetail(t.id, "Waiting for worker capacity.")
	}
	var starts int
	if err := c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE job_id = ?`, s.job).Scan(&starts); err != nil {
		return err
	}
	if starts >= c.h.opt.MaxJobStarts {
		return c.planDetail(t.id, "Job worker-start limit reached.")
	}
	name := fmt.Sprintf("plan-%d-task-%d-try-%d", s.job, s.task, s.attempts+1)
	// Use the existing spawn protocol, profile checks, event delivery and limits.
	result, err := c.spawnFromRequest(api.SpawnReq{Name: name, Profile: s.profile, Project: j.project, Job: s.job, Device: deviceName})
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			return c.planDetail(t.id, apiErr.msg)
		}
		return err
	}
	spawn := result.(api.Spawn)
	if _, err := c.tx.Exec(`INSERT INTO plan_attempt(task_id, attempt, spawn_id) VALUES (?, ?, ?)`, t.id, s.attempts+1, spawn.ID); err != nil {
		return err
	}

	// Subsequent steps build on the verified integration branch, not the
	// original job base. The dependency merges above establish that it exists.
	if j.repo != "" && len(t.deps) > 0 {
		if _, err := c.tx.Exec(`UPDATE spawn SET base = ? WHERE id = ?`, fmt.Sprintf("job/%d/integration", s.job), spawn.ID); err != nil {
			return err
		}
	}
	if _, err := c.tx.Exec(`UPDATE plan_step SET attempts = attempts + 1, spawn_id = ?, worker = ?, detail = '' WHERE task_id = ?`, spawn.ID, name, t.id); err != nil {
		return err
	}
	if err := c.touch(t, `assigned_to = NULL, assign_name = ?, owner_agent_id = NULL, lease_expires_at = NULL`, name); err != nil {
		return err
	}
	return c.record(j.projectID, 0, "plan.dispatch", t.target(), map[string]any{"worker": name, "attempt": s.attempts + 1, "spawn": spawn.ID})
}

func (c *call) plannedClaim(t *taskRow, a *agentRow) error {
	var expected string
	err := c.tx.QueryRow(`SELECT worker FROM plan_step WHERE task_id = ?`, t.id).Scan(&expected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected == "" || expected != a.name {
		return forbidden("task %d is managed by its plan; only its current worker may claim it", t.id)
	}
	var status string
	if err := c.tx.QueryRow(`SELECT status FROM task WHERE id = ?`, t.jobID.Int64).Scan(&status); err != nil {
		return err
	}
	if status != api.StatusOpen {
		return conflict("the job is no longer open")
	}
	return nil
}

// Completed plan workers are retired only after their merge finishes. The link
// stops the process before reporting it offline; merely changing a status would
// release capacity while leaving a running agent behind.
func devicePlanStops(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list its plan workers to stop")
	}
	rows, err := c.tx.Query(`SELECT DISTINCT sp.name FROM spawn sp JOIN agent a ON a.name = sp.name
 JOIN plan_attempt pa ON pa.spawn_id = sp.id JOIN plan_step ps ON ps.task_id = pa.task_id
 JOIN task t ON t.id = ps.task_id JOIN task j ON j.id = ps.job_id
 WHERE sp.device_id = ? AND a.device_id = sp.device_id AND a.state != 'offline'
 AND (j.status != 'open' OR t.status = 'cancelled' OR sp.name != ps.worker
 OR (t.status = 'done' AND (j.repo = '' OR EXISTS (SELECT 1 FROM merge m WHERE m.task_id = t.id AND m.status = 'merged'))))`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (c *call) refusePlannedAssignment(task int64) error {
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM plan_step WHERE task_id = ?`, task).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return conflict("task %d is managed by its plan; cancel it or inspect job plan instead of assigning another worker", task)
	}
	return nil
}

// Recheck launch authority at both discovery and claim time: cancellation or a
// replacement may occur between a link polling and claiming the spawn.
func (c *call) planLaunchAllowed(spawn int64) (bool, error) {
	var current int
	err := c.tx.QueryRow(`SELECT ps.spawn_id = pa.spawn_id AND j.status = 'open' AND t.status IN ('open','claimed','submitted')
 FROM plan_attempt pa JOIN plan_step ps ON ps.task_id = pa.task_id
 JOIN task t ON t.id = ps.task_id JOIN task j ON j.id = ps.job_id WHERE pa.spawn_id = ?`, spawn).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	} // ordinary manually managed spawn
	return current != 0, err
}

func (c *call) refuseManagedGraph(job int64) error {
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM job_plan WHERE job_id = ?`, job).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return conflict("this job has a managed plan; the controller creates tasks and starts workers")
	}
	return nil
}
