package link

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"handloom/internal/api"
)

// Usage, device side. The agent CLIs write their own logs; this reads the
// token counts out of them (never the text) and reports running totals per
// agent and model. Claude Code, Codex, OMP, Pi and OpenCode are read.
// For any other CLI nothing is reported, so the page says "no usage reported" instead of guessing.

const usageEvery = time.Minute

type usageReader struct {
	mu    sync.Mutex
	last  time.Time
	cache map[string]cachedFile // path -> parsed result for an unchanged file
}

type cachedFile struct {
	size int64
	mod  time.Time
	res  map[string]api.UsageModel
	cwd  string
	at   time.Time // when the session started
}

var slugRE = regexp.MustCompile(`[^A-Za-z0-9]`)

// claudeSlug is how Claude Code names the log folder of a working directory.
func claudeSlug(dir string) string { return slugRE.ReplaceAllString(dir, "-") }

func (l *Link) homeDir() string {
	if l.opt.UserHome != "" {
		return l.opt.UserHome
	}
	h, _ := os.UserHomeDir()
	return h
}

// runUsage reports usage for every agent on this device, at most once a minute.
func (l *Link) runUsage(ctx context.Context, agents []api.DeviceAgent) {
	l.usage.mu.Lock()
	if time.Since(l.usage.last) < usageEvery {
		l.usage.mu.Unlock()
		return
	}
	l.usage.last = time.Now()
	if l.usage.cache == nil {
		l.usage.cache = map[string]cachedFile{}
	}
	l.usage.mu.Unlock()
	home := l.homeDir()
	if home == "" {
		return
	}
	for _, a := range agents {
		if a.Dir == "" {
			continue
		}
		var models map[string]api.UsageModel
		switch a.Kind {
		case "claude":
			models = l.claudeUsage(home, a.Dir, a.RegisteredAt)
		case "codex":
			models = l.codexUsage(home, a.Dir, a.RegisteredAt)
		case "omp":
			models = l.piLikeUsage(filepath.Join(home, ".omp", "agent", "sessions"), a.Dir, a.RegisteredAt)
		case "pi":
			models = l.piLikeUsage(filepath.Join(home, ".pi", "agent", "sessions"), a.Dir, a.RegisteredAt)
		case "opencode":
			models = openCodeUsage(filepath.Join(home, ".local", "share", "opencode", "opencode.db"), a.Dir, a.RegisteredAt)
		}
		if len(models) == 0 {
			continue
		}
		req := api.UsageReq{Agent: a.Name}
		for _, m := range models {
			req.Models = append(req.Models, m)
		}
		sort.Slice(req.Models, func(i, j int) bool { return req.Models[i].Model < req.Models[j].Model })
		if err := l.hub.Do(ctx, "POST", "/v1/device/usage", req, nil); err != nil && ctx.Err() == nil {
			l.opt.Log.Printf("usage of %s: %v", a.Name, err)
		}
	}
}

// claudeUsage sums the sessions Claude Code logged for a working directory
// since the agent was registered. A reply is written to the log more than
// once while it streams, so each request is counted once, at its largest.
func (l *Link) claudeUsage(home, dir string, since time.Time) map[string]api.UsageModel {
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", claudeSlug(dir), "*.jsonl"))
	out := map[string]api.UsageModel{}
	for _, f := range files {
		res, ok := l.cachedParse(f, since, parseClaudeLog)
		if !ok {
			continue
		}
		for k, v := range res {
			m := out[k]
			m.Model = k
			m.Input += v.Input
			m.Output += v.Output
			m.CacheRead += v.CacheRead
			m.CacheWrite += v.CacheWrite
			out[k] = m
		}
	}
	return out
}

