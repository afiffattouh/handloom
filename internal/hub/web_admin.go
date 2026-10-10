package hub

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"handloom/internal/notify"
	"handloom/internal/store"
)

// ---- notification settings ----

// notifier returns where escalations are announced: ntfy from Settings plus
// whatever the environment configured, or just the environment's.
func (h *Hub) notifier() notify.Notifier {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dyn != nil {
		return h.dyn
	}
	return h.opt.Notifier
}

// reloadNotifier rebuilds the notifier from Settings (meta ntfy_url, ntfy_topic).
// The access token is never in the database: it comes from HANDLOOM_NTFY_TOKEN.
func (h *Hub) reloadNotifier() error {
	u, _, err := store.MetaGet(h.db, "ntfy_url")
	if err != nil {
		return err
	}
	topic, _, err := store.MetaGet(h.db, "ntfy_topic")
	if err != nil {
		return err
	}
	var dyn notify.Notifier
	if u != "" {
		n, err := notify.NewNtfy(u, topic, h.opt.NtfyToken)
		if err != nil {
			return err
		}
		m := notify.Multi{n}
		if h.opt.Notifier != nil {
			m = append(m, h.opt.Notifier)
		}
		dyn = m
	}
	h.mu.Lock()
	h.dyn = dyn
	h.mu.Unlock()
	return nil
}

// ---- helpers ----

func (h *Hub) publicBase(r *http.Request) string {
	if h.opt.BaseURL != "" {
		return strings.TrimRight(h.opt.BaseURL, "/")
	}
	return h.webOrigin(r)
}

func (q *webReq) isOwner() bool { return q.human.Role == store.RoleOwner }

func (q *webReq) refuse(msg string) error {
	q.page(403, "message", pageData{Title: "Not allowed", Error: msg})
	return errHandled
}

// stepUp asks for the signed-in human's password again before an action that
// widens what the hub can do (adding a device, inviting someone, minting a
// token). A stolen session alone cannot do these.
func (q *webReq) stepUp() error {
	h := q.c.h
	if !h.allowRate("stepup:"+q.human.Name, 5, 5, q.now) {
		return &apiError{429, "rate_limited", "Too many password checks. Wait a minute."}
	}
	pw := q.r.PostForm.Get("current_password")
	if q.human.PasswordHash == "" || len(pw) > maxPassword || !VerifyPassword(q.human.PasswordHash, pw) {
		q.c.audit("web.stepup.fail", "human:"+q.human.Name, nil)
		return &apiError{403, "forbidden", "Your password is not right."}
	}
	return nil
}

// ---- devices ----

// installScriptURL is the installer that can also join a machine to this hub in one go.
const installScriptURL = "https://raw.githubusercontent.com/afiffattouh/handloom/main/install.sh"

type deviceView struct {
	Name     string
	Status   string
	Seen     string
	Agents   int
	Revoked  bool
	Pending  bool
	Ready    bool // joined, and its link has called in lately
	Settling bool // waiting to join, or joined and its link has not called in yet: the page keeps looking
}

type devicesView struct {
	Devices  []deviceView
	CanAdmin bool
	NewName  string // a device that was just added
	JoinCmd  string // shown once: install if needed, join, keep the link running, check the machine
	AltCmd   string // the same, for a machine that already has handloom
	Base     string
	TTL      string
}

