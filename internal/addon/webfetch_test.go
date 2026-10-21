package addon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var webFetchDir = filepath.Join("..", "..", "examples", "addons", "web-fetch")

const testPage = `<html><head><title>A  test page</title><style>p{color:red}</style><script>var x = "hidden";</script></head>
<body><h1>Heading</h1><p>First <b>paragraph</b> &amp; more.</p><ul><li>one</li><li>two</li></ul></body></html>`

// pageServer is a web server on this computer that stands in for the internet.
func pageServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, testPage)
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "just text\n")
	})
	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("x", 600<<10)))
	})
	mux.HandleFunc("/bigchunked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := []byte(strings.Repeat("y", 64<<10))
		for i := 0; i < 12; i++ {
			w.Write(chunk)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/hop/"), "%d", &n)
		if n == 0 {
			http.Redirect(w, r, "/page", http.StatusFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/tofile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "file:///etc/passwd")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestWebFetchSavesAPageWithAHeader(t *testing.T) {
	r := newRig(t)
	r.install(webFetchDir, nil)
	srv := pageServer(t)

	if h := r.sup.Health(context.Background(), "web-fetch"); h.Code != CodeOK {
		t.Fatalf("health = %+v", h)
	}
	results := filepath.Join(t.TempDir(), "results", "web-fetch", "c1")
	res := r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": srv.URL + "/page"}, Results: results})
	if res.Code != CodeOK || len(res.Files) != 1 || !strings.HasSuffix(res.Files[0], ".md") || !strings.Contains(res.Text, res.Files[0]) {
		t.Fatalf("fetch = %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(results, res.Files[0]))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(testPage))
	got := string(b)
	for _, want := range []string{"---\naddress: " + srv.URL + "/page\n", "\nfetched: 20", "Z\nsha256: " + hex.EncodeToString(sum[:]) + "\n---\n",
		"# A test page", "# Heading", "First paragraph & more.", "one\n\ntwo"} {
		if !strings.Contains(got, want) {
			t.Errorf("the saved file lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"hidden", "color:red", "<p>"} {
		if strings.Contains(got, bad) {
			t.Errorf("the saved file holds %q:\n%s", bad, got)
		}
	}
	if !strings.HasPrefix(res.Files[0], "127-0-0-1-page-"+hex.EncodeToString(sum[:])[:8]) {
		t.Errorf("file name %q", res.Files[0])
	}
	// Plain text is saved as it is.
	res = r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": srv.URL + "/plain"}, Results: results})
	if b, _ := os.ReadFile(filepath.Join(results, res.Files[0])); res.Code != CodeOK || !strings.HasSuffix(string(b), "---\n\njust text\n") {
		t.Fatalf("plain: %+v %q", res, b)
	}
	// The add-on wrote nothing into its own folder, whose bytes are approved.
	if ins, _ := Installed(r.root, r.store); len(ins) != 1 || ins[0].State != "ready" {
		t.Fatalf("after running, installed = %+v", ins)
	}
}

func TestWebFetchRefusesWhatIsNotAWebAddress(t *testing.T) {
	r := newRig(t)
	r.install(webFetchDir, nil)
	srv := pageServer(t)
	results := filepath.Join(t.TempDir(), "r")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("secret"), 0o600)

	for url, want := range map[string]string{
		secret:                                "only http",
		"file://" + secret:                    "only http",
		"ftp://example.invalid/x":             "only http",
		"gopher://example.invalid/":           "only http",
		"javascript:alert(1)":                 "only http",
		"//example.invalid/x":                 "only http",
		"http://":                             "no host",
		"":                                    "give the address",
		"   ":                                 "give the address",
		srv.URL + "/tofile":                   "not http://",
		srv.URL + "/gone":                     "404",
		"http://127.0.0.1:1/nothing-listens":  "did not answer",
		"https://127.0.0.1:1/nothing-listens": "did not answer",
	} {
		res := r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": url}, Results: results})
		if res.Code != CodeError || !strings.Contains(res.Error, want) {
			t.Errorf("%q: %+v, want an error with %q", url, res, want)
		}
	}
	res := r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": srv.URL + "/image"}, Results: results})
	if res.Code != CodeError || !strings.Contains(res.Error, "not text") {
		t.Errorf("an image: %+v", res)
	}
	if left, _ := os.ReadDir(results); len(left) != 0 {
		t.Errorf("files saved for refused addresses: %v", left)
	}
}

func TestWebFetchStopsAtTheSizeCapAndTheRedirectLimit(t *testing.T) {
	r := newRig(t)
	r.install(webFetchDir, nil)
	srv := pageServer(t)
	results := filepath.Join(t.TempDir(), "r")

	for _, path := range []string{"/big", "/bigchunked"} {
		res := r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": srv.URL + path}, Results: results})
		if res.Code != CodeError || !strings.Contains(res.Error, "bytes") {
			t.Errorf("%s: %+v", path, res)
		}
	}
	// Five hops are followed, six are not, and a loop ends too.
	if res := r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": srv.URL + "/hop/4"}, Results: results}); res.Code != CodeOK {
		t.Errorf("five redirects: %+v", res)
	}
	for _, path := range []string{"/hop/6", "/loop"} {
		res := r.call("web-fetch", Call{Tool: "fetch", Args: map[string]any{"url": srv.URL + path}, Results: results})
		if res.Code != CodeError || !strings.Contains(res.Error, "redirected more than 5 times") {
			t.Errorf("%s: %+v", path, res)
		}
	}
	if left, _ := os.ReadDir(results); len(left) != 1 {
		t.Errorf("files saved: %v", left)
	}
}
