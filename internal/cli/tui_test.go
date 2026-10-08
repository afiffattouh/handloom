package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTuiNeedsAHubAndAToken(t *testing.T) {
	for _, k := range []string{"HUB", "TOKEN"} {
		t.Setenv("HANDLOOM_"+k, "")
		t.Setenv("HANDLOOM_"+k, "")
	}
	var out, errb bytes.Buffer
	if code := Main([]string{"tui"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "HANDLOOM_HUB and HANDLOOM_TOKEN") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func TestBareHandloomOutsideATerminalStillPrintsUsage(t *testing.T) {
	t.Setenv("HANDLOOM_HUB", "http://127.0.0.1:1")
	t.Setenv("HANDLOOM_TOKEN", "hvh_x")
	var out, errb bytes.Buffer
	if code := Main(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "tui") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}
