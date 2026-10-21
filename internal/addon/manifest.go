// Package addon is the add-on host's machinery on a computer: reading and checking an add-on's
// addon.yaml, hashing its folder, remembering what the owner approved, and running its service.
// See docs/direction/addons.md.
package addon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// APIVersion is the add-on API this program speaks. A manifest that names another is refused.
const APIVersion = 1

const (
	// DefaultTimeout and DefaultMaxOutput apply when a tool does not say.
	DefaultTimeout   = 30 * time.Second
	MaxTimeout       = 5 * time.Minute
	DefaultMaxOutput = 1 << 20
	MaxMaxOutput     = 50 << 20
)

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	toolRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	fieldRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
	sizeRe    = regexp.MustCompile(`^([0-9]+)\s*(B|KB|MB)$`)
)

// Manifest is a parsed, checked addon.yaml.
type Manifest struct {
	Schema  int    `yaml:"schema"`
	Kind    string `yaml:"kind"`
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	API     int    `yaml:"api"`
	Title   string `yaml:"title"`
	Summary string `yaml:"summary"`
	// Run is the command that starts the service, inside the add-on's folder. There is no shell.
	Run     []string `yaml:"run"`
	Hosts   []string `yaml:"hosts"`
	Secrets []string `yaml:"secrets"`
	Tools   []Tool   `yaml:"tools"`
}

// Tool is one thing an agent can call.
type Tool struct {
	Name    string `yaml:"name"`
	Summary string `yaml:"summary"`
	// Input is a flat object: argument names to their type.
	Input map[string]Field `yaml:"input"`
	// LeavesMachine is what the add-on says. The owner's value is the one used when granting.
	LeavesMachine bool          `yaml:"-"`
	Timeout       time.Duration `yaml:"-"`
	MaxOutput     int64         `yaml:"-"`
}

// Field is one argument of a tool.
type Field struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
	Summary  string `yaml:"summary"`
}

// rawTool is the file's spelling of a tool, which has a duration and a size as text.
type rawTool struct {
	Name          string           `yaml:"name"`
	Summary       string           `yaml:"summary"`
	Input         map[string]Field `yaml:"input"`
	LeavesMachine *bool            `yaml:"leaves_machine"`
	Timeout       string           `yaml:"timeout"`
	MaxOutput     string           `yaml:"max_output"`
}

type rawManifest struct {
	Schema  int       `yaml:"schema"`
	Kind    string    `yaml:"kind"`
	Name    string    `yaml:"name"`
	Version string    `yaml:"version"`
	API     int       `yaml:"api"`
	Title   string    `yaml:"title"`
	Summary string    `yaml:"summary"`
	Run     []string  `yaml:"run"`
	Hosts   []string  `yaml:"hosts"`
	Secrets []string  `yaml:"secrets"`
	Tools   []rawTool `yaml:"tools"`
}

// Parse reads addon.yaml text and checks it. The error says in plain words what to change.
func Parse(data []byte) (*Manifest, error) {
	var raw rawManifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("addon.yaml is empty")
		}
		return nil, fmt.Errorf("addon.yaml: %s", cleanYAMLError(err))
	}
	var extra rawManifest
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("addon.yaml holds more than one document")
	}
	if raw.Schema != 1 {
		return nil, fmt.Errorf("addon.yaml: schema must be 1 (this program reads schema 1), not %d", raw.Schema)
	}
	switch raw.Kind {
	case "tool":
	case "kit":
		return nil, errors.New("kits are not installable yet; the factory kit is built in")
	case "":
		return nil, errors.New("addon.yaml: kind is missing (it must be tool)")
	default:
		return nil, fmt.Errorf("addon.yaml: kind %q is not known (it must be tool)", raw.Kind)
	}
	if !nameRe.MatchString(raw.Name) {
		return nil, fmt.Errorf("addon.yaml: name %q must be lower-case letters, digits and dashes, starting with a letter, at most 32 characters", raw.Name)
	}
	if !versionRe.MatchString(raw.Version) {
		return nil, fmt.Errorf("addon.yaml: version %q must look like 1.2.3", raw.Version)
	}
	if raw.API != APIVersion {
		return nil, fmt.Errorf("addon.yaml: this add-on was written for add-on API %d; this program speaks API %d", raw.API, APIVersion)
	}
	if len(raw.Secrets) > 0 {
		return nil, errors.New("addon.yaml: secrets must be empty in this version. An add-on that keeps a key cannot be kept apart from the agents on the same computer yet, so it is refused rather than made to look safe")
	}
	if len(raw.Run) == 0 || strings.TrimSpace(raw.Run[0]) == "" {
		return nil, errors.New("addon.yaml: run must list the command that starts the add-on, for example [\"python3\", \"service.py\"]")
	}
	for _, h := range raw.Hosts {
		if strings.TrimSpace(h) == "" {
			return nil, errors.New("addon.yaml: hosts holds an empty entry")
		}
	}
	if len(raw.Tools) == 0 {
		return nil, errors.New("addon.yaml: tools is empty; an add-on needs at least one tool")
	}
	m := &Manifest{
		Schema: raw.Schema, Kind: raw.Kind, Name: raw.Name, Version: raw.Version, API: raw.API,
		Title: raw.Title, Summary: raw.Summary, Run: raw.Run, Hosts: raw.Hosts, Secrets: raw.Secrets,
	}
	seen := map[string]bool{}
	for _, rt := range raw.Tools {
		t, err := checkTool(rt)
		if err != nil {
			return nil, err
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("addon.yaml: the tool %q is listed twice", t.Name)
		}
		seen[t.Name] = true
		m.Tools = append(m.Tools, t)
	}
	return m, nil
}

