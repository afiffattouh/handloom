package setting

import "testing"

func TestGetAndBool(t *testing.T) {
	t.Setenv("HANDLOOM_AGENT", "new")
	if Get("AGENT") != "new" {
		t.Fatal("Get")
	}
	if Get("NOPE") != "" {
		t.Fatal("unset setting is not empty")
	}
	t.Setenv("HANDLOOM_INSECURE", "yes")
	if !Bool("INSECURE") || Bool("MISSING") {
		t.Fatal("Bool")
	}
}
