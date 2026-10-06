package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// Spawn: a lead (into its own job) or a human asks for an agent to be started
// on a device. The hub records the request and wakes that device's link; the
// link starts the agent in a terminal it owns, so the wake ladder, the
// liveness lease and the lead-lost alarm work for it like for any other
// agent. The hub never says what command to run: the link builds it from a
// fixed table by kind.

// spawnKinds are the agent kinds a link knows how to start.
var spawnKinds = map[string]bool{"claude": true}

var modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)

type spawnRow struct {
	id, projectID, deviceID int64
	job                     sql.NullInt64
	name, kind, model       string
	status, pane, errText   string
	createdBy               string
	created, updated        int64
	device, project         string
	profileName             string
	profileVersion          int
	profileHash             string
}

func (s *spawnRow) api() api.Spawn {
	return api.Spawn{ID: s.id, Name: s.name, Kind: s.kind, Model: s.model, Device: s.device, Job: nullInt(s.job),
		Project: s.project, Profile: profileRef(s.profileName, s.profileVersion), Status: s.status, Pane: s.pane, Error: s.errText, CreatedBy: s.createdBy, CreatedAt: store.Time(s.created)}
}

func profileRef(name string, version int) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf("%s@%d", name, version)
}

const spawnSelect = `SELECT s.id, s.project_id, s.device_id, s.job_id, s.name, s.kind, s.model, s.status, s.pane, s.error,
	s.created_by, s.created_at, s.updated_at, d.name, p.name, s.profile_name, s.profile_version, s.profile_hash FROM spawn s JOIN device d ON d.id = s.device_id
	JOIN project p ON p.id = s.project_id `

