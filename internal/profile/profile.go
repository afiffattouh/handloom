// Package profile is what an agent may do and know: a prompt, the tools it
// may use, and skills (folders with a SKILL.md). A profile is a directory on
// disk, a versioned and immutable row on the hub, and, when an agent is
// spawned, files and command-line flags in that agent's work directory.
//
//	researcher/
//	  profile.yaml       name, description, kind, runtime, model, tools
//	  PROMPT.md          instructions added to the agent's own
//	  skills/<name>/SKILL.md (and any other files of the skill)
//
// Tool limits are stated in a small set of words every agent CLI can be asked
// about (read, edit, shell, web) plus commands to deny; what a CLI cannot
// enforce is reported, never silently dropped.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the part of a profile that is versioned and hashed.
type Spec struct {
	Description string `json:"description,omitempty" yaml:"description"`
	Kind        string `json:"kind" yaml:"kind"`       // the agent CLI: claude
	Runtime     string `json:"runtime" yaml:"runtime"` // cloud | local: where the model runs
	Model       string `json:"model,omitempty" yaml:"model"`
	Prompt      string `json:"prompt,omitempty" yaml:"-"`
	Tools       Tools  `json:"tools" yaml:"tools"`
	Skills      []File `json:"skills,omitempty" yaml:"-"` // files of the skills, path relative to the skills directory
}

