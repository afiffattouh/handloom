package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func archive(t *testing.T, program string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "handloom", Mode: 0o755, Size: int64(len(program)), Typeflag: tar.TypeReg})
	tw.Write([]byte(program))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// release serves one release of a fake hosting site.
func release(t *testing.T, tag, program, sumOverride string) (Source, func()) {
	a := archive(t, program)
	sum := sha256.Sum256(a)
	hexSum := hex.EncodeToString(sum[:])
	if sumOverride != "" {
		hexSum = sumOverride
	}
	asset := fmt.Sprintf("handloom_%s_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/dl/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hexSum, asset)
	})
	mux.HandleFunc("/dl/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) { w.Write(a) })
	srv := httptest.NewServer(mux)
	return Source{LatestURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl"}, srv.Close
}

func program(t *testing.T, dir, text string) string {
	p := filepath.Join(dir, "handloom")
	if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLatestAndNewer(t *testing.T) {
	src, stop := release(t, "v1.2.4", "x", "")
	defer stop()
	tag, err := src.Latest(context.Background())
	if err != nil || tag != "v1.2.4" {
		t.Fatalf("latest = %q, %v", tag, err)
	}
	if !Newer("v1.2.3", "v1.2.4") || Newer("v1.2.4", "v1.2.4") || Newer("v1.10.0", "v1.9.9") || Newer("dev", "v1.0.0") {
		t.Fatal("Newer compares wrongly")
	}
}

func TestApplyReplacesTheProgram(t *testing.T) {
	src, stop := release(t, "v1.2.4", "#!/bin/sh\necho handloom v1.2.4 protocol handloom/1\n", "")
	defer stop()
	exe := program(t, t.TempDir(), "old")
	if err := src.Apply(context.Background(), exe, "v1.2.4"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	if !strings.Contains(string(b), "v1.2.4") {
		t.Fatalf("program not replaced: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".handloom-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestApplyRefusesAWrongChecksumAndWrongVersion(t *testing.T) {
	bad := strings.Repeat("0", 64)
	src, stop := release(t, "v1.2.4", "#!/bin/sh\necho handloom v1.2.4\n", bad)
	defer stop()
	exe := program(t, t.TempDir(), "old")
	if err := src.Apply(context.Background(), exe, "v1.2.4"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	// the archive is right but the program inside says another version
	src2, stop2 := release(t, "v1.2.4", "#!/bin/sh\necho handloom v9.9.9\n", "")
	defer stop2()
	if err := src2.Apply(context.Background(), exe, "v1.2.4"); err == nil {
		t.Fatal("a program that reports another version was installed")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("the program changed after a refusal: %q", b)
	}
	if err := src.Apply(context.Background(), exe, "latest"); err == nil {
		t.Fatal("a non-release version was accepted")
	}
}

func TestCheck(t *testing.T) {
	exe := program(t, t.TempDir(), "x")
	if Check("dev", exe) == "" || Check("v0.1.13-5-gabc", exe) == "" {
		t.Fatal("a build from source may not update itself")
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if got := Check("v1.2.3", exe); got != "" {
			t.Fatalf("a release in a writable folder should update: %s", got)
		}
		if os.Getuid() != 0 {
			dir := t.TempDir()
			exe := program(t, dir, "x")
			os.Chmod(dir, 0o500)
			defer os.Chmod(dir, 0o700)
			if Check("v1.2.3", exe) == "" {
				t.Fatal("a folder that cannot be written should be refused")
			}
		}
	}
}
