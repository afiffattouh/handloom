package hub

import (
	"net/http"

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
}

func (q *webReq) connectPage(status int, errMsg, notice, token string) error {
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot connect an AI app: it could start work on their behalf.")
	}
	has, err := store.HasOperatorToken(q.c.tx, q.human.ID)
	if err != nil {
		return err
	}
	q.page(status, "connect", pageData{Title: "Connect an AI app", Error: errMsg, Notice: notice,
		Extra: &connectView{Base: q.c.h.publicBase(q.r), HasToken: has || token != "", NewToken: token}})
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
