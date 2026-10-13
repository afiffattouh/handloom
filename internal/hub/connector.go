package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"handloom/internal/mcp"
	"handloom/internal/store"
)

// The remote connector (D109): a person adds Handloom to Claude by pasting one
// address, <hub>/mcp, and signing in. This file is the OAuth side of that (the
// hub is its own authorization server: discovery documents, app registration,
// the person's consent, the token exchange) and the /mcp endpoint itself.
//
// What an app gets is exactly what a D80 token gets: it acts as the person
// and the hub lets it make only the calls in operatorCalls. The access token
// authenticates as an operator principal (see authenticate), so there is one
// place that decides what it may do.

const (
	oauthScope       = "handloom"
	accessTTL        = time.Hour
	refreshTTL       = 60 * 24 * time.Hour // slides: every refresh issues a new one
	codeTTL          = 2 * time.Minute
	maxOAuthClients  = 500
	clientNeverUsed  = 7 * 24 * time.Hour
	claudeCallback   = "https://claude.ai/api/mcp/auth_callback"
	maxClientName    = 60
	maxRegisterBody  = 4 << 10
	maxTokenBody     = 16 << 10
	maxAuthzParamLen = 512
	toolTimeout      = 90 * time.Second
)

// OperatorRun runs one tool of the connector (the argument list of the
// matching `handloom` command) as the person whose access token is given,
// against the hub's own handler, and returns what the command printed. The
// command line provides it, because it owns the verbs; the hub cannot import it.
type OperatorRun func(ctx context.Context, handler http.Handler, token string, argv []string) (stdout, stderr string, code int)

// connectorBase is the hub's public address without a trailing slash, or ""
// when the connector is off. It is off without a configured address: the
// issuer in the discovery documents must be a fixed, trusted value.
func (h *Hub) connectorBase() string {
	if h.opt.BaseURL == "" {
		return ""
	}
	if ok, _ := h.WebEnabled(); !ok {
		return ""
	}
	return strings.TrimRight(h.opt.BaseURL, "/")
}

// The paths of the connector. Tests pin them: they are what Claude is told to look for.
const (
	pathResourceMeta  = "/.well-known/oauth-protected-resource"
	pathServerMeta    = "/.well-known/oauth-authorization-server"
	pathOpenIDMeta    = "/.well-known/openid-configuration"
	pathAuthorize     = "/oauth/authorize"
	pathToken         = "/oauth/token"
	pathRegister      = "/oauth/register"
	authorizeLoginKey = pathAuthorize + "?"
)

func (h *Hub) connectorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+pathResourceMeta, h.metaResource)
	mux.HandleFunc("GET "+pathResourceMeta+mcp.HTTPPath, h.metaResource)
	mux.HandleFunc("GET "+pathServerMeta, h.metaServer)
	mux.HandleFunc("GET "+pathOpenIDMeta, h.metaServer)
	mux.HandleFunc("POST "+pathRegister, h.oauthRegister)
	mux.HandleFunc("POST "+pathToken, h.oauthToken)
	mux.HandleFunc("GET "+pathAuthorize, h.webWrap(h.authed(h.oauthAuthorize)))
	mux.HandleFunc("POST "+pathAuthorize, h.webWrap(h.authed(h.oauthAuthorize)))
	mux.HandleFunc("POST /connect/apps/{id}/disconnect", h.webWrap(h.authed(h.webAppDisconnect)))
	mux.HandleFunc(mcp.HTTPPath, h.handleMCP)
	mux.HandleFunc(mcp.HTTPPath+"/{$}", h.handleMCP)
}

// safeNext is where the sign-in page may send a person afterwards: only the
// authorize address, with its query, as a path on this hub. Anything else gives
// "", so the sign-in page cannot be made an open redirect.
func safeNext(s string) string {
	if !strings.HasPrefix(s, authorizeLoginKey) || len(s) > 4096 {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ' ' {
			return ""
		}
	}
	return s
}

// ---- discovery ----

func oauthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func connectorOff(w http.ResponseWriter) {
	oauthJSON(w, 404, map[string]string{"error": "not_found", "error_description": "The connector is off on this hub: set HANDLOOM_BASE_URL to its public https address."})
}

// metaResource is RFC 9728: which resource this is and who signs people in for it.
func (h *Hub) metaResource(w http.ResponseWriter, r *http.Request) {
	base := h.connectorBase()
	if base == "" {
		connectorOff(w)
		return
	}
	oauthJSON(w, 200, map[string]any{
		"resource":                 base + mcp.HTTPPath,
		"authorization_servers":    []string{base}, // exactly one: Claude uses only the first
		"scopes_supported":         []string{oauthScope},
		"bearer_methods_supported": []string{"header"},
	})
}

// metaServer is RFC 8414 (also served as the OpenID address some clients try first).
func (h *Hub) metaServer(w http.ResponseWriter, r *http.Request) {
	base := h.connectorBase()
	if base == "" {
		connectorOff(w)
		return
	}
	oauthJSON(w, 200, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + pathAuthorize,
		"token_endpoint":                        base + pathToken,
		"registration_endpoint":                 base + pathRegister,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{oauthScope},
		// RFC 9207: the redirect carries iss, so an app can tell which server answered.
		"authorization_response_iss_parameter_supported": true,
	})
}

// ---- redirect addresses ----

func isLoopbackHost(host string) bool { return host == "localhost" || host == "127.0.0.1" }

var portRE = regexp.MustCompile(`^[0-9]{1,5}$`)

// parseRedirect parses an address an app may be sent back to. It must be absolute,
// with no fragment or login in it, and short.
func parseRedirect(s string) (*url.URL, bool) {
	if s == "" || len(s) > maxAuthzParamLen {
		return nil, false
	}
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	return u, true
}

func isLoopbackRedirect(u *url.URL) bool {
	return u.Scheme == "http" && isLoopbackHost(u.Hostname()) && (u.Port() == "" || portRE.MatchString(u.Port()))
}

// redirectAllowed says whether an app may register this address: Claude's own, the
// loopback addresses Claude Code listens on, or one the owner listed in HANDLOOM_OAUTH_REDIRECTS.
func (h *Hub) redirectAllowed(s string) bool {
	u, ok := parseRedirect(s)
	if !ok {
		return false
	}
	if isLoopbackRedirect(u) {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	if s == claudeCallback {
		return true
	}
	for _, extra := range h.opt.OAuthRedirects {
		if s == extra {
			return true
		}
	}
	return false
}

// redirectMatches is the check at authorize time (RFC 8252): the address must be
// the registered one exactly, except that a loopback address matches whatever port
// the app is listening on this time.
func redirectMatches(registered, presented string) bool {
	if subtle.ConstantTimeCompare([]byte(registered), []byte(presented)) == 1 {
		return true
	}
	ru, ok1 := parseRedirect(registered)
	pu, ok2 := parseRedirect(presented)
	if !ok1 || !ok2 || !isLoopbackRedirect(ru) || !isLoopbackRedirect(pu) {
		return false
	}
	return ru.Hostname() == pu.Hostname() && ru.EscapedPath() == pu.EscapedPath() && ru.RawQuery == pu.RawQuery
}

// cleanClientName keeps what a person will read on the consent page short and printable.
func cleanClientName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	if rs := []rune(name); len(rs) > maxClientName {
		name = string(rs[:maxClientName])
	}
	if name == "" {
		name = "An AI app"
	}
	return name
}

// ---- registration (RFC 7591) ----

type oauthError struct {
	status int
	code   string
	desc   string
	basic  bool // answer with a Basic challenge (the app tried to log in that way)
}

func (e *oauthError) write(w http.ResponseWriter) {
	if e.status == 401 && e.basic {
		w.Header().Set("WWW-Authenticate", `Basic realm="Handloom"`)
	}
	oauthJSON(w, e.status, map[string]string{"error": e.code, "error_description": e.desc})
}