func scanSpawn(s scanner) (*spawnRow, error) {
	r := &spawnRow{}
	err := s.Scan(&r.id, &r.projectID, &r.deviceID, &r.job, &r.name, &r.kind, &r.model, &r.status, &r.pane, &r.errText,
		&r.createdBy, &r.created, &r.updated, &r.device, &r.project, &r.profileName, &r.profileVersion, &r.profileHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (c *call) spawn(id int64) (*spawnRow, error) {
	r, err := scanSpawn(c.tx.QueryRow(spawnSelect+`WHERE s.id = ?`, id))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, notFound("no spawn %d", id)
	}
	return r, nil
}

func (c *call) spawns(where string, args ...any) ([]*spawnRow, error) {
	rows, err := c.tx.Query(spawnSelect+where+` ORDER BY s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*spawnRow
	for rows.Next() {
		r, err := scanSpawn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// liveInJob counts what a job already has running or about to: agents that
// are not offline plus spawns in flight. The cap is checked in the same
// transaction that records the request.
func (c *call) liveInJob(job sql.NullInt64, projectID int64) (int, error) {
	var agents, pending int
	if job.Valid {
		if err := c.tx.QueryRow(`SELECT count(*) FROM agent WHERE job_id = ? AND state != 'offline' AND role != 'lead'`, job.Int64).Scan(&agents); err != nil {
			return 0, err
		}
		if err := c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE job_id = ? AND status IN ('pending', 'launching')`, job.Int64).Scan(&pending); err != nil {
			return 0, err
		}
	} else {
		if err := c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE project_id = ? AND job_id IS NULL AND status IN ('pending', 'launching', 'started')`, projectID).Scan(&agents); err != nil {
			return 0, err
		}
	}
	return agents + pending, nil
}

func spawnNew(c *call) (any, error) {
	if err := c.allow(ActSpawn); err != nil {
		return nil, err
	}
	var req api.SpawnReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return c.spawnFromRequest(req)
}

// spawnFromRequest is the spawn request itself, for the API and the web form.
// The caller has checked the scope.
func (c *call) spawnFromRequest(req api.SpawnReq) (any, error) {
	if err := checkName("agent", req.Name); err != nil {
		return nil, err
	}
	var prof *profileRow
	if req.Profile != "" {
		name, version, err := parseProfileRef(req.Profile)
		if err != nil {
			return nil, err
		}
		if prof, err = c.profileVersion(name, version); err != nil {
			return nil, err
		}
		if req.Kind != "" && req.Kind != prof.spec.Kind {
			return nil, badRequest("profile %s is for %s agents, not %s", prof.name, prof.spec.Kind, req.Kind)
		}
		req.Kind = prof.spec.Kind
	}
	if req.Kind == "" {
		req.Kind = "claude"
	}
	if !spawnKinds[req.Kind] {
		return nil, badRequest("cannot start %q agents; kinds: %s", req.Kind, spawnKindList())
	}
	if req.Model != "" && !modelRE.MatchString(req.Model) {
		return nil, badRequest("bad model name %q", req.Model)
	}
	var projectID, deviceID int64
	var job sql.NullInt64
	if c.p.kind == kindDevice {
		// A lead starts agents on its own device, in its own job.
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if req.Device != "" || req.Job != 0 || req.Project != "" {
			return nil, forbidden("an agent starts agents on its own device, in its own job; it does not choose")
		}
		projectID, deviceID, job = a.projectID, a.deviceID, a.jobID
	} else {
		var err error
		if projectID, _, err = c.project(req.Project); err != nil {
			return nil, err
		}
		if req.Job != 0 {
			j, err := c.job(req.Job)
			if err != nil || j.projectID != projectID {
				return nil, notFound("no job %d", req.Job)
			}
			if j.status != api.StatusOpen {
				return nil, conflict("job %d is %s", j.id, j.status)
			}
			job = sql.NullInt64{Int64: req.Job, Valid: true}
		}
		var err2 error
		if deviceID, err2 = c.spawnDevice(req.Device); err2 != nil {
			return nil, err2
		}
	}
	// Confidential work stays on a model that runs on the owner's own machines.
	if job.Valid {
		j, err := c.task(job.Int64)
		if err != nil {
			return nil, err
		}
		if j.confidential && (prof == nil || prof.spec.Runtime != profile.Local) {
			return nil, badRequest("job %d is confidential: start its agents from a profile whose runtime is local", j.id)
		}
	}
	if existing, err := c.agentByName(req.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, conflict("there is already an agent called %q", req.Name)
	}
	n, err := c.liveInJob(job, projectID)
	if err != nil {
		return nil, err
	}
	if n >= c.h.opt.MaxSpawns {
		return nil, conflict("at most %d agents at a time in this job; wait for one to finish", c.h.opt.MaxSpawns)
	}
	if !c.h.allowRate("spawn:"+c.p.actor(), 10, 10, c.now) {
		return nil, &apiError{429, "rate_limited", "too many spawn requests; slow down"}
	}
	ms := store.Millis(c.now)
	pn, pv, ph := "", 0, ""
	if prof != nil {
		pn, pv, ph = prof.name, prof.version, prof.hash
	}
	res, err := c.tx.Exec(`INSERT INTO spawn(project_id, device_id, job_id, name, kind, model, status, created_by, created_at, updated_at, profile_name, profile_version, profile_hash)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?)`, projectID, deviceID, nullAny(job), req.Name, req.Kind, req.Model, c.p.actor(), ms, ms, pn, pv, ph)
	if err != nil {
		return nil, conflict("%q is already being started", req.Name)
	}
	id, _ := res.LastInsertId()
	s, err := c.spawn(id)
	if err != nil {
		return nil, err
	}
	if err := c.record(projectID, 0, "spawn.request", fmt.Sprintf("spawn:%d", id),
		map[string]any{"name": s.name, "kind": s.kind, "device": s.device, "job": nullInt(job), "profile": profileRef(pn, pv), "hash": ph}); err != nil {
		return nil, err
	}
	// The link of that device is waiting on its event poll.
	if err := c.emit(projectID, 0, "spawn.requested", map[string]any{"id": id, "device_id": deviceID}); err != nil {
		return nil, err
	}
	return s.api(), nil
}

func spawnKindList() string {
	var ks []string
	for k := range spawnKinds {
		ks = append(ks, k)
	}
	return strings.Join(ks, ", ")
}

// spawnDevice picks the device for a human's request: the one named, or the
// only joined one.
func (c *call) spawnDevice(name string) (int64, error) {
	var id int64
	if name != "" {
		err := c.tx.QueryRow(`SELECT id FROM device WHERE name = ? AND credential_hash IS NOT NULL AND revoked_at IS NULL`, name).Scan(&id)
		if err != nil {
			return 0, notFound("no joined device %q", name)
		}
		return id, nil
	}
	rows, err := c.tx.Query(`SELECT id FROM device WHERE credential_hash IS NOT NULL AND revoked_at IS NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			return 0, err
		}
		ids = append(ids, d)
	}
	if len(ids) != 1 {
		return 0, badRequest("name the device to start it on (--device); there are %d joined devices", len(ids))
	}
	return ids[0], nil
}

