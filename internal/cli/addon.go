package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"handloom/internal/addon"
	"handloom/internal/api"
	"handloom/internal/client"
)

// addonIn is where an install asks "go ahead?"; tests replace it.
var addonIn io.Reader = os.Stdin

// addonRoot is where this computer keeps its add-ons: inside the link's folder.
func addonRoot() string { return filepath.Join(client.Home(), "addons") }

// addonCmd works on the add-ons of this computer. Installing is a local act, so those verbs never talk to a
// hub. The one exception is status, which reads what every computer has reported to the hub.
//
//	handloom addon hash DIR | add DIR [--sha256 H] [--yes] | list | remove NAME | doctor
//	handloom addon status [NAME]
//	handloom addon grants [--profile P] | grant PROFILE ADDON.TOOL [--leaves yes|no] [--max-calls N] | revoke PROFILE ADDON.TOOL
func (e *env) addonCmd(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: handloom addon hash|add|list|remove|doctor|status|grants|grant|revoke")
	}
	rest := args[1:]
	switch args[0] {
	case "status":
		return e.addonStatus(rest)
	case "grants":
		return e.addonGrants(rest)
	case "grant":
		return e.addonGrant(rest)
	case "revoke":
		return e.addonRevoke(rest)
	}
	if e.dial != nil {
		return fmt.Errorf("add-ons are installed on the computer that will run them, not through a hub")
	}
	switch args[0] {
	case "hash":
		return e.addonHash(rest)
	case "add":
		return e.addonAdd(rest)
	case "list":
		return e.addonList(rest)
	case "remove":
		return e.addonRemove(rest)
	case "doctor":
		return e.addonDoctor(rest)
	}
	return usageErr("unknown add-on command %q; the commands are hash, add, list, remove, doctor, status, grants, grant and revoke", args[0])
}

func (e *env) addonHash(args []string) error {
	fs := e.flags("addon hash")
	pos, err := fs.need(args, 1, 1, "addon hash DIR")
	if err != nil {
		return err
	}
	m, ph, mh, err := addon.Load(pos[0])
	if err != nil {
		return err
	}
	e.print(map[string]string{"name": m.Name, "version": m.Version, "package_hash": ph, "manifest_hash": mh}, func() {
		fmt.Fprintln(e.out, ph)
	})
	return nil
}

func (e *env) addonAdd(args []string) error {
	fs := e.flags("addon add")
	pin := fs.String("sha256", "", "the package hash you expect (from the author); the install stops if the folder differs")
	yes := fs.Bool("yes", false, "go ahead without asking, after you have read what it does")
	pos, err := fs.need(args, 1, 1, "addon add DIR [--sha256 HASH] [--yes]")
	if err != nil {
		return err
	}
	src := pos[0]
	m, ph, _, err := addon.Load(src)
	if err != nil {
		return err
	}
	if *pin != "" && !strings.EqualFold(*pin, ph) {
		return fmt.Errorf("the folder's hash is %s, not the %s you gave; nothing was installed", ph, *pin)
	}
	root := addonRoot()
	store := addon.NewStore(root)
	old, hadOld, err := store.Get(m.Name)
	if err != nil {
		return err
	}
	if !e.json {
		e.describeAddon(m, ph)
		if hadOld {
			fmt.Fprintf(e.out, "This replaces %s %s, which was approved here.\n", m.Name, old.Version)
		}
	}
	if *pin == "" && !*yes {
		ok, err := confirmAddon(e, m.Name)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(e.out, "Nothing was installed.")
			return nil
		}
	}
	by := "someone"
	if u, err := user.Current(); err == nil && u.Username != "" {
		by = u.Username
	}
	a, err := addon.Install(root, store, src, ph, by, time.Now())
	if err != nil {
		return err
	}
	sup := addon.NewSupervisor(root, store)
	h := sup.Health(context.Background(), m.Name)
	sup.Close()
	e.print(map[string]any{"name": a.Name, "version": a.Version, "package_hash": a.PackageHash, "healthy": h.Code == addon.CodeOK, "health": h.Error}, func() {
		fmt.Fprintf(e.out, "Installed %s %s on this computer.\n", a.Name, a.Version)
	})
	if h.Code != addon.CodeOK {
		return fmt.Errorf("%s is installed, but it did not pass its health check: %s", m.Name, h.Error)
	}
	if !e.json {
		fmt.Fprintln(e.out, "It answered its health check.")
	}
	return nil
}

