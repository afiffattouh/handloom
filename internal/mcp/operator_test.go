package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func operatorTalk(t *testing.T, approvals bool, run Run, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := ServeOperator(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, "test", run, approvals); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response %q", line)
		}
		resps = append(resps, m)
	}
	return resps
}

func names(ts []tool) map[string]tool {
	m := map[string]tool{}
	for _, x := range ts {
		m[x.Name] = x
	}
	return m
}

func TestApprovalsAreNotToolsUnlessThePersonTurnsThemOn(t *testing.T) {
	plain := names(OperatorTools(false))
	for _, n := range []string{"handloom_task_accept", "handloom_task_reject", "handloom_answer", "handloom_job_close"} {
		if _, ok := plain[n]; ok {
			t.Errorf("%s is offered without --allow-approvals", n)
		}
	}
	for _, n := range []string{"handloom_digest", "handloom_job_new", "handloom_send", "handloom_screen", "handloom_metrics"} {
		if _, ok := plain[n]; !ok {
			t.Errorf("%s is missing", n)
		}
	}
	with := names(OperatorTools(true))
	for _, n := range []string{"handloom_task_accept", "handloom_task_reject", "handloom_answer", "handloom_job_close"} {
		x, ok := with[n]
		if !ok {
			t.Errorf("%s is missing with --allow-approvals", n)
			continue
		}
		if x.Annotations["destructiveHint"] != true || !strings.HasPrefix(x.Description, "Approval:") {
			t.Errorf("%s must be marked as an approval so a client asks every time: %+v", n, x.Annotations)
		}
	}
	// Reads say so, so a client may let them through; nothing that changes things claims to be read-only.
	for n, x := range plain {
		ro := x.Annotations["readOnlyHint"] == true
		changes := n == "handloom_job_new" || n == "handloom_send"
		if changes == ro {
			t.Errorf("%s: read-only hint is %v", n, ro)
		}
	}
}

func TestOperatorToolsRunTheMatchingCommands(t *testing.T) {
	var got [][]string
	run := func(argv []string) (string, string, int) { got = append(got, argv); return "ok\n", "", 0 }
	call := func(name, args string) {
		operatorTalk(t, true, run, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
	}
	call("handloom_digest", `{}`)
	call("handloom_job", `{"id":3}`)
	call("handloom_tasks", `{"job":3,"status":"submitted"}`)
	call("handloom_job_new", `{"title":"Fix it","lead_profile":"lead","brief":"do it","device":"GB10","repo":"/r","verify":"./check.sh"}`)
	call("handloom_screen", `{"agent":"coder-1"}`)
	call("handloom_task_accept", `{"id":"#7"}`)
	call("handloom_answer", `{"id":2,"answer":"yes"}`)
	call("handloom_job_close", `{"id":3,"cancel":true}`)
	want := []string{
		"digest", "job show 3", "task list --job 3 --status submitted",
		"job new Fix it --lead-profile lead --body do it --device GB10 --repo /r --verify ./check.sh",
		"screen coder-1", "task accept 7", "answer 2 yes", "job close 3 --cancel",
	}
	if len(got) != len(want) {
		t.Fatalf("ran %d commands, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if strings.Join(got[i], " ") != w {
			t.Errorf("command %d: %q, want %q", i, strings.Join(got[i], " "), w)
		}
	}
	// Without the flag the approval tools do not exist, even if a client asks for them by name.
	r := operatorTalk(t, false, run, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_task_accept","arguments":{"id":7}}}`)
	res := r[0]["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "unknown tool") {
		t.Fatalf("an approval called by name: %v", res)
	}
	if len(got) != len(want) {
		t.Fatal("the approval command ran")
	}
	// A call with missing arguments is a tool error, not a crash.
	r = operatorTalk(t, false, run, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"handloom_job_new","arguments":{"title":"x"}}}`)
	if r[0]["result"].(map[string]any)["isError"] != true {
		t.Fatal("a job without a lead profile should be refused")
	}
}

func TestOperatorInstructionsSayWhatItCannotDo(t *testing.T) {
	r := operatorTalk(t, false, nil, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	ins := r[0]["result"].(map[string]any)["instructions"].(string)
	if !strings.Contains(ins, "cannot accept work") {
		t.Fatalf("instructions: %s", ins)
	}
}