func (h *Hub) oauthRegister(w http.ResponseWriter, r *http.Request) {
	base := h.connectorBase()
	if base == "" {
		connectorOff(w)
		return
	}
	now := h.opt.Now()
	if !h.allowRate("oauthreg:ip:"+h.clientIP(r), 10, 10, now) {
		(&oauthError{429, "temporarily_unavailable", "Too many registrations from this address. Wait a minute.", false}).write(w)
		return
	}
	var req struct {
		RedirectURIs  []string `json:"redirect_uris"`
		ClientName    string   `json:"client_name"`
		AuthMethod    string   `json:"token_endpoint_auth_method"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
		Scope         string   `json:"scope"`
		ClientSecret  string   `json:"client_secret"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegisterBody))
	if err := dec.Decode(&req); err != nil {
		(&oauthError{400, "invalid_client_metadata", "The registration could not be read as JSON (at most 4 KB).", false}).write(w)
		return
	}
	bad := func(code, desc string) { (&oauthError{400, code, desc, false}).write(w) }
	if req.AuthMethod != "" && req.AuthMethod != "none" || req.ClientSecret != "" {
		bad("invalid_client_metadata", "This hub registers public apps only: token_endpoint_auth_method must be none, and no client secret is kept.")
		return
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			bad("invalid_client_metadata", "Only the authorization_code and refresh_token grants are supported.")
			return
		}
	}
	for _, t := range req.ResponseTypes {
		if t != "code" {
			bad("invalid_client_metadata", "Only the code response type is supported.")
			return
		}
	}
	if req.Scope != "" {
		for _, sc := range strings.Fields(req.Scope) {
			if sc != oauthScope {
				bad("invalid_client_metadata", "The only scope is "+oauthScope+".")
				return
			}
		}
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 5 {
		bad("invalid_redirect_uri", "Give between one and five redirect_uris.")
		return
	}
	for _, u := range req.RedirectURIs {
		if !h.redirectAllowed(u) {
			bad("invalid_redirect_uri", "That redirect address is not one this hub accepts. It takes Claude's own address, a http://localhost or http://127.0.0.1 address, and any https address the owner lists in HANDLOOM_OAUTH_REDIRECTS.")
			return
		}
	}
	name := cleanClientName(req.ClientName)
	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	clientID := store.NewToken(store.PrefixClient)
	tx, err := h.db.Begin()
	if err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	defer tx.Rollback()
	if err := store.PurgeOAuth(tx, store.Millis(now), clientNeverUsed.Milliseconds()); err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	n, err := store.CountOAuthClients(tx)
	if err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	if n >= maxOAuthClients {
		tx.Commit()
		(&oauthError{503, "temporarily_unavailable", "This hub has too many apps waiting to connect. Try again later.", false}).write(w)
		return
	}
	if err := store.CreateOAuthClient(tx, clientID, name, req.RedirectURIs, store.Millis(now)); err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	oauthJSON(w, 201, map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        now.Unix(),
		"redirect_uris":              req.RedirectURIs,
		"client_name":                name,
		"grant_types":                req.GrantTypes,
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      oauthScope,
	})
}

func (h *Hub) writeOAuthInternal(w http.ResponseWriter, err error) {
	h.opt.Log.Printf("oauth: %v", err)
	(&oauthError{500, "server_error", "Something went wrong on the hub. It has been logged.", false}).write(w)
}

// ---- the person's consent ----

type authzParams struct {
	clientID, redirectURI, responseType, challenge, method, state, resource, scope string
}

func authzFrom(v url.Values) authzParams {
	return authzParams{clientID: v.Get("client_id"), redirectURI: v.Get("redirect_uri"), responseType: v.Get("response_type"),
		challenge: v.Get("code_challenge"), method: v.Get("code_challenge_method"), state: v.Get("state"), resource: v.Get("resource"), scope: v.Get("scope")}
}

var challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (h *Hub) resourceOK(res string) bool {
	base := h.connectorBase()
	return res == "" || res == base || res == base+"/" || res == base+mcp.HTTPPath
}