func (q *webReq) devicesView(fresh *devicesView) (*devicesView, error) {
	h := q.c.h
	ds, err := store.ListDevices(q.c.tx)
	if err != nil {
		return nil, err
	}
	v := &devicesView{CanAdmin: q.isOwner(), Base: h.publicBase(q.r), TTL: humanDur(h.opt.JoinTTL)}
	if fresh != nil {
		v.NewName, v.JoinCmd, v.AltCmd = fresh.NewName, fresh.JoinCmd, fresh.AltCmd
	}
	for _, d := range ds {
		dv := deviceView{Name: d.Name, Agents: d.Agents}
		switch {
		case d.Revoked.Valid:
			dv.Status, dv.Revoked = "revoked "+ago(q.now, store.Time(d.Revoked.Int64)), true
		case d.Joined:
			dv.Status = "joined, link not running"
			dv.Settling = !d.LastSeen.Valid
			if d.LastSeen.Valid {
				dv.Seen = "seen " + ago(q.now, store.Time(d.LastSeen.Int64))
				if q.now.Sub(store.Time(d.LastSeen.Int64)) < time.Minute {
					dv.Status, dv.Ready = "ready", true
				}
			}
		default:
			dv.Pending, dv.Settling = true, true
			dv.Status = "waiting for it to join"
			if d.JoinUntil.Valid {
				if store.Time(d.JoinUntil.Int64).Before(q.now) {
					dv.Status = "join token expired; add the device again"
				} else {
					dv.Status = "waiting for it to join; the token works for " + humanDur(store.Time(d.JoinUntil.Int64).Sub(q.now))
				}
			}
		}
		v.Devices = append(v.Devices, dv)
	}
	return v, nil
}

func (q *webReq) devicesPage(status int, errMsg string, fresh *devicesView) error {
	v, err := q.devicesView(fresh)
	if err != nil {
		return err
	}
	q.page(status, "devices", pageData{Title: "Machines", Error: errMsg, Notice: map[string]string{"revoked": "Device revoked."}[q.r.URL.Query().Get("done")], Extra: v})
	return nil
}

func (h *Hub) webDevices(q *webReq) error { return q.devicesPage(200, "", nil) }

// webDevicesFragment is the list of machines alone, for the page to refresh while a machine is joining.
func (h *Hub) webDevicesFragment(q *webReq) error {
	v, err := q.devicesView(nil)
	if err != nil {
		return err
	}
	buf := &bytesBuffer{}
	if err := h.pages["devices"].ExecuteTemplate(buf, "machines", pageData{Human: q.human, CSRF: q.sess.CSRF, Extra: v}); err != nil {
		return err
	}
	q.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q.w.Write(buf.b)
	return nil
}

func (h *Hub) webDeviceAdd(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can add devices.")
	}
	if err := q.stepUp(); err != nil {
		return q.deviceError(err)
	}
	name := strings.TrimSpace(q.r.PostForm.Get("name"))
	if err := checkName("device", name); err != nil {
		return q.deviceError(err)
	}
	tok, err := q.c.addDevice(name)
	if err != nil {
		return q.deviceError(err)
	}
	base := h.publicBase(q.r)
	one := fmt.Sprintf("curl -fsSL %s | sh -s -- join %s %s", installScriptURL, base, tok)
	alt := fmt.Sprintf("handloom join %s %s", base, tok)
	return q.devicesPage(200, "", &devicesView{NewName: name, JoinCmd: one, AltCmd: alt})
}

func (q *webReq) deviceError(err error) error {
	var ae *apiError
	status, msg := 500, "Something went wrong."
	if errors.As(err, &ae) {
		status, msg = ae.status, ae.msg
	}
	return q.devicesPage(status, msg, nil)
}

func (h *Hub) webDeviceRevoke(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can revoke devices.")
	}
	if err := q.c.revokeDevice(q.r.PathValue("name")); err != nil {
		return q.deviceError(err)
	}
	http.Redirect(q.w, q.r, "/devices?done=revoked", http.StatusSeeOther)
	return nil
}

// ---- settings ----

type memberView struct {
	Name, Role string
	WebLogin   bool
	Self       bool
}

type reveal struct {
	InviteFor string
	InviteURL string
	NewToken  string
}

type settingsView struct {
	Members   []memberView
	NtfyURL   string
	NtfyTopic string
	NtfyToken bool // an access token is configured in the environment
	EnvNotify bool
	Base      string
	Reveal    reveal
	Prices    []priceRow
}

