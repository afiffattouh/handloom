package mcp

import (
	"fmt"
	"io"
)

// The operator tools are for a person's own AI app (Claude, Codex, ...) to
// look at and drive Handloom on their behalf: read the picture, start a job,
// message an agent. They run as the person's token, so the hub's rules apply.
//
// Approving is not among them unless the person turns it on. Accepting work,
// answering an agent's question, and closing or resuming a job are what only a
// human may do; a tool an AI can call would let an app set to auto-approve
// approve its own agents' work. With allowApprovals each of those is marked
// destructive, so a client asks every time.

const operatorInstructions = "handloom runs jobs made of coding agents on the person's machines. These tools let you look at the picture and start work for them. " +
	"You cannot accept work, answer an agent's question or close a job unless the person turned that on; when something needs their decision, tell them what it is and where to decide it (the Inbox in the web UI)."

const approvalPrefix = "Approval: the person must have asked for exactly this. "

var (
	readOnly    = map[string]any{"readOnlyHint": true}
	changesData = map[string]any{"readOnlyHint": false, "destructiveHint": false}
	approves    = map[string]any{"readOnlyHint": false, "destructiveHint": true}
)

func reader(name, desc string, props map[string]any, req []string, argv func(a args) ([]string, error)) tool {
	return tool{Name: name, Description: desc, InputSchema: schema(req, props), Annotations: readOnly, argv: argv}
}

func fixed(name, desc string, argv ...string) tool {
	return reader(name, desc, nil, nil, func(args) ([]string, error) { return argv, nil })
}

