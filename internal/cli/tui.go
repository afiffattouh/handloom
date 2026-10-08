package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"

	"handloom/internal/client"
	"handloom/internal/setting"
	"handloom/internal/tui"
)

// tui opens the full-screen console for the person whose token is in
// HANDLOOM_TOKEN. It never uses the link: the console is for people.
func (e *env) tui(args []string) error {
	fs := e.flags("tui")
	if _, err := fs.need(args, 0, 0, "tui"); err != nil {
		return err
	}
	hub, tok := setting.Get("HUB"), setting.Get("TOKEN")
	if hub == "" || tok == "" {
		return fmt.Errorf("the console needs the hub address and your token: set HANDLOOM_HUB and HANDLOOM_TOKEN (make a token with `handloom token new`, or in the web UI under Settings)")
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
	return ok && isatty.IsTerminal(f.Fd()) && isatty.IsTerminal(os.Stdin.Fd()) &&
		setting.Get("HUB") != "" && setting.Get("TOKEN") != ""
}