func (q *webReq) settingsPage(status int, errMsg, notice string, rv reveal) error {
	h := q.c.h
	hs, err := store.ListHumans(q.c.tx)
	if err != nil {
		return err
	}
	v := &settingsView{Base: h.publicBase(q.r), Reveal: rv, NtfyToken: h.opt.NtfyToken != "", EnvNotify: h.opt.Notifier != nil}
	for _, x := range hs {
		v.Members = append(v.Members, memberView{Name: x.Name, Role: x.Role, WebLogin: x.PasswordHash != "", Self: x.ID == q.human.ID})
	}
	if v.Prices, err = q.c.prices(); err != nil {
		return err
	}
	v.NtfyURL, _, _ = store.MetaGet(q.c.tx, "ntfy_url")
	v.NtfyTopic, _, _ = store.MetaGet(q.c.tx, "ntfy_topic")
	if notice == "" {
		notice = map[string]string{"saved": "Settings saved.", "role": "Role changed. That person was signed out."}[q.r.URL.Query().Get("done")]
	}
	q.page(status, "settings", pageData{Title: "Settings", Error: errMsg, Notice: notice, Extra: v})
	return nil
}

func (q *webReq) settingsError(err error) error {
	var ae *apiError
	status, msg := 500, "Something went wrong."
	if errors.As(err, &ae) {
		status, msg = ae.status, ae.msg
	}
	return q.settingsPage(status, msg, "", reveal{})
}

func (h *Hub) webSettings(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Settings are for the owner.")
	}
	return q.settingsPage(200, "", "", reveal{})
}

var memberRoles = map[string]bool{store.RoleMember: true, store.RoleViewer: true}

func (q *webReq) invite(humanID int64, name string) (string, error) {
	h := q.c.h
	tok := randomID()
	if err := store.CreateInvite(q.c.tx, sha(tok), humanID, store.Millis(q.now), store.Millis(q.now.Add(h.opt.InviteTTL))); err != nil {
		return "", err
	}
	if err := q.c.audit("human.invite", "human:"+name, nil); err != nil {
		return "", err
	}
	return h.publicBase(q.r) + "/invite/" + tok, nil
}

func (h *Hub) webMemberAdd(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can add people.")
	}
	if err := q.stepUp(); err != nil {
		return q.settingsError(err)
	}
	name, role := strings.TrimSpace(q.r.PostForm.Get("name")), q.r.PostForm.Get("role")
	if err := checkName("person", name); err != nil {
		return q.settingsError(err)
	}
	if !memberRoles[role] {
		return q.settingsError(badRequest("Choose member or viewer."))
	}
	id, err := store.CreateInvitedHuman(q.c.tx, name, role, store.Millis(q.now))
	if err != nil {
		return q.settingsError(conflict("There is already a person called %q.", name))
	}
	if err := q.c.audit("human.add", "human:"+name, map[string]any{"role": role}); err != nil {
		return err
	}
	link, err := q.invite(id, name)
	if err != nil {
		return err
	}
	return q.settingsPage(200, "", "", reveal{InviteFor: name, InviteURL: link})
}

func (h *Hub) webMemberInvite(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can invite people.")
	}
	if err := q.stepUp(); err != nil {
		return q.settingsError(err)
	}
	name := q.r.PathValue("name")
	target, err := store.HumanByName(q.c.tx, name)
	if err != nil {
		return err
	}
	if target == nil || target.ID == q.human.ID {
		return q.settingsError(notFound("No such person to invite."))
	}
	link, err := q.invite(target.ID, name)
	if err != nil {
		return err
	}
	return q.settingsPage(200, "", "", reveal{InviteFor: name, InviteURL: link})
}

func (h *Hub) webMemberRole(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can change roles.")
	}
	if err := q.stepUp(); err != nil {
		return q.settingsError(err)
	}
	name, role := q.r.PathValue("name"), q.r.PostForm.Get("role")
	target, err := store.HumanByName(q.c.tx, name)
	if err != nil {
		return err
	}
	if target == nil || target.ID == q.human.ID || target.Role == store.RoleOwner {
		return q.settingsError(badRequest("That person's role cannot be changed here."))
	}
	if !memberRoles[role] {
		return q.settingsError(badRequest("Choose member or viewer."))
	}
	if err := store.SetRole(q.c.tx, target.ID, role); err != nil {
		return err
	}
	if err := store.DeleteSessionsFor(q.c.tx, target.ID); err != nil { // a changed role takes effect at once
		return err
	}
	if err := q.c.audit("human.role", "human:"+name, map[string]any{"role": role}); err != nil {
		return err
	}
	http.Redirect(q.w, q.r, "/settings?done=role", http.StatusSeeOther)
	return nil
}

