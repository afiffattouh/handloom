package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

type overviewState struct{}

var ranges = []string{"24h", "7d", "30d"}

func rangeWords(r string) string {
	switch r {
	case "24h":
		return "Last 24 hours"
	case "30d":
		return "Last 30 days"
	}
	return "Last 7 days"
}

func (m *Model) overviewKey(s string) tea.Cmd {
	if s == "r" {
		for i, r := range ranges {
			if r == m.rng {
				m.rng = ranges[(i+1)%len(ranges)]
				break
			}
		}
		m.metrics = nil
		return m.refresh()
	}
	return nil
}

func tokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func (m *Model) overviewView(w, h int) string {
	title := rangeWords(m.rng)
	mt := m.metrics
	if mt == nil {
		return panel(title, []string{"", sMuted.Render("Loading the numbers…")}, w, h, true)
	}
	label := func(k string) string { return sMuted.Render(fit(k, 18)) }
	var stats []string
	needs := fmt.Sprintf("%d", m.needsYou())
	if m.digest != nil {
		needs = itoa(len(m.digest.NeedsYou))
	}
	line := label("Needs you") + sBold.Render(needs)
	if mt.Needs.Count > 0 && mt.Needs.Oldest > 0 {
		line += sMuted.Render("  oldest waiting " + span(mt.Needs.Oldest))
	}
	stats = append(stats, line)
	a := mt.Agents
	stats = append(stats, label("Agents")+sSuccess.Render("●")+fmt.Sprintf(" %d working ", a.Working)+sMuted.Render("○")+fmt.Sprintf(" %d idle ", a.Idle)+
		sWarn.Render("▲")+fmt.Sprintf(" %d blocked ", a.Blocked)+sMuted.Render("○")+fmt.Sprintf(" %d offline", a.Offline))
	t := mt.Tasks
	tl := label("Tasks in flight") + fmt.Sprintf("%d in progress · %d to review · %d waiting", t.Claimed, t.Submitted, t.Open)
	stats = append(stats, tl)
	switch {
	case mt.Checks.N == 0 || mt.Checks.Pct < 0:
		stats = append(stats, label("Checks passed")+sMuted.Render("no checks in this range"))
	default:
		stats = append(stats, label("Checks passed")+fmt.Sprintf("%.0f%%", mt.Checks.Pct)+sMuted.Render(fmt.Sprintf(" of %d", mt.Checks.N)))
	}
	if mt.TaskTime.N > 0 {
		stats = append(stats, label("Median task time")+span(mt.TaskTime.Value)+sMuted.Render(fmt.Sprintf(" over %s", plural(mt.TaskTime.N, "task", "tasks"))))
	} else {
		stats = append(stats, label("Median task time")+sMuted.Render("no finished tasks in this range"))
	}
	if mt.Answer.N > 0 {
		stats = append(stats, label("You answer in")+span(mt.Answer.Value)+sMuted.Render(fmt.Sprintf(" (median of %d)", mt.Answer.N)))
	}
	stats = append(stats, label("Usage")+usageWords(mt))

	var charts []string
	if len(mt.Buckets) > 1 {
		acc, rej, pass, fail := make([]int, len(mt.Buckets)), make([]int, len(mt.Buckets)), make([]int, len(mt.Buckets)), make([]int, len(mt.Buckets))
		var sa, sr, sp, sf int
		for i, b := range mt.Buckets {
			acc[i], rej[i], pass[i], fail[i] = b.Accepted, b.Rejected, b.Passed, b.Failed+b.TimedOut
			sa, sr, sp, sf = sa+b.Accepted, sr+b.Rejected, sp+b.Passed, sf+b.Failed+b.TimedOut
		}
		first, last := mt.Buckets[0].Label, mt.Buckets[len(mt.Buckets)-1].Label
		n := len(mt.Buckets)
		gap := n - len(first) - len(last)
		axis := first + repeat(" ", gap) + last
		if gap < 1 {
			axis = first + " … " + last
		}
		charts = append(charts, "", sMuted.Render("Per "+bucketWord(m.rng)))
		row := func(name string, v []int, note string) {
			charts = append(charts, label(name)+spark(v)+sMuted.Render("  "+note))
		}
		row("Accepted", acc, fmt.Sprintf("%d in total", sa))
		row("Sent back", rej, fmt.Sprintf("%d in total", sr))
		row("Checks passed", pass, fmt.Sprintf("%d in total", sp))
		row("Checks failed", fail, fmt.Sprintf("%d in total", sf))
		charts = append(charts, label("")+sMuted.Render(axis))
	}
	if len(mt.Util) > 0 {
		charts = append(charts, "", sMuted.Render("Time spent working"))
		for i, u := range mt.Util {
			if i == 5 {
				break
			}
			charts = append(charts, label(cut(u.Agent, 17))+bar(u.Working, 20)+sMuted.Render(fmt.Sprintf("  %d%%", u.Working)))
		}
	}

	var ins []string
	if len(mt.Insights) == 0 {
		ins = append(ins, "", sMuted.Render("Nothing to flag. Observations appear here when the numbers give a reason."))
	}
	iw := w - 4
	if w >= 100 {
		iw = w - w*58/100 - 4
	}
	for _, in := range mt.Insights {
		icon := sMuted.Render("●")
		switch in.Sev {
		case "bad":
			icon = sBad.Render("✗")
		case "warn":
			icon = sWarn.Render("▲")
		}
		ins = append(ins, "")
		for i, l := range wrap(in.Title, iw-2) {
			p := "  "
			if i == 0 {
				p = icon + " "
			}
			ins = append(ins, p+sBold.Render(l))
		}
		for _, l := range wrap(in.Detail, iw-2) {
			ins = append(ins, "  "+sMuted.Render(l))
		}
		if in.Basis != "" {
			for _, l := range wrap("Based on: "+in.Basis, iw-2) {
				ins = append(ins, "  "+sMuted.Render(l))
			}
		}
	}
	rng := rangeWords(m.rng) + sMuted.Render("   r: 24h · 7d · 30d")
	if w >= 100 {
		lw := w * 58 / 100
		left := panel(rng, append([]string{""}, append(stats, charts...)...), lw, h, true)
		right := panel(fmt.Sprintf("Insights %d", len(mt.Insights)), ins, w-lw, h, false)
		return side(left, right)
	}
	body := append([]string{""}, stats...)
	body = append(body, "")
	body = append(body, sBold.Render(fmt.Sprintf("Insights %d", len(mt.Insights))))
	body = append(body, ins...)
	body = append(body, charts...)
	return panel(rng, body, w, h, true)
}

func bucketWord(r string) string {
	if r == "24h" {
		return "hour"
	}
	return "day"
}

// usageWords says what was used, and a cost only when the models have a price.
func usageWords(mt *Metrics) string {
	sp := mt.Spend
	switch {
	case sp == nil || sp.Tokens == 0:
		return sMuted.Render("none reported yet")
	case !sp.HasPrice || sp.Priced == 0:
		return tokens(sp.Tokens) + " tokens" + sMuted.Render(" · no prices set, so no cost")
	}
	return fmt.Sprintf("%s tokens · about $%.2f", tokens(sp.Tokens), sp.Cost) + sMuted.Render(fmt.Sprintf(" (%d%% priced)", sp.Priced*100/sp.Tokens))
}