// authzCheck validates an authorization request. The app and the redirect address come first: if either is
// wrong the person gets a plain page and nothing is redirected to an address the app did not register.
// Everything else wrong is sent back to the app as an OAuth error.
func (h *Hub) authzCheck(q store.Querier, a authzParams) (cl *store.OAuthClient, page string, code, desc string, err error) {
	if a.clientID != "" && len(a.clientID) <= 128 {
		if cl, err = store.OAuthClientByID(q, a.clientID); err != nil {
			return nil, "", "", "", err
		}
	}
	if cl == nil {
		return nil, "This app is not registered with this hub. Go back to the app and try to connect again.", "", "", nil
	}
	ok := false
	for _, reg := range cl.RedirectURIs {
		ok = ok || redirectMatches(reg, a.redirectURI)
	}
	if !ok {
		return nil, "The address the app wants to come back to is not the one it registered. Nothing was sent there.", "", "", nil
	}
	switch {
	case a.responseType != "code":
		return cl, "", "unsupported_response_type", "Only response_type=code is supported.", nil
	case a.method != "S256" || !challengeRE.MatchString(a.challenge):
		return cl, "", "invalid_request", "A code_challenge made with S256 is required.", nil
	case len(a.state) > maxAuthzParamLen:
		return cl, "", "invalid_request", "The state is too long.", nil
	case !h.resourceOK(a.resource):
		return cl, "", "invalid_target", "The resource must be this hub's address or its /mcp address.", nil
	}
	for _, sc := range strings.Fields(a.scope) {
		if sc != oauthScope {
			return cl, "", "invalid_scope", "The only scope is " + oauthScope + ".", nil
		}
	}
	return cl, "", "", "", nil
}

