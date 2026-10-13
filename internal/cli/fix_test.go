package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func hasSet(names ...string) func(string) bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return func(b string) bool { return m[b] }
}

func TestDetectPackageManager(t *testing.T) {
	osr := func(id, like string) string {
		s := "NAME=\"x\"\nID=" + id + "\n"
		if like != "" {
			s += "ID_LIKE=\"" + like + "\"\n"
		}
		return s
	}
	for _, c := range []struct {
		name, goos, release string
		has                 []string
		want                string
	}{
		{"ubuntu", "linux", osr("ubuntu", "debian"), []string{"apt-get", "dnf"}, "apt-get"},
		{"debian", "linux", osr("debian", ""), []string{"apt-get"}, "apt-get"},
		{"mint by its parent", "linux", osr("linuxmint", "ubuntu debian"), []string{"apt-get"}, "apt-get"},
		{"fedora", "linux", osr("fedora", ""), []string{"dnf", "yum"}, "dnf"},
		{"old centos has only yum", "linux", osr("centos", "rhel fedora"), []string{"yum"}, "yum"},
		{"rocky", "linux", osr("rocky", "rhel centos fedora"), []string{"dnf", "yum"}, "dnf"},
		{"arch", "linux", osr("arch", ""), []string{"pacman"}, "pacman"},
		{"manjaro by its parent", "linux", osr("manjaro-custom", "arch"), []string{"pacman"}, "pacman"},
		{"alpine", "linux", osr("alpine", ""), []string{"apk"}, "apk"},
		{"opensuse", "linux", osr("opensuse-leap", "suse opensuse"), []string{"zypper"}, "zypper"},
		{"the file names one that is not installed: use what is there", "linux", osr("ubuntu", "debian"), []string{"dnf"}, "dnf"},
		{"no os-release: presence decides, apt first", "linux", "", []string{"zypper", "apt-get"}, "apt-get"},
		{"no os-release, only apk", "linux", "", []string{"apk"}, "apk"},
		{"nothing known", "linux", osr("plan9", ""), nil, ""},
		{"mac with brew", "darwin", "", []string{"brew", "apt-get"}, "brew"},
		{"mac without brew", "darwin", "", nil, ""},
		{"windows", "windows", "", []string{"apt-get"}, ""},
	} {
		if got := detectPackageManager(c.goos, c.release, hasSet(c.has...)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInstallCommands(t *testing.T) {
	both := []string{"git", "tmux"}
	join := func(cmds [][]string) string {
		var l []string
		for _, c := range cmds {
			l = append(l, strings.Join(c, " "))
		}
		return strings.Join(l, " ; ")
	}
	for _, c := range []struct {
		pm            string
		root, sudo    bool
		want          string
		wantNeedsRoot bool
	}{
		{"apt-get", false, true, "sudo apt-get update ; sudo apt-get install -y git tmux", false},
		{"apt-get", true, true, "apt-get update ; apt-get install -y git tmux", false},
		{"apt-get", true, false, "apt-get update ; apt-get install -y git tmux", false},
		{"apt-get", false, false, "apt-get update ; apt-get install -y git tmux", true},
		{"dnf", false, true, "sudo dnf install -y git tmux", false},
		{"yum", true, false, "yum install -y git tmux", false},
		{"pacman", false, true, "sudo pacman -S --noconfirm --needed git tmux", false},
		{"apk", true, false, "apk add git tmux", false},
		{"zypper", false, true, "sudo zypper --non-interactive install git tmux", false},
		{"brew", false, true, "brew install git tmux", false}, // Homebrew refuses to run as root, and never needs sudo
		{"brew", false, false, "brew install git tmux", false},
		{"", false, true, "", false},
		{"emerge", false, true, "", false},
	} {
		cmds, needs := installCommands(c.pm, both, c.root, c.sudo)
		if join(cmds) != c.want || needs != c.wantNeedsRoot {
			t.Errorf("%s root=%v sudo=%v: got %q needsRoot=%v, want %q %v", c.pm, c.root, c.sudo, join(cmds), needs, c.want, c.wantNeedsRoot)
		}
	}
	if cmds, _ := installCommands("apt-get", nil, true, true); cmds != nil {
		t.Error("nothing to install, nothing to run")
	}
	if cmds, _ := installCommands("apt-get", []string{"tmux"}, true, true); join(cmds) != "apt-get update ; apt-get install -y tmux" {
		t.Errorf("one tool: %s", join(cmds))
	}
}

type fixRig struct {
	f       *fixer
	out     *bytes.Buffer
	ran     [][]string
	asked   []string
	answer  bool
	runFail error
}

func newFixRig(goos string, euid int, interactive bool, has ...string) *fixRig {
	r := &fixRig{out: &bytes.Buffer{}}
	r.f = &fixer{
		out: r.out, goos: goos, euid: euid,
		osRelease:   func() string { return "ID=ubuntu\nID_LIKE=debian\n" },
		has:         hasSet(has...),
		interactive: interactive,
		run:         func(argv []string) error { r.ran = append(r.ran, argv); return r.runFail },
		confirm:     func(q string) bool { r.asked = append(r.asked, q); return r.answer },
	}
	return r
}

func TestFixToolsNeverRunsAnythingWhenNobodyCanBeAsked(t *testing.T) {
	r := newFixRig("linux", 1000, false, "apt-get", "sudo")
	r.answer = true
	if r.f.fixTools([]string{"git", "tmux"}) {
		t.Fatal("it claims to have installed")
	}
	if len(r.ran) != 0 || len(r.asked) != 0 {
		t.Fatalf("ran %v, asked %v", r.ran, r.asked)
	}
	for _, want := range []string{"sudo apt-get update", "sudo apt-get install -y git tmux", "Nothing was run"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, r.out)
		}
	}
}

func TestFixToolsAsksFirstAndUsesSudoOnlyOnYes(t *testing.T) {
	r := newFixRig("linux", 1000, true, "apt-get", "sudo")
	r.answer = false
	if r.f.fixTools([]string{"tmux"}) || len(r.ran) != 0 {
		t.Fatalf("a no must run nothing: %v", r.ran)
	}
	if len(r.asked) != 1 || !strings.Contains(r.asked[0], "sudo") {
		t.Fatalf("the question should mention sudo: %v", r.asked)
	}
	if !strings.Contains(r.out.String(), "nothing was changed") {
		t.Fatalf("says: %s", r.out)
	}
	r = newFixRig("linux", 1000, true, "apt-get", "sudo")
	r.answer = true
	if !r.f.fixTools([]string{"tmux"}) {
		t.Fatal("yes should install")
	}
	if len(r.ran) != 2 || strings.Join(r.ran[0], " ") != "sudo apt-get update" || strings.Join(r.ran[1], " ") != "sudo apt-get install -y tmux" {
		t.Fatalf("ran %v", r.ran)
	}
	// the commands are shown before the question
	if !strings.Contains(r.out.String(), "sudo apt-get install -y tmux") {
		t.Fatalf("the command was not shown:\n%s", r.out)
	}
}

func TestFixToolsAsRootNeedsNoSudo(t *testing.T) {
	r := newFixRig("linux", 0, true, "apt-get", "sudo")
	r.answer = true
	r.f.fixTools([]string{"git"})
	for _, c := range r.ran {
		if c[0] == "sudo" {
			t.Fatalf("root used sudo: %v", r.ran)
		}
	}
	if len(r.asked) != 1 || strings.Contains(r.asked[0], "sudo") {
		t.Fatalf("question: %v", r.asked)
	}
}

func TestFixToolsStopsAtTheFirstFailureAndSaysWhich(t *testing.T) {
	r := newFixRig("linux", 1000, true, "apt-get", "sudo")
	r.answer, r.runFail = true, errors.New("exit status 100")
	if r.f.fixTools([]string{"git"}) {
		t.Fatal("a failure is not an install")
	}
	if len(r.ran) != 1 || !strings.Contains(r.out.String(), "sudo apt-get update") || !strings.Contains(r.out.String(), "did not work") {
		t.Fatalf("ran %v, says:\n%s", r.ran, r.out)
	}
}

func TestFixToolsWithoutSudoOrRootAsksForRoot(t *testing.T) {
	r := newFixRig("linux", 1000, true, "apt-get")
	r.answer = true
	if r.f.fixTools([]string{"git"}) || len(r.ran) != 0 || len(r.asked) != 0 {
		t.Fatalf("ran %v asked %v", r.ran, r.asked)
	}
	if !strings.Contains(r.out.String(), "no sudo") || !strings.Contains(r.out.String(), "as root") {
		t.Fatalf("says: %s", r.out)
	}
}

func TestFixToolsOnAMac(t *testing.T) {
	r := newFixRig("darwin", 501, true, "brew")
	r.answer = true
	if !r.f.fixTools([]string{"tmux"}) || len(r.ran) != 1 || strings.Join(r.ran[0], " ") != "brew install tmux" {
		t.Fatalf("ran %v", r.ran)
	}
	if len(r.asked) != 1 || strings.Contains(r.asked[0], "sudo") {
		t.Fatalf("brew needs no sudo: %v", r.asked)
	}
	r = newFixRig("darwin", 501, true) // no Homebrew
	r.answer = true
	if r.f.fixTools([]string{"tmux"}) || len(r.ran) != 0 || !strings.Contains(r.out.String(), "https://brew.sh") {
		t.Fatalf("ran %v, says %s", r.ran, r.out)
	}
}

func TestFixToolsWithAnUnknownPackageManager(t *testing.T) {
	r := newFixRig("linux", 0, true)
	r.f.osRelease = func() string { return "" }
	if r.f.fixTools([]string{"git", "tmux"}) || len(r.ran) != 0 || !strings.Contains(r.out.String(), "git and tmux") {
		t.Fatalf("ran %v says %s", r.ran, r.out)
	}
}

func TestAgentHelpNamesTheOfficialLinesAndInstallsNothing(t *testing.T) {
	h := agentHelpText()
	for _, want := range []string{"curl -fsSL https://claude.ai/install.sh | bash", "run `claude` once and sign in", "npm install -g @openai/codex", "codex login", "does not install agent programs"} {
		if !strings.Contains(h, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}
