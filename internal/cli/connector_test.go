package cli

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/hub"
	"handloom/internal/store"
)

// The remote connector runs a tool's command in the hub's own process (D109). These tests use the real
// command line and a real hub handler, with no network and with the process environment pointing nowhere.
func TestOperatorRunRunsTheCommandLineAsTheTokensPerson(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin, _ := store.Init(db, time.Now())
	h := hub.New(db, hub.Options{})
	handler := h.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	var human api.TokenResp
	if err := client.Direct(srv.URL, admin).Post("/v1/admin/humans", api.NameReq{Name: "ola"}, &human); err != nil {
		t.Fatal(err)
	}
	var op api.TokenResp
	if err := client.Direct(srv.URL, human.Token).Post("/v1/me/operator-token", nil, &op); err != nil {
		t.Fatal(err)
	}
	// The environment of the hub's process must not matter: a bogus hub and token are set, and ignored.
	t.Setenv("HANDLOOM_HUB", "http://127.0.0.1:1")
	t.Setenv("HANDLOOM_TOKEN", "hvh_bogus")
	ctx := context.Background()

	out, errOut, code := operatorRun(ctx, handler, op.Token, []string{"digest"})
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("digest: %d %q %q", code, out, errOut)
	}
	out, errOut, code = operatorRun(ctx, handler, op.Token, []string{"job", "list"})
	if code != 0 {
		t.Fatalf("job list: %d %q %q", code, out, errOut)
	}
	// What an AI app may not do is refused by the hub, in words, through the same command line.
	_, errOut, code = operatorRun(ctx, handler, op.Token, []string{"task", "accept", "1"})
	if code == 0 || !strings.Contains(errOut, "for an AI app") {
		t.Fatalf("accept: %d %q", code, errOut)
	}
	_, errOut, code = operatorRun(ctx, handler, "hvo_wrong", []string{"digest"})
	if code == 0 || !strings.Contains(errOut, "invalid token") || strings.Contains(errOut, "hvo_wrong") {
		t.Fatalf("a bad token: %d %q", code, errOut)
	}
	// A cancelled request stops.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, code = operatorRun(cancelled, handler, op.Token, []string{"digest"}); code == 0 {
		t.Fatal("a cancelled tool call ran")
	}
	// Several at once, each as its own caller (this is why the token is not read from the environment).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e, c := operatorRun(ctx, handler, op.Token, []string{"digest"}); c != 0 {
				t.Errorf("parallel digest: %d %s", c, e)
			}
			if _, _, c := operatorRun(ctx, handler, "hvo_wrong", []string{"digest"}); c == 0 {
				t.Error("parallel digest with a bad token succeeded")
			}
		}()
	}
	wg.Wait()
}

func TestOAuthRedirectsSettingIsAList(t *testing.T) {
	got := oauthRedirects(" https://a.example/cb , ,https://b.example/cb,")
	if len(got) != 2 || got[0] != "https://a.example/cb" || got[1] != "https://b.example/cb" {
		t.Fatalf("%q", got)
	}
	if len(oauthRedirects("")) != 0 {
		t.Fatal("empty setting")
	}
}
