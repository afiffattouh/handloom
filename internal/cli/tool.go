package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/setting"
)

// Tools from add-ons, for the agent that calls them: `handloom tool list` and `handloom tool call`. They talk
// to the tool gateway in the link, on the same local socket as the other agent verbs, and the helper is known
// by its tool token (HANDLOOM_TOOL_TOKEN), never by a name it gives.

// toolConn is the link's socket with the helper's tool token. It never goes to the hub, whatever
// HANDLOOM_TOKEN says: a tool call is the helper's own act, and the link decides it.
func (e *env) toolConn() (*client.Client, error) {
	if e.dial != nil {
		return nil, fmt.Errorf("tools are called from a helper on its computer, not through a hub")
	}
	token := setting.Get("TOOL_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("this command works inside a helper that Handloom started, which has HANDLOOM_TOOL_TOKEN set; there is none here, so no tool can be called")
	}
	sock := client.SocketPath()
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("no link socket at %s: the link on this computer is not running", sock)
	}
	c := client.Socket(sock, "")
	c.ToolToken = token
	return c, nil
}

func (e *env) toolCmd(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: handloom tool list | call ADDON.TOOL [--arg name=value ...] [--args '{...}'] | promote FILE --to PATH")
	}
	switch args[0] {
	case "list":
		return e.toolList(args[1:])
	case "call":
		return e.toolCall(args[1:])
	case "promote":
		return e.toolPromote(args[1:])
	}
	return usageErr("unknown tool command %q; the commands are list, call and promote", args[0])
}

func (e *env) toolList(args []string) error {
	fs := e.flags("tool list")
	if _, err := fs.need(args, 0, 0, "tool list"); err != nil {
		return err
	}
	c, err := e.toolConn()
	if err != nil {
		return err
	}
	var list api.ToolList
	if err := c.Get("/local/tools", &list); err != nil {
		return err
	}
	e.print(list, func() {
		if len(list.Tools) == 0 && len(list.Unavailable) == 0 {
			fmt.Fprintln(e.out, "No tools from add-ons for this helper.")
			return
		}
		for _, t := range list.Tools {
			fmt.Fprintf(e.out, "%s.%s: %s\n", t.Addon, t.Tool, t.Summary)
			for _, f := range requiredFirst(t.Input) {
				need := ""
				if f.Required {
					need = ", required"
				}
				fmt.Fprintf(e.out, "    %s (%s%s) %s\n", f.Name, typeWords(f.Type), need, f.Summary)
			}
			fmt.Fprintf(e.out, "    call it: handloom tool call %s.%s%s\n", t.Addon, t.Tool, exampleArgs(t))
		}
		for _, u := range list.Unavailable {
			fmt.Fprintf(e.out, "%s.%s is not available: %s %s\n", u.Addon, u.Tool, u.Why, notAvailableWords)
		}
	})
	return nil
}

const notAvailableWords = "Do not guess the answer."

// typeWords is a field's type in the words an agent reads: the manifest says string, number or boolean.
func typeWords(t string) string {
	switch t {
	case "number":
		return "a number"
	case "boolean":
		return "true or false"
	}
	return "text"
}

// requiredFirst puts the arguments a call cannot do without before the optional ones, keeping the order otherwise.
func requiredFirst(in []api.ToolField) []api.ToolField {
	out := append([]api.ToolField(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Required && !out[j].Required })
	return out
}

func exampleArgs(t api.SpawnTool) string {
	var out string
	for _, f := range t.Input {
		if f.Required {
			out += " --arg " + f.Name + "=..."
		}
	}
	return out
}

