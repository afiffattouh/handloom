package hub

import (
	"fmt"
	"strings"
	"time"

	"handloom/internal/api"
	"handloom/internal/notify"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// The owner's settings and the things only the web used to do, as API calls,
// so the CLI (and anything else) can do everything the web UI can.

// ---- model prices ----

// setPrice saves what a model costs per million tokens (or every model whose
// name starts with it). The caller has checked who may.
func (c *call) setPrice(model string, p [4]float64) error {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 100 || strings.ContainsAny(model, "\x00\n\r") {
		return badRequest("Name the model, or the start of its name, such as claude-sonnet.")
	}
	for _, x := range p {
		if x < 0 || x > 100000 {
			return badRequest("A price is a number of dollars per million tokens, such as 3 or 0.30.")
		}
	}
	if _, err := c.tx.Exec(`INSERT INTO price(model, input, output, cache_read, cache_write) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET input = excluded.input, output = excluded.output, cache_read = excluded.cache_read, cache_write = excluded.cache_write`,
		model, p[0], p[1], p[2], p[3]); err != nil {
		return err
	}
	return c.audit("price.set", "price:"+model, map[string]any{"input": p[0], "output": p[1], "cache_read": p[2], "cache_write": p[3]})
}

func pricesList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	ps, err := c.prices()
	if err != nil {
		return nil, err
	}
	out := make([]api.Price, 0, len(ps))
	for _, p := range ps {
		out = append(out, api.Price{Model: p.Model, Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite})
	}
	return out, nil
}

func pricesSet(c *call) (any, error) {
	if err := c.ownerOnly("setting prices"); err != nil {
		return nil, err
	}
	var req api.Price
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Input == 0 && req.Output == 0 {
		return nil, badRequest("Enter the input and output price in dollars per million tokens.")
	}
	return nil, c.setPrice(req.Model, [4]float64{req.Input, req.Output, req.CacheRead, req.CacheWrite})
}

func pricesDelete(c *call) (any, error) {
	if err := c.ownerOnly("setting prices"); err != nil {
		return nil, err
	}
	var req api.Price
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if _, err := c.tx.Exec(`DELETE FROM price WHERE model = ?`, req.Model); err != nil {
		return nil, err
	}
	return nil, c.audit("price.delete", "price:"+req.Model, nil)
}

// ---- people ----

func (c *call) inviteLink(humanID int64, name string) (string, error) {
	tok := randomID()
	if err := store.CreateInvite(c.tx, sha(tok), humanID, store.Millis(c.now), store.Millis(c.now.Add(c.h.opt.InviteTTL))); err != nil {
		return "", err
	}
	if err := c.audit("human.invite", "human:"+name, nil); err != nil {
		return "", err
	}
	return c.h.publicBase(c.r) + "/invite/" + tok, nil
}

func peopleList(c *call) (any, error) {
	if err := c.ownerOnly("the list of people"); err != nil {
		return nil, err
	}
	hs, err := store.ListHumans(c.tx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Person, 0, len(hs))
	for _, h := range hs {
		out = append(out, api.Person{Name: h.Name, Role: h.Role, WebLogin: h.PasswordHash != ""})
	}
	return out, nil
}

func peopleAdd(c *call) (any, error) {
	if err := c.ownerOnly("adding people"); err != nil {
		return nil, err
	}
	var req api.PersonReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if req.Role == "" {
		req.Role = store.RoleMember
	}
	if err := checkName("person", req.Name); err != nil {
		return nil, err
	}
	if !memberRoles[req.Role] {
		return nil, badRequest("Choose member or viewer.")
	}
	id, err := store.CreateInvitedHuman(c.tx, req.Name, req.Role, store.Millis(c.now))
	if err != nil {
		return nil, conflict("There is already a person called %q.", req.Name)
	}
	if err := c.audit("human.add", "human:"+req.Name, map[string]any{"role": req.Role}); err != nil {
		return nil, err
	}
	link, err := c.inviteLink(id, req.Name)
	if err != nil {
		return nil, err
	}
	return api.PersonResp{Name: req.Name, Role: req.Role, InviteURL: link}, nil
}

func (c *call) person() (*store.Human, error) {
	name := c.r.PathValue("name")
	t, err := store.HumanByName(c.tx, name)
	if err != nil {
		return nil, err
	}
	if t == nil || t.Role == store.RoleOwner {
		return nil, notFound("no such person to change: %q", name)
	}
	return t, nil
}

func peopleInvite(c *call) (any, error) {
	if err := c.ownerOnly("inviting people"); err != nil {
		return nil, err
	}
	t, err := c.person()
	if err != nil {
		return nil, err
	}
	link, err := c.inviteLink(t.ID, t.Name)
	if err != nil {
		return nil, err
	}
	return api.PersonResp{Name: t.Name, Role: t.Role, InviteURL: link}, nil
}

func peopleRole(c *call) (any, error) {
	if err := c.ownerOnly("changing roles"); err != nil {
		return nil, err
	}
	var req api.PersonReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if !memberRoles[req.Role] {
		return nil, badRequest("Choose member or viewer.")
	}
	t, err := c.person()
	if err != nil {
		return nil, err
	}
	if err := store.SetRole(c.tx, t.ID, req.Role); err != nil {
		return nil, err
	}
	if err := store.DeleteSessionsFor(c.tx, t.ID); err != nil {
		return nil, err
	}
	return nil, c.audit("human.role", "human:"+t.Name, map[string]any{"role": req.Role})
}

// ---- notifications ----

