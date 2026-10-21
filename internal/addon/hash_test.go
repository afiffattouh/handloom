package addon

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func hashOf(t *testing.T, dir string) string {
	t.Helper()
	h, err := PackageHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPackageHashStableAndSensitive(t *testing.T) {
	files := map[string]string{"addon.yaml": "a", "service.py": "b", "lib/util.py": "c"}
	base := hashOf(t, writeTree(t, files))
	if len(base) != 64 {
		t.Fatalf("hash %q is not a sha256", base)
	}
	if again := hashOf(t, writeTree(t, files)); again != base {
		t.Fatal("the same files in another folder gave another hash")
	}

	// creating the files in another order must not matter
	dir := t.TempDir()
	for _, p := range []string{"lib/util.py", "service.py", "addon.yaml"} {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(files[p]), 0o644)
	}
	if hashOf(t, dir) != base {
		t.Fatal("creation order changed the hash")
	}

	changed := func(name string, f map[string]string) {
		t.Helper()
		if hashOf(t, writeTree(t, f)) == base {
			t.Errorf("%s: the hash did not change", name)
		}
	}
	changed("content", map[string]string{"addon.yaml": "a", "service.py": "B", "lib/util.py": "c"})
	changed("rename", map[string]string{"addon.yaml": "a", "service2.py": "b", "lib/util.py": "c"})
	changed("move", map[string]string{"addon.yaml": "a", "service.py": "b", "util.py": "c"})
	changed("added", map[string]string{"addon.yaml": "a", "service.py": "b", "lib/util.py": "c", "extra": ""})
	changed("removed", map[string]string{"addon.yaml": "a", "service.py": "b"})
	// two files swapping their contents is a change even though the set of contents is the same
	changed("swap", map[string]string{"addon.yaml": "b", "service.py": "a", "lib/util.py": "c"})
}

func TestPackageHashRefusesLinksAndEmpty(t *testing.T) {
	dir := writeTree(t, map[string]string{"addon.yaml": "a"})
	if err := os.Symlink("addon.yaml", filepath.Join(dir, "alias")); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := PackageHash(dir); err == nil {
		t.Fatal("a link was accepted")
	}
	if _, err := PackageHash(t.TempDir()); err == nil {
		t.Fatal("an empty folder was accepted")
	}
}

func TestManifestHash(t *testing.T) {
	a, b := ManifestHash([]byte("x")), ManifestHash([]byte("x "))
	if a == b || len(a) != 64 {
		t.Fatal("manifest hash is not sensitive to the text")
	}
}
