package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/mattn/go-isatty"
)

// Fixing what doctor finds. Handloom installs only the two plain tools it needs (git and tmux), with
// the machine's own package manager, after showing the command and after the person says yes. It
// never installs an agent CLI: it says where to get one and how to sign in.

// detectPackageManager names the machine's package manager: from the system's own description
// (/etc/os-release) when it names one that is installed, else by which one is installed.
func detectPackageManager(goos, osRelease string, has func(string) bool) string {
	if goos == "darwin" {
		if has("brew") {
			return "brew"
		}
		return ""
	}
	if goos != "linux" {
		return ""
	}
	ids := osReleaseIDs(osRelease)
	byID := map[string][]string{
		"debian": {"apt-get"}, "ubuntu": {"apt-get"}, "linuxmint": {"apt-get"}, "pop": {"apt-get"}, "raspbian": {"apt-get"}, "kali": {"apt-get"},
		"fedora": {"dnf"}, "rhel": {"dnf", "yum"}, "centos": {"dnf", "yum"}, "rocky": {"dnf", "yum"}, "almalinux": {"dnf", "yum"}, "amzn": {"dnf", "yum"}, "ol": {"dnf", "yum"},
		"arch": {"pacman"}, "manjaro": {"pacman"}, "endeavouros": {"pacman"},
		"alpine":   {"apk"},
		"opensuse": {"zypper"}, "opensuse-leap": {"zypper"}, "opensuse-tumbleweed": {"zypper"}, "sles": {"zypper"}, "suse": {"zypper"},
	}
	for _, id := range ids {
		for _, pm := range byID[id] {
			if has(pm) {
				return pm
			}
		}
	}
	for _, pm := range []string{"apt-get", "dnf", "yum", "pacman", "apk", "zypper", "brew"} {
		if has(pm) {
			return pm
		}
	}
	return ""
}

// osReleaseIDs reads ID and ID_LIKE from the text of /etc/os-release, in that order.
func osReleaseIDs(text string) []string {
	var id, like string
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.ToLower(strings.Trim(v, `"'`))
		switch k {
		case "ID":
			id = v
		case "ID_LIKE":
			like = v
		}
	}
	out := []string{}
	if id != "" {
		out = append(out, id)
	}
	return append(out, strings.Fields(like)...)
}

// installCommands is the commands that install pkgs with a package manager, one argument list each.
// Administrator rights are needed everywhere but Homebrew: sudo is put in front unless we are root
// already; when we are not and there is no sudo, no prefix is added and needsRoot says so.
func installCommands(manager string, pkgs []string, root, haveSudo bool) (cmds [][]string, needsRoot bool) {
	if manager == "" || len(pkgs) == 0 {
		return nil, false
	}
	var base [][]string
	switch manager {
	case "apt-get":
		base = [][]string{{"apt-get", "update"}, append([]string{"apt-get", "install", "-y"}, pkgs...)}
	case "dnf", "yum":
		base = [][]string{append([]string{manager, "install", "-y"}, pkgs...)}
	case "pacman":
		base = [][]string{append([]string{"pacman", "-S", "--noconfirm", "--needed"}, pkgs...)}
	case "apk":
		base = [][]string{append([]string{"apk", "add"}, pkgs...)}
	case "zypper":
		base = [][]string{append([]string{"zypper", "--non-interactive", "install"}, pkgs...)}
	case "brew":
		return [][]string{append([]string{"brew", "install"}, pkgs...)}, false
	default:
		return nil, false
	}
	if root {
		return base, false
	}
	if !haveSudo {
		return base, true
	}
	for _, c := range base {
		cmds = append(cmds, append([]string{"sudo"}, c...))
	}
	return cmds, false
}

func commandLine(argv []string) string { return strings.Join(argv, " ") }

// agentHelp is, for each agent CLI, how to get it and sign in. Handloom prints these and never runs them.
var agentHelp = []struct{ bin, name, install, login, page string }{
	{"claude", "Claude Code", "curl -fsSL https://claude.ai/install.sh | bash", "run `claude` once and sign in when it asks", "https://docs.claude.com/en/docs/claude-code"},
	{"codex", "Codex", "npm install -g @openai/codex", "run `codex login`", "https://github.com/openai/codex"},
	{"opencode", "OpenCode", "curl -fsSL https://opencode.ai/install | bash", "run `opencode auth login`", "https://opencode.ai"},
}

