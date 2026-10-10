package hub

import (
	"database/sql"
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// A finished job can teach. The lead proposes what the job showed as a short
// addition to one skill of one profile; a person reads it and accepts or
// rejects it. Accepting writes a new, hash-pinned version of the profile, so
// agents already running and earlier jobs are unaffected, and agents started
// afterwards from the profile have it. Lessons about a client go to that
// client's notes instead (they stay on the device); a skill is shared by every
// client, so a lesson here must not name one.

const (
	maxLessonText = 1500
	maxLessonWhy  = 600
	lessonHeading = "## Lessons learned"
)

type lessonRow struct {
	id, decidedAt, newVersion sql.NullInt64
	job                       sql.NullInt64
	by, profile, skill, text  string
	why, status, decidedBy    string
	note                      string
	at                        int64
}

const lessonSelect = `SELECT id, job_id, proposed_by, profile, skill, text, why, status, decided_by, decided_at, decision_note, new_version, created_at FROM lesson `

func scanLesson(s scanner) (*lessonRow, error) {
	l := &lessonRow{}
	err := s.Scan(&l.id, &l.job, &l.by, &l.profile, &l.skill, &l.text, &l.why, &l.status, &l.decidedBy, &l.decidedAt, &l.note, &l.newVersion, &l.at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return l, err
}

func (c *call) lessonAPI(l *lessonRow) (api.Lesson, error) {
	out := api.Lesson{ID: l.id.Int64, Job: l.job.Int64, ProposedBy: l.by, Profile: l.profile, Skill: l.skill, Text: l.text, Why: l.why,
		Status: l.status, DecidedBy: l.decidedBy, Note: l.note, NewVersion: int(l.newVersion.Int64), At: store.Time(l.at)}
	if l.status == "accepted" {
		if err := c.tx.QueryRow(`SELECT count(*) FROM spawn WHERE profile_name = ? AND profile_version >= ?`, l.profile, l.newVersion.Int64).Scan(&out.Used); err != nil {
			return out, err
		}
	}
	return out, nil
}

func lessonPropose(c *call) (any, error) {
	if err := c.allow(ActTaskManage); err != nil {
		return nil, err
	}
	var req api.LessonReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	req.Text, req.Why = strings.TrimSpace(req.Text), strings.TrimSpace(req.Why)
	switch {
	case req.Text == "" || req.Why == "":
		return nil, badRequest("a lesson needs both the text to add and why (what happened in the job that showed it)")
	case len(req.Text) > maxLessonText:
		return nil, badRequest("the lesson is longer than %d bytes: keep it to a few sentences", maxLessonText)
	case len(req.Why) > maxLessonWhy:
		return nil, badRequest("the reason is longer than %d bytes", maxLessonWhy)
	}
	spec, err := c.latestSpec(req.Profile)
	if err != nil {
		return nil, err
	}
	if _, ok := skillFile(spec, req.Skill); !ok {
		return nil, badRequest("profile %q has no skill %q (it has: %s)", req.Profile, req.Skill, strings.Join(profile.SkillNames(spec), ", "))
	}
	var job sql.NullInt64
	if c.p.kind == kindDevice {
		a, err := c.agent()
		if err != nil {
			return nil, err
		}
		job = a.jobID
	}
	res, err := c.tx.Exec(`INSERT INTO lesson(job_id, proposed_by, profile, skill, text, why, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job, c.p.actor(), req.Profile, req.Skill, req.Text, req.Why, store.Millis(c.now))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := c.audit("lesson.propose", fmt.Sprintf("lesson:%d", id), map[string]any{"profile": req.Profile, "skill": req.Skill, "job": job.Int64}); err != nil {
		return nil, err
	}
	return c.oneLesson(id)
}

func (c *call) oneLesson(id int64) (api.Lesson, error) {
	l, err := scanLesson(c.tx.QueryRow(lessonSelect+`WHERE id = ?`, id))
	if err != nil {
		return api.Lesson{}, err
	}
	if l == nil {
		return api.Lesson{}, notFound("no lesson %d", id)
	}
	return c.lessonAPI(l)
}

func lessonList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	q, args := lessonSelect, []any{}
	var where []string
	if s := c.r.URL.Query().Get("status"); s != "" {
		where = append(where, "status = ?")
		args = append(args, s)
	}
	if j := c.r.URL.Query().Get("job"); j != "" {
		where = append(where, "job_id = ?")
		args = append(args, j)
	}
	if len(where) > 0 {
		q += "WHERE " + strings.Join(where, " AND ") + " "
	}
	rows, err := c.tx.Query(q+`ORDER BY id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	var all []*lessonRow
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, l)
	}
	rows.Close()
	out := []api.Lesson{}
	for _, l := range all {
		a, err := c.lessonAPI(l)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func lessonAccept(c *call) (any, error) {
	if err := c.allow(ActLessonDecide); err != nil {
		return nil, err
	}
	return c.decideLesson(true, "")
}

func lessonReject(c *call) (any, error) {
	if err := c.allow(ActLessonDecide); err != nil {
		return nil, err
	}
	var req api.LessonDecideReq
	if c.r.ContentLength != 0 {
		if err := c.decode(&req); err != nil {
			return nil, err
		}
	}
	return c.decideLesson(false, strings.TrimSpace(req.Reason))
}

// decideLesson is the work for the API and the web UI; the caller checked the scope.
func (c *call) decideLesson(accept bool, reason string) (api.Lesson, error) {
	id, err := c.pathID()
	if err != nil {
		return api.Lesson{}, err
	}
	l, err := scanLesson(c.tx.QueryRow(lessonSelect+`WHERE id = ?`, id))
	if err != nil {
		return api.Lesson{}, err
	}
	if l == nil {
		return api.Lesson{}, notFound("no lesson %d", id)
	}
	if l.status != "proposed" {
		return api.Lesson{}, conflict("lesson %d is already %s", id, l.status)
	}
	if !accept {
		if _, err := c.tx.Exec(`UPDATE lesson SET status = 'rejected', decided_by = ?, decided_at = ?, decision_note = ? WHERE id = ?`, c.p.actor(), store.Millis(c.now), reason, id); err != nil {
			return api.Lesson{}, err
		}
		if err := c.audit("lesson.reject", fmt.Sprintf("lesson:%d", id), map[string]any{"reason": reason}); err != nil {
			return api.Lesson{}, err
		}
		return c.oneLesson(id)
	}
	if err := c.ownerOnly("accepting a lesson, because it changes a profile"); err != nil {
		return api.Lesson{}, err
	}
	spec, err := c.latestSpec(l.profile)
	if err != nil {
		return api.Lesson{}, err
	}
	i, ok := skillFile(spec, l.skill)
	if !ok {
		return api.Lesson{}, conflict("profile %q no longer has the skill %q", l.profile, l.skill)
	}
	spec.Skills[i].Content = addLesson(spec.Skills[i].Content, l.text, l.job.Int64)
	p, err := c.saveProfile(l.profile, *spec)
	if err != nil {
		return api.Lesson{}, err
	}
	if _, err := c.tx.Exec(`UPDATE lesson SET status = 'accepted', decided_by = ?, decided_at = ?, new_version = ? WHERE id = ?`, c.p.actor(), store.Millis(c.now), p.version, id); err != nil {
		return api.Lesson{}, err
	}
	if err := c.audit("lesson.accept", fmt.Sprintf("lesson:%d", id), map[string]any{"profile": l.profile, "version": p.version}); err != nil {
		return api.Lesson{}, err
	}
	return c.oneLesson(id)
}

// addLesson appends a bullet under "Lessons learned", adding the heading once.
func addLesson(content, text string, job int64) string {
	bullet := "- " + strings.Join(strings.Fields(text), " ")
	if job > 0 {
		bullet += fmt.Sprintf(" (from job #%d)", job)
	}
	content = strings.TrimRight(content, "\n")
	if strings.Contains(content, lessonHeading) {
		return content + "\n" + bullet + "\n"
	}
	return content + "\n\n" + lessonHeading + "\n\n" + bullet + "\n"
}

func (c *call) latestSpec(name string) (*profile.Spec, error) {
	p, err := c.profileVersion(name, 0)
	if err != nil {
		return nil, err
	}
	spec := p.full().Spec
	return &spec, nil
}

// skillFile finds the SKILL.md of a skill by name; it returns its index.
func skillFile(s *profile.Spec, name string) (int, bool) {
	for i, f := range s.Skills {
		if f.Path == name+"/SKILL.md" {
			return i, true
		}
	}
	return 0, false
}
