package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// talk sends requests (one JSON value per line) and returns the responses.
func talk(t *testing.T, run Run, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, "test", run); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestHandshakeAndToolList(t *testing.T) {
	resps := talk(t, nil,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"three","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`not json`,
	)
	if len(resps) != 5 { // the notification gets no answer
		t.Fatalf("%d responses, want 5: %v", len(resps), resps)
	}
	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["serverInfo"].(map[string]any)["name"] != "handloom" {
		t.Fatalf("initialize: %v", init)
	}
	if _, ok := init["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("no tools capability: %v", init)
	}
	if resps[2]["id"] != "three" {
		t.Fatalf("id not echoed: %v", resps[2])
	}
	tools := resps[2]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if m["description"] == "" || m["inputSchema"].(map[string]any)["type"] != "object" {
			t.Fatalf("bad tool: %v", m)
		}
	}
	for _, want := range []string{"handloom_inbox", "handloom_send", "handloom_task_claim", "handloom_task_submit", "handloom_task_create", "handloom_task_accept"} {
		if !names[want] {
			t.Errorf("tool %s missing", want)
		}
	}
	if resps[3]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", resps[3])
	}
	if resps[4]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("parse error: %v", resps[4])
	}
}

func TestToolCallsBecomeCommandLines(t *testing.T) {
	var got [][]string
	run := func(args []string) (string, string, int) {
		got = append(got, args)
		if args[0] == "task" && args[1] == "accept" {
			return "", "handloom: role worker may not task.manage\n", 1
		}
		return "done\n", "", 0
	}
	call := func(name, arguments string) map[string]any {
		t.Helper()
		r := talk(t, run, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+arguments+`}}`)
		return r[0]["result"].(map[string]any)
	}
	text := func(r map[string]any) string { return r["content"].([]any)[0].(map[string]any)["text"].(string) }

	cases := []struct{ name, arguments, want string }{
		{"handloom_inbox", `{}`, "inbox"},
		{"handloom_inbox", `{"all":true}`, "inbox --all"},
		{"handloom_send", `{"to":"role:lead","text":"--not a flag","task":3}`, "send --task 3 -- role:lead --not a flag"},
		{"handloom_task_claim", `{"id":7}`, "task claim 7"},
		{"handloom_task_show", `{"id":"#7"}`, "task show 7"},
		{"handloom_task_submit", `{"id":2,"evidence":["test:go test -> ok","file:a.txt"],"note":"n"}`,
			"task submit 2 --evidence test:go test -> ok --evidence file:a.txt --note n"},
		{"handloom_task_block", `{"id":2,"reason":"stuck"}`, "task block 2 --reason stuck"},
		{"handloom_task_block", `{"id":2}`, "task block 2 --clear"},
		{"handloom_task_create", `{"title":"T","body":"B","assign":"w","depends_on":[1,2]}`, "task create --body B --assign w --depends 1,2 -- T"},
		{"handloom_task_reject", `{"id":4,"reason":"no"}`, "task reject 4 --reason no"},
		{"handloom_task_list", `{"status":"open"}`, "task list --status open"},
	}
	for _, c := range cases {
		got = nil
		r := call(c.name, c.arguments)
		if len(got) != 1 || strings.Join(got[0], " ") != c.want {
			t.Errorf("%s %s -> %v, want %q", c.name, c.arguments, got, c.want)
		}
		if r["isError"] != false || text(r) != "done" {
			t.Errorf("%s: result %v", c.name, r)
		}
	}

	// A rejected command is a tool result the agent can read, not a protocol error.
	if r := call("handloom_task_accept", `{"id":1}`); r["isError"] != true || !strings.Contains(text(r), "may not task.manage") {
		t.Fatalf("rejected call: %v", r)
	}
	// Bad arguments never reach the command line.
	got = nil
	if r := call("handloom_task_claim", `{"id":"1; rm -rf /"}`); r["isError"] != true || len(got) != 0 {
		t.Fatalf("bad id: %v %v", r, got)
	}
	if r := call("handloom_nope", `{}`); r["isError"] != true {
		t.Fatalf("unknown tool: %v", r)
	}
}

func TestSearchToolsRunTheSearchCommand(t *testing.T) {
	var got [][]string
	run := func(argv []string) (string, string, int) { got = append(got, argv); return "ok", "", 0 }
	call("handloom_search", args{"query": "invoice prefix", "limit": 3.0}, run, Tools)
	call("handloom_search", args{"query": "invoice prefix"}, run, OperatorTools(false))
	if len(got) != 2 || strings.Join(got[0], " ") != "search invoice prefix --limit 3" || strings.Join(got[1], " ") != "search --jobs invoice prefix" {
		t.Fatalf("commands: %v", got)
	}
	if r := call("handloom_search", args{}, run, Tools); r["isError"] != true {
		t.Fatal("an empty search should be a tool error")
	}
}
