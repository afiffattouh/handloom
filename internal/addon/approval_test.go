package addon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApprovalStore(t *testing.T) {
	root := t.TempDir()
	s := NewStore(filepath.Join(root, "addons")) // the folder does not exist yet
	if _, ok, err := s.Get("echo"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	a := Approval{Name: "echo", Version: "0.1.0", PackageHash: "p1", ManifestHash: "m1", By: "afif", At: at}
	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("echo")
	if err != nil || !ok || got != a {
		t.Fatalf("Get = %+v %v %v", got, ok, err)
	}
	// a new instance reads the same file
	if got, ok, _ := NewStore(filepath.Join(root, "addons")).Get("echo"); !ok || got != a {
		t.Fatal("not persisted")
	}
	a2 := a
	a2.PackageHash = "p2"
	s.Put(a2)
	s.Put(Approval{Name: "alpha", PackageHash: "x"})
	list, _ := s.List()
	if len(list) != 2 || list[0].Name != "alpha" || list[1].PackageHash != "p2" {
		t.Fatalf("list = %+v", list)
	}
	if err := s.Delete("echo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("echo"); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
	if _, ok, _ := s.Get("echo"); ok {
		t.Fatal("still there")
	}
	if info, err := os.Stat(filepath.Join(root, "addons", ApprovalsFile)); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("file mode: %v %v", info, err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "addons"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".approvals-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestApprovalStoreDamagedFile(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ApprovalsFile), []byte("{not json"), 0o600)
	s := NewStore(root)
	if _, _, err := s.Get("a"); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("err = %v", err)
	}
	if err := s.Put(Approval{Name: "a"}); err == nil {
		t.Fatal("a damaged file was overwritten without a word")
	}
}

func TestApprovalStoreConcurrentWrites(t *testing.T) {
	s := NewStore(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a'+i%26)) + "x"
			if err := s.Put(Approval{Name: name, PackageHash: "h"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	list, err := s.List()
	if err != nil || len(list) != 20 {
		t.Fatalf("list = %d, %v", len(list), err)
	}
}
