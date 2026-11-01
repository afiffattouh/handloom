package link

import (
	"context"
	"net/url"
	"strings"

	"handloom/internal/api"
)

func (l *Link) stopPlanWorkers(ctx context.Context) {
	var names []string
	if err := l.hub.Do(ctx, "GET", "/v1/device/plan-stops", nil, &names); err != nil {
		return
	}
	if len(names) == 0 {
		return
	}
	// Read the actual names rather than using tmux's prefix matching. Only the
	// link's own session/socket is addressed, never a user's other sessions.
	out, err := l.opt.Tmux(ctx, "tmux", "-L", l.opt.TmuxSocket, "list-windows", "-t", "=handloom", "-F", "#{window_name}")
	if err != nil {
		return
	} // lease expiry handles an absent server; errors are not proof of death
	windows := map[string]bool{}
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		windows[name] = true
	}
	for _, name := range names {
		if !spawnNameRE.MatchString(name) {
			continue
		}
		if windows[name] {
			if _, err := l.opt.Tmux(ctx, "tmux", "-L", l.opt.TmuxSocket, "kill-window", "-t", "=handloom:="+name); err != nil {
				l.opt.Log.Printf("stop completed plan worker %s: %v", name, err)
				continue
			}
		}
		if err := l.hub.Do(ctx, "POST", "/v1/agents/"+url.PathEscape(name)+"/state", api.StateReq{State: api.StateOffline}, nil); err != nil {
			l.opt.Log.Printf("report stopped plan worker %s: %v", name, err)
		}
	}
}