func parseClaudeLog(path string) (map[string]api.UsageModel, string, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", time.Time{}
	}
	defer f.Close()
	type usage struct {
		model                          string
		in, out, cacheRead, cacheWrite int64
	}
	byReq := map[string]usage{}
	var start time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var d struct {
			Timestamp string `json:"timestamp"`
			RequestID string `json:"requestId"`
			Message   struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *struct {
					In         int64 `json:"input_tokens"`
					Out        int64 `json:"output_tokens"`
					CacheRead  int64 `json:"cache_read_input_tokens"`
					CacheWrite int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		if start.IsZero() && d.Timestamp != "" {
			start, _ = time.Parse(time.RFC3339Nano, d.Timestamp)
		}
		if d.Message.Usage == nil || d.Message.Model == "" || strings.HasPrefix(d.Message.Model, "<") {
			continue
		}
		key := d.RequestID
		if key == "" {
			key = d.Message.ID
		}
		u := usage{d.Message.Model, d.Message.Usage.In, d.Message.Usage.Out, d.Message.Usage.CacheRead, d.Message.Usage.CacheWrite}
		if prev, ok := byReq[key]; !ok || u.out >= prev.out {
			byReq[key] = u
		}
	}
	res := map[string]api.UsageModel{}
	for _, u := range byReq {
		m := res[u.model]
		m.Model = u.model
		m.Input += u.in
		m.Output += u.out
		m.CacheRead += u.cacheRead
		m.CacheWrite += u.cacheWrite
		res[u.model] = m
	}
	return res, "", start
}

// codexUsage reads Codex's session logs whose working directory is the
// agent's. Codex writes running totals, so the last one of a session is its total.
func (l *Link) codexUsage(home, dir string, since time.Time) map[string]api.UsageModel {
	root := filepath.Join(home, ".codex", "sessions")
	out := map[string]api.UsageModel{}
	cutoff := since.Add(-24 * time.Hour)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		res, ok := l.cachedParse(p, since, func(path string) (map[string]api.UsageModel, string, time.Time) { return parseCodexLog(path) })
		if !ok {
			return nil
		}
		l.usage.mu.Lock()
		cwd := l.usage.cache[p].cwd
		l.usage.mu.Unlock()
		if cwd != dir {
			return nil
		}
		for k, v := range res {
			m := out[k]
			m.Model = k
			m.Input += v.Input
			m.Output += v.Output
			m.CacheRead += v.CacheRead
			m.CacheWrite += v.CacheWrite
			out[k] = m
		}
		return nil
	})
	return out
}

func parseCodexLog(path string) (map[string]api.UsageModel, string, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", time.Time{}
	}
	defer f.Close()
	var cwd, model string
	var start time.Time
	var total struct{ in, cached, write, out int64 }
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var d struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Cwd   string `json:"cwd"`
				Model string `json:"model"`
				Type  string `json:"type"`
				Info  *struct {
					Total struct {
						In     int64 `json:"input_tokens"`
						Cached int64 `json:"cached_input_tokens"`
						Write  int64 `json:"cache_write_input_tokens"`
						Out    int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		switch {
		case d.Type == "session_meta":
			cwd = d.Payload.Cwd
			start, _ = time.Parse(time.RFC3339Nano, d.Timestamp)
		case d.Payload.Model != "":
			model = d.Payload.Model
		}
		if d.Type == "event_msg" && d.Payload.Type == "token_count" && d.Payload.Info != nil {
			t := d.Payload.Info.Total
			total.in, total.cached, total.write, total.out = t.In, t.Cached, t.Write, t.Out
		}
	}
	if model == "" {
		model = "codex"
	}
	res := map[string]api.UsageModel{}
	if total.in+total.out > 0 {
		fresh := total.in - total.cached
		if fresh < 0 {
			fresh = 0
		}
		res[model] = api.UsageModel{Model: model, Input: fresh, Output: total.out, CacheRead: total.cached, CacheWrite: total.write}
	}
	return res, cwd, start
}

