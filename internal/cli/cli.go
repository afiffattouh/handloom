// Package cli is the handloom command line: the verbs agents and humans use,
// plus the commands that start the hub and the link.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"handloom/internal/setting"
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
	fs := e.flags("mcp")
	operator := fs.Bool("operator", false, "serve the tools of a person driving Handloom from their own AI app (needs HANDLOOM_HUB and HANDLOOM_TOKEN) instead of an agent's tools")
	approvals := fs.Bool("allow-approvals", false, "with --operator: also offer accepting work, answering questions and closing jobs, which only a human should do")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || (*approvals && !*operator) {
		return usageErr("usage: handloom mcp [--operator [--allow-approvals]]")
	}
	run := func(argv []string) (string, string, int) {
		var out, errb strings.Builder
		code := Main(argv, &out, &errb)
		return out.String(), errb.String(), code
	}
	if *operator {
		return mcp.ServeOperator(os.Stdin, e.out, Version, run, *approvals)
	}
	return mcp.Serve(os.Stdin, e.out, Version, run)
}

const usage = `handloom: coordination for coding agents across machines

Agent verbs (run inside an agent's shell, through the local link):
  handloom register <name> [--kind K] [--project P]   register this agent on this device
  handloom whoami                                     show who the hub thinks you are
  handloom agents                                     list agents and their state
  handloom send <to> <text> [--task N]                to: agent name, role:lead, or task:<id>
  handloom inbox [--all]                              read new messages (marks them read)
  handloom job list | show <id>                   jobs: a root task with its own lead
  handloom job new <title> [--body B] [--lead AGENT | --lead-profile P] [--repo PATH [--base BRANCH] --verify CMD --device D] [--knowledge PATH] [--confidential]   (human)
  handloom job resume <id> --lead AGENT                                 (human) a new lead takes over a job whose lead is gone
  handloom job close <id> [--cancel]                                    (human)
  handloom task list [--status S] [--job N]
  handloom task show <id>
  handloom task create <title> [--body B] [--assign A] [--depends 1,2]      (lead, human)
  handloom task assign <id> <agent>                                         (lead, human)
  handloom task accept <id> | reject <id> --reason R | cancel <id>          (lead, human)
  handloom task claim <id> | heartbeat <id> | release <id>                  (lead, worker)
  handloom task block <id> --reason R | block <id> --clear                  (lead, worker)
  handloom task submit <id> --evidence E [--evidence E ...] [--note N]      (lead, worker)
  handloom ask <question> [--option A --option B] [--task N] [--wait 10m]   (lead) ask the human; the answer arrives as a message
  handloom state <idle|working|blocked|offline>       report agent state (adapters do this)

Device:
  handloom link join <hub-url> <join-token>           exchange a join token for a device credential
  handloom link run                                   run the link daemon in the foreground
  handloom link install | uninstall                   run it as a systemd service, now and at boot
  handloom link status
  handloom doctor                                     is this machine ready? checks the link, git, tmux and the agent CLIs
  handloom adapter install <kind> --name N [--dir D]  install an agent adapter in a project
                                                  (kinds: claude, codex, pi, omp, opencode)
  handloom run <name> -- <command...>                 start an agent CLI as a registered, wakeable agent
  handloom mcp [--operator [--allow-approvals]]       MCP server (stdio): the agent verbs as tools, or with --operator the tools of a person driving Handloom from their own AI app

Hub and administration (HANDLOOM_HUB and HANDLOOM_TOKEN set to the hub URL and a token):
  handloom hub init [--data DIR]                      create the database, print the admin token once
  handloom hub serve [--data DIR] [--addr A] [--lease 15m] [--auto-init]
  handloom hub reset-admin-token [--data DIR]         a new admin token, if the old one is lost (prints it once)
  handloom hub create-owner <name> [--data DIR]       make the first owner without the web setup page (prints the password once)
  handloom hub backup --to FILE [--data DIR]          consistent copy while the hub runs
  handloom hub restore --from FILE [--data DIR] [--force]    hub must be stopped
  handloom healthcheck [--addr A]                     exit 0 if the hub answers /healthz (for containers)
  handloom hub install [--data DIR] [--addr A] | uninstall     run the hub as a systemd service
  handloom device add <name> | list | revoke <name>   (admin)
  handloom human add <name>                           (admin)
  handloom project add <name> | list                  (admin)
  handloom agent role <name> <lead|worker|observer>   (admin, human)
  handloom agent job <name> <job id | 0>              move an agent into a job (admin, human)
  handloom people [add <name> [--role R] | invite <name> | role <name> --role R]   (owner) who can sign in; invite links
  handloom prices [set <model> --input N --output N | remove <model>]              (owner) what models cost, for usage figures
  handloom notifications [set --url U --topic T | test]                             (owner) phone push through ntfy
  handloom token new [--app] | token remove-app       a new personal API token (the one you use stops working); --app: a token for your AI app, which cannot approve
  handloom screen <agent>                             the end of an agent's terminal (held in memory for a minute)
  handloom metrics [--range 7d]                       the command center's numbers
  handloom starters [name]                        the starter library: ready-made profiles; add one with: handloom profile add <name> --kind K --runtime R
  handloom profiles                               the library: what an agent may do and know
  handloom profile new <dir> | check <dir> | show <name> | export <name> <dir> | versions <name>   (new: owner)
  handloom spawn <name> [--profile P] [--kind claude] [--model M] [--device D] [--job N]   start an agent in a terminal the link owns (lead, human)
  handloom spawns                                 list spawn requests and how they went
  handloom digest                                 what needs you, what waits for review, what runs
  handloom escalations [--status open|answered|all]   list the lead's questions (human)
  handloom answer <id> <answer>                       answer a question (human only)
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
	case "healthcheck":
		err = e.healthcheck(rest)
	case "doctor":
		err = e.doctor(rest)
	case "profile":
		err = e.profile(rest)
	case "profiles":
		err = e.profiles(rest)
	case "starters":
		err = e.starters(rest)
	case "prices":
		err = e.prices(rest)
	case "people":
		err = e.people(rest)
	case "notifications":
		err = e.notifications(rest)
	case "token":
		err = e.token(rest)
	case "screen":
		err = e.screen(rest)
	case "metrics":
		err = e.metrics(rest)
	case "spawn":
		err = e.spawn(rest)
	case "spawns":
		err = e.spawns(rest)
	case "spawn-exec":
		err = e.spawnExec(rest)
	case "digest":
		err = e.digest(rest)
	case "ask":
		err = e.ask(rest)
	case "answer":
		err = e.answer(rest)
	case "escalations":
		err = e.escalations(rest)
	case "job":
		err = e.job(rest)
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
	if a := setting.Get("AGENT"); a != "" {
		return a
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	for dir != "" {
		for _, d := range []string{".handloom", ".handloom"} { // the old directory still identifies old installs
			if b, err := os.ReadFile(filepath.Join(dir, d, "agent")); err == nil {
				return strings.TrimSpace(string(b))
			}
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
	if tok := setting.Get("TOKEN"); tok != "" {
		hub := setting.Get("HUB")
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
