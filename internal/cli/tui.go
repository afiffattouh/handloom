package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"

	"handloom/internal/client"
	"handloom/internal/tui"
)

// tui opens the full-screen console for the person whose token is in
// HANDLOOM_TOKEN or was saved by `handloom login`. It never uses the link: the console is for people.
func (e *env) tui(args []string) error {
	fs := e.flags("tui")
	if _, err := fs.need(args, 0, 0, "tui"); err != nil {
		return err
	}
	hub, tok := consoleLogin()
	if hub == "" || tok == "" {
		return fmt.Errorf("the console needs to know who you are: run `handloom login https://your-hub` and paste your personal token (Settings in the web UI, then \"Create a new token\"), or set HANDLOOM_HUB and HANDLOOM_TOKEN")
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("the console needs a terminal; for scripts use the commands (handloom digest, handloom metrics, ...)")
	}
	c := client.Direct(hub, tok)
	c.HTTP.Timeout = 10 * time.Second
	return tui.Run(tui.Options{API: tui.NewHTTP(c), Hub: hub})
}

// wantsConsole is true for a bare `handloom` typed in a terminal by someone
// who has set up a hub and a token; everyone else still gets the usage text.
func wantsConsole(stdout any) bool {
	f, ok := stdout.(*os.File)
	if !ok || !isatty.IsTerminal(f.Fd()) || !isatty.IsTerminal(os.Stdin.Fd()) {
		return false
	}
	hub, tok := consoleLogin()
	return hub != "" && tok != ""
}
