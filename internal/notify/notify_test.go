package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNtfyPostsTitleLinkAndBody(t *testing.T) {
	var gotPath, gotTitle, gotClick, gotAuth, gotBody, gotIcon string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotTitle, gotClick, gotAuth, gotBody, gotIcon = r.URL.Path, r.Header.Get("Title"), r.Header.Get("Click"), r.Header.Get("Authorization"), string(b), r.Header.Get("Icon")
	}))
	defer srv.Close()
	n, err := NewNtfy(srv.URL+"/", "my-topic", "tk_1")
	if err != nil {
		t.Fatal(err)
	}
	err = n.Notify(context.Background(), Notification{Kind: KindEscalation, Title: "Handloom", Text: "A question needs you.", Link: "https://h.example/inbox"})
	if err != nil {
		t.Fatal(err)
	}
	if gotIcon != "https://h.example/static/apple-touch-icon.png" {
		t.Fatalf("icon: %q", gotIcon)
	}
	if gotPath != "/my-topic" || gotTitle != "Handloom" || gotClick != "https://h.example/inbox" || gotAuth != "Bearer tk_1" || gotBody != "A question needs you." {
		t.Fatalf("path=%q title=%q click=%q auth=%q body=%q", gotPath, gotTitle, gotClick, gotAuth, gotBody)
	}
}

func TestNtfyRejectsBadSettingsAndErrors(t *testing.T) {
	for _, c := range [][2]string{{"ftp://x", "t"}, {"https://x", "bad topic"}, {"https://x", "../x"}, {"", "t"}} {
		if _, err := NewNtfy(c[0], c[1], ""); err == nil {
			t.Errorf("NewNtfy(%q, %q) accepted", c[0], c[1])
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	n, _ := NewNtfy(srv.URL, "t", "")
	if err := n.Notify(context.Background(), Notification{Text: "x"}); err == nil {
		t.Fatal("a 500 answer was not reported")
	}
}

func TestWebhookPostsJSON(t *testing.T) {
	var got Notification
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	w, err := NewWebhook(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Notify(context.Background(), Notification{Kind: KindEscalation, Text: "hi", Link: "l"}); err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindEscalation || got.Text != "hi" || got.Link != "l" {
		t.Fatalf("got %+v", got)
	}
}
