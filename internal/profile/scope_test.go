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
}