// agentHelpText says, in plain words, how to get an agent CLI working. It installs nothing.
func agentHelpText() string {
	var b strings.Builder
	b.WriteString("Handloom does not install agent programs for you. Pick one, install it with its own installer (check its page, the line can change), sign in, then run `handloom doctor` again:\n")
	for _, a := range agentHelp {
		fmt.Fprintf(&b, "  %s: %s\n      then %s. Page: %s\n", a.name, a.install, a.login, a.page)
	}
	b.WriteString("  Codex needs Node.js (npm). Pi and OMP: see their own project pages.\n")
	return b.String()
}

// fixer carries what fixing needs from the machine, so tests can supply fakes.
type fixer struct {
	out         io.Writer
	goos        string
	euid        int
	osRelease   func() string
	has         func(string) bool
	run         func(argv []string) error // runs a command with the person's terminal attached
	interactive bool
	confirm     func(question string) bool
}

func (e *env) newFixer() *fixer {
	return &fixer{
		out: e.out, goos: goos, euid: os.Geteuid(),
		osRelease: func() string { b, _ := os.ReadFile("/etc/os-release"); return string(b) },
		has:       hasTool,
		run: func(argv []string) error {
			c := exec.Command(argv[0], argv[1:]...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
		interactive: isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()),
		confirm: func(q string) bool {
			fmt.Fprint(os.Stderr, q+" [y/N] ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			a := strings.ToLower(strings.TrimSpace(line))
			return a == "y" || a == "yes"
		},
	}
}

// plan says how to install the missing tools on this machine, and the plain-words reason when it cannot.
func (f *fixer) plan(missing []string) (cmds [][]string, needsRoot bool, problem string) {
	pm := detectPackageManager(f.goos, f.osRelease(), f.has)
	if pm == "" {
		if f.goos == "darwin" {
			return nil, false, "this Mac has no Homebrew. Install it from https://brew.sh (it is the usual way to add tools on a Mac), then run `handloom doctor --fix` again. " +
				"(git alone also comes with Apple's tools: `xcode-select --install`.)"
		}
		return nil, false, "I could not tell which package manager this machine uses. Install " + strings.Join(missing, " and ") + " with it, then run `handloom doctor` again."
	}
	cmds, needsRoot = installCommands(pm, missing, f.euid == 0, f.has("sudo"))
	return cmds, needsRoot, ""
}

// fixTools installs the missing tools (git, tmux) when the person agrees. It returns true only if it ran the commands.
func (f *fixer) fixTools(missing []string) bool {
	if len(missing) == 0 {
		return false
	}
	cmds, needsRoot, problem := f.plan(missing)
	if problem != "" {
		fmt.Fprintln(f.out, "Cannot install "+strings.Join(missing, " and ")+" for you: "+problem)
		return false
	}
	fmt.Fprintf(f.out, "To install %s I would run:\n", strings.Join(missing, " and "))
	usesSudo := false
	for _, c := range cmds {
		fmt.Fprintf(f.out, "    %s\n", commandLine(c))
		usesSudo = usesSudo || c[0] == "sudo"
	}
	if needsRoot {
		fmt.Fprintln(f.out, "  These need administrator rights and there is no sudo here: run them as root.")
		return false
	}
	if !f.interactive {
		fmt.Fprintln(f.out, "  Nothing was run, because there is no one to ask here. Run those commands yourself, or run `handloom doctor --fix` in a terminal.")
		return false
	}
	q := "Run " + map[bool]string{true: "this", false: "these"}[len(cmds) == 1] + " now?"
	if usesSudo {
		q = "Run " + map[bool]string{true: "this", false: "these"}[len(cmds) == 1] + " now? It uses sudo, which may ask for your password."
	}
	if !f.confirm(q) {
		fmt.Fprintln(f.out, "  OK, nothing was changed. Run the commands above when you are ready.")
		return false
	}
	for _, c := range cmds {
		if err := f.run(c); err != nil {
			fmt.Fprintf(f.out, "  That did not work (%s: %v). Fix that, then run `handloom doctor --fix` again.\n", commandLine(c), err)
			return false
		}
	}
	return true
}