func notificationsGet(c *call) (any, error) {
	if err := c.ownerOnly("notification settings"); err != nil {
		return nil, err
	}
	u, _, _ := store.MetaGet(c.tx, "ntfy_url")
	t, _, _ := store.MetaGet(c.tx, "ntfy_topic")
	return api.Notifications{NtfyURL: u, NtfyTopic: t, FromEnvironment: c.h.opt.Notifier != nil}, nil
}

func notificationsSet(c *call) (any, error) {
	if err := c.ownerOnly("notification settings"); err != nil {
		return nil, err
	}
	var req api.Notifications
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	u, topic := strings.TrimSpace(req.NtfyURL), strings.TrimSpace(req.NtfyTopic)
	if u != "" {
		if _, err := notify.NewNtfy(u, topic, ""); err != nil {
			return nil, badRequest("%s", err.Error())
		}
	}
	if err := store.MetaSet(c.tx, "ntfy_url", u); err != nil {
		return nil, err
	}
	if err := store.MetaSet(c.tx, "ntfy_topic", topic); err != nil {
		return nil, err
	}
	if err := c.audit("settings.notifications", "hub", map[string]any{"ntfy_url": u, "ntfy_topic": topic}); err != nil {
		return nil, err
	}
	c.after = append(c.after, func() {
		if err := c.h.reloadNotifier(); err != nil {
			c.h.opt.Log.Printf("notification settings: %v", err)
		}
	})
	return nil, nil
}

func notificationsTest(c *call) (any, error) {
	if err := c.ownerOnly("notification settings"); err != nil {
		return nil, err
	}
	if c.h.notifier() == nil {
		return nil, badRequest("No notification is configured yet.")
	}
	c.tell(notify.Notification{Kind: "test", Title: "Handloom: test", Text: "This is a test notification. If you can read it, push works."})
	return nil, nil
}

// ---- your own token ----

// meToken gives the signed-in human a new API token; the one used for this call stops working.
func meToken(c *call) (any, error) {
	if c.p.kind != kindHuman {
		return nil, forbidden("only a person has a personal token: the admin token is the admin's")
	}
	h, err := store.HumanByName(c.tx, c.p.name)
	if err != nil || h == nil {
		return nil, notFound("no such person")
	}
	tok := store.NewToken(store.PrefixHuman)
	if err := store.ReplaceHumanToken(c.tx, h.ID, store.HashToken(tok)); err != nil {
		return nil, err
	}
	if err := c.audit("human.token", "human:"+h.Name, nil); err != nil {
		return nil, err
	}
	return api.TokenResp{Name: h.Name, Token: tok}, nil
}

// ---- an agent's screen ----

// watchAllowed says whether this caller may look at an agent's terminal.
func (c *call) watchAllowed(a *agentRow) error {
	if c.p.kind == kindHuman && c.p.role == store.RoleViewer || c.p.kind == kindDevice {
		return forbidden("only an owner or a member may look at an agent's terminal")
	}
	if a.jobID.Valid {
		if j, err := c.task(a.jobID.Int64); err == nil && j.confidential && !c.h.opt.AllowConfidential {
			return forbidden("this agent works on a confidential job, and this hub is not set up to carry that material")
		}
	}
	if a.wakeTarget == "" || a.wakeTarget == "headless" {
		return conflict("%s has no terminal the machine's link can read", a.name)
	}
	return nil
}

// agentScreen asks for an agent's screen and returns the last one read. It
// does not wait: the caller asks again in a moment.
func agentScreen(c *call) (any, error) {
	if c.p.kind == kindDevice {
		return nil, forbidden("a device does not read screens through this call")
	}
	a, err := c.agentByName(c.r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, notFound("no agent %q", c.r.PathValue("name"))
	}
	if err := c.watchAllowed(a); err != nil {
		return nil, err
	}
	c.h.tails.ask(a.name, c.now)
	if err := c.emit(a.projectID, a.id, "pane.want", map[string]any{"agent": a.name}); err != nil {
		return nil, err
	}
	s, ok := c.h.tails.get(a.name, c.now)
	if !ok {
		return api.Screen{Pending: true}, nil
	}
	return api.Screen{Text: strings.TrimRight(s.text, "\n "), AgeSeconds: int(c.now.Sub(s.at) / time.Second)}, nil
}

var _ = profile.Cloud
var _ = fmt.Sprint

// meOperatorToken gives the signed-in person a token for their AI app. It
// works only for the calls in operatorCalls, so an app set to approve
// everything still cannot approve.
func meOperatorToken(c *call) (any, error) {
	if c.p.kind != kindHuman || c.p.operator {
		return nil, forbidden("only a person, with their own token, makes a token for an AI app")
	}
	h, err := store.HumanByName(c.tx, c.p.name)
	if err != nil || h == nil {
		return nil, notFound("no such person")
	}
	tok := store.NewToken(store.PrefixOperator)
	if err := store.SetOperatorToken(c.tx, h.ID, store.HashToken(tok), store.Millis(c.now)); err != nil {
		return nil, err
	}
	if err := c.audit("human.operator_token", "human:"+h.Name, nil); err != nil {
		return nil, err
	}
	return api.TokenResp{Name: h.Name, Token: tok}, nil
}

func meOperatorTokenRemove(c *call) (any, error) {
	if c.p.kind != kindHuman || c.p.operator {
		return nil, forbidden("only a person, with their own token, removes an AI app's access")
	}
	h, err := store.HumanByName(c.tx, c.p.name)
	if err != nil || h == nil {
		return nil, notFound("no such person")
	}
	if err := store.RemoveOperatorToken(c.tx, h.ID); err != nil {
		return nil, err
	}
	return nil, c.audit("human.operator_token.remove", "human:"+h.Name, nil)
}
