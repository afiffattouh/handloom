package link

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"handloom/internal/api"
)

// Pull request status (D105). After a publish opened a pull request, the
// machine that published asks gh where it stands: open, merged or closed, the
// review decision and whether it can be merged. It looks at most once per
// interval per pull request and the hub stops listing one that was merged or
// closed, or that is more than 14 days old. Nothing happens because of the
// answer except that the hub shows it: no closing, no deleting, no notes.
// Without gh, or when gh fails, the pull request is simply not looked at.

const (
	defaultPRPoll = 2 * time.Minute
	prViewTimeout = 30 * time.Second
)

func (l *Link) pollPullRequests(ctx context.Context) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return // no gh: the publish still shows its link
	}
	var ps []api.PendingPR
	if err := l.hub.Do(ctx, "GET", "/v1/device/pull-requests", nil, &ps); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("pull requests: %v", err)
		}
		return
	}
	for _, p := range ps {
		if ctx.Err() != nil {
			return
		}
		if !strings.HasPrefix(p.URL, "https://") {
			continue
		}
		now := l.opt.Now()
		l.mu.Lock()
		if l.prSeen == nil {
			l.prSeen = map[int64]time.Time{}
		}
		last, seen := l.prSeen[p.ID]
		due := !seen || now.Sub(last) >= l.opt.PRPoll
		if due {
			l.prSeen[p.ID] = now // counted even when gh fails, so a broken gh is not called every tick
		}
		l.mu.Unlock()
		if !due {
			continue
		}
		rep, ok := l.viewPR(ctx, gh, p.URL)
		if !ok {
			continue
		}
		if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/publishes/%d/pr", p.ID), rep, nil); err != nil {
			l.opt.Log.Printf("report pull request of publish %d: %v", p.ID, err)
		}
	}
}

// viewPR asks gh about one pull request by its address.
func (l *Link) viewPR(ctx context.Context, gh, url string) (api.PRReport, bool) {
	cctx, cancel := context.WithTimeout(ctx, prViewTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, gh, "pr", "view", url, "--json", "state,mergedAt,mergeable,reviewDecision,title")
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return api.PRReport{}, false
	}
	var v struct {
		State          string `json:"state"`
		MergedAt       string `json:"mergedAt"`
		Mergeable      string `json:"mergeable"`
		ReviewDecision string `json:"reviewDecision"`
	}
	if json.Unmarshal(out, &v) != nil {
		return api.PRReport{}, false
	}
	rep := api.PRReport{Mergeable: "unknown"}
	switch strings.ToUpper(v.State) {
	case "MERGED":
		rep.State = "merged"
	case "CLOSED":
		rep.State = "closed"
		if v.MergedAt != "" && !strings.HasPrefix(v.MergedAt, "0001-") {
			rep.State = "merged"
		}
	case "OPEN":
		rep.State = "open"
	default:
		return api.PRReport{}, false
	}
	switch strings.ToUpper(v.ReviewDecision) {
	case "APPROVED":
		rep.Review = "approved"
	case "CHANGES_REQUESTED":
		rep.Review = "changes_requested"
	case "REVIEW_REQUIRED":
		rep.Review = "review_required"
	}
	switch strings.ToUpper(v.Mergeable) {
	case "MERGEABLE":
		rep.Mergeable = "true"
	case "CONFLICTING":
		rep.Mergeable = "false"
	}
	return rep, true
}