// describeAddon prints what a person needs to read before saying yes.
func (e *env) describeAddon(m *addon.Manifest, ph string) {
	fmt.Fprintf(e.out, "Add-on %s %s", m.Name, m.Version)
	if m.Title != "" {
		fmt.Fprintf(e.out, ": %s", m.Title)
	}
	fmt.Fprintln(e.out)
	if m.Summary != "" {
		fmt.Fprintf(e.out, "  %s\n", m.Summary)
	}
	fmt.Fprintf(e.out, "  It starts: %s\n", strings.Join(m.Run, " "))
	if len(m.Hosts) == 0 {
		fmt.Fprintln(e.out, "  It says it contacts no other computers.")
	} else {
		fmt.Fprintf(e.out, "  It says it contacts: %s\n", strings.Join(m.Hosts, ", "))
	}
	fmt.Fprintln(e.out, "  Tools:")
	for _, t := range m.Tools {
		leaves := "no"
		if t.LeavesMachine {
			leaves = "yes"
		}
		fmt.Fprintf(e.out, "    %s: %s (leaves this computer: %s)\n", t.Name, t.Summary, leaves)
	}
	fmt.Fprintf(e.out, "  Package hash: %s\n", ph)
	fmt.Fprintln(e.out, "Handloom will run exactly these files, and only these. If any of them changes, the add-on stops until you approve it again.")
}

func confirmAddon(e *env, name string) (bool, error) {
	if addonIn == io.Reader(os.Stdin) && !isatty.IsTerminal(os.Stdin.Fd()) {
		return false, fmt.Errorf("there is no one to ask here, so nothing was installed; read what the add-on does (without --json), then add --yes, or --sha256 with the hash the author published")
	}
	fmt.Fprintf(e.err, "Install %s on this computer? [y/N] ", name)
	line, _ := bufio.NewReader(addonIn).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

func (e *env) addonList(args []string) error {
	fs := e.flags("addon list")
	if _, err := fs.need(args, 0, 0, "addon list"); err != nil {
		return err
	}
	root := addonRoot()
	list, err := addon.Installed(root, addon.NewStore(root))
	if err != nil {
		return err
	}
	type row struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		State   string   `json:"state"`
		Detail  string   `json:"detail,omitempty"`
		Tools   []string `json:"tools"`
		Hash    string   `json:"package_hash,omitempty"`
	}
	rows := []row{}
	for _, i := range list {
		r := row{Name: i.Name, Version: i.Version, State: addonStateWords(i.State), Detail: i.Detail, Hash: i.PackageHash, Tools: []string{}}
		if i.Manifest != nil {
			for _, t := range i.Manifest.Tools {
				r.Tools = append(r.Tools, t.Name)
			}
		}
		rows = append(rows, r)
	}
	e.print(rows, func() {
		if len(rows) == 0 {
			fmt.Fprintln(e.out, "No add-ons are installed on this computer. That is a complete setup; install one with: handloom addon add DIR")
			return
		}
		for _, r := range rows {
			fmt.Fprintf(e.out, "%-16s %-8s %-28s %s\n", r.Name, r.Version, r.State, strings.Join(r.Tools, ", "))
		}
	})
	return nil
}

// addonStateWords are the words a person sees for a state; the hub uses the same.
func addonStateWords(state string) string {
	switch state {
	case "ready":
		return "Ready"
	case "changed":
		return "Changed since approved"
	case "down":
		return "Not running"
	case "missing":
		return "Missing on this computer"
	}
	return state
}

func (e *env) addonRemove(args []string) error {
	fs := e.flags("addon remove")
	pos, err := fs.need(args, 1, 1, "addon remove NAME")
	if err != nil {
		return err
	}
	root := addonRoot()
	if err := addon.Remove(root, addon.NewStore(root), pos[0]); err != nil {
		return err
	}
	e.print(map[string]string{"removed": pos[0]}, func() { fmt.Fprintf(e.out, "Removed %s from this computer.\n", pos[0]) })
	return nil
}

// addonDoctor asks every installed add-on whether it is well, and says so in a plain line each.
func (e *env) addonDoctor(args []string) error {
	fs := e.flags("addon doctor")
	if _, err := fs.need(args, 0, 0, "addon doctor"); err != nil {
		return err
	}
	root := addonRoot()
	store := addon.NewStore(root)
	list, err := addon.Installed(root, store)
	if err != nil {
		return err
	}
	sup := addon.NewSupervisor(root, store)
	defer sup.Close()
	type line struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		OK      bool   `json:"ok"`
		Health  string `json:"health,omitempty"`
		Text    string `json:"text"`
	}
	lines := []line{}
	bad := 0
	for _, i := range list {
		l := line{Name: i.Name, Version: i.Version}
		switch i.State {
		case "ready":
			if h := sup.Health(context.Background(), i.Name); h.Code == addon.CodeOK {
				l.OK, l.Text = true, "ready; it answered its health check"
			} else {
				l.Health, l.Text = h.Code, "approved, but it did not answer its health check: "+h.Error
			}
		case "missing":
			l.Text = "approved here, but its folder is gone; install it again or remove it"
		default:
			l.Text = "changed since it was approved (" + i.Detail + "); it will not run until you approve it again with handloom addon add"
		}
		if !l.OK {
			bad++
		}
		lines = append(lines, l)
	}
	e.print(lines, func() {
		if len(lines) == 0 {
			fmt.Fprintln(e.out, "No add-ons are installed on this computer.")
		}
		for _, l := range lines {
			mark := "ok  "
			if !l.OK {
				mark = "FAIL"
			}
			name := l.Name
			if l.Version != "" {
				name += " " + l.Version
			}
			fmt.Fprintf(e.out, "%s %s: %s\n", mark, name, l.Text)
		}
	})
	if bad > 0 {
		return fmt.Errorf("%d add-on(s) need attention", bad)
	}
	return nil
}