func (h *Hub) webNotifications(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can change notifications.")
	}
	u, topic := strings.TrimSpace(q.r.PostForm.Get("ntfy_url")), strings.TrimSpace(q.r.PostForm.Get("ntfy_topic"))
	if u != "" {
		if _, err := notify.NewNtfy(u, topic, ""); err != nil {
			return q.settingsError(badRequest("%s", err.Error()))
		}
	}
	if err := store.MetaSet(q.c.tx, "ntfy_url", u); err != nil {
		return err
	}
	if err := store.MetaSet(q.c.tx, "ntfy_topic", topic); err != nil {
		return err
	}
	if err := q.c.audit("settings.notifications", "hub", map[string]any{"ntfy_url": u, "ntfy_topic": topic}); err != nil {
		return err
	}
	q.c.after = append(q.c.after, func() {
		if err := h.reloadNotifier(); err != nil {
			h.opt.Log.Printf("notification settings: %v", err)
		}
	})
	http.Redirect(q.w, q.r, "/settings?done=saved", http.StatusSeeOther)
	return nil
}

func (h *Hub) webNotifyTest(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can send a test.")
	}
	if h.notifier() == nil {
		return q.settingsError(badRequest("No notification is configured yet."))
	}
	q.c.tell(notify.Notification{Kind: "test", Title: "Handloom: test", Text: "This is a test notification. If you can read it, push works."})
	return q.settingsPage(200, "", "Test notification sent.", reveal{})
}

// webToken gives the signed-in human a new personal API token (for the CLI
// and the TUI). The old one stops working. It is shown once.
func (h *Hub) webToken(q *webReq) error {
	if err := q.stepUp(); err != nil {
		return q.settingsError(err)
	}
	tok := store.NewToken(store.PrefixHuman)
	if err := store.ReplaceHumanToken(q.c.tx, q.human.ID, store.HashToken(tok)); err != nil {
		return err
	}
	if err := q.c.audit("human.token", "human:"+q.human.Name, nil); err != nil {
		return err
	}
	if q.human.Role == store.RoleOwner {
		return q.settingsPage(200, "", "", reveal{NewToken: tok})
	}
	q.page(200, "message", pageData{Title: "Your new API token", Notice: "Copy it now; it is not shown again: " + tok})
	return nil
}

// ---- invite ----

func (h *Hub) inviteHuman(r *http.Request) (*store.Human, string, error) {
	tok := r.PathValue("token")
	human, err := store.InviteHuman(h.db, sha(tok), store.Millis(h.opt.Now()))
	return human, tok, err
}

func (h *Hub) webInviteGet(w http.ResponseWriter, r *http.Request) {
	if !h.allowRate("invite:ip:"+h.clientIP(r), 20, 20, h.opt.Now()) {
		h.render(w, 429, "message", pageData{Title: "Slow down", Error: "Too many attempts. Wait a minute."})
		return
	}
	human, _, err := h.inviteHuman(r)
	if err != nil || human == nil {
		h.render(w, 404, "closed", pageData{Title: "Not found"})
		return
	}
	h.render(w, 200, "invite", pageData{Title: "Welcome", Name: human.Name})
}

