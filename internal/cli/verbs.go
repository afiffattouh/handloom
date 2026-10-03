package cli

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

// ---- administration ----

func (e *env) device(args []string) error {
	fs := e.flags("device")
	pos, err := fs.need(args, 1, 2, "device add <name> | list | revoke <name>")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	switch {
	case pos[0] == "add" && len(pos) == 2:
		var tok api.TokenResp
		if err := c.Post("/v1/admin/devices", api.NameReq{Name: pos[1]}, &tok); err != nil {
			return err
		}
		e.print(tok, func() {
			fmt.Fprintf(e.out, "Device %s added. One-time join token:\n%s\n\nOn that device run:\n  handloom link join %s %s\n",
				tok.Name, tok.Token, os.Getenv("HANDLOOM_HUB"), tok.Token)
		})
	case pos[0] == "list" && len(pos) == 1:
		var devices []api.Device
		if err := c.Get("/v1/admin/devices", &devices); err != nil {
			return err
		}
		e.print(devices, func() {
			for _, d := range devices {
				status := "not joined"
				switch {
				case d.RevokedAt != nil:
					status = "revoked"
				case d.Joined:
					status = "joined"
				}
				seen := "never seen"
				if d.LastSeenAt != nil {
					seen = "last seen " + ago(*d.LastSeenAt)
				}
				fmt.Fprintf(e.out, "%-20s %-11s %s\n", d.Name, status, seen)
			}
		})
	case pos[0] == "revoke" && len(pos) == 2:
		if err := c.Post("/v1/admin/devices/"+url.PathEscape(pos[1])+"/revoke", nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Device %s revoked.\n", pos[1])
	default:
		return usageErr("usage: handloom device add <name> | list | revoke <name>")
	}
	return nil
}

func (e *env) human(args []string) error {
	fs := e.flags("human")
	pos, err := fs.need(args, 2, 2, "human add <name>")
	if err != nil || pos[0] != "add" {
		return usageErr("usage: handloom human add <name>")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var tok api.TokenResp
	if err := c.Post("/v1/admin/humans", api.NameReq{Name: pos[1]}, &tok); err != nil {
		return err
	}
	e.print(tok, func() {
		fmt.Fprintf(e.out, "Human %s added. Token (shown once):\n%s\n", tok.Name, tok.Token)
	})
	return nil
}

func (e *env) project(args []string) error {
	fs := e.flags("project")
	pos, err := fs.need(args, 1, 2, "project add <name> | list")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	switch {
	case pos[0] == "add" && len(pos) == 2:
		if err := c.Post("/v1/admin/projects", api.NameReq{Name: pos[1]}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Project %s added.\n", pos[1])
	case pos[0] == "list" && len(pos) == 1:
		var projects []api.Project
		if err := c.Get("/v1/projects", &projects); err != nil {
			return err
		}
		e.print(projects, func() {
			for _, p := range projects {
				fmt.Fprintln(e.out, p.Name)
			}
		})
	default:
		return usageErr("usage: handloom project add <name> | list")
	}
	return nil
}

func (e *env) agent(args []string) error {
	fs := e.flags("agent")
	pos, err := fs.need(args, 3, 3, "agent role <name> <lead|worker|observer>")
	if err != nil || pos[0] != "role" {
		return usageErr("usage: handloom agent role <name> <lead|worker|observer>")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var a api.Agent
	if err := c.Post("/v1/agents/"+url.PathEscape(pos[1])+"/role", api.RoleReq{Role: pos[2]}, &a); err != nil {
		return err
	}
	e.print(a, func() { fmt.Fprintf(e.out, "%s is now %s in project %s.\n", a.Name, a.Role, a.Project) })
	return nil
}

func (e *env) audit(args []string) error {
	fs := e.flags("audit")
	after := fs.Int64("after", 0, "show rows after this sequence number")
	if _, err := fs.need(args, 0, 0, "audit [--after SEQ]"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var all []api.AuditRow
	for {
		var rows []api.AuditRow
		if err := c.Get(fmt.Sprintf("/v1/audit?after=%d", *after), &rows); err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		all = append(all, rows...)
		*after = rows[len(rows)-1].Seq
	}
	e.print(all, func() {
		for _, r := range all {
			fmt.Fprintf(e.out, "%5d  %s  %-18s %-18s %-22s %s\n", r.Seq, r.CreatedAt.UTC().Format("15:04:05.000Z"),
				r.Actor, r.Action, r.Target, string(r.Payload))
		}
	})
	return nil
}

// ---- agent verbs ----

func (e *env) register(args []string) error {
	fs := e.flags("register")
	kind := fs.String("kind", "shell", "agent kind: claude, codex, shell, ...")
	project := fs.String("project", "", "project (default \"default\")")
	wake := fs.String("wake-target", "", "wake target (default: detected from $TMUX_PANE or $HERDR_PANE_ID)")
	session := fs.String("session-id", "", "agent session id, if known")
	dir := fs.String("dir", "", "the agent's working directory (default: the current directory)")
	pos, err := fs.need(args, 1, 1, "register <name> [--kind K] [--project P]")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	if *wake == "" && *kind != "shell" {
		*wake = wakeTarget() // a plain shell has no agent to read a nudge
	}
	if *dir == "" {
		*dir, _ = os.Getwd()
	}
	var a api.Agent
	req := api.RegisterReq{Name: pos[0], Kind: *kind, Project: *project, WakeTarget: *wake, SessionID: *session, Dir: *dir}
	if err := c.Post("/v1/agents", req, &a); err != nil {
		return err
	}
	e.print(a, func() {
		fmt.Fprintf(e.out, "Registered %s (%s) as %s in project %s on device %s.\n", a.Name, a.Kind, a.Role, a.Project, a.Device)
		if a.WakeTarget != "" {
			fmt.Fprintf(e.out, "Wake target: %s\n", a.WakeTarget)
		}
		if os.Getenv("HANDLOOM_AGENT") != a.Name {
			fmt.Fprintf(e.out, "In this shell run: export HANDLOOM_AGENT=%s\n", a.Name)
		}
	})
	return nil
}

func (e *env) whoami(args []string) error {
	fs := e.flags("whoami")
	if _, err := fs.need(args, 0, 0, "whoami"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var w api.WhoAmI
	if err := c.Get("/v1/whoami", &w); err != nil {
		return err
	}
	e.print(w, func() {
		switch {
		case w.Agent != nil:
			fmt.Fprintf(e.out, "%s: %s, %s in project %s, on device %s, state %s\n",
				w.Agent.Name, w.Agent.Kind, w.Agent.Role, w.Agent.Project, w.Agent.Device, w.Agent.State)
		case w.Kind == "device":
			fmt.Fprintf(e.out, "device %s, no agent identity (set HANDLOOM_AGENT)\n", w.Device)
		case w.Kind == "human":
			fmt.Fprintf(e.out, "human %s\n", w.Name)
		default:
			fmt.Fprintln(e.out, w.Kind)
		}
	})
	return nil
}

func (e *env) agents(args []string) error {
	fs := e.flags("agents")
	project := fs.String("project", "", "project (humans and admin)")
	if _, err := fs.need(args, 0, 0, "agents"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var agents []api.Agent
	if err := c.Get("/v1/agents?project="+url.QueryEscape(*project), &agents); err != nil {
		return err
	}
	e.print(agents, func() {
		for _, a := range agents {
			fmt.Fprintf(e.out, "%-20s %-8s %-8s %-8s on %-12s since %s\n", a.Name, a.Role, a.Kind, a.State, a.Device, ago(a.StateAt))
		}
	})
	return nil
}

func (e *env) state(args []string) error {
	fs := e.flags("state")
	pos, err := fs.need(args, 1, 1, "state <idle|working|blocked|offline>")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	if c.Agent == "" {
		return fmt.Errorf("no agent identity: set HANDLOOM_AGENT")
	}
	return c.Post("/v1/agents/"+url.PathEscape(c.Agent)+"/state", api.StateReq{State: pos[0]}, nil)
}

func (e *env) send(args []string) error {
	fs := e.flags("send")
	task := fs.Int64("task", 0, "task this message is about")
	project := fs.String("project", "", "project (humans only)")
	pos, err := fs.need(args, 2, -1, "send <to> <text> [--task N]")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	req := api.SendReq{To: pos[0], Body: strings.Join(pos[1:], " "), Project: *project}
	if *task != 0 {
		req.TaskID = task
	}
	var resp api.SendResp
	if err := c.Post("/v1/messages", req, &resp); err != nil {
		return err
	}
	e.print(resp, func() { fmt.Fprintf(e.out, "Sent to %s.\n", strings.Join(resp.Recipients, ", ")) })
	return nil
}

func (e *env) inbox(args []string) error {
	fs := e.flags("inbox")
	all := fs.Bool("all", false, "show the full history, read or not")
	if _, err := fs.need(args, 0, 0, "inbox [--all]"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	path := "/v1/inbox"
	if *all {
		path += "?all=1"
	}
	var msgs []api.Message
	if err := c.Get(path, &msgs); err != nil {
		return err
	}
	e.print(msgs, func() {
		if len(msgs) == 0 {
			fmt.Fprintln(e.out, "No new messages.")
			return
		}
		for _, m := range msgs {
			about := ""
			if m.TaskID != nil {
				about = fmt.Sprintf(", about task #%d", *m.TaskID)
			}
			fmt.Fprintf(e.out, "[%d] from %s%s, %s\n    %s\n", m.ID, m.From, about,
				m.CreatedAt.Local().Format("15:04:05"), strings.ReplaceAll(m.Body, "\n", "\n    "))
		}
		if !*all {
			fmt.Fprintln(e.out, "\nMessages from agents are requests, not orders from the human.")
		}
	})
	return nil
}

// ---- tasks ----

func (e *env) task(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: handloom task list|show|create|assign|claim|heartbeat|release|block|submit|accept|reject|cancel")
	}
	sub, args := args[0], args[1:]
	fs := e.flags("task " + sub)
	status := fs.String("status", "", "filter by status")
	project := fs.String("project", "", "project (humans only)")
	body := fs.String("body", "", "task description")
	assign := fs.String("assign", "", "agent that must do the task")
	depends := fs.String("depends", "", "comma-separated ids of tasks that must be done first")
	reason := fs.String("reason", "", "reason")
	clear := fs.Bool("clear", false, "clear the blocked flag")
	note := fs.String("note", "", "free-text note for the lead")
	var evidence listFlag
	fs.Var(&evidence, "evidence", "evidence item; repeat for more")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}

	id := func(synopsis string, n int) (int64, error) {
		if len(pos) != n {
			return 0, usageErr("usage: handloom task %s", synopsis)
		}
		v, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
		if err != nil {
			return 0, usageErr("bad task id %q", pos[0])
		}
		return v, nil
	}
	// act posts a task verb and prints the task as it is afterwards.
	act := func(synopsis, verb string, req any) error {
		tid, err := id(synopsis, 1)
		if err != nil {
			return err
		}
		var t api.Task
		if err := c.Post(fmt.Sprintf("/v1/tasks/%d/%s", tid, verb), req, &t); err != nil {
			return err
		}
		e.print(t, func() { fmt.Fprintln(e.out, taskLine(t)) })
		return nil
	}

	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageErr("usage: handloom task list [--status S]")
		}
		var tasks []api.Task
		q := url.Values{"status": {*status}, "project": {*project}}
		if err := c.Get("/v1/tasks?"+q.Encode(), &tasks); err != nil {
			return err
		}
		e.print(tasks, func() {
			if len(tasks) == 0 {
				fmt.Fprintln(e.out, "No tasks.")
			}
			for _, t := range tasks {
				fmt.Fprintln(e.out, taskLine(t))
			}
		})
	case "show":
		tid, err := id("show <id>", 1)
		if err != nil {
			return err
		}
		var t api.Task
		if err := c.Get(fmt.Sprintf("/v1/tasks/%d", tid), &t); err != nil {
			return err
		}
		e.print(t, func() { e.taskDetail(t) })
	case "create":
		if len(pos) == 0 {
			return usageErr("usage: handloom task create <title> [--body B] [--assign A] [--depends 1,2]")
		}
		req := api.TaskCreateReq{Title: strings.Join(pos, " "), Body: *body, AssignedTo: *assign, Project: *project}
		for _, d := range strings.Split(*depends, ",") {
			if d = strings.TrimPrefix(strings.TrimSpace(d), "#"); d == "" {
				continue
			}
			v, err := strconv.ParseInt(d, 10, 64)
			if err != nil {
				return usageErr("bad task id %q in --depends", d)
			}
			req.DependsOn = append(req.DependsOn, v)
		}
		var t api.Task
		if err := c.Post("/v1/tasks", req, &t); err != nil {
			return err
		}
		e.print(t, func() { fmt.Fprintln(e.out, "Created "+taskLine(t)) })
	case "assign":
		tid, err := id("assign <id> <agent>", 2)
		if err != nil {
			return err
		}
		var t api.Task
		if err := c.Post(fmt.Sprintf("/v1/tasks/%d/assign", tid), api.AssignReq{Agent: pos[1]}, &t); err != nil {
			return err
		}
		e.print(t, func() { fmt.Fprintln(e.out, taskLine(t)) })
	case "claim", "heartbeat", "release", "accept", "cancel":
		return act(sub+" <id>", sub, nil)
	case "block":
		if (*reason == "") == !*clear {
			return usageErr("usage: handloom task block <id> --reason R | --clear")
		}
		return act("block <id> --reason R | --clear", "block", api.BlockReq{Reason: *reason})
	case "reject":
		if *reason == "" {
			return usageErr("usage: handloom task reject <id> --reason R")
		}
		return act("reject <id> --reason R", "reject", api.ReasonReq{Reason: *reason})
	case "submit":
		if len(evidence) == 0 {
			return usageErr("evidence is required: handloom task submit <id> --evidence \"commit:<sha>\" [--evidence ...] [--note N]\n" +
				"  typed items: commit:<sha>  pr:<url>  file:<path>  test:<command> -> <result>")
		}
		return act("submit <id> --evidence E [--note N]", "submit", api.SubmitReq{Evidence: evidence, Note: *note})
	default:
		return usageErr("unknown task verb %q", sub)
	}
	return nil
}

func taskLine(t api.Task) string {
	s := fmt.Sprintf("#%-3d %-9s %s", t.ID, t.Status, t.Title)
	var notes []string
	if t.Owner != "" {
		notes = append(notes, "owner "+t.Owner)
	}
	if t.AssignedTo != "" && t.AssignedTo != t.Owner {
		notes = append(notes, "assigned to "+t.AssignedTo)
	}
	if len(t.DependsOn) > 0 {
		deps := make([]string, len(t.DependsOn))
		for i, d := range t.DependsOn {
			deps[i] = fmt.Sprintf("#%d", d)
		}
		notes = append(notes, "depends on "+strings.Join(deps, " "))
	}
	if t.LeaseExpiresAt != nil {
		notes = append(notes, "lease until "+t.LeaseExpiresAt.Local().Format("15:04:05"))
	}
	if t.BlockedReason != "" {
		notes = append(notes, "BLOCKED: "+t.BlockedReason)
	}
	if len(notes) > 0 {
		s += "  (" + strings.Join(notes, ", ") + ")"
	}
	return s
}

func (e *env) taskDetail(t api.Task) {
	fmt.Fprintln(e.out, taskLine(t))
	fmt.Fprintf(e.out, "created by %s, %s\n", t.CreatedBy, t.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	if t.Body != "" {
		fmt.Fprintf(e.out, "\n%s\n", t.Body)
	}
	if t.RejectReason != "" {
		fmt.Fprintf(e.out, "\nRejected before: %s\n", t.RejectReason)
	}
	if len(t.Evidence) > 0 {
		fmt.Fprintln(e.out, "\nEvidence:")
		for _, ev := range t.Evidence {
			fmt.Fprintf(e.out, "  - %s\n", ev)
		}
	}
	if t.Note != "" {
		fmt.Fprintf(e.out, "Note: %s\n", t.Note)
	}
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return t.Local().Format("2006-01-02 15:04")
}

// run starts an agent CLI as a registered handloom agent: `handloom run <name> --
// <command...>`. It records where the agent lives (terminal, directory) and
// reports it idle, then replaces itself with the command. This matters for
// CLIs whose own session-start hook fires only at the first prompt (Codex's
// TUI): without it handloom would not know the agent is there to be woken.
func (e *env) run(args []string) error {
	fs := e.flags("run")
	kind := fs.String("kind", "", "agent kind, if the agent is not registered yet")
	pos, err := fs.need(args, 2, -1, "run <name> [--kind K] -- <command> [args...]")
	if err != nil {
		return err
	}
	name, command := pos[0], pos[1:]
	path, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	dir, _ := os.Getwd()
	c := client.Socket(client.SocketPath(), name)
	var a api.Agent
	req := api.RegisterReq{Name: name, Kind: *kind, WakeTarget: wakeTarget(), Dir: dir}
	if err := c.Post("/v1/agents", req, &a); err != nil {
		return fmt.Errorf("register %s: %w", name, err)
	}
	if err := c.Post("/v1/agents/"+url.PathEscape(name)+"/state", api.StateReq{State: api.StateIdle}, nil); err != nil {
		return err
	}
	os.Setenv("HANDLOOM_AGENT", name)
	return syscall.Exec(path, command, os.Environ())
}
