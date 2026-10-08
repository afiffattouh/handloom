package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"handloom/internal/api"
)

// The owner's settings and the rest of what the web UI can do, as commands.

// prices lists, sets and removes the price of a model (dollars per million tokens).
func (e *env) prices(args []string) error {
	fs := e.flags("prices")
	in := fs.Float64("input", 0, "set: dollars per million input tokens")
	out := fs.Float64("output", 0, "set: dollars per million output tokens")
	cr := fs.Float64("cache-read", 0, "set: dollars per million tokens read from a cache")
	cw := fs.Float64("cache-write", 0, "set: dollars per million tokens written to a cache")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		var list []api.Price
		if err := c.Get("/v1/prices", &list); err != nil {
			return err
		}
		e.print(list, func() {
			if len(list) == 0 {
				fmt.Fprintln(e.out, "No prices yet: with none, usage shows tokens but no cost.\nSet one: handloom prices set <model> --input 3 --output 15")
			}
			for _, p := range list {
				fmt.Fprintf(e.out, "%-32s in %-8g out %-8g cache read %-8g cache write %-8g (USD per million tokens)\n", p.Model, p.Input, p.Output, p.CacheRead, p.CacheWrite)
			}
		})
		return nil
	case pos[0] == "set" && len(pos) == 2:
		if err := c.Post("/v1/prices", api.Price{Model: pos[1], Input: *in, Output: *out, CacheRead: *cr, CacheWrite: *cw}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Price of %s saved.\n", pos[1])
		return nil
	case pos[0] == "remove" && len(pos) == 2:
		if err := c.Post("/v1/prices/delete", api.Price{Model: pos[1]}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Price of %s removed.\n", pos[1])
		return nil
	}
	return usageErr("usage: handloom prices | prices set <model> --input N --output N [--cache-read N] [--cache-write N] | prices remove <model>")
}

// people lists, adds and manages the people who can sign in.
func (e *env) people(args []string) error {
	fs := e.flags("people")
	role := fs.String("role", "", "add or role: member or viewer")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	show := func(r api.PersonResp, what string) {
		e.print(r, func() {
			fmt.Fprintf(e.out, "%s %s (%s). Send them this link; it works once, for a limited time, and is not shown again:\n%s\n", what, r.Name, r.Role, r.InviteURL)
		})
	}
	switch {
	case len(pos) == 0:
		var list []api.Person
		if err := c.Get("/v1/people", &list); err != nil {
			return err
		}
		e.print(list, func() {
			for _, p := range list {
				login := "has signed in"
				if !p.WebLogin {
					login = "has not set a password yet"
				}
				fmt.Fprintf(e.out, "%-20s %-7s %s\n", p.Name, p.Role, login)
			}
		})
		return nil
	case pos[0] == "add" && len(pos) == 2:
		var r api.PersonResp
		if err := c.Post("/v1/people", api.PersonReq{Name: pos[1], Role: *role}, &r); err != nil {
			return err
		}
		show(r, "Added")
		return nil
	case pos[0] == "invite" && len(pos) == 2:
		var r api.PersonResp
		if err := c.Post("/v1/people/"+pos[1]+"/invite", nil, &r); err != nil {
			return err
		}
		show(r, "New invite for")
		return nil
	case pos[0] == "role" && len(pos) == 2 && *role != "":
		if err := c.Post("/v1/people/"+pos[1]+"/role", api.PersonReq{Role: *role}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s is now a %s (and was signed out).\n", pos[1], *role)
		return nil
	}
	return usageErr("usage: handloom people | people add <name> [--role member|viewer] | people invite <name> | people role <name> --role member|viewer")
}

// notifications shows and changes the phone-push settings.
func (e *env) notifications(args []string) error {
	fs := e.flags("notifications")
	u := fs.String("url", "", "set: the ntfy server, such as https://ntfy.sh")
	topic := fs.String("topic", "", "set: the topic (a long random name)")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		var n api.Notifications
		if err := c.Get("/v1/notifications", &n); err != nil {
			return err
		}
		e.print(n, func() {
			if n.NtfyURL == "" {
				fmt.Fprintln(e.out, "Phone notifications are not set up here.")
			} else {
				fmt.Fprintf(e.out, "ntfy server %s, topic %s\n", n.NtfyURL, n.NtfyTopic)
			}
			if n.FromEnvironment {
				fmt.Fprintln(e.out, "The hub's environment also configures notifications; those keep working.")
			}
		})
		return nil
	case pos[0] == "set":
		if err := c.Post("/v1/notifications", api.Notifications{NtfyURL: *u, NtfyTopic: *topic}, nil); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "Saved.")
		return nil
	case pos[0] == "test":
		if err := c.Post("/v1/notifications/test", nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "Test notification sent.")
		return nil
	}
	return usageErr("usage: handloom notifications | notifications set --url U --topic T | notifications test")
}