// addonStatus lists the add-ons every computer has reported to the hub, or one add-on with the manifest each
// computer holds.
func (e *env) addonStatus(args []string) error {
	fs := e.flags("addon status")
	pos, err := fs.need(args, 0, 1, "addon status [NAME]")
	if err != nil {
		return err
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	path := "/v1/addons"
	if len(pos) == 1 {
		path += "/" + url.PathEscape(pos[0])
	}
	var rows []api.Addon
	if err := c.Get(path, &rows); err != nil {
		return err
	}
	e.print(rows, func() {
		if len(rows) == 0 {
			fmt.Fprintln(e.out, "No computer has reported an add-on yet. Install one on a computer with: handloom addon add DIR")
			return
		}
		for _, r := range rows {
			detail := ""
			if r.Detail != "" {
				detail = " (" + r.Detail + ")"
			}
			fmt.Fprintf(e.out, "%-16s %-8s on %-16s %s%s\n", r.Name, r.Version, r.Computer, addonStateWords(r.Status), detail)
		}
	})
	return nil
}

// splitTool reads ADDON.TOOL.
func splitTool(s string) (string, string, error) {
	a, t, ok := strings.Cut(s, ".")
	if !ok || a == "" || t == "" || strings.Contains(t, ".") {
		return "", "", usageErr("name the tool as ADDON.TOOL, for example notes.search")
	}
	return a, t, nil
}

func yesNo(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "yes", "y", "true":
		return true, nil
	case "no", "n", "false":
		return false, nil
	}
	return false, usageErr("--leaves is yes or no")
}

func leavesWords(leaves bool) string {
	if leaves {
		return "leaves this computer"
	}
	return "stays on this computer"
}

func (e *env) addonGrants(args []string) error {
	fs := e.flags("addon grants")
	prof := fs.String("profile", "", "only this profile's grants")
	if _, err := fs.need(args, 0, 0, "addon grants [--profile P]"); err != nil {
		return err
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	path := "/v1/addon-grants"
	if *prof != "" {
		path += "?profile=" + url.QueryEscape(*prof)
	}
	var rows []api.AddonGrant
	if err := c.Get(path, &rows); err != nil {
		return err
	}
	e.print(rows, func() {
		if len(rows) == 0 {
			fmt.Fprintln(e.out, "No tool is granted to a profile yet. Grant one with: handloom addon grant PROFILE ADDON.TOOL")
			return
		}
		for _, g := range rows {
			fmt.Fprintf(e.out, "%-16s %-24s %s, up to %d calls a job (granted by %s)\n", g.Profile, g.Addon+"."+g.Tool, leavesWords(g.LeavesMachine), g.MaxCalls, g.GrantedBy)
		}
	})
	return nil
}

func (e *env) addonGrant(args []string) error {
	fs := e.flags("addon grant")
	leaves := fs.String("leaves", "yes", "does the tool send something off the computer: yes or no (yes unless you know it stays)")
	max := fs.Int("max-calls", 0, "calls allowed per job (default 50)")
	computer := fs.String("computer", "", "the computer whose version of the add-on this is for, when computers differ")
	pos, err := fs.need(args, 2, 2, "addon grant PROFILE ADDON.TOOL [--leaves yes|no] [--max-calls N]")
	if err != nil {
		return err
	}
	a, t, err := splitTool(pos[1])
	if err != nil {
		return err
	}
	l, err := yesNo(*leaves)
	if err != nil {
		return err
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	var g api.AddonGrant
	if err := c.Post("/v1/profiles/"+url.PathEscape(pos[0])+"/addon-grants", api.AddonGrantReq{Addon: a, Tool: t, LeavesMachine: &l, MaxCalls: *max, Computer: *computer}, &g); err != nil {
		return err
	}
	e.print(g, func() {
		fmt.Fprintf(e.out, "Granted %s.%s to %s: it %s, up to %d calls a job.\nHelpers already running keep the tools they started with; the change reaches helpers started from now on.\n", g.Addon, g.Tool, g.Profile, leavesWords(g.LeavesMachine), g.MaxCalls)
	})
	return nil
}

func (e *env) addonRevoke(args []string) error {
	fs := e.flags("addon revoke")
	pos, err := fs.need(args, 2, 2, "addon revoke PROFILE ADDON.TOOL")
	if err != nil {
		return err
	}
	a, t, err := splitTool(pos[1])
	if err != nil {
		return err
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	if err := c.Do(context.Background(), "DELETE", "/v1/profiles/"+url.PathEscape(pos[0])+"/addon-grants/"+url.PathEscape(a)+"/"+url.PathEscape(t), nil, nil); err != nil {
		return err
	}
	e.print(map[string]string{"profile": pos[0], "addon": a, "tool": t}, func() {
		fmt.Fprintf(e.out, "Revoked %s.%s from %s.\nHelpers already running keep the tool until they end; helpers started from now on do not get it.\n", a, t, pos[0])
	})
	return nil
}