func checkTool(rt rawTool) (Tool, error) {
	if !toolRe.MatchString(rt.Name) || strings.HasPrefix(rt.Name, "_") {
		return Tool{}, fmt.Errorf("addon.yaml: tool name %q must be lower-case letters, digits, dashes and underscores, starting with a letter", rt.Name)
	}
	t := Tool{Name: rt.Name, Summary: rt.Summary, Input: rt.Input, LeavesMachine: true, Timeout: DefaultTimeout, MaxOutput: DefaultMaxOutput}
	// When the add-on does not say, the cautious value stands: the tool leaves the computer.
	if rt.LeavesMachine != nil {
		t.LeavesMachine = *rt.LeavesMachine
	}
	for name, f := range rt.Input {
		if !fieldRe.MatchString(name) {
			return Tool{}, fmt.Errorf("addon.yaml: tool %s: input name %q must be lower-case letters, digits and underscores", t.Name, name)
		}
		switch f.Type {
		case "string", "number", "boolean":
		default:
			return Tool{}, fmt.Errorf("addon.yaml: tool %s: input %q has type %q; the types are string, number and boolean (inputs are flat)", t.Name, name, f.Type)
		}
	}
	if rt.Timeout != "" {
		d, err := time.ParseDuration(rt.Timeout)
		if err != nil || d <= 0 {
			return Tool{}, fmt.Errorf("addon.yaml: tool %s: timeout %q must be a time such as 30s", t.Name, rt.Timeout)
		}
		if d > MaxTimeout {
			return Tool{}, fmt.Errorf("addon.yaml: tool %s: timeout %s is more than the 5m limit", t.Name, rt.Timeout)
		}
		t.Timeout = d
	}
	if rt.MaxOutput != "" {
		n, err := parseSize(rt.MaxOutput)
		if err != nil || n <= 0 {
			return Tool{}, fmt.Errorf("addon.yaml: tool %s: max_output %q must be a size such as 1MB", t.Name, rt.MaxOutput)
		}
		if n > MaxMaxOutput {
			return Tool{}, fmt.Errorf("addon.yaml: tool %s: max_output %s is more than the 50MB limit", t.Name, rt.MaxOutput)
		}
		t.MaxOutput = n
	}
	return t, nil
}

func parseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, errors.New("bad size")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	switch m[2] {
	case "KB":
		n <<= 10
	case "MB":
		n <<= 20
	}
	return n, nil
}

// cleanYAMLError drops the library's "yaml: " prefix and "unmarshal errors" wrapper.
func cleanYAMLError(err error) string {
	s := strings.ReplaceAll(err.Error(), "yaml: unmarshal errors:\n  ", "")
	s = strings.ReplaceAll(s, "yaml: ", "")
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", "; ")), " ")
}

// Tool returns the named tool.
func (m *Manifest) Tool(name string) (Tool, bool) {
	for _, t := range m.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// CheckArgs says what is wrong with a call's arguments, or nil: a required one missing, one the tool
// does not take, or one of the wrong type. Numbers arrive from JSON as float64.
func (t Tool) CheckArgs(args map[string]any) error {
	names := make([]string, 0, len(t.Input))
	for n := range t.Input {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := t.Input[n]
		v, ok := args[n]
		if !ok {
			if f.Required {
				return fmt.Errorf("the tool %s needs %q", t.Name, n)
			}
			continue
		}
		okType := false
		switch f.Type {
		case "string":
			_, okType = v.(string)
		case "number":
			switch v.(type) {
			case float64, int, int64:
				okType = true
			}
		case "boolean":
			_, okType = v.(bool)
		}
		if !okType {
			return fmt.Errorf("%q for the tool %s must be a %s", n, t.Name, f.Type)
		}
	}
	for n := range args {
		if _, ok := t.Input[n]; !ok {
			return fmt.Errorf("the tool %s does not take %q", t.Name, n)
		}
	}
	return nil
}
