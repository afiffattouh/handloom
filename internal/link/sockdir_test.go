//go:build unix

package link

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketFolderIsMadePrivateAndAForeignOrLinkedOneIsRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "handloom-1", "x")
	if err := prepareSocketDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if err := prepareSocketDir(dir); err != nil {
		t.Fatalf("an existing folder of ours is fine: %v", err)
	}
	// a symbolic link in the way (what someone else could plant in /tmp) is refused
	target := t.TempDir()
	link := filepath.Join(root, "planted")
	os.Symlink(target, link)
	if err := prepareSocketDir(link); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Fatalf("symlink: %v", err)
	}
}
