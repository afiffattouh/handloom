package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/setting"
)

// The console's own sign-in. A person's token is kept in one private file so
// `handloom tui` works without exporting variables. Only the console and
// `login` read it: the agent verbs and the admin commands still take their
// token from the environment, so an agent's shell never picks up a person's
// token by accident.

func loginFile() string {
	if p := os.Getenv("HANDLOOM_HUMAN_ENV"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "handloom", "human.env")
}

// readLogin returns the hub and token saved by `handloom login`.
func readLogin() (hub, token string) {
	b, err := os.ReadFile(loginFile())
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "HANDLOOM_HUB":
			hub = strings.TrimSpace(v)
		case "HANDLOOM_TOKEN":
			token = strings.TrimSpace(v)
		}
	}
	return hub, token
}

// consoleLogin is who the console signs in as: the environment first, then the
// saved login. An agent's shell (HANDLOOM_AGENT set) never uses the saved one.
func consoleLogin() (hub, token string) {
	hub, token = setting.Get("HUB"), setting.Get("TOKEN")
	if (hub == "" || token == "") && setting.Get("AGENT") == "" {
		h, t := readLogin()
		if hub == "" {
			hub = h
		}
		if token == "" {
			token = t
		}
	}
	return hub, token
}

// login saves a person's token after checking that it works and belongs to a person.
func (e *env) login(args []string) error {
	fs := e.flags("login")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageErr("usage: handloom login [HUB_URL]   (asks for your personal token: Settings in the web UI, or `handloom token new`)")
	}
	hub := setting.Get("HUB")
	if len(pos) == 1 {
		hub = pos[0]
	}
	if hub == "" {
		hub, _ = readLogin()
	}
	if hub == "" {
		return usageErr("which hub? usage: handloom login https://your-hub")
	}
	tok := setting.Get("TOKEN")
	if tok == "" {
		fmt.Fprint(e.err, "Your personal token (Settings in the web UI, then \"Create a new token\"): ")
		if f, ok := os.Stdin, isatty.IsTerminal(os.Stdin.Fd()); ok {
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(e.err)
			if err != nil {
				return err
			}
			tok = string(b)
		} else {
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			tok = line
		}
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return fmt.Errorf("no token given")
	}
	c := client.Direct(hub, tok)
	c.HTTP.Timeout = 10 * time.Second
	var who api.WhoAmI
	if err := c.Get("/v1/whoami", &who); err != nil {
		return fmt.Errorf("the hub did not accept that token: %w", err)
	}
	if who.Kind != "human" {
		return fmt.Errorf("that is a %s token; the console needs a person's token (Settings in the web UI, or `handloom token new`)", who.Kind)
	}
	path := loginFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := "# Written by `handloom login`. Your own sign-in for the console; keep it private.\nHANDLOOM_HUB=" + strings.TrimRight(hub, "/") + "\nHANDLOOM_TOKEN=" + tok + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	fmt.Fprintf(e.out, "Signed in to %s as %s (%s). Saved in %s (private).\nOpen the console with: handloom tui\n", strings.TrimRight(hub, "/"), who.Name, who.Role, path)
	return nil
}

// logout forgets the saved sign-in.
func (e *env) logout(args []string) error {
	if _, err := e.flags("logout").need(args, 0, 0, "logout"); err != nil {
		return err
	}
	if err := os.Remove(loginFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintln(e.out, "Signed out of the console on this machine. The token itself still works until you make a new one.")
	return nil
}