type Tools struct {
	Allow        []string `json:"allow" yaml:"allow"`                           // read, edit, shell, web
	DenyCommands []string `json:"deny_commands,omitempty" yaml:"deny_commands"` // shell commands the agent must not run
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

const (
	Cloud = "cloud"
	Local = "local"

	maxPrompt     = 32 << 10
	maxSkillFile  = 256 << 10
	maxSkillTotal = 2 << 20
	maxSkillFiles = 100
)

var (
	nameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)
	skillRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	modelRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)
	commandRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+( [A-Za-z0-9._/=:@-]+){0,3}$`)
	tools     = map[string]bool{"read": true, "edit": true, "shell": true, "web": true}
	kinds     = map[string]bool{"claude": true}
)

// ValidName reports whether name can name a profile.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Normalize fills defaults and returns the problems with the spec.
func Normalize(s *Spec) []string {
	var bad []string
	if s.Runtime == "" {
		s.Runtime = Cloud
	}
	if s.Kind == "" {
		s.Kind = "claude"
	}
	if !kinds[s.Kind] {
		bad = append(bad, fmt.Sprintf("kind %q is not supported yet (supported: claude)", s.Kind))
	}
	if s.Runtime != Cloud && s.Runtime != Local {
		bad = append(bad, fmt.Sprintf("runtime must be cloud or local, not %q", s.Runtime))
	}
	if s.Model != "" && !modelRE.MatchString(s.Model) {
		bad = append(bad, fmt.Sprintf("bad model name %q", s.Model))
	}
	if len(s.Prompt) > maxPrompt {
		bad = append(bad, fmt.Sprintf("the prompt is larger than %d bytes", maxPrompt))
	}
	if strings.ContainsRune(s.Prompt, 0) {
		bad = append(bad, "the prompt contains a NUL byte")
	}
	seen := map[string]bool{}
	for _, t := range s.Tools.Allow {
		if !tools[t] {
			bad = append(bad, fmt.Sprintf("unknown tool %q (read, edit, shell, web)", t))
		}
		seen[t] = true
	}
	sort.Strings(s.Tools.Allow)
	for _, c := range s.Tools.DenyCommands {
		if !commandRE.MatchString(c) || len(c) > 80 {
			bad = append(bad, fmt.Sprintf("deny_commands entry %q must be a command with at most three plain words (no quotes, brackets or wildcards)", c))
		}
	}
	if len(s.Tools.DenyCommands) > 0 && !seen["shell"] {
		bad = append(bad, "deny_commands only makes sense with the shell tool allowed")
	}
	names := map[string]bool{}
	total := 0
	if len(s.Skills) > maxSkillFiles {
		bad = append(bad, fmt.Sprintf("more than %d skill files", maxSkillFiles))
	}
	for _, f := range s.Skills {
		clean := path.Clean(f.Path)
		parts := strings.Split(clean, "/")
		switch {
		case f.Path == "" || clean != f.Path || path.IsAbs(clean) || strings.HasPrefix(clean, ".."):
			bad = append(bad, fmt.Sprintf("skill file path %q must be a plain relative path", f.Path))
			continue
		case len(parts) < 2 || !skillRE.MatchString(parts[0]):
			bad = append(bad, fmt.Sprintf("skill file %q must be inside a skill folder, like name/SKILL.md", f.Path))
			continue
		}
		for _, p := range parts {
			if p == "" || p == "." || p == ".." {
				bad = append(bad, fmt.Sprintf("skill file path %q is not plain", f.Path))
			}
		}
		if len(f.Content) > maxSkillFile {
			bad = append(bad, fmt.Sprintf("skill file %q is larger than %d bytes", f.Path, maxSkillFile))
		}
		total += len(f.Content)
		if clean == parts[0]+"/SKILL.md" {
			names[parts[0]] = true
		}
		names["~"+parts[0]] = true
	}
	for n := range names {
		if strings.HasPrefix(n, "~") && !names[n[1:]] {
			bad = append(bad, fmt.Sprintf("skill %q has no SKILL.md", n[1:]))
		}
	}
	if total > maxSkillTotal {
		bad = append(bad, fmt.Sprintf("skills are larger than %d bytes in all", maxSkillTotal))
	}
	sort.Slice(s.Skills, func(i, j int) bool { return s.Skills[i].Path < s.Skills[j].Path })
	return bad
}

// Hash identifies the exact content of a spec: what a spawn is pinned to.
func Hash(s *Spec) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SkillNames lists the skills in a spec.
func SkillNames(s *Spec) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range s.Skills {
		n := strings.SplitN(f.Path, "/", 2)[0]
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

type yamlFile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Kind        string `yaml:"kind"`
	Runtime     string `yaml:"runtime"`
	Model       string `yaml:"model"`
	Tools       Tools  `yaml:"tools"`
}

// ReadDir reads a profile directory.
func ReadDir(dir string) (string, *Spec, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "profile.yaml"))
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", dir, err)
	}
	var y yamlFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&y); err != nil {
		return "", nil, fmt.Errorf("profile.yaml: %w", err)
	}
	s := &Spec{Description: y.Description, Kind: y.Kind, Runtime: y.Runtime, Model: y.Model, Tools: y.Tools}
	if b, err := os.ReadFile(filepath.Join(dir, "PROMPT.md")); err == nil {
		s.Prompt = strings.TrimSpace(string(b))
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	skills := filepath.Join(dir, "skills")
	if st, err := os.Stat(skills); err == nil && st.IsDir() {
		err := filepath.WalkDir(skills, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() { // no symlinks, devices or sockets in a skill
				return fmt.Errorf("%s is not a regular file", p)
			}
			rel, _ := filepath.Rel(skills, p)
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s.Skills = append(s.Skills, File{Path: filepath.ToSlash(rel), Content: string(b)})
			return nil
		})
		if err != nil {
			return "", nil, err
		}
	}
	return y.Name, s, nil
}

// WriteDir writes a profile as a directory (the inverse of ReadDir).
func WriteDir(dir, name string, s *Spec) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	y, err := yaml.Marshal(yamlFile{Name: name, Description: s.Description, Kind: s.Kind, Runtime: s.Runtime, Model: s.Model, Tools: s.Tools})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), y, 0o644); err != nil {
		return err
	}
	if s.Prompt != "" {
		if err := os.WriteFile(filepath.Join(dir, "PROMPT.md"), []byte(s.Prompt+"\n"), 0o644); err != nil {
			return err
		}
	}
	for _, f := range s.Skills {
		p := filepath.Join(dir, "skills", filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