// splitJSONArgs takes the arguments object out of a command line. `--json '{...}'` (the form the design
// names) is the arguments when the next word starts with a brace; a bare --json still asks for output a
// program can read. `--args` is the unambiguous spelling of the same thing.
func splitJSONArgs(args []string) (rest []string, obj string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		val, has := "", false
		switch {
		case a == "--args" || a == "-args":
			if i+1 >= len(args) {
				return nil, "", usageErr("--args needs a JSON object, for example --args '{\"text\":\"hi\"}'")
			}
			val, has = args[i+1], true
			i++
		case strings.HasPrefix(a, "--args="):
			val, has = strings.TrimPrefix(a, "--args="), true
		case (a == "--json" || a == "-json") && i+1 < len(args) && strings.HasPrefix(strings.TrimSpace(args[i+1]), "{"):
			val, has = args[i+1], true
			i++
		case strings.HasPrefix(a, "--json={"):
			val, has = strings.TrimPrefix(a, "--json="), true
		}
		if !has {
			rest = append(rest, a)
			continue
		}
		if obj != "" {
			return nil, "", usageErr("give the arguments as JSON once")
		}
		obj = val
	}
	return rest, obj, nil
}

func (e *env) toolCall(args []string) error {
	args, obj, err := splitJSONArgs(args)
	if err != nil {
		return err
	}
	fs := e.flags("tool call")
	var kv listFlag
	fs.Var(&kv, "arg", "an argument, name=value (repeat for more)")
	pos, err := fs.need(args, 1, 1, "tool call ADDON.TOOL [--arg name=value ...] [--args '{...}'] [--json]")
	if err != nil {
		return err
	}
	name := pos[0]
	if !strings.Contains(name, ".") {
		return usageErr("name the tool as ADDON.TOOL, for example echo.say")
	}
	c, err := e.toolConn()
	if err != nil {
		return err
	}
	call := api.ToolCallReq{Tool: name, Args: map[string]any{}}
	if obj != "" {
		if err := json.Unmarshal([]byte(obj), &call.Args); err != nil || call.Args == nil {
			return usageErr("the arguments are not a JSON object: %v", err)
		}
	}
	if len(kv) > 0 {
		types := e.argTypes(c, name)
		for _, s := range kv {
			k, v, ok := strings.Cut(s, "=")
			if !ok || k == "" {
				return usageErr("an argument is written name=value, not %q", s)
			}
			if _, dup := call.Args[k]; dup {
				return usageErr("the argument %s is given twice", k)
			}
			val, err := typedArg(types[k], k, v)
			if err != nil {
				return usageErr("%v", err)
			}
			call.Args[k] = val
		}
	}
	// A call can take as long as the tool's limit, up to five minutes, and the link waits for it.
	var resp api.ToolCallResp
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if err := c.Do(ctx, "POST", "/local/tools/call", call, &resp); err != nil {
		return err
	}
	e.print(resp, func() {
		if !resp.OK {
			return
		}
		fmt.Fprintln(e.out, strings.TrimRight(resp.Text, "\n"))
		for _, f := range resp.Files {
			fmt.Fprintf(e.out, "Saved file: %s\n", f)
		}
	})
	if !resp.OK {
		// The code word first, so a program (or a person) can tell the cases apart; the sentence says what to do.
		return fmt.Errorf("%s: %s", resp.Code, resp.Text)
	}
	return nil
}

// argTypes is the declared type of each argument of a tool, from the helper's own tool list. A tool it does
// not list has none, and its arguments go as text; the gateway refuses the call anyway.
func (e *env) argTypes(c *client.Client, name string) map[string]string {
	var list api.ToolList
	out := map[string]string{}
	if c.Get("/local/tools", &list) != nil {
		return out
	}
	for _, t := range list.Tools {
		if t.Addon+"."+t.Tool == name {
			for _, f := range t.Input {
				out[f.Name] = f.Type
			}
		}
	}
	return out
}

func typedArg(typ, name, v string) (any, error) {
	switch typ {
	case "number":
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("%s must be a number, not %q", name, v)
		}
		return n, nil
	case "boolean":
		switch strings.ToLower(v) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		}
		return nil, fmt.Errorf("%s must be true or false, not %q", name, v)
	}
	return v, nil
}

