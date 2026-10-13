package hub

import (
	"net/http"
	"strconv"

	"handloom/internal/mcp"
	"handloom/internal/store"
)

// The "Connect an AI app" page: the settings to paste into Claude, Codex and
// the like, with this hub's address filled in, and a token that lets the app
// read and start work but not approve.

type connectView struct {
	Base      string
	HasToken  bool
	NewToken  string // shown once, right after it is made
	CanCreate bool
	Connector string // the address to paste into an AI app, or "" when the connector is off (no HANDLOOM_BASE_URL)
	Apps      []connectedApp
}

// connectedApp is one AI app the person allowed through the connector.
type connectedApp struct {
	ID        int64
	Name      string
	Connected string
	LastUsed  string
}

func (q *webReq) connectPage(status int, errMsg, notice, token string) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot connect an AI app: it could start work on their behalf.")
	}
	has, err := store.HasOperatorToken(q.c.tx, q.human.ID)
	if err != nil {
		return err
	}
	v := &connectView{Base: q.c.h.publicBase(q.r), HasToken: has || token != "", NewToken: token}
	if base := q.c.h.connectorBase(); base != "" {
		v.Connector = base + mcp.HTTPPath
	}
	grants, err := store.ConnectedApps(q.c.tx, q.human.ID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		app := connectedApp{ID: g.ID, Name: g.ClientName, Connected: store.Time(g.ActivatedAt.Int64).Format("2 Jan 2006 15:04 UTC"), LastUsed: "not yet"}
		if g.LastUsedAt.Valid && g.LastUsedAt.Int64 > g.ActivatedAt.Int64 {
			app.LastUsed = store.Time(g.LastUsedAt.Int64).Format("2 Jan 2006 15:04 UTC")
		}
		v.Apps = append(v.Apps, app)
	}
	q.page(status, "connect", pageData{Title: "Connect an AI app", Error: errMsg, Notice: notice, Extra: v})
	return nil
}

func (h *Hub) webConnect(q *webReq) error { return q.connectPage(200, "", "", "") }

func (h *Hub) webConnectToken(q *webReq) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot connect an AI app.")
	}
	if err := q.stepUp(); err != nil {
		return q.connectError(err)
	}
	tok := store.NewToken(store.PrefixOperator)
	if err := store.SetOperatorToken(q.c.tx, q.human.ID, store.HashToken(tok), store.Millis(q.now)); err != nil {
		return err
	}
	if err := q.c.audit("human.operator_token", "human:"+q.human.Name, nil); err != nil {
		return err
	}
	return q.connectPage(200, "", "", tok)
}

func (h *Hub) webConnectRemove(q *webReq) error {
	if err := q.stepUp(); err != nil {
		return q.connectError(err)
	}
	if err := store.RemoveOperatorToken(q.c.tx, q.human.ID); err != nil {
		return err
	}
	if err := q.c.audit("human.operator_token.remove", "human:"+q.human.Name, nil); err != nil {
		return err
	}
	http.Redirect(q.w, q.r, "/connect", http.StatusSeeOther)
	return nil
}

func (q *webReq) connectError(err error) error {
	msg, status := "Something went wrong.", 500
	if ae, ok := err.(*apiError); ok {
		status, msg = ae.status, ae.msg
	}
	return q.connectPage(status, msg, "", "")
}

// webAppDisconnect ends a connected app's grant: every access and refresh token it ever got stops working
// at once. It needs the session and the page's security token, not the password: it only takes access away.
func (h *Hub) webAppDisconnect(q *webReq) error {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return q.connectError(&apiError{404, "not_found", "That app is not connected."})
	}
	g, err := store.GrantByID(q.c.tx, id)
	if err != nil {
		return err
	}
	if g == nil || g.HumanID != q.human.ID || g.RevokedAt.Valid {
		return q.connectError(&apiError{404, "not_found", "That app is not connected."})
	}
	if _, err := store.RevokeGrant(q.c.tx, g.ID, store.Millis(q.now)); err != nil {
		return err
	}
	if err := q.c.audit("app.revoke", "human:"+q.human.Name, map[string]any{"app": g.ClientName, "reason": "disconnected by the person"}); err != nil {
		return err
	}
	http.Redirect(q.w, q.r, "/connect", http.StatusSeeOther)
	return nil
}
