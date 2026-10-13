package client

import (
	"bytes"
	"strings"
	"testing"
)

// The credential helper (D106) gives the machine's device credential to the hub it joined and to nobody else.

const secret = "hvd_THIS-IS-A-SECRET-FOR-THE-TEST"

func helperHome(t *testing.T, hub string) {
	t.Helper()
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	if err := SaveLinkConfig(&LinkConfig{Hub: hub, Device: "dev", Credential: secret}); err != nil {
		t.Fatal(err)
	}
}

func ask(t *testing.T, op, request string) string {
	t.Helper()
	var out bytes.Buffer
	if err := GitCredential(op, strings.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestGitCredentialAnswersOnlyForTheHubItJoined(t *testing.T) {
	helperHome(t, "http://127.0.0.1:7420")
	want := "username=handloom\npassword=" + secret + "\n"
	if got := ask(t, "get", "protocol=http\nhost=127.0.0.1:7420\n\n"); got != want {
		t.Fatalf("for the hub: %q", got)
	}
	// With the path git sends when useHttpPath is on, and with a case difference in the host.
	if got := ask(t, "get", "protocol=http\nhost=127.0.0.1:7420\npath=git/demo.git\n\n"); got != want {
		t.Fatalf("with a path: %q", got)
	}
	// Nothing for any other host, port, scheme or a path outside /git/.
	for name, req := range map[string]string{
		"another host":         "protocol=http\nhost=github.com\n\n",
		"another port":         "protocol=http\nhost=127.0.0.1:7421\n\n",
		"no port":              "protocol=http\nhost=127.0.0.1\n\n",
		"another scheme":       "protocol=https\nhost=127.0.0.1:7420\n\n",
		"a lookalike host":     "protocol=http\nhost=127.0.0.1:7420.evil.test\n\n",
		"a host with a prefix": "protocol=http\nhost=evil.test/127.0.0.1:7420\n\n",
		"a user in the host":   "protocol=http\nhost=127.0.0.1:7420@evil.test\n\n",
		"a path outside git":   "protocol=http\nhost=127.0.0.1:7420\npath=v1/admin\n\n",
		"nothing":              "\n",
		"no protocol":          "host=127.0.0.1:7420\n\n",
		"no host":              "protocol=http\n\n",
		"empty":                "",
	} {
		if got := ask(t, "get", req); got != "" {
			t.Errorf("%s: answered %q", name, got)
		}
	}
	// store and erase remember and forget nothing, whatever they are told.
	for _, op := range []string{"store", "erase", "", "nonsense"} {
		if got := ask(t, op, "protocol=http\nhost=127.0.0.1:7420\nusername=handloom\npassword="+secret+"\n\n"); got != "" {
			t.Errorf("%q: answered %q", op, got)
		}
	}
}

func TestGitCredentialDefaultPortsAndNoLinkConfig(t *testing.T) {
	helperHome(t, "https://hub.example.test")
	want := "username=handloom\npassword=" + secret + "\n"
	for _, host := range []string{"hub.example.test", "hub.example.test:443", "HUB.Example.Test"} {
		if got := ask(t, "get", "protocol=https\nhost="+host+"\n\n"); got != want {
			t.Errorf("%s: %q", host, got)
		}
	}
	if got := ask(t, "get", "protocol=http\nhost=hub.example.test\n\n"); got != "" {
		t.Errorf("plain http to an https hub: %q", got)
	}
	// A machine that has not joined any hub: silence, not an error that could print anything.
	t.Setenv("HANDLOOM_HOME", t.TempDir())
	if got := ask(t, "get", "protocol=https\nhost=hub.example.test\n\n"); got != "" {
		t.Errorf("without a link config: %q", got)
	}
}

func TestGitCredentialNeverEchoesWhatItWasToldAndReadsALimitedRequest(t *testing.T) {
	helperHome(t, "http://127.0.0.1:7420")
	// A request that carries a password of its own is not repeated, and a huge request is cut off.
	got := ask(t, "get", "protocol=http\nhost=127.0.0.1:7420\npassword=other-secret\n\n")
	if strings.Contains(got, "other-secret") {
		t.Fatalf("the helper repeated what it was given: %q", got)
	}
	huge := "protocol=http\nhost=127.0.0.1:7420\n" + strings.Repeat("x=y\n", 100000) + "\n"
	ask(t, "get", huge)
}
