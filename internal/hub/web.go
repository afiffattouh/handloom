package hub

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"handloom/internal/store"
)

//go:embed web/templates/*.html web/static/*
var webFS embed.FS

// The web UI: server-rendered pages, no script framework. Agent-written text
// (task bodies, notes, questions) is rendered only through html/template's
// escaping; nothing in this package uses template.HTML on such text.

const (
	cookiePlain  = "handloom_session"
	cookieSecure = "__Host-handloom_session"
	maxForm      = 64 << 10
)

var pageNames = []string{"setup", "login", "inbox", "closed", "message"}

type pageData struct {
	Title  string
	Human  *store.Human
	CSRF   string
	Error  string
	Notice string
	Name   string // form value kept after an error
	Setup  bool
	Extra  any
}

func (h *Hub) loadTemplates() error {
	h.pages = map[string]*template.Template{}
	for _, name := range pageNames {
		t, err := template.ParseFS(webFS, "web/templates/base.html", "web/templates/"+name+".html")
		if err != nil {
			return err
		}
		h.pages[name] = t
	}
	return nil
}

// webOrigin returns the origin this hub expects browsers to use, or "" when
// the web UI must stay off.
func (h *Hub) webOrigin(r *http.Request) string {
	if h.opt.BaseURL != "" {
		u, err := url.Parse(h.opt.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
	if h.opt.Insecure && r != nil {
		return "http://" + r.Host
	}
	return ""
}

// useSecureCookie is true when the hub is reached over https.
func (h *Hub) useSecureCookie() bool {
	return strings.HasPrefix(strings.ToLower(h.opt.BaseURL), "https://")
}

func isLocalhost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// WebEnabled reports whether the web UI is on, and if not, why.
func (h *Hub) WebEnabled() (bool, string) {
	switch {
	case h.opt.BaseURL == "" && !h.opt.Insecure:
		return false, "set HANDLOOM_BASE_URL to the public https:// URL of this hub (or HANDLOOM_INSECURE=1 on a private network)"
	case h.opt.BaseURL != "" && h.webOrigin(nil) == "":
		return false, "HANDLOOM_BASE_URL is not a valid http(s) URL"
	case h.opt.BaseURL != "" && !h.useSecureCookie() && !isLocalhost(h.opt.BaseURL) && !h.opt.Insecure:
		return false, "the web UI needs https: HANDLOOM_BASE_URL is plain http (use https, localhost, or HANDLOOM_INSECURE=1 on a private network)"
	}
	return true, ""
}

func (h *Hub) cookieName() string {
	if h.useSecureCookie() {
		return cookieSecure
	}
	return cookiePlain
}

func (h *Hub) clientIP(r *http.Request) string {
	if h.opt.TrustProxy {
		// The rightmost entry is the one our own proxy appended; the ones
		// before it are whatever the client sent.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- headers and rendering ----

func (h *Hub) webHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Cross-Origin-Opener-Policy", "same-origin")
	if h.useSecureCookie() {
		hd.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func (h *Hub) render(w http.ResponseWriter, status int, page string, d pageData) {
	t := h.pages[page]
	if t == nil {
		http.Error(w, "no such page", 500)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", d); err != nil {
		h.opt.Log.Printf("render %s: %v", page, err)
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// webRoutes adds the browser-facing routes to mux. They are always present;
// each one answers "web UI is off" when the hub is not configured for it.
func (h *Hub) webRoutes(mux *http.ServeMux) {
	web := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h.webHeaders(w)
			if ok, why := h.WebEnabled(); !ok {
				h.render(w, 503, "message", pageData{Title: "Web UI is off", Error: "The web UI is off on this hub: " + why + "."})
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /{$}", web(h.webRoot))
	mux.HandleFunc("GET /setup", web(h.webSetupGet))
	mux.HandleFunc("POST /setup", web(h.webSetupPost))
	mux.HandleFunc("GET /login", web(h.webLoginGet))
	mux.HandleFunc("POST /login", web(h.webLoginPost))
	mux.HandleFunc("POST /logout", web(h.authed(h.webLogout)))
	mux.HandleFunc("GET /inbox", web(h.authed(h.webInbox)))
	mux.HandleFunc("GET /inbox/fragment", web(h.authed(h.webInboxFragment)))
	mux.HandleFunc("POST /inbox/escalations/{id}/answer", web(h.authed(h.webAnswer)))
	mux.HandleFunc("POST /inbox/tasks/{id}/accept", web(h.authed(h.webAccept)))
	mux.HandleFunc("POST /inbox/tasks/{id}/reject", web(h.authed(h.webReject)))
	mux.HandleFunc("GET /ui/stream", web(h.webStream))
	static, _ := fs.Sub(webFS, "web/static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		h.webHeaders(w)
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// ---- same-origin checks ----

// strictSameOrigin is for forms that run before a session exists (login,
// setup): the browser must say the request is same-origin.
func (h *Hub) strictSameOrigin(r *http.Request) bool {
	want := h.webOrigin(r)
	if o := r.Header.Get("Origin"); o != "" {
		return want != "" && o == want
	}
	sfs := r.Header.Get("Sec-Fetch-Site")
	return sfs == "same-origin" || sfs == "none"
}

// looseSameOrigin is for signed-in requests, which also carry a CSRF token:
// a request without an Origin header (old browsers, curl) passes this check
// and has to pass the token check.
func (h *Hub) looseSameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return o == h.webOrigin(r)
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		return sfs == "same-origin" || sfs == "none"
	}
	return true
}

func (h *Hub) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		h.render(w, 400, "message", pageData{Title: "Bad request", Error: "The form could not be read."})
		return false
	}
	return true
}

// ---- setup ----

const setupAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no I, L, O, 0, 1

func normalizeCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// PrepareSetup is called when the hub starts. While the hub has no owner it
// creates a one-time setup code, stores only its hash, and returns the code
// to be shown in the server log. Whoever reads the log can set up the hub;
// whoever merely finds the URL cannot. With an owner it returns "".
func (h *Hub) PrepareSetup() (string, error) {
	owner, err := store.OwnerExists(h.db)
	if err != nil {
		return "", err
	}
	if owner {
		return "", store.MetaDelete(h.db, "setup_code_hash")
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := make([]byte, 12)
	for i, b := range raw {
		code[i] = setupAlphabet[int(b)%len(setupAlphabet)]
	}
	if err := store.MetaSet(h.db, "setup_code_hash", sha(string(code))); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", code[:4], code[4:8], code[8:]), nil
}

func (h *Hub) webSetupGet(w http.ResponseWriter, r *http.Request) {
	if owner, err := store.OwnerExists(h.db); err != nil || owner {
		h.render(w, 404, "closed", pageData{Title: "Not found"})
		return
	}
	h.render(w, 200, "setup", pageData{Title: "Set up Handloom", Setup: true})
}

func (h *Hub) webSetupPost(w http.ResponseWriter, r *http.Request) {
	if !h.strictSameOrigin(r) {
		h.render(w, 403, "message", pageData{Title: "Refused", Error: "The request did not come from this site."})
		return
	}
	if !h.parseForm(w, r) {
		return
	}
	now := h.opt.Now()
	if !h.allowRate("setup:ip:"+h.clientIP(r), 10, 10, now) {
		h.render(w, 429, "message", pageData{Title: "Slow down", Error: "Too many attempts. Wait a minute and try again."})
		return
	}
	if owner, err := store.OwnerExists(h.db); err != nil || owner {
		h.render(w, 404, "closed", pageData{Title: "Not found"})
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	pw, pw2 := r.PostForm.Get("password"), r.PostForm.Get("password2")
	fail := func(status int, msg string) {
		h.render(w, status, "setup", pageData{Title: "Set up Handloom", Setup: true, Error: msg, Name: name})
	}
	want, ok, err := store.MetaGet(h.db, "setup_code_hash")
	if err != nil || !ok || subtle.ConstantTimeCompare([]byte(sha(normalizeCode(r.PostForm.Get("code")))), []byte(want)) != 1 {
		h.auditAnon("setup.denied", "hub", map[string]any{"ip": h.clientIP(r)})
		fail(403, "That setup code is not right. It is printed in the hub's log when it starts.")
		return
	}
	if err := checkName("owner", name); err != nil {
		fail(400, "Use letters, digits, '.', '_' or '-' for the name, at most 64 characters.")
		return
	}
	if pw != pw2 {
		fail(400, "The two passwords differ.")
		return
	}
	if err := CheckPassword(name, pw); err != nil {
		fail(400, "Choose a stronger password: "+err.Error()+".")
		return
	}
	hash, err := HashPassword(pw) // slow; outside the transaction
	if err != nil {
		h.opt.Log.Printf("setup: %v", err)
		fail(500, "Something went wrong.")
		return
	}
	tx, err := h.db.Begin()
	if err != nil {
		fail(500, "Something went wrong.")
		return
	}
	defer tx.Rollback()
	// The transaction is serialised with every other write: of two
	// simultaneous submissions only one sees "no owner yet".
	if owner, err := store.OwnerExists(tx); err != nil || owner {
		h.render(w, 404, "closed", pageData{Title: "Not found"})
		return
	}
	id, err := store.CreateWebHuman(tx, name, store.RoleOwner, hash, store.Millis(now))
	if err != nil {
		fail(409, "That name is taken.")
		return
	}
	if err := store.MetaDelete(tx, "setup_code_hash"); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	c := &call{h: h, tx: tx, now: now, p: &principal{kind: kindHuman, name: name, role: store.RoleOwner}}
	if err := c.audit("setup.owner", "human:"+name, map[string]any{"ip": h.clientIP(r)}); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	if err := h.startSession(w, r, tx, id, now); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	if err := tx.Commit(); err != nil {
		fail(500, "Something went wrong.")
		return
	}
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

// auditAnon records an action by nobody in particular (a failed login).
func (h *Hub) auditAnon(action, target string, payload any) {
	_, err := h.db.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES ('anonymous', ?, ?, ?, ?)`,
		action, target, string(marshal(payload)), store.Millis(h.opt.Now()))
	if err != nil {
		h.opt.Log.Printf("audit %s: %v", action, err)
	}
}

// ---- sessions ----

func (h *Hub) startSession(w http.ResponseWriter, r *http.Request, q store.Querier, humanID int64, now time.Time) error {
	id := randomID()
	s := store.Session{IDHash: sha(id), HumanID: humanID, CSRF: randomID(),
		CreatedAt: store.Millis(now), LastSeenAt: store.Millis(now), ExpiresAt: store.Millis(now.Add(h.opt.SessionMax))}
	if err := store.CreateSession(q, s, h.clientIP(r)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: id, Path: "/", HttpOnly: true,
		Secure: h.useSecureCookie(), SameSite: http.SameSiteLaxMode, MaxAge: int(h.opt.SessionMax.Seconds())})
	return nil
}

func (h *Hub) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: "", Path: "/", HttpOnly: true,
		Secure: h.useSecureCookie(), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// webReq is one signed-in request. The transaction is open for the whole
// handler and committed if it returns nil.
type webReq struct {
	w     http.ResponseWriter
	r     *http.Request
	c     *call
	human *store.Human
	sess  *store.Session
	now   time.Time
}

func (q *webReq) page(status int, page string, d pageData) {
	d.Human, d.CSRF = q.human, q.sess.CSRF
	q.c.h.render(q.w, status, page, d)
}

// lookupSession resolves the session cookie to a session and a human. It
// returns nil, nil for a missing, unknown or expired session (and deletes an
// expired one).
func (h *Hub) lookupSession(q store.Querier, r *http.Request, now time.Time) (*store.Session, *store.Human, error) {
	cookie, err := r.Cookie(h.cookieName())
	if err != nil || cookie.Value == "" {
		return nil, nil, nil
	}
	idHash := sha(cookie.Value)
	sess, err := store.SessionByHash(q, idHash)
	if err != nil || sess == nil {
		return nil, nil, err
	}
	nowMs := store.Millis(now)
	if nowMs > sess.ExpiresAt || nowMs-sess.LastSeenAt > h.opt.SessionIdle.Milliseconds() {
		return nil, nil, store.DeleteSession(q, idHash)
	}
	human, err := store.HumanByID(q, sess.HumanID)
	if err != nil || human == nil {
		return nil, nil, err
	}
	return sess, human, nil
}

// authed wraps a handler that needs a signed-in human. Unsafe methods also
// need the session's CSRF token and a matching Origin.
func (h *Hub) authed(fn func(*webReq) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := h.opt.Now()
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
			return
		}
		defer tx.Rollback()
		sess, human, err := h.lookupSession(tx, r, now)
		if err != nil {
			h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
			return
		}
		if sess == nil {
			tx.Commit() // keeps the deletion of an expired session
			h.clearCookie(w)
			h.toLogin(w, r)
			return
		}
		nowMs := store.Millis(now)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !h.looseSameOrigin(r) {
				h.render(w, 403, "message", pageData{Title: "Refused", Error: "The request did not come from this site."})
				return
			}
			if !h.parseForm(w, r) {
				return
			}
			tok := r.Header.Get("X-CSRF-Token")
			if tok == "" {
				tok = r.PostForm.Get("csrf")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) != 1 {
				h.render(w, 403, "message", pageData{Title: "Refused", Error: "The security token is missing or out of date. Go back, reload the page and try again."})
				return
			}
		}
		if nowMs-sess.LastSeenAt > 60_000 {
			store.TouchSession(tx, sess.IDHash, nowMs)
		}
		c := &call{h: h, tx: tx, r: r, now: now, p: &principal{kind: kindHuman, name: human.Name, role: human.Role}}
		q := &webReq{w: w, r: r, c: c, human: human, sess: sess, now: now}
		if err := fn(q); err != nil {
			if !errors.Is(err, errHandled) {
				h.opt.Log.Printf("web %s %s: %v", r.Method, r.URL.Path, err)
			}
			return
		}
		if err := tx.Commit(); err != nil {
			h.opt.Log.Printf("web commit: %v", err)
			return
		}
		if c.events {
			h.wake()
		}
		c.runAfter()
	}
}

func (h *Hub) toLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.render(w, 401, "message", pageData{Title: "Signed out", Error: "Your session ended. Sign in again."})
}

// ---- login, logout, root ----

func (h *Hub) webRoot(w http.ResponseWriter, r *http.Request) {
	if owner, err := store.OwnerExists(h.db); err == nil && !owner {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

func (h *Hub) webLoginGet(w http.ResponseWriter, r *http.Request) {
	if owner, err := store.OwnerExists(h.db); err == nil && !owner {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	h.render(w, 200, "login", pageData{Title: "Sign in"})
}

const wrongLogin = "Wrong name or password."

func (h *Hub) webLoginPost(w http.ResponseWriter, r *http.Request) {
	if !h.strictSameOrigin(r) {
		h.render(w, 403, "message", pageData{Title: "Refused", Error: "The request did not come from this site."})
		return
	}
	if !h.parseForm(w, r) {
		return
	}
	now := h.opt.Now()
	name := strings.TrimSpace(r.PostForm.Get("name"))
	pw := r.PostForm.Get("password")
	ip := h.clientIP(r)
	// Rate limits come first, before any lookup or hashing.
	if !h.allowRate("login:ip:"+ip, 20, 10, now) || !h.allowRate("login:name:"+strings.ToLower(name), 5, 5, now) {
		h.render(w, 429, "login", pageData{Title: "Sign in", Error: "Too many attempts. Wait a minute and try again.", Name: name})
		return
	}
	if len(pw) > maxPassword || len(name) > 64 {
		h.render(w, 401, "login", pageData{Title: "Sign in", Error: wrongLogin, Name: name})
		return
	}
	human, err := store.HumanByName(h.db, name)
	if err != nil {
		h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
		return
	}
	ok := false
	if human != nil && human.PasswordHash != "" {
		ok = VerifyPassword(human.PasswordHash, pw)
	} else {
		VerifyPasswordDummy(pw) // same cost whether or not the name exists
	}
	if !ok {
		h.auditAnon("web.login.fail", "human:"+clip(name, 64), map[string]any{"ip": ip})
		h.render(w, 401, "login", pageData{Title: "Sign in", Error: wrongLogin, Name: name})
		return
	}
	tx, err := h.db.Begin()
	if err != nil {
		h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
		return
	}
	defer tx.Rollback()
	store.PurgeSessions(tx, store.Millis(now), h.opt.SessionIdle.Milliseconds())
	c := &call{h: h, tx: tx, now: now, p: &principal{kind: kindHuman, name: human.Name, role: human.Role}}
	if err := c.audit("web.login", "human:"+human.Name, map[string]any{"ip": ip}); err != nil {
		h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
		return
	}
	if err := h.startSession(w, r, tx, human.ID, now); err != nil { // a fresh id at every login
		h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
		return
	}
	if err := tx.Commit(); err != nil {
		h.render(w, 500, "message", pageData{Title: "Error", Error: "Something went wrong."})
		return
	}
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

func (h *Hub) webLogout(q *webReq) error {
	if err := store.DeleteSession(q.c.tx, q.sess.IDHash); err != nil {
		return err
	}
	if err := q.c.audit("web.logout", "human:"+q.human.Name, nil); err != nil {
		return err
	}
	h.clearCookie(q.w)
	http.Redirect(q.w, q.r, "/login", http.StatusSeeOther)
	return nil
}

// ResetPassword sets a new random password for a human and signs them out
// everywhere. It is the recovery path for a lost password: there is no web
// reset and no email. Whoever can open the database can run it.
func ResetPassword(db *sql.DB, name string, now time.Time) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	human, err := store.HumanByName(tx, name)
	if err != nil {
		return "", err
	}
	if human == nil {
		return "", fmt.Errorf("no human named %q", name)
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	pw := make([]byte, 20)
	for i, b := range raw {
		pw[i] = setupAlphabet[int(b)%len(setupAlphabet)]
	}
	hash, err := HashPassword(string(pw))
	if err != nil {
		return "", err
	}
	if err := store.SetPassword(tx, human.ID, hash); err != nil {
		return "", err
	}
	if err := store.DeleteSessionsFor(tx, human.ID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES ('admin', 'human.password.reset', ?, '{}', ?)`,
		"human:"+name, store.Millis(now)); err != nil {
		return "", err
	}
	return string(pw), tx.Commit()
}
