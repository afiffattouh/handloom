package link

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/selfupdate"
)

// releaseHost serves one fake release, "v0.2.0", whose program says so.
func releaseHost(t *testing.T) selfupdate.Source {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prog := "#!/bin/sh\necho handloom v0.2.0 protocol handloom/1\n"
	tw.WriteHeader(&tar.Header{Name: "handloom", Mode: 0o755, Size: int64(len(prog)), Typeflag: tar.TypeReg})
	tw.Write([]byte(prog))
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	asset := fmt.Sprintf("handloom_v0.2.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/tag/v0.2.0", http.StatusFound) })
	mux.HandleFunc("/dl/v0.2.0/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	mux.HandleFunc("/dl/v0.2.0/"+asset, func(w http.ResponseWriter, r *http.Request) { w.Write(buf.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return selfupdate.Source{LatestURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl"}
}

func (f *fixture) updateRequest() error { return f.human.Post("/v1/devices/dev/update", nil, nil) }

func (f *fixture) audited(action string) int {
	f.t.Helper()
	var rows []api.AuditRow
	f.must(f.admin.Get("/v1/audit?limit=200", &rows))
	n := 0
	for _, r := range rows {
		if r.Action == action {
			n++
		}
	}
	return n
}

func TestTheLinkUpdatesItselfWhenAPersonAsks(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("updating is for Linux and macOS")
	}
	f := newFixture(t)
	exe := filepath.Join(t.TempDir(), "handloom")
	os.WriteFile(exe, []byte("old program"), 0o755)
	restarted := 0
	f.link.opt.Version, f.link.opt.Exe, f.link.opt.Source = "v0.1.0", exe, releaseHost(t)
	f.link.opt.Restart = func(string) error { restarted++; return nil }

	// nothing to update to until the link has told the hub, and nothing happens without a request
	if err := f.updateRequest(); err == nil || !strings.Contains(err.Error(), "has not reported") {
		t.Fatalf("before any report: %v", err)
	}
	f.ladder()
	if restarted != 0 {
		t.Fatal("the link updated without being asked")
	}
	f.must(f.updateRequest())
	if err := f.updateRequest(); err == nil || !strings.Contains(err.Error(), "already under way") {
		t.Fatalf("a second request: %v", err)
	}
	// an agent may not ask
	if err := f.worker.Post("/v1/devices/dev/update", nil, nil); err == nil {
		t.Fatal("an agent asked for an update")
	}

	f.state(f.worker, api.StateWorking)
	f.ladder()
	if restarted != 0 {
		t.Fatal("the link updated while a helper was mid-turn")
	}
	f.state(f.worker, api.StateIdle)
	f.ladder()
	if restarted != 1 {
		t.Fatalf("restarted %d times", restarted)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "v0.2.0") {
		t.Fatalf("program not replaced: %q", b)
	}

	// the new link starts as v0.2.0, finds the note and reports how it went
	f.link.opt.Version = "v0.2.0"
	f.ladder()
	if f.audited("device.update.request") != 1 || f.audited("device.update.done") != 1 {
		t.Fatal("the request and its result are not both in the audit log")
	}
	if _, err := os.Stat(filepath.Join(client.Home(), updateMarker)); !os.IsNotExist(err) {
		t.Fatal("the note for the next start was not removed")
	}
	f.ladder()
	if restarted != 1 {
		t.Fatal("a finished request ran again")
	}
}

func TestABuildFromSourceNeverUpdatesItself(t *testing.T) {
	f := newFixture(t)
	f.link.opt.Version, f.link.opt.Source = "dev", releaseHost(t)
	f.link.opt.Restart = func(string) error { t.Fatal("restarted"); return nil }
	f.ladder()
	err := f.updateRequest()
	var ce *client.Error
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "built from source") {
		t.Fatalf("request for a source build: %v", err)
	}
}
