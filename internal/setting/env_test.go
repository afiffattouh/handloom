package setting

import "testing"

func TestNewNameWinsAndOldNameStillWorks(t *testing.T) {
	t.Setenv("HANDLOOM_AGENT", "old")
	if Get("AGENT") != "old" {
		t.Fatal("the old name is ignored")
	}
	t.Setenv("HANDLOOM_AGENT", "new")
	if Get("AGENT") != "new" {
		t.Fatal("the new name does not win")
	}
	if Get("NOPE") != "" {
		t.Fatal("unset setting is not empty")
	}
	t.Setenv("HANDLOOM_INSECURE", "yes")
	if !Bool("INSECURE") || Bool("MISSING") {
		t.Fatal("Bool")
	}
	if got := Both("HOME", "/x"); got[0] != "HANDLOOM_HOME=/x" || got[1] != "HANDLOOM_HOME=/x" {
		t.Fatalf("Both: %v", got)
	}
}
