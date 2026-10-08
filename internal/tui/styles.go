package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The palette follows docs/design-principles.md: neutrals with a slight green
// cast carry nearly everything, one accent marks where to act, and the three
// state colours appear only on small text, icons and the count pill.
var (
	cFg      = lipgloss.AdaptiveColor{Light: "#1c2420", Dark: "#e3e9e5"}
	cMuted   = lipgloss.AdaptiveColor{Light: "#5a6860", Dark: "#8d9b93"}
	cBorder  = lipgloss.AdaptiveColor{Light: "#bfcac3", Dark: "#3b4640"}
	cAccent  = lipgloss.AdaptiveColor{Light: "#1f5f4a", Dark: "#4fc3b5"}
	cSuccess = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#56c27a"}
	cWarn    = lipgloss.AdaptiveColor{Light: "#8a5a00", Dark: "#e3b341"}
	cBad     = lipgloss.AdaptiveColor{Light: "#c0262d", Dark: "#ff7b72"}
)

var (
	sFg      = lipgloss.NewStyle().Foreground(cFg)
	sBold    = lipgloss.NewStyle().Foreground(cFg).Bold(true)
	sMuted   = lipgloss.NewStyle().Foreground(cMuted)
	sBorder  = lipgloss.NewStyle().Foreground(cBorder)
	sAccent  = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sSuccess = lipgloss.NewStyle().Foreground(cSuccess)
	sWarn    = lipgloss.NewStyle().Foreground(cWarn)
	sBad     = lipgloss.NewStyle().Foreground(cBad)
)

// colorOn is false when the terminal shows no colour (NO_COLOR, a dumb
// terminal, or a test). The console then adds marks that colour would carry.
func colorOn() bool {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("x") != "x"
}

// ---- text helpers ----

// fit cuts s to w columns with an ellipsis and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " ")
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// cut shortens s to at most w columns with an ellipsis, without padding.
func cut(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// wrap breaks text into lines of at most w columns, keeping blank lines.
func wrap(text string, w int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(para, w, ""), "\n")...)
	}
	return out
}

// clip returns exactly h lines, each exactly w columns wide.
func clip(lines []string, w, h int) []string {
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out = append(out, fit(lines[i], w))
		} else {
			out = append(out, strings.Repeat(" ", w))
		}
	}
	return out
}

// panel draws lines inside a rounded hairline box of exactly w by h cells.
// The title, if any, is the first line inside.
func panel(title string, lines []string, w, h int, focus bool) string {
	inner := w - 4
	if inner < 1 || h < 3 {
		return ""
	}
	var body []string
	if title != "" {
		body = append(body, sBold.Render(cut(title, inner)))
	}
	body = append(body, lines...)
	body = clip(body, inner, h-2)
	bc := cBorder
	if focus {
		bc = cMuted
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(bc).Padding(0, 1).
		Render(strings.Join(body, "\n"))
	return box
}

// side joins two blocks of equal height next to each other.
func side(a, b string) string { return lipgloss.JoinHorizontal(lipgloss.Top, a, b) }

func repeat(s string, n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat(s, n)
}

// ---- small widgets ----

const sparkRunes = "▁▂▃▄▅▆▇█"

// spark draws one bar per value, scaled to the largest.
func spark(vals []int) string {
	max := 0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	rs := []rune(sparkRunes)
	var b strings.Builder
	for _, v := range vals {
		switch {
		case v <= 0:
			b.WriteRune('·')
		case max == 0:
			b.WriteRune(rs[0])
		default:
			i := v * (len(rs) - 1) / max
			b.WriteRune(rs[i])
		}
	}
	return b.String()
}

// bar draws pct (0-100) as a bar of w cells.
func bar(pct, w int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	n := pct * w / 100
	return strings.Repeat("█", n) + strings.Repeat("░", w-n)
}

// stateMark is an icon and a word for an agent's state; the colour only repeats it.
func stateMark(state string) string {
	switch state {
	case "working":
		return sSuccess.Render("●") + " working"
	case "idle":
		return sMuted.Render("○") + " idle"
	case "blocked":
		return sWarn.Render("▲") + " blocked"
	case "offline":
		return sMuted.Render("○") + sMuted.Render(" offline")
	}
	return sMuted.Render("–") + " " + state
}

// statusMark is an icon and a word for a task or job status.
func statusMark(status string) string {
	switch status {
	case "open":
		return sMuted.Render("○") + " open"
	case "claimed":
		return sSuccess.Render("●") + " in progress"
	case "submitted":
		return sWarn.Render("▲") + " to review"
	case "done":
		return sSuccess.Render("✓") + " done"
	case "cancelled":
		return sMuted.Render("✗") + sMuted.Render(" cancelled")
	}
	return status
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return span(time.Since(t)) + " ago"
}

// span says a duration in the two largest units that matter.
func span(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if h < 10 && m > 0 {
			return itoa(h) + "h " + itoa(m) + "m"
		}
		return itoa(h) + "h"
	}
	return itoa(int(d.Hours()/24)) + "d"
}
