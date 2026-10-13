package client

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// GitCredential is git's credential helper protocol for hosted projects (D106). Git runs
// `handloom git-credential get|store|erase` and writes "key=value" lines on standard input,
// ended by an empty line. For `get` this answers with the machine's own device credential,
// the one `link join` saved, and only when the request is for the hub this machine joined
// (same scheme, host and port). For any other address, and for `store` and `erase`, it
// says nothing, so the credential is never given to another host and nothing is cached
// anywhere else. The credential is printed to git on standard output and nowhere else.
func GitCredential(op string, in io.Reader, out io.Writer) error {
	req := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(in, 64<<10))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			req[k] = v
		}
	}
	if op != "get" {
		return nil // store and erase: nothing to remember, nothing to forget
	}
	cfg, err := LoadLinkConfig()
	if err != nil || cfg.Credential == "" {
		return nil
	}
	if !sameHubOrigin(cfg.Hub, req["protocol"], req["host"]) {
		return nil
	}
	if p := strings.TrimPrefix(req["path"], "/"); p != "" && !strings.HasPrefix(p, "git/") {
		return nil // only the repositories the hub serves
	}
	fmt.Fprintf(out, "username=handloom\npassword=%s\n", cfg.Credential)
	return nil
}

// sameHubOrigin reports whether protocol://host is the origin of the hub address.
func sameHubOrigin(hub, protocol, host string) bool {
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" || protocol == "" || host == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, protocol) && hostPort(u.Scheme, u.Host) == hostPort(protocol, host)
}

// hostPort lowercases a host and makes the port explicit.
func hostPort(scheme, host string) string {
	host = strings.ToLower(host)
	i := strings.LastIndex(host, ":")
	if i > strings.LastIndex(host, "]") && i >= 0 {
		return host
	}
	switch strings.ToLower(scheme) {
	case "https":
		return host + ":443"
	case "http":
		return host + ":80"
	}
	return host
}
