// Package selfupdate replaces the running handloom program with a published release. The hub never sends
// a program or a link to one: the computer itself asks the release host for the latest release, checks
// the download against that release's checksums.txt, and swaps the file only if they match.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "afiffattouh/handloom"

// maxProgram is the most a release program may expand to.
const maxProgram = 200 << 20

var releaseRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// IsRelease says whether a version string names a published release (v1.2.3), as opposed to a build from source.
func IsRelease(v string) bool { return releaseRe.MatchString(v) }

// Source is where releases come from. The zero value is GitHub.
type Source struct {
	// LatestURL answers a request with a redirect to .../tag/<version>; default the repository's latest release.
	LatestURL string
	// DownloadBase is the folder holding a release's files: <DownloadBase>/<version>/<file>.
	DownloadBase string
	Client       *http.Client
	GOOS, GOARCH string // default: this machine
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (s Source) platform() (string, string) {
	os, arch := s.GOOS, s.GOARCH
	if os == "" {
		os = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	return os, arch
}

// Latest is the newest full release.
func (s Source) Latest(ctx context.Context) (string, error) {
	u := s.LatestURL
	if u == "" {
		u = "https://github.com/" + Repo + "/releases/latest"
	}
	req, err := http.NewRequestWithContext(ctx, "HEAD", u, nil)
	if err != nil {
		return "", err
	}
	c := *s.client()
	c.Timeout = 30 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not look up the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if resp.StatusCode/100 != 3 || !IsRelease(tag) {
		return "", errors.New("there is no full release to update to yet")
	}
	return tag, nil
}

func (s Source) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", url, limit>>20)
	}
	return b, nil
}

// Check says why this program cannot replace itself, or "" when it can: it must be a release (never a
// build from source, which may be newer than the latest release), on Linux or macOS, in a folder this
// user may write to.
func Check(version, exe string) string {
	if !IsRelease(version) {
		return "this program was built from source (" + version + "); update it by building again"
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "updating is supported on Linux and macOS"
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "cannot find the program file: " + err.Error()
	}
	f, err := os.CreateTemp(filepath.Dir(real), ".handloom-check-*")
	if err != nil {
		return "the folder " + filepath.Dir(real) + " is not writable by this user, so the program cannot replace itself"
	}
	f.Close()
	os.Remove(f.Name())
	return ""
}

// Newer is true when b is a later release than a. Both are v1.2.3.
func Newer(a, b string) bool {
	var x, y [3]int
	if _, err := fmt.Sscanf(a, "v%d.%d.%d", &x[0], &x[1], &x[2]); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(b, "v%d.%d.%d", &y[0], &y[1], &y[2]); err != nil {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return y[i] > x[i]
		}
	}
	return false
}

// Apply downloads release tag, checks it, and swaps it in for the program at exe. Nothing changes unless
// every check passes. The new program is run once with `version` to see that it starts and is the release asked for.
func (s Source) Apply(ctx context.Context, exe, tag string) error {
	if !IsRelease(tag) {
		return fmt.Errorf("%q is not a release version", tag)
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	goos, goarch := s.platform()
	base := s.DownloadBase
	if base == "" {
		base = "https://github.com/" + Repo + "/releases/download"
	}
	base = strings.TrimSuffix(base, "/") + "/" + tag
	asset := fmt.Sprintf("handloom_%s_%s_%s.tar.gz", tag, goos, goarch)
	sums, err := s.get(ctx, base+"/checksums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("could not download the checksums: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in the release's checksums", asset)
	}
	archive, err := s.get(ctx, base+"/"+asset, 100<<20)
	if err != nil {
		return fmt.Errorf("could not download %s: %w", asset, err)
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return errors.New("the download does not match the release's checksum; nothing was changed")
	}
	tmp, err := os.CreateTemp(filepath.Dir(real), ".handloom-new-*")
	if err != nil {
		return fmt.Errorf("cannot write next to the program: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := extract(archive, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, tmp.Name(), "version").Output()
	if err != nil || !strings.Contains(string(out), tag) {
		return fmt.Errorf("the new program does not start as %s; nothing was changed", tag)
	}
	return os.Rename(tmp.Name(), real)
}

// extract writes the file named handloom from a tar.gz.
func extract(archive []byte, dst io.Writer) error {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("the download has no handloom program in it")
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "handloom" {
			continue
		}
		n, err := io.Copy(dst, io.LimitReader(tr, maxProgram+1))
		if err != nil {
			return err
		}
		if n > maxProgram {
			return errors.New("the program in the download is too large")
		}
		return nil
	}
}
