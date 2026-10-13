package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	"handloom/internal/client"
)

// operatorRun is the hub's hook for the remote connector (D109): it runs one tool's command as the
// person whose access token is given, in this process, against the hub's own handler. The tool table,
// the command line and the hub's rules for what an AI app may do are the same as for
// `handloom mcp --operator`; only the road to the hub differs (no network, no environment).
func operatorRun(ctx context.Context, handler http.Handler, token string, argv []string) (stdout, stderr string, code int) {
	c := &client.Client{HTTP: &http.Client{Transport: inProcess{ctx, handler}}, Base: "http://hub.internal", Token: token}
	var out, errb strings.Builder
	code = MainWith(c, argv, &out, &errb)
	return out.String(), errb.String(), code
}

// inProcess is an http.RoundTripper that hands the request to a handler instead of a network.
type inProcess struct {
	ctx     context.Context
	handler http.Handler
}

func (t inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(t.ctx)
	if req.Body == nil {
		req.Body = http.NoBody
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.RequestURI = req.URL.RequestURI()
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// oauthRedirects reads HANDLOOM_OAUTH_REDIRECTS: extra https addresses an AI app may be sent back to.
func oauthRedirects(v string) []string {
	var out []string
	for _, u := range strings.Split(v, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}
