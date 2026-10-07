package hub

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Usage: each device reads what its agents have used from the agent CLI's own
// log (counts only, never text) and reports running totals. The hub keeps a
// sample whenever a total changes, so usage over any range is a difference of
// two samples. Cost needs a price the owner entered; there are no defaults.

// usagePost is a device reporting an agent's totals.
func usagePost(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device reports usage")
	}
	var req api.UsageReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	a, err := c.agentByName(req.Agent)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not on this device", req.Agent)
	}
	if len(req.Models) > 20 {
		return nil, badRequest("too many models")
	}
	for _, m := range req.Models {
		if m.Model == "" || len(m.Model) > 100 || m.Input < 0 || m.Output < 0 || m.CacheRead < 0 || m.CacheWrite < 0 {
			return nil, badRequest("bad usage row")
		}
		var in, out, cr, cw int64
		err := c.tx.QueryRow(`SELECT input, output, cache_read, cache_write FROM usage_sample WHERE agent = ? AND model = ? ORDER BY at DESC, id DESC LIMIT 1`, req.Agent, m.Model).Scan(&in, &out, &cr, &cw)
		if err == nil && in == m.Input && out == m.Output && cr == m.CacheRead && cw == m.CacheWrite {
			continue // nothing new
		}
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if _, err := c.tx.Exec(`INSERT INTO usage_sample(agent, model, input, output, cache_read, cache_write, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			req.Agent, m.Model, m.Input, m.Output, m.CacheRead, m.CacheWrite, store.Millis(c.now)); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type tokens struct{ Input, Output, CacheRead, CacheWrite int64 }

func (t tokens) total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

type priceRow struct {
	Model                                string
	Input, Output, CacheRead, CacheWrite float64
}

func (c *call) prices() ([]priceRow, error) {
	rows, err := c.tx.Query(`SELECT model, input, output, cache_read, cache_write FROM price ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []priceRow
	for rows.Next() {
		var p priceRow
		if err := rows.Scan(&p.Model, &p.Input, &p.Output, &p.CacheRead, &p.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// priceFor is the longest price entry that the model name starts with.
func priceFor(prices []priceRow, model string) *priceRow {
	var best *priceRow
	for i := range prices {
		p := &prices[i]
		if strings.HasPrefix(model, p.Model) && (best == nil || len(p.Model) > len(best.Model)) {
			best = p
		}
	}
	return best
}

func (t tokens) cost(p *priceRow) float64 {
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output + float64(t.CacheRead)*p.CacheRead + float64(t.CacheWrite)*p.CacheWrite) / 1e6
}

// Spend is usage over a range.
type Spend struct {
	Tokens   int64        `json:"tokens"`
	Cost     float64      `json:"cost_usd"`      // of the tokens that have a price
	Priced   int64        `json:"priced_tokens"` // tokens a price covers
	HasPrice bool         `json:"has_price"`     // any price entered at all
	PerAgent []AgentSpend `json:"per_agent"`
}

type AgentSpend struct {
	Agent  string  `json:"agent"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost_usd"`
	Priced bool    `json:"priced"`
}

// spendBetween is, per agent and model, the growth of its running totals
// between two moments: the last sample at or before each. A first sample counts from zero.
func (c *call) spendBetween(from, to time.Time) (*Spend, error) {
	prices, err := c.prices()
	if err != nil {
		return nil, err
	}
	rows, err := c.tx.Query(`SELECT agent, model, input, output, cache_read, cache_write, at FROM usage_sample WHERE at <= ? ORDER BY at, id`, store.Millis(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ agent, model string }
	atFrom, atTo := map[key]tokens{}, map[key]tokens{}
	for rows.Next() {
		var k key
		var t tokens
		var at int64
		if err := rows.Scan(&k.agent, &k.model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &at); err != nil {
			return nil, err
		}
		atTo[k] = t
		if at <= store.Millis(from) {
			atFrom[k] = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s := &Spend{HasPrice: len(prices) > 0}
	per := map[string]*AgentSpend{}
	for k, now := range atTo {
		before := atFrom[k]
		d := tokens{now.Input - before.Input, now.Output - before.Output, now.CacheRead - before.CacheRead, now.CacheWrite - before.CacheWrite}
		if d.total() <= 0 {
			continue
		}
		s.Tokens += d.total()
		a := per[k.agent]
		if a == nil {
			a = &AgentSpend{Agent: k.agent}
			per[k.agent] = a
		}
		a.Tokens += d.total()
		if p := priceFor(prices, k.model); p != nil {
			cost := d.cost(p)
			s.Cost += cost
			s.Priced += d.total()
			a.Cost += cost
			a.Priced = true
		}
	}
	for _, a := range per {
		s.PerAgent = append(s.PerAgent, *a)
	}
	sort.Slice(s.PerAgent, func(i, j int) bool { return s.PerAgent[i].Tokens > s.PerAgent[j].Tokens })
	return s, nil
}

func tokensWords(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