// token makes a new personal API token for the person the current token belongs to.
func (e *env) token(args []string) error {
	fs := e.flags("token")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "new" {
		return usageErr("usage: handloom token new   (a new personal API token; the one you are using stops working)")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var r api.TokenResp
	if err := c.Post("/v1/me/token", nil, &r); err != nil {
		return err
	}
	e.print(r, func() {
		fmt.Fprintf(e.out, "New token for %s (shown once; the token you used for this command no longer works):\n%s\n", r.Name, r.Token)
	})
	return nil
}

// screen shows the end of an agent's terminal: it asks the agent's machine for it and waits a few seconds.
func (e *env) screen(args []string) error {
	fs := e.flags("screen")
	wait := fs.Duration("wait", 15*time.Second, "how long to wait for the machine to answer")
	pos, err := fs.need(args, 1, 1, "screen <agent> [--wait 15s]")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*wait)
	for {
		var s api.Screen
		if err := c.Get("/v1/agents/"+pos[0]+"/screen", &s); err != nil {
			return err
		}
		if !s.Pending {
			e.print(s, func() {
				fmt.Fprintln(e.out, s.Text)
				fmt.Fprintf(e.err, "(read %ds ago; held in the hub's memory for a minute, never stored)\n", s.AgeSeconds)
			})
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the machine has not sent %s's screen yet: is its link running?", pos[0])
		}
		time.Sleep(2 * time.Second)
	}
}

// metrics prints the command center's numbers.
func (e *env) metrics(args []string) error {
	fs := e.flags("metrics")
	rng := fs.String("range", "7d", "24h, 7d or 30d")
	if _, err := fs.need(args, 0, 0, "metrics [--range 24h|7d|30d]"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var m struct {
		Range string `json:"range"`
		Needs struct {
			Count int `json:"count"`
		} `json:"needs_you"`
		Agents struct{ Total, Working, Idle, Blocked, Offline int }
		Tasks  struct{ Claimed, Submitted, Open int }
		Checks struct {
			Pct float64
			N   int
		}
		TaskTime struct {
			Value time.Duration
			N     int
		} `json:"task_time"`
		Usage struct {
			Tokens int64   `json:"tokens"`
			Cost   float64 `json:"cost_usd"`
			Priced int64   `json:"priced_tokens"`
		} `json:"usage"`
		Insights []struct{ Sev, Title, Basis string }
	}
	if err := c.Get("/v1/metrics?range="+*rng, &m); err != nil {
		return err
	}
	e.print(m, func() {
		fmt.Fprintf(e.out, "Last %s\n", m.Range)
		fmt.Fprintf(e.out, "  needs you        %d\n", m.Needs.Count)
		fmt.Fprintf(e.out, "  agents           %d working of %d (%d idle, %d blocked, %d offline)\n", m.Agents.Working, m.Agents.Total, m.Agents.Idle, m.Agents.Blocked, m.Agents.Offline)
		fmt.Fprintf(e.out, "  tasks in flight  %d being worked, %d submitted, %d waiting\n", m.Tasks.Claimed, m.Tasks.Submitted, m.Tasks.Open)
		if m.Checks.N > 0 {
			fmt.Fprintf(e.out, "  checks passed    %.0f%% of %d\n", m.Checks.Pct, m.Checks.N)
		} else {
			fmt.Fprintln(e.out, "  checks passed    no checks in this range")
		}
		if m.TaskTime.N > 0 {
			fmt.Fprintf(e.out, "  median task time %s over %d tasks\n", m.TaskTime.Value.Round(time.Second), m.TaskTime.N)
		}
		switch {
		case m.Usage.Tokens == 0:
			fmt.Fprintln(e.out, "  usage            none reported yet")
		case m.Usage.Priced == 0:
			fmt.Fprintf(e.out, "  usage            %d tokens (no price for these models: handloom prices set ...)\n", m.Usage.Tokens)
		default:
			fmt.Fprintf(e.out, "  usage            %d tokens, about $%.2f on the %d%% that have a price\n", m.Usage.Tokens, m.Usage.Cost, m.Usage.Priced*100/m.Usage.Tokens)
		}
		for _, i := range m.Insights {
			fmt.Fprintf(e.out, "  [%s] %s (based on: %s)\n", i.Sev, i.Title, i.Basis)
		}
	})
	return nil
}

var _ = os.Stderr
var _ = strings.Join