// toolsSection is what a helper is told about its tools, written into its instructions. A helper with no
// granted tools and none unavailable gets nothing.
func toolsSection(list api.ToolList) string {
	if len(list.Tools) == 0 && len(list.Unavailable) == 0 {
		return ""
	}
	var b strings.Builder
	if len(list.Tools) > 0 {
		b.WriteString("## Tools from add-ons\n\n")
		b.WriteString("You may call these tools. Run `handloom tool call ADDON.TOOL --arg name=value` (add `--json` for output a program can read, or `handloom tool list` to see them again). ")
		b.WriteString("The answer is printed; a file the tool saved is named after it, and it is not part of the project until you run `handloom tool promote FILE --to PATH`. Each tool may be called a limited number of times in this job. ")
		b.WriteString("If a call is refused or fails, the tool is not available for this job: say so, and do not guess what it would have answered.\n\n")
		tools := append([]api.SpawnTool(nil), list.Tools...)
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Addon+"."+tools[i].Tool < tools[j].Addon+"."+tools[j].Tool
		})
		for _, t := range tools {
			fmt.Fprintf(&b, "- `%s.%s`: %s", t.Addon, t.Tool, strings.TrimSpace(t.Summary))
			for i, f := range requiredFirst(t.Input) {
				sep := " Takes "
				if i > 0 {
					sep = "; "
				}
				need := ""
				if f.Required {
					need = ", required"
				}
				fmt.Fprintf(&b, "%s`%s` (%s%s)", sep, f.Name, typeWords(f.Type), need)
				if s := strings.TrimSpace(f.Summary); s != "" {
					fmt.Fprintf(&b, ", %s", strings.ToLower(s[:1])+s[1:])
				}
			}
			if len(t.Input) > 0 {
				b.WriteString(".")
			}
			fmt.Fprintf(&b, " Call it: `handloom tool call %s.%s%s`. At most %d calls.\n", t.Addon, t.Tool, exampleArgs(t), t.MaxCalls)
		}
	}
	if len(list.Unavailable) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## Tools that are not available\n\n")
		b.WriteString("Do not call these and do not guess what they would have answered.\n\n")
		un := append([]api.ToolUnavailable(nil), list.Unavailable...)
		sort.Slice(un, func(i, j int) bool { return un[i].Addon+"."+un[i].Tool < un[j].Addon+"."+un[j].Tool })
		for _, u := range un {
			fmt.Fprintf(&b, "- `%s.%s`: this tool is not available, do not guess the answer. %s\n", u.Addon, u.Tool, u.Why)
		}
	}
	return strings.TrimSpace(b.String())
}

// helperToolList asks the link what this helper's tools are. A helper started without a tool token, or a link
// that cannot answer, has none: starting the helper does not depend on it.
func (e *env) helperToolList() api.ToolList {
	var list api.ToolList
	c, err := e.toolConn()
	if err != nil {
		return list
	}
	if err := c.Get("/local/tools", &list); err != nil {
		fmt.Fprintf(e.err, "handloom: could not read this helper's tools from the link (%v); its instructions will not list any\n", err)
		return api.ToolList{}
	}
	return list
}

// toolPromote copies a file a tool saved into the project, so it becomes a change like any other.
func (e *env) toolPromote(args []string) error {
	fs := e.flags("tool promote")
	to := fs.String("to", "", "where to put the file in the project, relative to the project folder")
	force := fs.Bool("force", false, "replace the file if it already exists")
	pos, err := fs.need(args, 1, 1, "tool promote FILE --to PATH [--force]")
	if err != nil {
		return err
	}
	if *to == "" {
		return usageErr("say where the file goes: handloom tool promote FILE --to PATH")
	}
	c, err := e.toolConn()
	if err != nil {
		return err
	}
	var resp api.ToolPromoteResp
	if err := c.Do(context.Background(), "POST", "/local/tools/promote", api.ToolPromoteReq{File: pos[0], To: *to, Force: *force}, &resp); err != nil {
		return err
	}
	e.print(resp, func() { fmt.Fprintln(e.out, resp.Message) })
	return nil
}
