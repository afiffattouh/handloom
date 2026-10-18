package link

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"handloom/internal/api"
)

const maxEvidenceFile = 256 << 10

// kitEvidence reads the summary a project kit's check names on its last line ("evidence: runs/<run>/summary.json")
// and keeps the part people read. A command that is not a kit has no such line, and the answer is nil. The path
// must stay inside the worktree, because the output is the project's to write, not this device's to trust.
func kitEvidence(dir, output string) *api.CheckEvidence {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var rel string
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		if r, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "evidence: "); ok {
			rel = strings.TrimSpace(r)
			break
		}
	}
	if rel == "" || !strings.HasSuffix(rel, "/summary.json") || filepath.IsAbs(rel) {
		return nil
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil
	}
	path := filepath.Join(dir, clean)
	if real, err := filepath.EvalSymlinks(path); err != nil {
		return nil
	} else if root, err := filepath.EvalSymlinks(dir); err != nil || !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var raw struct {
		Schema  int      `json:"schema"`
		Mode    string   `json:"mode"`
		Result  string   `json:"result"`
		Reason  string   `json:"reason"`
		Blocked string   `json:"blocked"`
		Refused []string `json:"refused"`
		Stages  []struct {
			Name    string  `json:"name"`
			Status  string  `json:"status"`
			Detail  string  `json:"detail"`
			Seconds float64 `json:"seconds"`
		} `json:"stages"`
	}
	if err := json.NewDecoder(io.LimitReader(f, maxEvidenceFile)).Decode(&raw); err != nil || raw.Schema != 1 || raw.Result == "" {
		return nil
	}
	ev := &api.CheckEvidence{Result: raw.Result, Reason: raw.Reason, Blocked: raw.Blocked, Mode: raw.Mode, Refused: raw.Refused}
	for _, s := range raw.Stages {
		ev.Stages = append(ev.Stages, api.CheckStage{Name: s.Name, Status: s.Status, Detail: s.Detail, Seconds: s.Seconds})
	}
	return ev
}
