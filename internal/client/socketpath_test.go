package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathStaysInTheFolderWhenItFits(t *testing.T) {
	p, short := socketPathFor("/Users/ann/.config/handloom", "darwin", 501)
	if short || p != "/Users/ann/.config/handloom/link.sock" {
		t.Fatalf("%q %v", p, short)
	}
}

func TestSocketPathMovesWhenTooLongForTheSystem(t *testing.T) {
	// 103 characters is the longest that works on macOS; 107 on Linux.
	fit := func(n int) string { // a folder whose socket path is exactly n characters
		dir := "/" + strings.Repeat("a", n-len("/link.sock")-1)
		if len(filepath.Join(dir, "link.sock")) != n {
			t.Fatalf("test setup: %d", len(filepath.Join(dir, "link.sock")))
		}
		return dir
	}
	for _, c := range []struct {
		goos  string
		n     int
		short bool
	}{
		{"darwin", 103, false}, {"darwin", 104, true},
		{"linux", 107, false}, {"linux", 108, true},
	} {
		p, short := socketPathFor(fit(c.n), c.goos, 1000)
		if short != c.short {
			t.Errorf("%s %d: short=%v (%s)", c.goos, c.n, short, p)
		}
		if short && (len(p) > socketMax(c.goos) || !strings.HasPrefix(p, "/tmp/handloom-1000/") || !strings.HasSuffix(p, ".sock")) {
			t.Errorf("%s: bad short path %q", c.goos, p)
		}
	}
}

func TestShortSocketPathIsStableAndPerFolder(t *testing.T) {
	long1 := "/" + strings.Repeat("x", 120)
	long2 := "/" + strings.Repeat("y", 120)
	a1, _ := socketPathFor(long1, "darwin", 501)
	a2, _ := socketPathFor(long1+"/", "darwin", 501) // the same folder, written another way
	b, _ := socketPathFor(long2, "darwin", 501)
	other, _ := socketPathFor(long1, "darwin", 502)
	if a1 != a2 {
		t.Errorf("not stable: %s %s", a1, a2)
	}
	if a1 == b || a1 == other {
		t.Errorf("two links share a socket: %s %s %s", a1, b, other)
	}
}

func TestSocketPathNoteOnlyWhenMoved(t *testing.T) {
	t.Setenv("HANDLOOM_HOME", "/tmp/hl")
	if SocketPathNote() != "" {
		t.Fatal("a normal folder needs no note")
	}
	t.Setenv("HANDLOOM_HOME", "/"+strings.Repeat("z", 150))
	if n := SocketPathNote(); !strings.Contains(n, "too long") || !strings.Contains(n, SocketPath()) {
		t.Fatalf("note: %q", n)
	}
}
