// Package installscripts holds checks on the install scripts at the repository root. install.sh is
// run for real by test/e2e/install_nosystemd.sh; install.ps1 cannot be run here (no Windows), so these
// tests only look at it: they catch a broken file or an unsafe pattern, not a wrong behaviour.
package installscripts

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

func TestInstallPs1Lint(t *testing.T) {
	s := read(t, "install.ps1")
	// PowerShell 5.1 reads a file without a byte order mark as the local code page: only ASCII is safe.
	for i, r := range s {
		if r > 127 {
			t.Fatalf("install.ps1 has a non-ASCII character at byte %d (%q): Windows PowerShell 5.1 would misread it", i, r)
		}
	}
	for _, want := range []string{
		"[CmdletBinding()]", "[string]$Hub", "[string]$Token", "[string]$Distro", "[switch]$SkipTask", "[switch]$Help",
		"raw.githubusercontent.com/afiffattouh/handloom/main/install.sh",
		"wsl.exe", "--status", "--install", "-Verb RunAs", "19041",
		"New-ScheduledTaskAction", "New-ScheduledTaskTrigger -AtLogOn", "Register-ScheduledTask", "Start-ScheduledTask", "-WindowStyle Hidden",
		"link start --wait", "handloom doctor",
		"^hvj_", "^https?://", // the hub and token are checked before they go into a Linux command line
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install.ps1 lacks %q", want)
		}
	}
	// Nothing downloaded may be executed unchecked. The only download that runs is install.sh,
	// inside Linux, and it verifies its own download against checksums.txt (and the signature).
	code := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(s, "")
	for _, bad := range []string{"Invoke-Expression", "iex ", "iex(", "|iex", "| iex", "-ExecutionPolicy", "Set-ExecutionPolicy", "DownloadString", "Invoke-WebRequest", "Invoke-RestMethod", "irm ", "Start-BitsTransfer", "curl.exe"} {
		if strings.Contains(strings.ToLower(code), strings.ToLower(bad)) && bad != "irm " {
			t.Errorf("install.ps1 uses %q", bad)
		}
	}
	// the one `irm` is in the help text that prints the paste line, never executed here
	if n := strings.Count(strings.ToLower(code), "irm "); n != 1 || !strings.Contains(code, "'  & ([scriptblock]::Create((irm '") {
		t.Errorf("install.ps1 must not call irm itself (found %d mentions)", n)
	}
	// the Linux side must be install.sh with the join arguments, so install.sh's checksum logic runs
	if !strings.Contains(s, "| sh -s -- join '$Hub' '$Token'") {
		t.Error("install.ps1 does not run install.sh join inside WSL")
	}
	if strings.Contains(s, "sudo") {
		t.Error("install.ps1 must not use sudo")
	}
	// cheap syntax check: braces, parentheses and brackets balance outside strings and comments
	for _, p := range [][2]string{{"{", "}"}, {"(", ")"}, {"[", "]"}} {
		if a, b := strings.Count(stripStrings(code), p[0]), strings.Count(stripStrings(code), p[1]); a != b {
			t.Errorf("install.ps1: %d %q but %d %q", a, p[0], b, p[1])
		}
	}
	if !strings.Contains(s, "try {\n    Install-Handloom\n}") {
		t.Error("the main call must be inside try/catch (an `exit` or an error would close the person's window)")
	}
	if regexp.MustCompile(`(?m)^\s*exit\b`).MatchString(code) {
		t.Error("install.ps1 must not call exit: pasted as a script block, it would close the PowerShell window")
	}
}

// stripStrings removes single- and double-quoted strings so their brackets do not count.
func stripStrings(s string) string {
	var b strings.Builder
	var q rune
	for _, r := range s {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '\'' || r == '"':
			q = r
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestInstallShHasHelpAndPortableShell(t *testing.T) {
	s := read(t, "install.sh")
	if out, err := exec.Command("sh", "-n", "../../install.sh").CombinedOutput(); err != nil {
		t.Fatalf("install.sh has a syntax error: %v %s", err, out)
	}
	out, err := exec.Command("sh", "../../install.sh", "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "install.sh join <hub-url> <token>") || !strings.Contains(string(out), "install.ps1") {
		t.Fatalf("--help: %v\n%s", err, out)
	}
	for _, bad := range []string{"readlink -f", "sed -i", "grep -P", "date -d", "stat -c", "xargs -r", "sudo ", "realpath", "mktemp --", "tar --", "echo -e", "[[ "} {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, bad) {
				t.Errorf("install.sh uses %q (not portable to macOS's tools): %s", bad, line)
			}
		}
	}
	// the safety logic stays: checksum compared, signature checked when cosign is there, no download kept on a mismatch
	for _, want := range []string{"sha256sum", "shasum -a 256", "checksums.txt", "cosign verify-blob", "Nothing was installed.", `[ "$got" = "$want" ]`, "arm64", "mktemp -d"} {
		if !strings.Contains(s, want) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	// a pipe from curl has no keyboard: join hands the terminal to the program when there is one
	if !strings.Contains(s, "</dev/tty") {
		t.Error("install.sh join does not reattach to the terminal")
	}
}