// OperatorTools is the tool set of `handloom mcp --operator`.
func OperatorTools(allowApprovals bool) []tool {
	ts := []tool{
		fixed("handloom_digest", "What needs the person, what waits for review, and what is running. Start here.", "digest"),
		fixed("handloom_jobs", "List the jobs with their state and task counts.", "job", "list"),
		reader("handloom_job", "Show one job: its brief, tasks and team.", map[string]any{"id": prop("integer", "job number")}, []string{"id"},
			func(a args) ([]string, error) {
				id, err := a.id()
				if err != nil {
					return nil, err
				}
				return []string{"job", "show", id}, nil
			}),
		reader("handloom_tasks", "List tasks, optionally of one job or one status (open, claimed, submitted, done).",
			map[string]any{"job": prop("integer", "only this job"), "status": prop("string", "only this status")}, nil,
			func(a args) ([]string, error) {
				out := []string{"task", "list"}
				if v := a.str("job"); v != "" {
					out = append(out, "--job", v)
				}
				if v := a.str("status"); v != "" {
					out = append(out, "--status", v)
				}
				return out, nil
			}),
		reader("handloom_task", "Show one task: owner, evidence, the device's check, and whether it merged.", map[string]any{"id": prop("integer", "task number")}, []string{"id"},
			func(a args) ([]string, error) {
				id, err := a.id()
				if err != nil {
					return nil, err
				}
				return []string{"task", "show", id}, nil
			}),
		reader("handloom_search", "Search what earlier jobs recorded on the hub: briefs, finished work, handoff notes and answered questions.", map[string]any{"query": prop("string", "a few words, such as: invoice prefix")}, []string{"query"},
			func(a args) ([]string, error) {
				if a.str("query") == "" {
					return nil, fmt.Errorf("query is required")
				}
				return []string{"search", "--jobs", a.str("query")}, nil
			}),
		fixed("handloom_agents", "List every agent with its role, machine and state.", "agents"),
		fixed("handloom_questions", "List the questions agents have asked the person, answered or not.", "escalations", "--status", "all"),
		fixed("handloom_profiles", "List the person's profiles: what each kind of agent may do.", "profiles"),
		reader("handloom_starters", "List the ready-made starter profiles, or show one by name.", map[string]any{"name": prop("string", "a starter name; empty lists them all")}, nil,
			func(a args) ([]string, error) {
				if n := a.str("name"); n != "" {
					return []string{"starters", n}, nil
				}
				return []string{"starters"}, nil
			}),
		reader("handloom_metrics", "The command center's numbers: checks passed, task time, usage and cost, insights.", map[string]any{"range": prop("string", "24h, 7d or 30d")}, nil,
			func(a args) ([]string, error) {
				if r := a.str("range"); r != "" {
					return []string{"metrics", "--range", r}, nil
				}
				return []string{"metrics"}, nil
			}),
		reader("handloom_screen", "The last lines on an agent's terminal. They may contain anything the agent printed.", map[string]any{"agent": prop("string", "the agent's name")}, []string{"agent"},
			func(a args) ([]string, error) {
				if a.str("agent") == "" {
					return nil, fmt.Errorf("agent is required")
				}
				return []string{"screen", a.str("agent")}, nil
			}),
		{Name: "handloom_job_new", Description: "Start a job. A lead agent plans it and starts workers on the person's machine, which costs time and model usage. Give a clear title and a brief that says what done looks like. Check profiles and the machine name first.",
			InputSchema: schema([]string{"title", "lead_profile"}, map[string]any{
				"title": prop("string", "one line"), "brief": prop("string", "the goal, how it is done, what to leave alone"),
				"lead_profile": prop("string", "the profile to start the lead from"), "device": prop("string", "the machine the work is on"),
				"repo":      prop("string", "absolute path of the git repository on that machine, if the job changes code"),
				"verify":    prop("string", "a command that exits 0 when the work is right"),
				"knowledge": prop("string", "absolute path of the client's knowledge repository, if any"), "base": prop("string", "a branch to start from")}),
			Annotations: changesData,
			argv: func(a args) ([]string, error) {
				if a.str("title") == "" || a.str("lead_profile") == "" {
					return nil, fmt.Errorf("title and lead_profile are required")
				}
				out := []string{"job", "new", a.str("title"), "--lead-profile", a.str("lead_profile")}
				for _, f := range [][2]string{{"brief", "--body"}, {"device", "--device"}, {"repo", "--repo"}, {"verify", "--verify"}, {"knowledge", "--knowledge"}, {"base", "--base"}} {
					if v := a.str(f[0]); v != "" {
						out = append(out, f[1], v)
					}
				}
				return out, nil
			}},
		{Name: "handloom_send", Description: "Send a message to an agent or to a job's lead (to: an agent name, or role:lead).",
			InputSchema: schema([]string{"to", "text"}, map[string]any{"to": prop("string", "agent name or role:lead"), "text": prop("string", "the message")}),
			Annotations: changesData,
			argv: func(a args) ([]string, error) {
				if a.str("to") == "" || a.str("text") == "" {
					return nil, fmt.Errorf("to and text are required")
				}
				return []string{"send", a.str("to"), a.str("text")}, nil
			}},
	}
	if allowApprovals {
		idTool := func(name, desc string, argv func(id string, a args) []string, extra map[string]any, req []string) tool {
			props := map[string]any{"id": prop("integer", "the number")}
			for k, v := range extra {
				props[k] = v
			}
			return tool{Name: name, Description: approvalPrefix + desc, InputSchema: schema(append([]string{"id"}, req...), props), Annotations: approves,
				argv: func(a args) ([]string, error) {
					id, err := a.id()
					if err != nil {
						return nil, err
					}
					return argv(id, a), nil
				}}
		}
		ts = append(ts,
			idTool("handloom_task_accept", "Accept a submitted task as done.", func(id string, _ args) []string { return []string{"task", "accept", id} }, nil, nil),
			idTool("handloom_task_reject", "Send a submitted task back with a reason.", func(id string, a args) []string { return []string{"task", "reject", id, "--reason", a.str("reason")} },
				map[string]any{"reason": prop("string", "what needs changing")}, []string{"reason"}),
			idTool("handloom_answer", "Answer an agent's question.", func(id string, a args) []string { return []string{"answer", id, a.str("answer")} },
				map[string]any{"answer": prop("string", "the answer")}, []string{"answer"}),
			idTool("handloom_job_close", "Close a job as done, or cancel it.", func(id string, a args) []string {
				if v, _ := a["cancel"].(bool); v {
					return []string{"job", "close", id, "--cancel"}
				}
				return []string{"job", "close", id}
			}, map[string]any{"cancel": prop("boolean", "cancel the job and its unfinished tasks instead")}, nil),
		)
	}
	return ts
}

// ServeOperator serves the operator tools.
func ServeOperator(in io.Reader, out io.Writer, version string, run Run, allowApprovals bool) error {
	return ServeTools(in, out, version, run, OperatorTools(allowApprovals), operatorInstructions)
}
