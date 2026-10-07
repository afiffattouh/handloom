package link

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/drivers"
)

// The terminal view, device side. When a person has an agent's page open the
// hub lists the agent as wanted; this reads the end of its terminal every few
// seconds, takes out anything that looks like a secret, and posts it. When
// nobody is looking any more the hub stops listing it and this stops.

const (
	tailLines = 40
	tailEvery = 3 * time.Second
)

// secretRE matches things that should never reach a screen shared through the
// hub: Handloom's own tokens and common API key shapes.
var secretRE = regexp.MustCompile(`hv[adjhr]_[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9\-]{10,}|(?i:bearer)\s+[A-Za-z0-9._\-]{20,}`)

// redactScreen removes secrets from a screen: pattern matches, and any exact
// secret this device holds.
func redactScreen(text string, exact ...string) string {
	for _, s := range exact {
		if len(s) >= 8 {
			text = strings.ReplaceAll(text, s, "[removed]")
		}
	}
	return secretRE.ReplaceAllString(text, "[removed]")
}

// runTails starts the capture loop when the hub says some screen is wanted.
func (l *Link) runTails(ctx context.Context) {
	l.mu.Lock()
	if l.tailing {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	var wanted []string
	if err := l.hub.Do(ctx, "GET", "/v1/device/tails", nil, &wanted); err != nil || len(wanted) == 0 {
		return
	}
	l.mu.Lock()
	if l.tailing {
		l.mu.Unlock()
		return
	}
	l.tailing = true
	l.mu.Unlock()
	go func() {
		defer func() { l.mu.Lock(); l.tailing = false; l.mu.Unlock() }()
		for ctx.Err() == nil {
			var names []string
			if err := l.hub.Do(ctx, "GET", "/v1/device/tails", nil, &names); err != nil || len(names) == 0 {
				return
			}
			l.postTails(ctx, names)
			select {
			case <-ctx.Done():
				return
			case <-time.After(tailEvery):
			}
		}
	}()
}

func (l *Link) postTails(ctx context.Context, names []string) {
	var agents []api.DeviceAgent
	if err := l.hub.Do(ctx, "GET", "/v1/device/agents", nil, &agents); err != nil {
		return
	}
	target := map[string]string{}
	for _, a := range agents {
		target[a.Name] = a.WakeTarget
	}
	for _, name := range names {
		wt := target[name]
		if wt == "" || wt == HeadlessTarget {
			continue
		}
		drv, t, err := l.opt.Drivers(wt)
		if err != nil {
			continue
		}
		cp, ok := drv.(drivers.Capturer)
		if !ok {
			continue
		}
		text, err := cp.Capture(ctx, t, tailLines)
		if err != nil {
			text = fmt.Sprintf("(cannot read the terminal: %v)", err)
		}
		text = redactScreen(text, l.opt.Credential, l.agentToken(name))
		if err := l.hub.Do(ctx, "POST", "/v1/device/tails/"+url.PathEscape(name), api.TailReq{Text: text}, nil); err != nil {
			l.opt.Log.Printf("screen of %s: %v", name, err)
		}
	}
}

// agentToken is the run token this device handed an agent, if the link can find it.
func (l *Link) agentToken(name string) string {
	rec := l.loadScope(name)
	if rec == nil || rec.Dir == "" {
		return ""
	}
	b, err := readSmall(rec.Dir + "/.handloom/tokens/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, 512)
	n, _ := f.Read(b)
	return b[:n], nil
}
