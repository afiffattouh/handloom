package hub

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Search over what earlier jobs recorded on the hub: briefs, task text, the
// evidence and notes of submitted work, handoff notes, and questions with their
// answers. It never touches a client's knowledge repository: those notes stay on
// the machine that holds them and are searched there (`handloom search --notes`).
//
// Who sees what: a person sees every job (a confidential one too). An agent sees
// the jobs that share its job's knowledge repository (or, without one, its job's
// repository; without either, only its own job), and a confidential job only if its
// own job is confidential too, so a cloud-model agent never reads confidential work.

type jobInfo struct {
	id                int64
	title, scopeKey   string
	confidential      bool
	knowledge, repo   string
	scopeOK, includes bool
}

// searchTerms splits a query into lower-case words worth matching.
func searchTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r >= 0x80)
	}) {
		if len([]rune(w)) >= 2 && !seen[w] && !stopWords[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "are": true, "was": true, "how": true, "what": true, "does": true, "do": true, "we": true, "is": true, "to": true, "of": true, "in": true, "on": true, "a": true, "an": true}

// score counts how many different terms a text contains, and how often.
func score(text string, terms []string) (distinct, total int) {
	l := strings.ToLower(text)
	for _, t := range terms {
		if n := strings.Count(l, t); n > 0 {
			distinct++
			total += n
		}
	}
	return
}

// snippet is the part of text around the first match, in one line.
func snippet(text string, terms []string) string {
	flat := strings.Join(strings.Fields(text), " ")
	l := strings.ToLower(flat)
	at := -1
	for _, t := range terms {
		if i := strings.Index(l, t); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 {
		at = 0
	}
	start := at - 60
	if start < 0 {
		start = 0
	}
	end := at + 200
	if end > len(flat) {
		end = len(flat)
	}
	for start > 0 && !isRuneStart(flat[start]) {
		start--
	}
	for end < len(flat) && !isRuneStart(flat[end]) {
		end++
	}
	s := flat[start:end]
	if start > 0 {
		s = "…" + s
	}
	if end < len(flat) {
		s += "…"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func searchGet(c *call) (any, error) {
	if c.p.kind == kindDevice && c.p.agent == nil && c.p.agentName == "" {
		return nil, forbidden("searching earlier jobs needs an agent identity (HANDLOOM_AGENT) or a person's token (handloom login); this machine's device token alone does not say which client's work it may see")
	}
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	terms := searchTerms(c.r.URL.Query().Get("q"))
	if len(terms) == 0 {
		return nil, badRequest("say what to look for: a few words, such as \"invoice prefix\"")
	}
	limit := 20
	if n, err := strconv.Atoi(c.r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	jobs, err := c.searchableJobs()
	if err != nil {
		return nil, err
	}
	var hits []api.SearchHit
	add := func(kind string, job, task int64, title, text, who string, at int64, boost int) {
		j, ok := jobs[job]
		if !ok || !j.includes {
			return
		}
		d, n := score(text+" "+title, terms)
		if d == 0 {
			return
		}
		hits = append(hits, api.SearchHit{Kind: kind, Job: job, Task: task, Title: title, Snippet: snippet(text, terms), Who: who, At: store.Time(at), Score: d*100 + n + boost})
	}
	// jobs and tasks: brief, evidence and the note that came with it
	rows, err := c.tx.Query(`SELECT id, kind, COALESCE(job_id, id), title, body, evidence, note, status, updated_at FROM task`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, jobID, at int64
		var kind, title, body, ev, note, status string
		if err := rows.Scan(&id, &kind, &jobID, &title, &body, &ev, &note, &status, &at); err != nil {
			rows.Close()
			return nil, err
		}
		var evs []string
		json.Unmarshal([]byte(ev), &evs)
		text := strings.TrimSpace(body + " " + note + " " + strings.Join(evs, " "))
		if kind == "job" {
			add("job", id, 0, title, body, "", at, 30)
		} else {
			boost := 0
			if status == "done" {
				boost = 20 // accepted work is the most trustworthy thing a past job has to say
			}
			add("task", jobID, id, title, text, "", at, boost)
		}
	}
	rows.Close()
	// handoff notes
	rows, err = c.tx.Query(`SELECT h.task_id, COALESCE(t.job_id, t.id), t.title, h.author, h.done, h.tried, h.next, h.verify, h.created_at FROM handoff h JOIN task t ON t.id = h.task_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tid, jid, at int64
		var title, author, done, tried, next, verify string
		if err := rows.Scan(&tid, &jid, &title, &author, &done, &tried, &next, &verify, &at); err != nil {
			rows.Close()
			return nil, err
		}
		add("handoff", jid, tid, title, done+" "+tried+" "+next+" "+verify, author, at, 10)
	}
	rows.Close()
	// questions and their answers
	rows, err = c.tx.Query(`SELECT e.id, COALESCE(t.job_id, t.id, a.job_id, 0), e.question, COALESCE(e.answer, ''), e.answered_by, e.created_at
		FROM escalation e LEFT JOIN task t ON t.id = e.task_id JOIN agent a ON a.id = e.from_agent_id WHERE e.answer IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, jid, at int64
		var question, answer, by string
		if err := rows.Scan(&id, &jid, &question, &answer, &by, &at); err != nil {
			rows.Close()
			return nil, err
		}
		add("answer", jid, 0, question, "Q: "+question+" A: "+answer, by, at, 25)
	}
	rows.Close()

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].At.After(hits[j].At)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	for i := range hits {
		if j := jobs[hits[i].Job]; j != nil && hits[i].Kind != "job" {
			hits[i].Title = "#" + strconv.FormatInt(hits[i].Job, 10) + " " + j.title + ": " + hits[i].Title
		}
	}
	if hits == nil {
		hits = []api.SearchHit{}
	}
	return hits, nil
}

// searchableJobs lists the jobs and marks the ones the caller may search.
func (c *call) searchableJobs() (map[int64]*jobInfo, error) {
	rows, err := c.tx.Query(`SELECT id, title, confidential, knowledge, repo FROM task WHERE kind = 'job'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := map[int64]*jobInfo{}
	for rows.Next() {
		j := &jobInfo{}
		if err := rows.Scan(&j.id, &j.title, &j.confidential, &j.knowledge, &j.repo); err != nil {
			return nil, err
		}
		jobs[j.id] = j
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if c.p.kind != kindDevice {
		for _, j := range jobs { // a person (or the admin): everything
			j.includes = true
		}
		return jobs, nil
	}
	a, err := c.agent()
	if err != nil {
		return nil, forbidden("searching earlier jobs needs an agent identity (HANDLOOM_AGENT) or a person's token (handloom login); this machine's device token alone does not say which client's work it may see")
	}
	var own *jobInfo
	if a.jobID.Valid {
		own = jobs[a.jobID.Int64]
	}
	for _, j := range jobs {
		switch {
		case own == nil:
			j.includes = false
		case j.id == own.id:
			j.includes = true
		case own.knowledge != "" && j.knowledge == own.knowledge, own.knowledge == "" && own.repo != "" && j.repo == own.repo:
			j.includes = !j.confidential || own.confidential
		}
	}
	return jobs, nil
}