func (h *Hub) webInvitePost(w http.ResponseWriter, r *http.Request) {
	if !h.strictSameOrigin(r) {
		h.render(w, 403, "message", pageData{Title: "Refused", Error: "The request did not come from this site."})
		return
	}
	if !h.parseForm(w, r) {
		return
	}
	now := h.opt.Now()
	if !h.allowRate("invite:ip:"+h.clientIP(r), 20, 20, now) {
		h.render(w, 429, "message", pageData{Title: "Slow down", Error: "Too many attempts. Wait a minute."})
		return
	}
	human, tok, err := h.inviteHuman(r)
	if err != nil || human == nil {
		h.render(w, 404, "closed", pageData{Title: "Not found"})
		return
	}
	pw, pw2 := r.PostForm.Get("password"), r.PostForm.Get("password2")
	fail := func(status int, msg string) {
		h.render(w, status, "invite", pageData{Title: "Welcome", Name: human.Name, Error: msg})
	}
	if pw != pw2 {
		fail(400, "The two passwords differ.")
		return
	}
	if err := CheckPassword(human.Name, pw); err != nil {
		fail(400, "Choose a stronger password: "+err.Error()+".")
		return
	}
	hash, err := HashPassword(pw)
	if err != nil {
		fail(500, "Something went wrong.")
		return
	}
	tx, err := h.db.Begin()
	if err != nil {
		fail(500, "Something went wrong.")
		return
	}
	defer tx.Rollback()
	// Re-check inside the transaction: a link is good for one use.
	if again, err := store.InviteHuman(tx, sha(tok), store.Millis(now)); err != nil || again == nil {
		h.render(w, 404, "closed", pageData{Title: "Not found"})
		return
	}
	if err := store.SetPassword(tx, human.ID, hash); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	store.DeleteSessionsFor(tx, human.ID) // an invite to an existing person is also a password reset
	store.DeleteInvite(tx, sha(tok))
	c := &call{h: h, tx: tx, now: now, p: &principal{kind: kindHuman, name: human.Name, role: human.Role}}
	if err := c.audit("human.invite.accept", "human:"+human.Name, map[string]any{"ip": h.clientIP(r)}); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	if err := h.startSession(w, r, tx, human.ID, now); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	if err := tx.Commit(); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	http.Redirect(w, r, "/command", http.StatusSeeOther)
}

var _ = url.PathEscape

// humanDur says a duration in words: "15 minutes", "2 hours", "1 minute".
func humanDur(d time.Duration) string {
	unit, n := "minute", int(d.Round(time.Minute)/time.Minute)
	switch {
	case d >= 48*time.Hour:
		unit, n = "day", int(d.Round(24*time.Hour)/(24*time.Hour))
	case d >= 2*time.Hour:
		unit, n = "hour", int(d.Round(time.Hour)/time.Hour)
	}
	if n < 1 {
		n = 1
	}
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// ---- model prices ----

// webPriceSet saves the price of a model (or of every model whose name starts with it).
func (h *Hub) webPriceSet(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can set prices.")
	}
	if err := q.stepUp(); err != nil {
		return q.settingsError(err)
	}
	f := q.r.PostForm
	model := strings.TrimSpace(f.Get("model"))
	if model == "" || len(model) > 100 || strings.ContainsAny(model, "\x00\n\r") {
		return q.settingsError(badRequest("Name the model, or the start of its name, such as claude-sonnet."))
	}
	var p [4]float64
	for i, k := range []string{"input", "output", "cache_read", "cache_write"} {
		v := strings.TrimSpace(f.Get(k))
		if v == "" {
			if i < 2 {
				return q.settingsError(badRequest("Enter the input and output price in dollars per million tokens."))
			}
			continue
		}
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || x < 0 || x > 100000 {
			return q.settingsError(badRequest("A price is a number of dollars per million tokens, such as 3 or 0.30."))
		}
		p[i] = x
	}
	if _, err := q.c.tx.Exec(`INSERT INTO price(model, input, output, cache_read, cache_write) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET input = excluded.input, output = excluded.output, cache_read = excluded.cache_read, cache_write = excluded.cache_write`,
		model, p[0], p[1], p[2], p[3]); err != nil {
		return err
	}
	if err := q.c.audit("price.set", "price:"+model, map[string]any{"input": p[0], "output": p[1], "cache_read": p[2], "cache_write": p[3]}); err != nil {
		return err
	}
	http.Redirect(q.w, q.r, "/settings?done=saved", http.StatusSeeOther)
	return nil
}

func (h *Hub) webPriceDelete(q *webReq) error {
	if !q.isOwner() {
		return q.refuse("Only the owner can set prices.")
	}
	model := q.r.PostForm.Get("model")
	if _, err := q.c.tx.Exec(`DELETE FROM price WHERE model = ?`, model); err != nil {
		return err
	}
	if err := q.c.audit("price.delete", "price:"+model, nil); err != nil {
		return err
	}
	http.Redirect(q.w, q.r, "/settings?done=saved", http.StatusSeeOther)
	return nil
}