// spawnList: humans see all of them; an agent sees its project's.
func spawnList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	where, args := ``, []any{}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		where, args = `WHERE s.project_id = ? `, []any{a.projectID}
	}
	rows, err := c.spawns(where, args...)
	if err != nil {
		return nil, err
	}
	out := make([]api.Spawn, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.api())
	}
	return out, nil
}

func spawnGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	s, err := c.spawn(id)
	if err != nil {
		return nil, err
	}
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		if a.projectID != s.projectID {
			return nil, notFound("no spawn %d", id)
		}
	}
	return s.api(), nil
}

// deviceSpawns is what a link asks for: the requests waiting for its device.
func deviceSpawns(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may list its spawns")
	}
	rows, err := c.spawns(`WHERE s.device_id = ? AND s.status IN ('pending', 'launching', 'started')`, c.p.deviceID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Spawn, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.api())
	}
	return out, nil
}

// spawnReport is the link's account of a start. Only the device the spawn
// belongs to may report, and the status only moves forward.
func spawnReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only the device that starts an agent reports on it")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	var req api.SpawnReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	s, err := c.spawn(id)
	if err != nil {
		return nil, err
	}
	if s.deviceID != c.p.deviceID {
		return nil, forbidden("spawn %d belongs to another device", id)
	}
	allowed := map[string][]string{
		api.SpawnLaunching: {api.SpawnPending},
		api.SpawnStarted:   {api.SpawnPending, api.SpawnLaunching},
		api.SpawnFailed:    {api.SpawnPending, api.SpawnLaunching, api.SpawnStarted},
	}
	from, ok := allowed[req.Status]
	if !ok {
		return nil, badRequest("status must be launching, started or failed")
	}
	okFrom := false
	for _, f := range from {
		okFrom = okFrom || f == s.status
	}
	if !okFrom {
		return nil, conflict("spawn %d is %s; it cannot become %s", id, s.status, req.Status)
	}
	if len(req.Error) > 500 {
		req.Error = req.Error[:500]
	}
	if _, err := c.tx.Exec(`UPDATE spawn SET status = ?, pane = ?, error = ?, updated_at = ? WHERE id = ?`,
		req.Status, req.Pane, req.Error, store.Millis(c.now), id); err != nil {
		return nil, err
	}
	s.status, s.pane, s.errText = req.Status, req.Pane, req.Error
	if req.Status == api.SpawnStarted && s.job.Valid {
		// The new agent joins the job as a worker.
		if a, err := c.agentByName(s.name); err != nil {
			return nil, err
		} else if a != nil && a.deviceID == s.deviceID {
			if err := c.putInJob(a, s.job, api.RoleWorker); err != nil {
				return nil, err
			}
		}
	}
	if err := c.record(s.projectID, 0, "spawn."+req.Status, fmt.Sprintf("spawn:%d", id),
		map[string]any{"name": s.name, "pane": req.Pane, "error": req.Error}); err != nil {
		return nil, err
	}
	return s.api(), nil
}

// expireSpawns fails requests that no link picked up or finished in time.
func (c *call) expireSpawns() error {
	rows, err := c.spawns(`WHERE s.status IN ('pending', 'launching') AND s.updated_at < ?`, store.Millis(c.now.Add(-c.h.opt.SpawnTimeout)))
	if err != nil {
		return err
	}
	for _, s := range rows {
		if _, err := c.tx.Exec(`UPDATE spawn SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`,
			"the device did not start it in time (is its link running?)", store.Millis(c.now), s.id); err != nil {
			return err
		}
		if err := c.record(s.projectID, 0, "spawn.failed", fmt.Sprintf("spawn:%d", s.id),
			map[string]any{"name": s.name, "error": "timeout"}); err != nil {
			return err
		}
	}
	return nil
}
