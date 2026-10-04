// Package cli is the handloom command line: the verbs agents and humans use,
// plus the commands that start the hub and the link.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"handloom/internal/client"
	"handloom/internal/mcp"
)

// mcp serves the agent verbs as MCP tools on stdin/stdout. Each tool call
// runs the same code path as the matching command line.
func (e *env) mcp(args []string) error {
	if len(args) != 0 {
		return usageErr("usage: handloom mcp")
	}
	return mcp.Serve(os.Stdin, e.out, Version, func(argv []string) (string, string, int) {
		var out, errb strings.Builder
		code := Main(argv, &out, &errb)
		return out.String(), errb.String(), code
	})
}

const usage = `handloom: coordination for coding agents across machines

Agent verbs (run inside an agent's shell, through the local link):
  handloom register <name> [--kind K] [--project P]   register this agent on this device
  handloom whoami                                     show who the hub thinks you are
  handloom agents                                     list agents and their state
  handloom send <to> <text> [--task N]                to: agent name, role:lead, or task:<id>
  handloom inbox [--all]                              read new messages (marks them read)
  handloom task list [--status S]
  handloom task show <id>
  handloom task create <title> [--body B] [--assign A] [--depends 1,2]      (lead, human)
  handloom task assign <id> <agent>                                         (lead, human)
  handloom task accept <id> | reject <id> --reason R | cancel <id>          (lead, human)
  handloom task claim <id> | heartbeat <id> | release <id>                  (lead, worker)
  handloom task block <id> --reason R | block <id> --clear                  (lead, worker)
  handloom task submit <id> --evidence E [--evidence E ...] [--note N]      (lead, worker)
  handloom state <idle|working|blocked|offline>       report agent state (adapters do this)

Device:
  handloom link join <hub-url> <join-token>           exchange a join token for a device credential
  handloom link run                                   run the link daemon in the foreground
  handloom link install | uninstall                   run it as a systemd service, now and at boot
  handloom link status
  handloom adapter install <kind> --name N [--dir D]  install an agent adapter in a project
                                                  (kinds: claude, codex, pi, omp, opencode)
  handloom run <name> -- <command...>                 start an agent CLI as a registered, wakeable agent
  handloom mcp                                        MCP server (stdio) offering the agent verbs as tools

Hub and administration (HANDLOOM_HUB and HANDLOOM_TOKEN set to the hub URL and a token):
  handloom hub init [--data DIR]                      create the database, print the admin token once
  handloom hub serve [--data DIR] [--addr A] [--lease 15m]
  handloom hub install [--data DIR] [--addr A] | uninstall     run the hub as a systemd service
  handloom device add <name> | list | revoke <name>   (admin)
  handloom human add <name>                           (admin)
  handloom project add <name> | list                  (admin)
  handloom agent role <name> <lead|worker|observer>   (admin, human)
  handloom audit [--after SEQ]                        (admin, human)

Every verb takes --json. Agents are identified by $HANDLOOM_AGENT, or by a
.handloom/agent file in the working directory or a parent.
`

type env struct {
	out, err io.Writer
	json     bool
}

// Main runs one command and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	e := &env{out: stdout, err: stderr}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	case "version":
		fmt.Fprintln(stdout, "handloom", Version, "protocol handloom/1")
	case "hub":
		err = e.hub(rest)
	case "link":
		err = e.link(rest)
	case "device":
		err = e.device(rest)
	case "human":
		err = e.human(rest)
	case "project":
		err = e.project(rest)
	case "agent":
		err = e.agent(rest)
	case "audit":
		err = e.audit(rest)
	case "register":
		err = e.register(rest)
	case "whoami":
		err = e.whoami(rest)
	case "agents":
		err = e.agents(rest)
	case "state":
		err = e.state(rest)
	case "send":
		err = e.send(rest)
	case "inbox":
		err = e.inbox(rest)
	case "task":
		err = e.task(rest)
	case "hook":
		return e.hook(rest)
	case "adapter":
		err = e.adapter(rest)
	case "mcp":
		err = e.mcp(rest)
	case "run":
		err = e.run(rest)
	default:
		err = usageErr("unknown command %q; run `handloom help`", cmd)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, "handloom:", err)
	var ue *usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

// Version is set at build time with -ldflags.
var Version = "dev"

type usageError struct{ msg string }

func (u *usageError) Error() string { return u.msg }

func usageErr(format string, args ...any) error {
	return &usageError{fmt.Sprintf(format, args...)}
}

// ---- flags ----

type flags struct {
	*flag.FlagSet
	e *env
}

func (e *env) flags(name string) *flags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&e.json, "json", false, "print JSON")
	return &flags{fs, e}
}

// parse accepts flags before, between and after positional arguments, and
// returns the positionals.
// A "--" ends the flags: everything after it is positional, even if it
// starts with a dash.
func (f *flags) parse(args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := f.Parse(args); err != nil {
			return nil, usageErr("%v", err)
		}
		args = f.Args()
		if len(args) == 0 {
			return append(pos, tail...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// need parses and checks the number of positional arguments.
func (f *flags) need(args []string, min, max int, synopsis string) ([]string, error) {
	pos, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	if len(pos) < min || (max >= 0 && len(pos) > max) {
		return nil, usageErr("usage: handloom %s", synopsis)
	}
	return pos, nil
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// ---- connection ----

// AgentName finds the calling agent: $HANDLOOM_AGENT, or the nearest .handloom/agent
// file from dir upward.
func AgentName(dir string) string {
	if a := os.Getenv("HANDLOOM_AGENT"); a != "" {
		return a
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	for dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, ".handloom", "agent")); err == nil {
			return strings.TrimSpace(string(b))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// conn picks how to reach the hub: directly with HANDLOOM_TOKEN (admin or
// human), or through the local link as an agent.
func conn() (*client.Client, error) {
	if tok := os.Getenv("HANDLOOM_TOKEN"); tok != "" {
		hub := os.Getenv("HANDLOOM_HUB")
		if hub == "" {
			return nil, fmt.Errorf("HANDLOOM_TOKEN is set but HANDLOOM_HUB is not")
		}
		return client.Direct(hub, tok), nil
	}
	sock := client.SocketPath()
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("no link socket at %s: start `handloom link run` on this device", sock)
	}
	return client.Socket(sock, AgentName("")), nil
}

// print writes v as JSON when --json is set, else calls text.
func (e *env) print(v any, text func()) {
	if e.json {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(e.out, string(b))
		return
	}
	text()
}
