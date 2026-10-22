package link

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/selfupdate"
)

// Updating the link, computer side. A person asks on the Computers page; the hub holds the request; this link
// sees it, waits until no helper here is mid-turn, downloads the newest release itself, checks it against the
// release's checksums, swaps its own program file and starts again in place. The hub sends no program and no
// address: only "update". A computer built from source never updates itself.

const (
	latestEvery  = 6 * time.Hour // how often the release host is asked what is newest
	latestRetry  = time.Hour     // after a failed look
	selfResend   = 5 * time.Minute
	updateMarker = "update-result.json"
)

// updateResult is left for the next start, which reports how the update went.
type updateResult struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (l *Link) exe() string {
	if l.opt.Exe != "" {
		return l.opt.Exe
	}
	e, _ := os.Executable()
	return e
}

func (l *Link) markerPath() string { return filepath.Join(client.Home(), updateMarker) }

// refreshLatest asks the release host what is newest, now and then.
func (l *Link) refreshLatest(ctx context.Context) {
	now := l.opt.Now()
	l.mu.Lock()
	due := now.After(l.latestNext)
	l.mu.Unlock()
	if !due || !selfupdate.IsRelease(l.opt.Version) {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag, err := l.opt.Source.Latest(lctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.latestNext = now.Add(latestRetry)
		l.opt.Log.Printf("update: %v", err)
		return
	}
	l.latest, l.latestNext = tag, now.Add(latestEvery)
}

// reportSelf tells the hub what this link is. It sends when the report changed, or after selfResend.
func (l *Link) reportSelf(ctx context.Context, state, detail string) {
	note := selfupdate.Check(l.opt.Version, l.exe())
	l.mu.Lock()
	rep := api.LinkSelf{Version: l.opt.Version, Latest: l.latest, CanUpdate: note == "", Note: note, State: state, Detail: detail}
	sig, _ := json.Marshal(rep)
	now := l.opt.Now()
	same := string(l.selfSent) == string(sig) && now.Sub(l.selfAt) < selfResend
	l.mu.Unlock()
	if same {
		return
	}
	if err := l.hub.Do(ctx, "POST", "/v1/device/self", rep, nil); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("report version: %v", err)
		}
		return
	}
	l.mu.Lock()
	l.selfSent, l.selfAt = sig, now
	l.mu.Unlock()
}

// runSelfUpdate is one pass of the ladder: tell the hub the version, and do an update someone asked for.
func (l *Link) runSelfUpdate(ctx context.Context, agents []api.DeviceAgent) {
	if l.opt.Version == "" {
		return
	}
	l.refreshLatest(ctx)
	if data, err := os.ReadFile(l.markerPath()); err == nil {
		var res updateResult
		json.Unmarshal(data, &res)
		state, detail := "done", fmt.Sprintf("Updated from %s to %s.", res.From, res.To)
		if res.To != l.opt.Version {
			state, detail = "failed", fmt.Sprintf("The program was replaced with %s, but this link is still %s: restart it.", res.To, l.opt.Version)
		}
		l.reportSelf(ctx, state, detail)
		os.Remove(l.markerPath())
		return
	}
	l.reportSelf(ctx, "", "")
	var want api.LinkUpdateWant
	if err := l.hub.Do(ctx, "GET", "/v1/device/update", nil, &want); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("update request: %v", err)
		}
		return
	}
	if !want.Want {
		return
	}
	if busy := workingHere(agents); busy > 0 {
		l.reportSelf(ctx, "waiting", fmt.Sprintf("waiting for %d helper(s) to finish their turn", busy))
		return
	}
	l.reportSelf(ctx, "updating", "downloading the new release")
	fail := func(format string, a ...any) {
		l.opt.Log.Printf("update: "+format, a...)
		l.reportSelf(ctx, "failed", fmt.Sprintf(format, a...))
	}
	if note := selfupdate.Check(l.opt.Version, l.exe()); note != "" {
		fail("%s", note)
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	tag, err := l.opt.Source.Latest(lctx)
	if err != nil {
		fail("%v", err)
		return
	}
	if !selfupdate.Newer(l.opt.Version, tag) {
		fail("already on the newest release (%s)", l.opt.Version)
		return
	}
	if err := l.opt.Source.Apply(lctx, l.exe(), tag); err != nil {
		fail("%v", err)
		return
	}
	data, _ := json.Marshal(updateResult{From: l.opt.Version, To: tag})
	if err := os.WriteFile(l.markerPath(), data, 0o600); err != nil {
		l.opt.Log.Printf("update: could not leave a note for the next start: %v", err)
	}
	l.opt.Log.Printf("update: %s -> %s installed; restarting", l.opt.Version, tag)
	l.closeAddons()
	restart := l.opt.Restart
	if restart == nil {
		restart = func(exe string) error { return syscall.Exec(exe, os.Args, os.Environ()) }
	}
	if err := restart(l.exe()); err != nil {
		os.Remove(l.markerPath())
		l.reportSelf(ctx, "done", fmt.Sprintf("Installed %s, but the link could not restart itself (%v): restart it.", tag, err))
	}
}

// workingHere counts the helpers on this computer that are in the middle of a turn.
func workingHere(agents []api.DeviceAgent) int {
	n := 0
	for _, a := range agents {
		if a.State == api.StateWorking {
			n++
		}
	}
	return n
}
