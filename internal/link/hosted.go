package link

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"handloom/internal/setting"
)

// Hosted projects (D106): the hub holds the repository, and this machine reaches it with
// the credential it already has (the one `link join` saved). Nobody types a password and
// no credential is written into an address or into the repository's config: the clone
// is told, in its own config, to ask `handloom git-credential` for a login, and that
// answers only for the hub's address. Plain `git fetch` and `git push` in the clone and
// in its worktrees then work without a prompt. gh is never used for these projects.

// origin is scheme://host[:port] of an address, lowercased, or "".
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// normOrigin drops :80 on http and :443 on https so two spellings of one hub compare equal.
func normOrigin(o string) string {
	o = strings.TrimSuffix(o, ":80")
	if strings.HasPrefix(o, "https://") {
		o = strings.TrimSuffix(o, ":443")
		return o
	}
	return o
}

func sameOrigin(a, b string) bool {
	a, b = originOf(a), originOf(b)
	return a != "" && normOrigin(a) == normOrigin(b)
}

// helperValue is the credential helper the clone is configured with: this binary's git-credential.
// A hub home chosen by HANDLOOM_HOME is passed on, so git run from another shell finds the same credential.
func (l *Link) helperValue() string {
	bin := l.opt.Binary
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	env := ""
	if h := setting.Get("HOME"); h != "" {
		env = "HANDLOOM_HOME=" + shellQuote(h) + " "
	}
	return "!" + env + shellQuote(bin) + " git-credential"
}

// hostedRemoteOK checks the project's address is on the hub this machine joined, so the credential
// can never be offered to anywhere else.
func (l *Link) hostedRemoteOK(remote string) error {
	if !sameOrigin(remote, l.opt.Hub) {
		return fmt.Errorf("the project's address (%s) is not on the hub this machine joined (%s), so this machine will not send its credential there; set HANDLOOM_BASE_URL on the hub to the address the machines use", remote, l.opt.Hub)
	}
	return nil
}

// credentialKey is the git config key that scopes the helper to the hub's address.
func credentialKey(remote string) string {
	return "credential." + normOrigin(originOf(remote)) + ".helper"
}

// cloneHosted clones a hosted project with the helper given on the command line (the clone's own
// config does not exist yet), then writes the same helper into the clone's config.
func (l *Link) cloneHosted(ctx context.Context, remote, dir string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	key := credentialKey(remote)
	out, err := l.opt.Git(cctx, "git", "-c", key+"=", "-c", key+"="+l.helperValue(), "clone", "--", remote, dir)
	if err != nil {
		return out, err
	}
	return out, l.configureHosted(ctx, dir, remote)
}

// configureHosted sets the clone's own git config: an empty helper first (so no other helper on this
// machine is offered, or asked to remember, the credential), then ours, for the hub's address only.
func (l *Link) configureHosted(ctx context.Context, dir, remote string) error {
	key := credentialKey(remote)
	run := func(args ...string) error {
		_, err := l.gitIn(ctx, 30*time.Second, dir, append([]string{"config", "--local"}, args...)...)
		return err
	}
	run("--unset-all", key) // nothing to remove the first time
	if err := run("--add", key, ""); err != nil {
		return fmt.Errorf("could not set up git's login in %s: %v", dir, err)
	}
	if err := run("--add", key, l.helperValue()); err != nil {
		return fmt.Errorf("could not set up git's login in %s: %v", dir, err)
	}
	return nil
}
