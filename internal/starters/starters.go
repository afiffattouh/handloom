// Package starters is the library of ready-made profiles and skills that ship
// with Handloom. A starter is a template, not a profile: it has no agent CLI,
// no model and no runtime. A person picks those when adding it, and what is
// added is an ordinary profile (version 1, editable, owned by them).
package starters

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"handloom/internal/profile"
)

//go:embed library
var library embed.FS

// Starter is one entry of the library.
type Starter struct {
	Name             string   `yaml:"name" json:"name"`
	Title            string   `yaml:"title" json:"title"`
	Group            string   `yaml:"group" json:"group"`
	Summary          string   `yaml:"summary" json:"summary"`
	Core             bool     `yaml:"core" json:"core"`
	Skills           []string `yaml:"skills" json:"skills"`
	RecommendRuntime string   `yaml:"recommend_runtime" json:"recommend_runtime,omitempty"`
	Note             string   `yaml:"note" json:"note,omitempty"`
}

// Skill is a skill of the library with its one-line description.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

var (
	once    sync.Once
	list    []Starter
	byName  map[string]*Starter
	skills  map[string]Skill
	loadErr error
)

func load() {
	raw, err := fs.ReadFile(library, "library/index.yaml")
	if err != nil {
		loadErr = err
		return
	}
	var idx struct {
		Starters []Starter `yaml:"starters"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&idx); err != nil {
		loadErr = fmt.Errorf("library/index.yaml: %w", err)
		return
	}
	list = idx.Starters
	byName = map[string]*Starter{}
	for i := range list {
		byName[list[i].Name] = &list[i]
	}
	skills = map[string]Skill{}
	entries, _ := fs.ReadDir(library, "library/skills")
	for _, e := range entries {
		b, err := fs.ReadFile(library, "library/skills/"+e.Name()+"/SKILL.md")
		if err != nil {
			loadErr = err
			return
		}
		skills[e.Name()] = Skill{Name: e.Name(), Description: frontmatter(string(b), "description"), Content: string(b)}
	}
}

func frontmatter(s, key string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, key+": ") {
			return strings.TrimSpace(strings.TrimPrefix(l, key+": "))
		}
	}
	return ""
}

// All lists the starters in library order.
func All() ([]Starter, error) {
	once.Do(load)
	return list, loadErr
}

// Get returns one starter, or nil.
func Get(name string) *Starter {
	once.Do(load)
	return byName[name]
}

// Skills lists the library's skills by name.
func Skills() []Skill {
	once.Do(load)
	out := make([]Skill, 0, len(skills))
	for _, s := range skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetSkill returns one skill of the library.
func GetSkill(name string) (Skill, bool) {
	once.Do(load)
	s, ok := skills[name]
	return s, ok
}

// Template reads the spec of a starter with no CLI, runtime or model chosen,
// and its skills attached.
func Template(name string) (*profile.Spec, error) {
	once.Do(load)
	st := byName[name]
	if st == nil {
		return nil, fmt.Errorf("no starter %q", name)
	}
	_, spec, err := profile.ReadFS(library, "library/profiles/"+name)
	if err != nil {
		return nil, err
	}
	for _, sk := range st.Skills {
		s, ok := skills[sk]
		if !ok {
			return nil, fmt.Errorf("starter %s names the skill %s, which is not in the library", name, sk)
		}
		spec.Skills = append(spec.Skills, profile.File{Path: sk + "/SKILL.md", Content: s.Content})
	}
	return spec, nil
}

// Choice is what a person decides when adding a starter.
type Choice struct {
	Kind    string // claude or codex: required
	Runtime string // cloud or local: required
	Model   string
	// Adapt leaves out what the chosen CLI cannot do (denied commands, a
	// missing shell or web tool) instead of refusing, and says what it left out.
	Adapt bool
}

// Result is a spec ready to save, and what the person should know about it.
type Result struct {
	Spec     *profile.Spec
	Problems []string // why it cannot be saved as it is
	Adapted  []string // what was left out because Adapt was set
	Warnings []string
}

// Build applies a choice to a starter.
func Build(name string, c Choice) (*Result, error) {
	spec, err := Template(name)
	if err != nil {
		return nil, err
	}
	r := &Result{Spec: spec}
	spec.Kind, spec.Runtime, spec.Model = c.Kind, c.Runtime, c.Model
	switch {
	case spec.Kind == "":
		r.Problems = append(r.Problems, "Choose the agent CLI.")
	case spec.Runtime == "":
		r.Problems = append(r.Problems, "Choose where the model runs (cloud or local).")
	}
	if len(r.Problems) > 0 {
		return r, nil
	}
	if c.Adapt {
		r.Adapted = profile.Adapt(spec)
	}
	r.Problems = profile.Normalize(spec)
	r.Warnings = profile.Warnings(spec)
	return r, nil
}
