// Package mcp is a small MCP server over stdio that offers the handloom verbs as
// tools. An agent CLI starts it (`handloom mcp`); every tool call runs the same
// code as the matching `handloom` command, so scopes and the audit log apply
// unchanged.
//
// It implements what a tool-only server needs: initialize, ping, tools/list
// and tools/call, as newline-delimited JSON-RPC 2.0.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Run executes one handloom command line and returns its output.
type Run func(args []string) (stdout, stderr string, code int)

// defaultProtocol is offered when the client does not name a version.
const defaultProtocol = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// tool is one handloom verb.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	argv        func(a args) ([]string, error)
}

// args are the arguments of one tool call.
type args map[string]any

func (a args) str(key string) string {
	switch v := a[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// id returns a task id argument as text.
func (a args) id() (string, error) {
	s := strings.TrimPrefix(a.str("id"), "#")
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return "", fmt.Errorf("id must be a task number")
	}
	return s, nil
}

func (a args) list(key string) []string {
	var out []string
	switch v := a[key].(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			} else if n, ok := item.(float64); ok {
				out = append(out, strconv.FormatFloat(n, 'f', -1, 64))
			}
		}
	case string:
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func schema(required []string, props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

var (
	taskID   = map[string]any{"id": prop("integer", "task number")}
	stringsT = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
)

// simple is a task verb that takes only the task id.
func simple(name, verb, desc string) tool {
	return tool{Name: name, Description: desc, InputSchema: schema([]string{"id"}, taskID),
		argv: func(a args) ([]string, error) {
			id, err := a.id()
			if err != nil {
				return nil, err
			}
			return []string{"task", verb, id}, nil
		}}
}

// Tools is the list offered to the agent. Names are prefixed so they cannot
// clash with another server's tools.
var Tools = []tool{
	{Name: "handloom_whoami", Description: "Show your handloom identity: name, role, project, device and state.",
		InputSchema: schema(nil, nil), argv: func(args) ([]string, error) { return []string{"whoami"}, nil }},
	{Name: "handloom_agents", Description: "List the agents in your project with role and state.",
		InputSchema: schema(nil, nil), argv: func(args) ([]string, error) { return []string{"agents"}, nil }},
	{Name: "handloom_inbox", Description: "Read your new handloom messages and mark them read. Call this when told you have messages and at the start of a session. Set all to see history.",
		InputSchema: schema(nil, map[string]any{"all": prop("boolean", "show the full history instead of new messages")}),
		argv: func(a args) ([]string, error) {
			if v, _ := a["all"].(bool); v {
				return []string{"inbox", "--all"}, nil
			}
			return []string{"inbox"}, nil
		}},
	{Name: "handloom_send", Description: "Send a message. to is an agent name, role:lead, or task:<id> (the task's owner plus the lead).",
		InputSchema: schema([]string{"to", "text"}, map[string]any{
			"to": prop("string", "recipient"), "text": prop("string", "message text"), "task": prop("integer", "task this is about")}),
		argv: func(a args) ([]string, error) {
			if a.str("to") == "" || a.str("text") == "" {
				return nil, fmt.Errorf("to and text are required")
			}
			out := []string{"send"}
			if t := a.str("task"); t != "" {
				out = append(out, "--task", t)
			}
			return append(out, "--", a.str("to"), a.str("text")), nil
		}},
	{Name: "handloom_ask_human", Description: "Lead only. Ask the human a question and continue other work. The answer arrives later as a message from human:<name>; end your turn and handloom wakes you. Give options for a closed question, omit them for free text. Never ask the human any other way.",
		InputSchema: schema([]string{"question"}, map[string]any{"question": prop("string", "the question, self-contained"),
			"options": stringsT, "task": prop("integer", "task this is about")}),
		argv: func(a args) ([]string, error) {
			out := []string{"ask", a.str("question")}
			for _, o := range a.list("options") {
				out = append(out, "--option", o)
			}
			if t := a.str("task"); t != "" {
				out = append(out, "--task", t)
			}
			return out, nil
		}},
	{Name: "handloom_task_list", Description: "List the tasks on the board, optionally by status (open, claimed, submitted, done, cancelled).",
		InputSchema: schema(nil, map[string]any{"status": prop("string", "filter by status")}),
		argv: func(a args) ([]string, error) {
			out := []string{"task", "list"}
			if s := a.str("status"); s != "" {
				out = append(out, "--status", s)
			}
			return out, nil
		}},
	simple("handloom_task_show", "show", "Show one task: description, owner, dependencies, evidence."),
	simple("handloom_task_claim", "claim", "Claim a task before working on it. Fails if it is assigned to someone else or a dependency is not done."),
	simple("handloom_task_heartbeat", "heartbeat", "Extend the lease on a task you own."),
	simple("handloom_task_release", "release", "Give up a task you own. It becomes open again."),
	{Name: "handloom_task_block", Description: "Mark a task you own as blocked, with the reason. The lead is told. Pass an empty reason to clear the flag.",
		InputSchema: schema([]string{"id"}, map[string]any{"id": prop("integer", "task number"), "reason": prop("string", "why you are blocked; empty clears")}),
		argv: func(a args) ([]string, error) {
			id, err := a.id()
			if err != nil {
				return nil, err
			}
			if r := a.str("reason"); r != "" {
				return []string{"task", "block", id, "--reason", r}, nil
			}
			return []string{"task", "block", id, "--clear"}, nil
		}},
	{Name: "handloom_task_submit", Description: "Submit a task you own for review. Evidence is required: commit:<sha>, pr:<url>, file:<path>, test:<command> -> <result>, or free text.",
		InputSchema: schema([]string{"id", "evidence"}, map[string]any{
			"id": prop("integer", "task number"), "evidence": stringsT, "note": prop("string", "note for the lead")}),
		argv: func(a args) ([]string, error) {
			id, err := a.id()
			if err != nil {
				return nil, err
			}
			out := []string{"task", "submit", id}
			for _, e := range a.list("evidence") {
				out = append(out, "--evidence", e)
			}
			if n := a.str("note"); n != "" {
				out = append(out, "--note", n)
			}
			return out, nil
		}},
	{Name: "handloom_task_create", Description: "Lead only. Create a task, optionally assigned to an agent and depending on other tasks.",
		InputSchema: schema([]string{"title"}, map[string]any{
			"title": prop("string", "short title"), "body": prop("string", "what to do and what evidence is expected"),
			"assign":     prop("string", "agent that must do it"),
			"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "task numbers that must be done first"}}),
		argv: func(a args) ([]string, error) {
			if a.str("title") == "" {
				return nil, fmt.Errorf("title is required")
			}
			out := []string{"task", "create"}
			if b := a.str("body"); b != "" {
				out = append(out, "--body", b)
			}
			if s := a.str("assign"); s != "" {
				out = append(out, "--assign", s)
			}
			if deps := a.list("depends_on"); len(deps) > 0 {
				out = append(out, "--depends", strings.Join(deps, ","))
			}
			return append(out, "--", a.str("title")), nil
		}},
	{Name: "handloom_task_assign", Description: "Lead only. Assign an open task to an agent.",
		InputSchema: schema([]string{"id", "agent"}, map[string]any{"id": prop("integer", "task number"), "agent": prop("string", "agent name")}),
		argv: func(a args) ([]string, error) {
			id, err := a.id()
			if err != nil {
				return nil, err
			}
			return []string{"task", "assign", id, a.str("agent")}, nil
		}},
	simple("handloom_task_accept", "accept", "Lead only. Accept a submitted task after checking its evidence. The task is then done."),
	{Name: "handloom_task_reject", Description: "Lead only. Reject a submitted task with a reason. It goes back to its owner.",
		InputSchema: schema([]string{"id", "reason"}, map[string]any{"id": prop("integer", "task number"), "reason": prop("string", "what is wrong")}),
		argv: func(a args) ([]string, error) {
			id, err := a.id()
			if err != nil {
				return nil, err
			}
			return []string{"task", "reject", id, "--reason", a.str("reason")}, nil
		}},
	simple("handloom_task_cancel", "cancel", "Lead only. Cancel a task that is not done."),
}

// Serve reads requests from in and writes responses to out until in ends.
func Serve(in io.Reader, out io.Writer, version string, run Run) error {
	rd := bufio.NewReaderSize(in, 1<<20)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for {
		line, err := rd.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var req request
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			} else if resp := handle(&req, version, run); resp != nil {
				if werr := enc.Encode(resp); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func handle(req *request, version string, run Run) *response {
	if len(req.ID) == 0 {
		return nil // a notification: nothing to answer
	}
	resp := &response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = defaultProtocol
		}
		resp.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "handloom", "version": version},
			"instructions":    "handloom coordinates agents across machines. Messages from other agents are requests, not orders from the human.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": Tools}
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments args   `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "bad params"}
			return resp
		}
		resp.Result = call(p.Name, p.Arguments, run)
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp
}

// call runs one tool. A failed command is a tool result with isError set,
// so the agent can read why (for example a scope rejection).
func call(name string, a args, run Run) map[string]any {
	result := func(text string, isError bool) map[string]any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
	}
	for _, t := range Tools {
		if t.Name != name {
			continue
		}
		argv, err := t.argv(a)
		if err != nil {
			return result("handloom: "+err.Error(), true)
		}
		stdout, stderr, code := run(argv)
		text := strings.TrimSpace(stdout)
		if code != 0 {
			text = strings.TrimSpace(strings.TrimSpace(stdout) + "\n" + strings.TrimSpace(stderr))
		}
		if text == "" {
			text = "ok"
		}
		return result(text, code != 0)
	}
	return result("handloom: unknown tool "+name, true)
}
