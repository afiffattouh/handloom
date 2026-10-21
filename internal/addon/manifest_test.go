package addon

import (
	"strings"
	"testing"
	"time"
)

const goodManifest = `schema: 1
kind: tool
name: desk-notes
version: 1.2.3
api: 1
title: Notes
summary: Keeps notes.
run: ["python3", "service.py"]
hosts: ["example.org"]
secrets: []
tools:
  - name: save
    summary: Save a note.
    input:
      text: {type: string, required: true, summary: The note}
      count: {type: number}
      loud: {type: boolean}
    leaves_machine: false
    timeout: 90s
    max_output: 2MB
`

func TestParseGood(t *testing.T) {
	m, err := Parse([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := m.Tool("save")
	if !ok || tool.Timeout != 90*time.Second || tool.MaxOutput != 2<<20 || tool.LeavesMachine {
		t.Fatalf("tool = %+v", tool)
	}
	if m.Name != "desk-notes" || len(m.Run) != 2 || len(tool.Input) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestParseDefaults(t *testing.T) {
	m, err := Parse([]byte(strings.Replace(goodManifest, "    leaves_machine: false\n    timeout: 90s\n    max_output: 2MB\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := m.Tool("save")
	if !tool.LeavesMachine || tool.Timeout != DefaultTimeout || tool.MaxOutput != DefaultMaxOutput {
		t.Fatalf("defaults wrong: %+v", tool)
	}
}

func TestParseRefusals(t *testing.T) {
	sub := func(old, new string) string {
		if !strings.Contains(goodManifest, old) {
			t.Fatalf("test bug: %q not in the manifest", old)
		}
		return strings.Replace(goodManifest, old, new, 1)
	}
	cases := []struct {
		name, text, want string
	}{
		{"empty", "", "empty"},
		{"not yaml", "schema: [", "addon.yaml:"},
		{"schema 2", sub("schema: 1", "schema: 2"), "schema must be 1"},
		{"no schema", sub("schema: 1\n", ""), "schema must be 1"},
		{"kit", sub("kind: tool", "kind: kit"), "kits are not installable yet; the factory kit is built in"},
		{"no kind", sub("kind: tool\n", ""), "kind is missing"},
		{"other kind", sub("kind: tool", "kind: theme"), "not known"},
		{"capital name", sub("name: desk-notes", "name: Desk"), "name"},
		{"digit name", sub("name: desk-notes", "name: 1desk"), "name"},
		{"long name", sub("name: desk-notes", "name: "+strings.Repeat("a", 33)), "name"},
		{"underscore name", sub("name: desk-notes", "name: desk_notes"), "name"},
		{"version", sub("version: 1.2.3", "version: one"), "version"},
		{"api 2", sub("api: 1", "api: 2"), "API 2"},
		{"secrets", sub("secrets: []", "secrets: [KEY]"), "secrets must be empty"},
		{"no run", sub(`run: ["python3", "service.py"]`, "run: []"), "run must list"},
		{"blank run", sub(`run: ["python3", "service.py"]`, `run: [" "]`), "run must list"},
		{"blank host", sub(`hosts: ["example.org"]`, `hosts: [""]`), "empty entry"},
		{"unknown field", sub("title: Notes", "title: Notes\nlicense: MIT"), "license"},
		{"unknown tool field", sub("    timeout: 90s", "    timeout: 90s\n    retries: 3"), "retries"},
		{"two documents", goodManifest + "---\nschema: 1\n", "more than one"},
		{"no tools", goodManifest[:strings.Index(goodManifest, "tools:")] + "tools: []\n", "at least one tool"},
		{"tool name", sub("name: save", "name: Save"), "tool name"},
		{"reserved tool name", sub("name: save", "name: _health"), "tool name"},
		{"duplicate tool", goodManifest + "  - name: save\n    summary: again\n", "listed twice"},
		{"nested input", sub("count: {type: number}", "count: {type: object}"), "types are string, number and boolean"},
		{"no input type", sub("count: {type: number}", "count: {required: true}"), "type"},
		{"input name", sub("loud:", "Loud:"), "input name"},
		{"timeout text", sub("timeout: 90s", "timeout: soon"), "timeout"},
		{"timeout zero", sub("timeout: 90s", "timeout: 0s"), "timeout"},
		{"timeout over", sub("timeout: 90s", "timeout: 301s"), "5m limit"},
		{"size text", sub("max_output: 2MB", "max_output: lots"), "max_output"},
		{"size over", sub("max_output: 2MB", "max_output: 51MB"), "50MB limit"},
		{"size zero", sub("max_output: 2MB", "max_output: 0KB"), "max_output"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.text))
			if err == nil {
				t.Fatalf("accepted:\n%s", c.text)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestParseLimitsAreInclusive(t *testing.T) {
	text := strings.Replace(strings.Replace(goodManifest, "timeout: 90s", "timeout: 5m", 1), "max_output: 2MB", "max_output: 50MB", 1)
	m, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if tool, _ := m.Tool("save"); tool.Timeout != MaxTimeout || tool.MaxOutput != MaxMaxOutput {
		t.Fatalf("limits not kept: %+v", tool)
	}
}

func TestCheckArgs(t *testing.T) {
	m, _ := Parse([]byte(goodManifest))
	tool, _ := m.Tool("save")
	good := []map[string]any{
		{"text": "a"},
		{"text": "a", "count": float64(2), "loud": true},
	}
	for _, a := range good {
		if err := tool.CheckArgs(a); err != nil {
			t.Errorf("%v refused: %v", a, err)
		}
	}
	bad := []map[string]any{
		nil,
		{},
		{"text": 5},
		{"text": "a", "count": "x"},
		{"text": "a", "loud": "yes"},
		{"text": "a", "extra": 1},
	}
	for _, a := range bad {
		if err := tool.CheckArgs(a); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
}

// A manifest the hub would not take is refused here, so an add-on that installs can always be granted.
func TestParseRefusesAManifestTheHubWouldNotTake(t *testing.T) {
	pad := "\n# " + strings.Repeat("x", MaxManifestBytes)
	if _, err := Parse([]byte(goodManifest + pad)); err == nil || !strings.Contains(err.Error(), "64 KB") {
		t.Fatalf("a long manifest: %v", err)
	}
	if _, err := Parse([]byte(goodManifest + "\n# " + strings.Repeat("x", 1000))); err != nil {
		t.Fatalf("a manifest under the limit: %v", err)
	}
}
