// Package plan defines the bounded, immutable task graph accepted by the hub.
package plan

import (
	"fmt"
	"regexp"
	"strings"
)

type Step struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Profile   string   `json:"profile"`
	DependsOn []string `json:"depends_on,omitempty"`
}

type Plan struct {
	Version     int    `json:"version"`
	Playbook    string `json:"playbook,omitempty"`
	Device      string `json:"device,omitempty"`
	MaxAttempts int    `json:"max_attempts"`
	Steps       []Step `json:"steps"`
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

// Order validates the whole plan before any tasks or workers are created.
// Independent steps keep their input order, making dispatch reproducible.
func (p Plan) Order() ([]int, error) {
	if p.Version != 1 {
		return nil, fmt.Errorf("plan version must be 1")
	}
	if p.MaxAttempts < 1 || p.MaxAttempts > 3 {
		return nil, fmt.Errorf("max_attempts must be between 1 and 3")
	}
	if len(p.Steps) == 0 || len(p.Steps) > 50 {
		return nil, fmt.Errorf("a plan needs 1 to 50 steps")
	}
	index := make(map[string]int)
	for i, s := range p.Steps {
		if !keyPattern.MatchString(s.Key) {
			return nil, fmt.Errorf("invalid step key %q", s.Key)
		}
		if _, exists := index[s.Key]; exists {
			return nil, fmt.Errorf("duplicate step %q", s.Key)
		}
		if strings.TrimSpace(s.Title) == "" || len(s.Title) > 200 {
			return nil, fmt.Errorf("step %s needs a title of at most 200 bytes", s.Key)
		}
		if strings.TrimSpace(s.Profile) == "" {
			return nil, fmt.Errorf("step %s needs a profile", s.Key)
		}
		if len(s.Body) > 32000 {
			return nil, fmt.Errorf("step %s body is too long", s.Key)
		}
		index[s.Key] = i
	}
	for _, s := range p.Steps {
		seen := make(map[string]bool)
		for _, dep := range s.DependsOn {
			if _, ok := index[dep]; !ok {
				return nil, fmt.Errorf("step %s depends on unknown step %s", s.Key, dep)
			}
			if dep == s.Key || seen[dep] {
				return nil, fmt.Errorf("step %s has a self or duplicate dependency", s.Key)
			}
			seen[dep] = true
		}
	}
	var order []int
	done := make(map[string]bool)
	for len(order) < len(p.Steps) {
		progressed := false
		for i, s := range p.Steps {
			if done[s.Key] {
				continue
			}
			ready := true
			for _, dep := range s.DependsOn {
				if !done[dep] {
					ready = false
				}
			}
			if ready {
				done[s.Key] = true
				order = append(order, i)
				progressed = true
			}
		}
		if !progressed {
			return nil, fmt.Errorf("plan dependencies contain a cycle")
		}
	}
	return order, nil
}

type StepStatus struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Task     int64  `json:"task"`
	Profile  string `json:"profile"`
	Attempts int    `json:"attempts"`
	Worker   string `json:"worker,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

type Status struct {
	Job         int64        `json:"job"`
	Playbook    string       `json:"playbook,omitempty"`
	Version     int          `json:"version"`
	MaxAttempts int          `json:"max_attempts"`
	Steps       []StepStatus `json:"steps"`
}