// cachedParse parses a log file, remembering the result while the file is unchanged.
// It reports false for a session that started before the agent did.
func (l *Link) cachedParse(path string, since time.Time, parse func(string) (map[string]api.UsageModel, string, time.Time)) (map[string]api.UsageModel, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	l.usage.mu.Lock()
	c, ok := l.usage.cache[path]
	l.usage.mu.Unlock()
	if !ok || c.size != info.Size() || !c.mod.Equal(info.ModTime()) {
		res, cwd, at := parse(path)
		c = cachedFile{size: info.Size(), mod: info.ModTime(), res: res, cwd: cwd, at: at}
		l.usage.mu.Lock()
		if l.usage.cache == nil {
			l.usage.cache = map[string]cachedFile{}
		}
		l.usage.cache[path] = c
		l.usage.mu.Unlock()
	}
	if !since.IsZero() && !c.at.IsZero() && c.at.Before(since.Add(-time.Minute)) {
		return nil, false
	}
	return c.res, true
}

// piLikeUsage reads the session logs of OMP and Pi (the same format): each
// assistant message carries the tokens it used, and the first line names the
// working directory. Only sessions of this agent's directory, started since it was, count.
func (l *Link) piLikeUsage(root, dir string, since time.Time) map[string]api.UsageModel {
	out := map[string]api.UsageModel{}
	cutoff := since.Add(-24 * time.Hour)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		res, ok := l.cachedParse(p, since, parsePiLog)
		if !ok {
			return nil
		}
		l.usage.mu.Lock()
		cwd := l.usage.cache[p].cwd
		l.usage.mu.Unlock()
		if cwd != dir {
			return nil
		}
		for k, v := range res {
			m := out[k]
			m.Model = k
			m.Input += v.Input
			m.Output += v.Output
			m.CacheRead += v.CacheRead
			m.CacheWrite += v.CacheWrite
			out[k] = m
		}
		return nil
	})
	return out
}

func parsePiLog(path string) (map[string]api.UsageModel, string, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", time.Time{}
	}
	defer f.Close()
	var cwd string
	var start time.Time
	res := map[string]api.UsageModel{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var d struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Cwd       string `json:"cwd"`
			Message   struct {
				Role  string `json:"role"`
				Model string `json:"model"`
				Usage *struct {
					In         int64 `json:"input"`
					Out        int64 `json:"output"`
					CacheRead  int64 `json:"cacheRead"`
					CacheWrite int64 `json:"cacheWrite"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		if d.Type == "session" {
			cwd = d.Cwd
			start, _ = time.Parse(time.RFC3339Nano, d.Timestamp)
			continue
		}
		u := d.Message.Usage
		if d.Type != "message" || d.Message.Role != "assistant" || u == nil || d.Message.Model == "" {
			continue
		}
		m := res[d.Message.Model]
		m.Model = d.Message.Model
		m.Input += u.In
		m.Output += u.Out
		m.CacheRead += u.CacheRead
		m.CacheWrite += u.CacheWrite
		res[d.Message.Model] = m
	}
	return res, cwd, start
}

// openCodeUsage reads OpenCode's own database, read-only: the tokens of the
// assistant messages written in this agent's working directory since it started.
func openCodeUsage(dbPath, dir string, since time.Time) map[string]api.UsageModel {
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT json_extract(data, '$.modelID'),
			coalesce(sum(json_extract(data, '$.tokens.input')), 0), coalesce(sum(json_extract(data, '$.tokens.output')), 0),
			coalesce(sum(json_extract(data, '$.tokens.cache.read')), 0), coalesce(sum(json_extract(data, '$.tokens.cache.write')), 0)
		FROM message WHERE json_extract(data, '$.role') = 'assistant' AND json_extract(data, '$.path.cwd') = ? AND time_created >= ?
		GROUP BY 1`, dir, since.Add(-time.Minute).UnixMilli())
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]api.UsageModel{}
	for rows.Next() {
		var m api.UsageModel
		var name sql.NullString
		if rows.Scan(&name, &m.Input, &m.Output, &m.CacheRead, &m.CacheWrite) != nil || !name.Valid || name.String == "" {
			continue
		}
		m.Model = name.String
		if m.Input+m.Output+m.CacheRead+m.CacheWrite > 0 {
			out[m.Model] = m
		}
	}
	return out
}