// backToApp redirects the person's browser to the app's address with the answer in the query (RFC 6749, with iss from RFC 9207).
func (h *Hub) backToApp(w http.ResponseWriter, r *http.Request, a authzParams, answer url.Values) {
	u, _ := url.Parse(a.redirectURI)
	qv := u.Query()
	for k, v := range answer {
		qv[k] = v
	}
	if a.state != "" {
		qv.Set("state", a.state)
	}
	qv.Set("iss", h.connectorBase())
	u.RawQuery = qv.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type consentView struct {
	Client, Person string
	Fields         []consentField
}

type consentField struct{ Name, Value string }

// allowFormTo lets the consent page's form end in a redirect to the app. Browsers check form-action against
// the redirect that follows the post too, and the page's usual rule is 'self' only.
func (h *Hub) allowFormTo(w http.ResponseWriter, redirectURI string) {
	u, ok := parseRedirect(redirectURI)
	if !ok {
		return
	}
	csp := w.Header().Get("Content-Security-Policy")
	w.Header().Set("Content-Security-Policy", strings.Replace(csp, "form-action 'self'", "form-action 'self' "+u.Scheme+"://"+u.Host, 1))
}

func (h *Hub) oauthAuthorize(q *webReq) error {
	if h.connectorBase() == "" {
		q.page(404, "message", pageData{Title: "Connector is off", Error: "Connecting an app is off on this hub: set HANDLOOM_BASE_URL to its public https address."})
		return nil
	}
	if q.human.Role == store.RoleViewer {
		return q.refuse("Viewers cannot connect an AI app: it could start work on their behalf.")
	}
	vals := q.r.URL.Query()
	post := q.r.Method == http.MethodPost
	if post {
		vals = q.r.PostForm
	}
	a := authzFrom(vals)
	cl, page, code, desc, err := h.authzCheck(q.c.tx, a)
	if err != nil {
		return err
	}
	if page != "" {
		q.page(400, "message", pageData{Title: "Cannot connect", Error: page})
		return nil
	}
	if code != "" {
		h.backToApp(q.w, q.r, a, url.Values{"error": {code}, "error_description": {desc}})
		return nil
	}
	view := func(errMsg string, status int) error {
		h.allowFormTo(q.w, a.redirectURI)
		fields := []consentField{{"client_id", a.clientID}, {"redirect_uri", a.redirectURI}, {"response_type", a.responseType}, {"code_challenge", a.challenge},
			{"code_challenge_method", a.method}, {"state", a.state}, {"resource", a.resource}, {"scope", a.scope}}
		q.page(status, "consent", pageData{Title: "Connect " + cl.Name, Error: errMsg, Extra: &consentView{Client: cl.Name, Person: q.human.Name, Fields: fields}})
		return nil
	}
	if !post {
		return view("", 200)
	}
	if q.r.PostForm.Get("decision") != "allow" {
		if err := q.c.audit("app.deny", "human:"+q.human.Name, map[string]any{"app": cl.Name}); err != nil {
			return err
		}
		h.backToApp(q.w, q.r, a, url.Values{"error": {"access_denied"}, "error_description": {"The person chose not to connect."}})
		return nil
	}
	if err := q.stepUp(); err != nil {
		msg, status := "Something went wrong.", 500
		if ae, ok := err.(*apiError); ok {
			status, msg = ae.status, ae.msg
		}
		return view(msg, status)
	}
	now := store.Millis(q.now)
	gid, err := store.CreateGrant(q.c.tx, q.human.ID, cl.ID, now)
	if err != nil {
		return err
	}
	authCode := store.NewToken(store.PrefixCode)
	if err := store.InsertOAuthToken(q.c.tx, store.OAuthToken{GrantID: gid, Kind: store.TokenCode, Hash: store.HashToken(authCode),
		ExpiresAt: store.Millis(q.now.Add(codeTTL)), CodeChallenge: a.challenge, RedirectURI: a.redirectURI, Resource: a.resource}, now); err != nil {
		return err
	}
	if err := q.c.audit("app.connect", "human:"+q.human.Name, map[string]any{"app": cl.Name}); err != nil {
		return err
	}
	// The code goes to the app in the redirect. The pages send Referrer-Policy same-origin, so it is not passed on from there.
	h.backToApp(q.w, q.r, a, url.Values{"code": {authCode}})
	return nil
}

// ---- tokens (RFC 6749 section 3.2, with PKCE) ----

type tokenSet struct{ access, refresh string }

func (h *Hub) auditApp(q store.Querier, action, humanName string, payload any, now time.Time) {
	_, err := q.Exec(`INSERT INTO audit(actor, action, target, payload, created_at, via) VALUES ('connector', ?, ?, ?, ?, 'connector')`,
		action, "human:"+humanName, string(marshal(payload)), store.Millis(now))
	if err != nil {
		h.opt.Log.Printf("audit %s: %v", action, err)
	}
}

// revokeGrant ends a grant and says so in the audit log.
func (h *Hub) revokeGrant(q store.Querier, g *store.Grant, reason string, now time.Time) {
	changed, err := store.RevokeGrant(q, g.ID, store.Millis(now))
	if err != nil {
		h.opt.Log.Printf("revoke grant: %v", err)
		return
	}
	if changed {
		name := ""
		if hu, _ := store.HumanByID(q, g.HumanID); hu != nil {
			name = hu.Name
		}
		h.auditApp(q, "app.revoke", name, map[string]any{"app": g.ClientName, "reason": reason}, now)
	}
}

// issue makes a new pair of tokens for a grant.
func (h *Hub) issue(q store.Querier, grantID int64, resource string, now time.Time) (*tokenSet, error) {
	ts := &tokenSet{access: store.NewToken(store.PrefixConnector), refresh: store.NewToken(store.PrefixRefresh)}
	ms := store.Millis(now)
	if err := store.InsertOAuthToken(q, store.OAuthToken{GrantID: grantID, Kind: store.TokenAccess, Hash: store.HashToken(ts.access), ExpiresAt: store.Millis(now.Add(accessTTL)), Resource: resource}, ms); err != nil {
		return nil, err
	}
	if err := store.InsertOAuthToken(q, store.OAuthToken{GrantID: grantID, Kind: store.TokenRefresh, Hash: store.HashToken(ts.refresh), ExpiresAt: store.Millis(now.Add(refreshTTL)), Resource: resource}, ms); err != nil {
		return nil, err
	}
	return ts, nil
}

var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (h *Hub) oauthToken(w http.ResponseWriter, r *http.Request) {
	if h.connectorBase() == "" {
		connectorOff(w)
		return
	}
	now := h.opt.Now()
	if !h.allowRate("oauthtoken:ip:"+h.clientIP(r), 60, 60, now) {
		(&oauthError{429, "temporarily_unavailable", "Too many requests from this address. Wait a minute.", false}).write(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenBody)
	if err := r.ParseForm(); err != nil {
		(&oauthError{400, "invalid_request", "The request must be a form (application/x-www-form-urlencoded) of at most 16 KB.", false}).write(w)
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	user, pass, basic := r.BasicAuth()
	if clientID == "" && basic {
		clientID = user
	}
	if f.Get("client_secret") != "" || pass != "" {
		(&oauthError{401, "invalid_client", "This hub's apps are public: do not send a client secret.", basic}).write(w)
		return
	}
	if clientID == "" || len(clientID) > 128 {
		(&oauthError{400, "invalid_request", "client_id is required.", false}).write(w)
		return
	}
	if !h.allowRate("oauthtoken:client:"+clientID, 30, 30, now) {
		(&oauthError{429, "temporarily_unavailable", "Too many requests from this app. Wait a minute.", false}).write(w)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	defer tx.Rollback()
	if err := store.PurgeOAuth(tx, store.Millis(now), clientNeverUsed.Milliseconds()); err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	cl, err := store.OAuthClientByID(tx, clientID)
	if err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	var ts *tokenSet
	var oerr *oauthError
	switch {
	case cl == nil:
		oerr = &oauthError{401, "invalid_client", "This hub does not know that app. Connect it again.", basic}
	case f.Get("grant_type") == "authorization_code":
		ts, oerr, err = h.exchangeCode(tx, cl, f, now)
	case f.Get("grant_type") == "refresh_token":
		ts, oerr, err = h.exchangeRefresh(tx, cl, f, now)
	case f.Get("grant_type") == "":
		oerr = &oauthError{400, "invalid_request", "grant_type is required.", false}
	default:
		oerr = &oauthError{400, "unsupported_grant_type", "Use authorization_code or refresh_token.", false}
	}
	if err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	// A refusal can carry a revocation (a replayed code, a reused refresh token): keep it.
	if err := tx.Commit(); err != nil {
		h.writeOAuthInternal(w, err)
		return
	}
	if oerr != nil {
		oerr.write(w)
		return
	}
	oauthJSON(w, 200, map[string]any{
		"access_token":  ts.access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": ts.refresh,
		"scope":         oauthScope,
	})
}

var errGrant = func(desc string) *oauthError { return &oauthError{400, "invalid_grant", desc, false} }

func (h *Hub) exchangeCode(tx store.Querier, cl *store.OAuthClient, f url.Values, now time.Time) (*tokenSet, *oauthError, error) {
	code, verifier, redirect := f.Get("code"), f.Get("code_verifier"), f.Get("redirect_uri")
	if code == "" || verifier == "" || redirect == "" {
		return nil, &oauthError{400, "invalid_request", "code, code_verifier and redirect_uri are required.", false}, nil
	}
	if !h.resourceOK(f.Get("resource")) {
		return nil, &oauthError{400, "invalid_target", "The resource must be this hub's address or its /mcp address.", false}, nil
	}
	hash := store.HashToken(code)
	tok, err := store.OAuthTokenByHash(tx, hash, store.TokenCode)
	if err != nil {
		return nil, nil, err
	}
	if tok == nil || subtle.ConstantTimeCompare([]byte(hash), []byte(tok.Hash)) != 1 {
		return nil, errGrant("That code is not valid. Connect again."), nil
	}
	g, err := store.GrantByID(tx, tok.GrantID)
	if err != nil || g == nil {
		return nil, errGrant("That code is not valid. Connect again."), err
	}
	if g.ClientID != cl.ClientID || g.RevokedAt.Valid {
		return nil, errGrant("That code is not valid. Connect again."), nil
	}
	if tok.UsedAt.Valid { // a code is good once; seeing it again means it leaked, so everything it led to ends
		h.revokeGrant(tx, g, "the authorization code was used twice", now)
		return nil, errGrant("That code was already used. The connection was ended; connect again."), nil
	}
	if store.Millis(now) > tok.ExpiresAt {
		return nil, errGrant("That code has expired. Connect again."), nil
	}
	if subtle.ConstantTimeCompare([]byte(redirect), []byte(tok.RedirectURI)) != 1 {
		return nil, errGrant("The redirect_uri is not the one the code was issued for."), nil
	}
	if f.Get("resource") != "" && tok.Resource != "" && !sameResource(h.connectorBase(), f.Get("resource"), tok.Resource) {
		return nil, &oauthError{400, "invalid_target", "The resource is not the one the code was issued for.", false}, nil
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if !verifierRE.MatchString(verifier) || subtle.ConstantTimeCompare([]byte(want), []byte(tok.CodeChallenge)) != 1 {
		h.revokeGrant(tx, g, "the code verifier did not match", now)
		return nil, errGrant("The code_verifier does not match the code_challenge."), nil
	}
	if used, err := store.UseOAuthToken(tx, tok.ID, store.Millis(now)); err != nil {
		return nil, nil, err
	} else if !used {
		h.revokeGrant(tx, g, "the authorization code was used twice", now)
		return nil, errGrant("That code was already used. The connection was ended; connect again."), nil
	}
	if err := store.ActivateGrant(tx, g.ID, store.Millis(now)); err != nil {
		return nil, nil, err
	}
	store.TouchOAuthClient(tx, cl.ID, store.Millis(now))
	ts, err := h.issue(tx, g.ID, tok.Resource, now)
	return ts, nil, err
}

// sameResource treats the hub's address and its /mcp address as the same resource.
func sameResource(base, a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimRight(s, "/")
		if s == base+mcp.HTTPPath {
			return base
		}
		return s
	}
	return norm(a) == norm(b)
}

func (h *Hub) exchangeRefresh(tx store.Querier, cl *store.OAuthClient, f url.Values, now time.Time) (*tokenSet, *oauthError, error) {
	rt := f.Get("refresh_token")
	if rt == "" {
		return nil, &oauthError{400, "invalid_request", "refresh_token is required.", false}, nil
	}
	if !h.resourceOK(f.Get("resource")) {
		return nil, &oauthError{400, "invalid_target", "The resource must be this hub's address or its /mcp address.", false}, nil
	}
	for _, sc := range strings.Fields(f.Get("scope")) {
		if sc != oauthScope {
			return nil, &oauthError{400, "invalid_scope", "The only scope is " + oauthScope + ".", false}, nil
		}
	}
	hash := store.HashToken(rt)
	tok, err := store.OAuthTokenByHash(tx, hash, store.TokenRefresh)
	if err != nil {
		return nil, nil, err
	}
	if tok == nil || subtle.ConstantTimeCompare([]byte(hash), []byte(tok.Hash)) != 1 {
		return nil, errGrant("That refresh token is not valid. Connect again."), nil
	}
	g, err := store.GrantByID(tx, tok.GrantID)
	if err != nil || g == nil {
		return nil, errGrant("That refresh token is not valid. Connect again."), err
	}
	if g.ClientID != cl.ClientID || g.RevokedAt.Valid {
		return nil, errGrant("That refresh token is not valid. Connect again."), nil
	}
	if tok.UsedAt.Valid { // already traded: whoever shows it now is not the app that holds the newest one
		h.revokeGrant(tx, g, "a refresh token was used twice", now)
		return nil, errGrant("That refresh token was already used. The connection was ended; connect again."), nil
	}
	if store.Millis(now) > tok.ExpiresAt {
		return nil, errGrant("That refresh token has expired. Connect again."), nil
	}
	if used, err := store.UseOAuthToken(tx, tok.ID, store.Millis(now)); err != nil {
		return nil, nil, err
	} else if !used {
		h.revokeGrant(tx, g, "a refresh token was used twice", now)
		return nil, errGrant("That refresh token was already used. The connection was ended; connect again."), nil
	}
	store.TouchGrant(tx, g.ID, store.Millis(now))
	store.TouchOAuthClient(tx, cl.ID, store.Millis(now))
	ts, err := h.issue(tx, g.ID, tok.Resource, now)
	return ts, nil, err
}

// ---- /mcp ----

// originAllowed is the browser-origin check for /mcp (the defence against DNS rebinding): a
// request with an Origin is accepted from this hub's own page and from Claude's. Servers calling
// servers send no Origin at all.
func (h *Hub) originAllowed(origin string, r *http.Request) bool {
	if origin == "" {
		return true
	}
	if want := h.webOrigin(r); want != "" && origin == want {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Port() != "" || u.Path != "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range []string{"claude.ai", "claude.com"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func (h *Hub) handleMCP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	base := h.connectorBase()
	if base == "" {
		connectorOff(w)
		return
	}
	origin := r.Header.Get("Origin")
	if !h.originAllowed(origin, r) {
		oauthJSON(w, 403, map[string]string{"error": "forbidden", "error_description": "This address does not take requests from web pages of other sites."})
		return
	}
	if origin != "" {
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, MCP-Protocol-Version, Mcp-Method, Mcp-Name, Mcp-Session-Id")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	deny := func(desc string) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+pathResourceMeta+`"`)
		oauthJSON(w, 401, map[string]string{"error": "invalid_token", "error_description": desc})
	}
	// Who is asking is settled first, in a short transaction of its own: a tool call below runs
	// requests through the hub, and the database has one connection.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	p, err := authenticate(tx, r, h.opt.Now())
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == 401 {
			if bearer(r) == "" {
				deny("Sign in to connect. This address needs an access token from the sign-in flow.")
			} else {
				deny("That access token is not valid or has ended. Connect the app again.")
			}
			return
		}
		h.writeErr(w, err)
		return
	}
	if !p.operator { // an admin, a person's own token, a machine's credential, an agent's: none of them belong on an AI app's address
		deny("That token is not for AI apps. Connect the app through its sign-in, or use a token from the Connect an AI app page.")
		return
	}
	if !h.allowRate("mcp:"+p.name, 300, 300, h.opt.Now()) {
		w.Header().Set("Retry-After", "30")
		oauthJSON(w, 429, map[string]string{"error": "rate_limited", "error_description": "Too many requests. Wait a moment."})
		return
	}
	if r.Method != http.MethodPost { // there is no event stream to open, and nothing to end
		w.Header().Set("Allow", "POST")
		oauthJSON(w, 405, map[string]string{"error": "method_not_allowed", "error_description": "Send JSON-RPC messages with POST."})
		return
	}
	tok := bearer(r)
	h.mcpServer().HandleHTTP(w, r, h.operatorRunner(r.Context(), tok))
}

func (h *Hub) mcpServer() *mcp.Server { return mcp.OperatorServer(h.opt.Version) }

// operatorRunner runs a tool's command as the connected person. What comes back is a tool
// result for the app to read, so it never carries a Go panic or the token itself.
func (h *Hub) operatorRunner(ctx context.Context, tok string) mcp.Run {
	return func(argv []string) (out, errOut string, code int) {
		if h.opt.OperatorRun == nil {
			return "", "this hub cannot run tools for a connected app", 1
		}
		ctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		func() {
			defer func() {
				if recover() != nil {
					h.opt.Log.Printf("a connector tool stopped unexpectedly")
					out, errOut, code = "", "Something went wrong running that. It has been logged.", 1
				}
			}()
			out, errOut, code = h.opt.OperatorRun(ctx, h.Handler(), tok, argv)
		}()
		return strings.ReplaceAll(out, tok, "[token]"), strings.ReplaceAll(errOut, tok, "[token]"), code
	}
}
