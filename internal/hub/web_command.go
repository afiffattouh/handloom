package hub

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// The command center page: the metrics of metrics.go, written out in words and
// in server-drawn SVG (no script, no inline style: the page's policy forbids both).

type commandView struct {
	Range    string
	Ranges   []rangeTab
	Machine  string
	Machines []machineTab
	KPIs     []kpiView
	Fleet    []fleetView
	Insights []Insight
	ChartAcc template.HTML
	ChartChk template.HTML
	Util     []UtilRow
	Activity []activityView
	Profiles []profStat
	CanStart bool
	Quiet    bool // no agents, no jobs: the page says how to begin
}

type rangeTab struct {
	Key, Href string
	Current   bool
}
type machineTab struct {
	Name, Href string
	Current    bool
}
type kpiView struct {
	Label, Value, Unit, Note string
	Trend                    string // up | down | "" : only shown when there is a prior period to compare with
	TrendGood                bool
	Href                     string
}
type fleetView struct {
	FleetRow
	Badge, Dot string
	Seen       string
	LeaseLow   bool
}
type profStat struct {
	Name      string
	Tasks     int
	FirstTime string
	Checks    string
	Median    string
}

func dur(d time.Duration) string {
	switch {
	case d <= 0:
		return "–"
	case d < time.Minute:
		return "<1 min"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return human(d)
}

func (q *webReq) commandView() (*commandView, error) {
	key := q.r.URL.Query().Get("range")
	if _, ok := metricsRanges[key]; !ok {
		key = "7d"
	}
	m, err := q.c.computeMetrics(key)
	if err != nil {
		return nil, err
	}
	machine := q.r.URL.Query().Get("machine")
	v := &commandView{Range: key, Machine: machine, CanStart: q.human.Role != store.RoleViewer, Insights: m.Insights, Util: m.Util}
	for _, k := range []string{"24h", "7d", "30d"} {
		href := "/command?range=" + k
		if machine != "" {
			href += "&machine=" + machine
		}
		v.Ranges = append(v.Ranges, rangeTab{k, href, k == key})
	}
	seen := map[string]bool{}
	v.Machines = append(v.Machines, machineTab{"All", "/command?range=" + key, machine == ""})
	for _, f := range m.Fleet {
		if !seen[f.Device] {
			seen[f.Device] = true
			v.Machines = append(v.Machines, machineTab{f.Device, "/command?range=" + key + "&machine=" + f.Device, machine == f.Device})
		}
	}

	// ---- the numbers, each with what it rests on ----
	kpi := func(label, value, unit, note string) kpiView {
		return kpiView{Label: label, Value: value, Unit: unit, Note: note}
	}
	needs := kpi("Needs you", fmt.Sprint(m.Needs.Count), "", "nothing is waiting")
	needs.Href = "/inbox"
	if m.Needs.Count > 0 {
		needs.Note = "oldest waiting " + dur(m.Needs.Oldest)
	}
	ag := m.Agents
	working := kpi("Agents working", fmt.Sprint(ag.Working), fmt.Sprintf("of %d", ag.Total), fmt.Sprintf("%d idle · %d blocked · %d offline", ag.Idle, ag.Blocked, ag.Offline+ag.Other))
	working.Href = "/agents"
	flight := kpi("Tasks in flight", fmt.Sprint(m.Tasks.Claimed+m.Tasks.Submitted), "", fmt.Sprintf("%d being worked · %d submitted · %d waiting to start", m.Tasks.Claimed, m.Tasks.Submitted, m.Tasks.Open))
	checks := kpi("Checks passed", "–", "", "no device checks in this range")
	if m.Checks.N > 0 {
		checks = kpi("Checks passed", fmt.Sprintf("%d", int(m.Checks.Pct+0.5)), "%", fmt.Sprintf("%d of %d checks", int(m.Checks.Pct*float64(m.Checks.N)/100+0.5), m.Checks.N))
		if m.Checks.PriorN > 0 {
			diff := int(m.Checks.Pct+0.5) - int(m.Checks.PriorPct+0.5)
			switch {
			case diff > 0:
				checks.Trend, checks.TrendGood, checks.Note = "up", true, fmt.Sprintf("%d points more than the period before", diff)
			case diff < 0:
				checks.Trend, checks.Note = "down", fmt.Sprintf("%d points fewer than the period before", -diff)
			}
		}
	}
	tt := kpi("Median task time", "–", "", "no accepted tasks in this range")
	if m.TaskTime.N > 0 {
		tt = kpi("Median task time", dur(m.TaskTime.Value), "", fmt.Sprintf("claim to accepted · %d tasks", m.TaskTime.N))
		if m.TaskTime.PriorN > 0 {
			switch d := m.TaskTime.Value - m.TaskTime.Prior; {
			case d > time.Minute:
				tt.Trend, tt.Note = "up", dur(d)+" slower than the period before"
			case d < -time.Minute:
				tt.Trend, tt.Note, tt.TrendGood = "down", dur(-d)+" faster than the period before", true
			}
		}
	}
	ans := kpi("Your answer time", "–", "", "no questions answered in this range")
	if m.Answer.N > 0 {
		ans = kpi("Your answer time", dur(m.Answer.Value), "", fmt.Sprintf("median of %d question(s)", m.Answer.N))
	}
	v.KPIs = []kpiView{needs, working, flight, checks, tt, ans}

	// ---- fleet ----
	for _, f := range m.Fleet {
		if machine != "" && f.Device != machine {
			continue
		}
		fv := fleetView{FleetRow: f, LeaseLow: f.LeasePct < 30}
		switch f.State {
		case api.StateWorking:
			fv.Badge, fv.Dot = "success", ""
		case api.StateBlocked:
			fv.Badge = "warning"
		case api.StateOffline:
			fv.Badge, fv.Dot = "destructive", " ring"
		case api.StateIdle:
			fv.Badge = ""
		default:
			fv.Badge, fv.Dot = "outline", " ring"
		}
		fv.Seen = dur(f.StateFor)
		v.Fleet = append(v.Fleet, fv)
	}
	v.Quiet = len(m.Fleet) == 0

	// ---- charts ----
	labels := make([]string, len(m.Buckets))
	acc, rej, pass, fail, tmo := make([]int, len(m.Buckets)), make([]int, len(m.Buckets)), make([]int, len(m.Buckets)), make([]int, len(m.Buckets)), make([]int, len(m.Buckets))
	for i, b := range m.Buckets {
		labels[i], acc[i], rej[i], pass[i], fail[i], tmo[i] = b.Label, b.Accepted, b.Rejected, b.Passed, b.Failed, b.TimedOut
	}
	v.ChartAcc = barChart("Tasks accepted and sent back per "+bucketWord(key), labels, [][]int{acc, rej}, []string{"c-primary", "c-bad"})
	v.ChartChk = barChart("Device check results per "+bucketWord(key), labels, [][]int{pass, fail, tmo}, []string{"c-primary", "c-bad", "c-warn"})

	// ---- profiles ----
	for _, p := range m.Profiles {
		pv := profStat{Name: p.Name, Tasks: p.Tasks, FirstTime: "–", Checks: "no check", Median: dur(p.Median)}
		if p.Tasks > 0 {
			pv.FirstTime = fmt.Sprintf("%d%%", p.FirstTime*100/p.Tasks)
		}
		if p.ChecksN > 0 {
			pv.Checks = fmt.Sprintf("%d%% of %d", p.ChecksPass*100/p.ChecksN, p.ChecksN)
		}
		v.Profiles = append(v.Profiles, pv)
	}

	d, err := q.c.digest()
	if err != nil {
		return nil, err
	}
	for i, a := range d.Activity {
		if i == 8 {
			break
		}
		v.Activity = append(v.Activity, activityView{ago(q.now, a.At), a.Text})
	}
	return v, nil
}

func bucketWord(key string) string {
	if key == "24h" {
		return "hour"
	}
	return "day"
}

// barChart draws stacked bars to one scale: gridlines at 0, half and the top
// tick, a label under about every bucket that fits. Everything it writes is a
// number or an escaped label.
func barChart(title string, labels []string, series [][]int, classes []string) template.HTML {
	const W, H, L, B, T = 300.0, 150.0, 26.0, 18.0, 6.0
	n := len(labels)
	max := 1
	for i := 0; i < n; i++ {
		s := 0
		for _, ser := range series {
			s += ser[i]
		}
		if s > max {
			max = s
		}
	}
	top := niceTop(max)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 300 150" role="img" aria-label="%s">`, html.EscapeString(title))
	for _, f := range []float64{0, 0.5, 1} {
		y := T + (H-T-B)*(1-f)
		fmt.Fprintf(&b, `<line class="grid" x1="%.0f" x2="298" y1="%.1f" y2="%.1f"/><text x="%.0f" y="%.1f" text-anchor="end">%d</text>`, L, y, y, L-5, y+3, int(float64(top)*f))
	}
	bw := (W - L - 4) / float64(n)
	every := 1
	if n > 12 {
		every = (n + 7) / 8
	}
	for i := 0; i < n; i++ {
		y := H - B
		x := L + float64(i)*bw + bw*0.2
		w := bw * 0.6
		for k, ser := range series {
			if ser[i] == 0 {
				continue
			}
			h := float64(ser[i]) / float64(top) * (H - T - B)
			y -= h
			fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="2"/>`, classes[k], x, y, w, h)
		}
		if i%every == 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, x+w/2, H-4, html.EscapeString(labels[i]))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// niceTop rounds a maximum up to a tick the axis can label: 1, 2, 4, 6, 8, 10, 20, ...
func niceTop(max int) int {
	switch {
	case max <= 2:
		return 2
	case max <= 4:
		return 4
	case max <= 6:
		return 6
	case max <= 8:
		return 8
	case max <= 10:
		return 10
	}
	step := 10
	for step*5 < max {
		step *= 10
	}
	t := ((max + step - 1) / step) * step
	return t
}

func (h *Hub) webCommand(q *webReq) error {
	v, err := q.commandView()
	if err != nil {
		return err
	}
	q.page(http.StatusOK, "command", pageData{Title: "Command center", Extra: v})
	return nil
}

func (h *Hub) webCommandFragment(q *webReq) error {
	v, err := q.commandView()
	if err != nil {
		return err
	}
	buf := &bytesBuffer{}
	if err := h.pages["command"].ExecuteTemplate(buf, "commandbody", pageData{Human: q.human, CSRF: q.sess.CSRF, Extra: v}); err != nil {
		return err
	}
	q.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q.w.Write(buf.b)
	return nil
}
