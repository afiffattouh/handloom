package profile

import "testing"

func TestGeneratedAndAdapterPathsAreNotWork(t *testing.T) {
	for p, want := range map[string]bool{
		".factory/__pycache__/_toml.cpython-312.pyc": true, "a/b.pyc": true, "node_modules/x/index.js": true, "pkg/.pytest_cache/v": true,
		"about.html": false, "src/cache.py": false, "docs/__pycache__x/a.txt": false,
	} {
		if got := GeneratedPath(p); got != want {
			t.Errorf("GeneratedPath(%q) = %v, want %v", p, got, want)
		}
	}
	if AdapterPath("opencode.json") || AdapterPath(".opencode/plugins/handloom.js") {
		t.Error("OpenCode is retired: its files are ordinary files now")
	}
	// What add-ons save for a call lives under .handloom/results and is never a change of the task.
	for _, p := range []string{".handloom/results/web-fetch/c1/page.md", ".handloom/results", ".handloom/knowledge/x.md"} {
		if !AdapterPath(p) {
			t.Errorf("AdapterPath(%q) = false, want true", p)
		}
	}
	if AdapterPath("docs/.handloom/page.md") || AdapterPath(".handloomrc") {
		t.Error("only the top .handloom folder is the link's")
	}
}
