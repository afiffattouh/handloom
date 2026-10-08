package cli

import (
	"strings"
	"testing"
)

func TestTheOwnersSettingsAreCommands(t *testing.T) {
	r := newRig(t)
	// prices
	if out := r.as(r.admin, "prices"); !strings.Contains(out, "No prices yet") {
		t.Fatalf("prices (none): %s", out)
	}
	r.as(r.admin, "prices", "set", "claude-sonnet", "--input", "3", "--output", "15", "--cache-read", "0.3")
	if out := r.as(r.admin, "prices"); !strings.Contains(out, "claude-sonnet") || !strings.Contains(out, "out 15") {
		t.Fatalf("prices: %s", out)
	}
	r.as(r.admin, "prices", "remove", "claude-sonnet")
	if out := r.as(r.admin, "prices"); !strings.Contains(out, "No prices yet") {
		t.Fatalf("after remove: %s", out)
	}
	if code, _, errs := r.exec("prices", "set", "x"); code == 0 || errs == "" {
		t.Fatal("a price with no numbers should be refused")
	}
	// people
	out := r.as(r.admin, "people", "add", "mia", "--role", "viewer")
	if !strings.Contains(out, "Added mia (viewer)") || !strings.Contains(out, "/invite/") {
		t.Fatalf("people add: %s", out)
	}
	r.as(r.admin, "people", "role", "mia", "--role", "member")
	if out := r.as(r.admin, "people"); !strings.Contains(out, "mia") || !strings.Contains(out, "member") || !strings.Contains(out, "has not set a password yet") {
		t.Fatalf("people: %s", out)
	}
	if out := r.as(r.admin, "people", "invite", "mia"); !strings.Contains(out, "New invite for mia") {
		t.Fatalf("people invite: %s", out)
	}
	// notifications
	r.as(r.admin, "notifications", "set", "--url", "https://ntfy.example.org", "--topic", "a-long-random-topic")
	if out := r.as(r.admin, "notifications"); !strings.Contains(out, "ntfy.example.org") || !strings.Contains(out, "a-long-random-topic") {
		t.Fatalf("notifications: %s", out)
	}
	// metrics and the personal token
	if out := r.as(r.admin, "metrics", "--range", "24h"); !strings.Contains(out, "Last 24h") || !strings.Contains(out, "usage") {
		t.Fatalf("metrics: %s", out)
	}
	out = r.as(r.human, "token", "new")
	if !strings.Contains(out, "New token for afif") || !strings.Contains(out, "hvh_") {
		t.Fatalf("token new: %s", out)
	}
	// the old token no longer works
	r.t.Setenv("HANDLOOM_HUB", r.hubURL)
	r.t.Setenv("HANDLOOM_TOKEN", r.human)
	if code, _, _ := r.exec("metrics"); code == 0 {
		t.Fatal("the replaced token still works")
	}
	r.t.Setenv("HANDLOOM_TOKEN", "")
}
