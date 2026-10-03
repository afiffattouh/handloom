package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/drivers"
	"handloom/internal/link"
)

// hookInput is the part of an agent CLI's hook JSON that handloom reads. Claude
// Code and Codex send it natively; the Pi, OMP and OpenCode shims build the
// same shape from their own events.
type hookInput struct {
	Event            string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	AgentID          string `json:"agent_id"` // set for subagents
	NotificationType string `json:"notification_type"`
}

// stopDecision is what a Stop hook prints to make the agent continue.
type stopDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// StopReason is the fixed text the end-of-turn hook gives the agent. Like the
// terminal nudge it never contains message content.
func StopReason(unread int) string {
	s := "s"
	if unread == 1 {
		s = ""
	}
	return fmt.Sprintf("You have %d new handloom message%s. Run `handloom inbox` now and act on them before you stop.", unread, s)
}

// hook handles an agent CLI's lifecycle hook: `handloom hook <kind>`, with the
// hook JSON on stdin. A hook must never break the agent, so every failure is
// logged and the exit code is always 0.
func (e *env) hook(args []string) int {
	if len(args) != 1 || adapterSpecs[args[0]] == nil {
		fmt.Fprintln(e.err, "handloom: usage: handloom hook claude|codex|pi|omp|opencode  (hook JSON on stdin)")
		return 2
	}
	if err := e.agentHook(args[0], os.Stdin); err != nil {
		hookLog("%s hook: %v", args[0], err)
	}
	return 0
}

func hookLog(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(client.Home(), "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// wakeTarget says where this agent can be woken: its terminal, or, for an
// agent started with HANDLOOM_HEADLESS=1, a headless turn on its session.
func wakeTarget() string {
	if os.Getenv("HANDLOOM_HEADLESS") != "" {
		return link.HeadlessTarget
	}
	return drivers.Detect()
}

func (e *env) agentHook(kind string, stdin io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err != nil {
		return err
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("bad hook input: %v", err)
	}
	if in.AgentID != "" {
		return nil // a subagent's event, not the agent's own
	}
	name := AgentName(in.Cwd)
	if name == "" {
		return nil // not a handloom agent
	}
	c := client.Socket(client.SocketPath(), name)
	c.HTTP.Timeout = 8 * time.Second
	agentPath := "/v1/agents/" + url.PathEscape(name)
	setState := func(state string) error {
		return c.Post(agentPath+"/state", api.StateReq{State: state}, nil)
	}

	switch in.Event {
	case "SessionStart":
		var a api.Agent
		req := api.RegisterReq{Name: name, Kind: kind, WakeTarget: wakeTarget(), SessionID: in.SessionID, Dir: in.Cwd}
		if err := c.Post("/v1/agents", req, &a); err != nil {
			return err
		}
		if err := setState(api.StateIdle); err != nil {
			return err
		}
		if kind == "claude" {
			// Claude Code adds SessionStart output to the agent's context.
			fmt.Fprintf(e.out, "handloom: you are %s, a %s in project %s. Run `handloom inbox` to check for messages.\n", a.Name, a.Role, a.Project)
		}
	case "UserPromptSubmit":
		return setState(api.StateWorking)
	case "PostToolUse":
		return c.Post("/local/activity", nil, nil)
	case "PermissionRequest":
		return setState(api.StateBlocked)
	case "Notification":
		switch in.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return setState(api.StateBlocked)
		}
	case "Stop":
		var r api.TurnEndResp
		if err := c.Post(agentPath+"/turn-end", nil, &r); err != nil {
			return err
		}
		if r.Block {
			return json.NewEncoder(e.out).Encode(stopDecision{Decision: "block", Reason: StopReason(r.Unread)})
		}
	case "SessionEnd":
		return setState(api.StateOffline)
	}
	return nil
}
