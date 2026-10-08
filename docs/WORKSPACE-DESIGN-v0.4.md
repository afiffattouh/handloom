# Handloom workspace design v0.4 (second review round folded in)

Status: draft, 2026-10-06. Delta on `WORKSPACE-DESIGN-v0.3.md`; where they disagree, this file wins. Three reviewers (public-hub security, web/deploy engineering, agent-CLI integration) checked v0.3 against the code and the installed CLIs. Nothing is built.


## 1. Corrections to v0.3

| v0.3 claim | Reality | Fix |
|---|---|---|
| "No SQLite-only SQL outside one package" | False: raw SQL sits in `internal/hub` at 16+ call sites; AUTOINCREMENT, PRAGMA, `unixepoch` leak | Milestone A moves every query behind a store interface. Decided against the full refactor (D37 in DECISIONS.md): new SQL goes in `store`, the existing ~55 hub sites stay, and the Postgres promise is dropped for now |
| Agents have a lead/worker boundary | Agent identity is a self-asserted `Handloom-Agent` header and the link proxy injects the device credential for any local caller (`hub.go` authenticate, `link.go` proxy) | Until per-run tokens (milestone B), **one trust domain per device**. Docs must say so; A must not advertise the boundary |
| Human tokens | Never expire (`agents.go`) | Become scoped API tokens with expiry, shown once |
| Join tokens | No TTL, unauthenticated endpoint | 15-minute TTL, rate limit before DB lookup |
| Materialize skills "nothing hidden" | No CLI isolates from the user's global skills, instructions or MCP by default | Each adapter declares isolation flags/env; conformance test plants a decoy global skill (§4) |
| Tool limit "edit-in-scope" | Enforceable only as a directory (codex `workspace-write`, claude path rules); pi/omp have no path scope | Promise the worktree, not sub-paths |
| Dokploy one-click template | Template format not verified | Ship "compose app from git" first; template later |

## 2. Milestone A, concrete (about 2–3 weeks of agent-assisted work)

**Goal:** deploy the hub on Dokploy or any VPS, log in, join a device, and answer an agent's question from the browser and phone.

Pages: `/setup`, `/login`, **Inbox**, **Devices**, **Settings** (members, ntfy URL and topic, API tokens, base URL). Nothing else; job, profile and skill pages arrive with C and D.

Endpoints: `GET /healthz` · `GET /v1/digest` · `GET /ui/stream` (SSE hint stream) · `GET /ui/inbox` (fragment) · `POST /ui/escalations/{id}/answer` · `POST /ui/devices/token` · `POST /ui/settings` · `/setup` · `/login` · `/logout`. Everything else reuses the existing `/v1` API.

Also in A: Dockerfile, compose file, `handloom healthcheck`, `handloom hub backup`, pre-migration snapshot, store-boundary refactor, `handloom ask` escalations, ntfy.

Cut from A: roles beyond owner/member, OIDC, web push, Dokploy template, multi-arch (add arm64 once the build works), Playwright, git-URL skill import, remote MCP, confidential mode.

**Demo = release criterion:** deploy from scratch, run setup, join one device, an agent runs `handloom ask`, the phone buzzes (link only, no content), answer in the browser, the lead receives it.

### Tech decisions
- stdlib `net/http` (Go 1.22+ patterns), `html/template` via `go:embed`, one template per page plus partials shared by full loads and htmx fragments (`HX-Request`).
- htmx and its SSE extension **vendored**, plain CSS vendored, strict CSP, no external fetches.
- **SSE carries hints only** (`inbox-changed`); the client refetches the fragment. Never push HTML over SSE, so reconnects self-heal. `id: <seq>` on events, a `resync` event on reconnect, one stream per page.
- Markdown: goldmark without raw HTML, then bluemonday UGC policy, server-side only; no remote images.
- Forms: plain textarea with POST-redirect-GET; diffs rendered server-side. No JS editor, no JS diff library.
- Auth: argon2id; server-side hashed session ids; cookie `__Host-` + Secure + HttpOnly + SameSite=Lax; per-session CSRF token + Origin check; in-memory login rate limit. `HANDLOOM_BASE_URL` is the origin source of truth; trust forwarded headers only from the proxy.
- Phone: **ntfy**; payload is a link, never content. Web push deferred (service worker, VAPID, iOS PWA only).
- Tests: `httptest` against the real mux with an in-memory store, one SSE test. Playwright smoke later.
- Deploy: multi-stage build, `CGO_ENABLED=0`, distroless nonroot, `/data` created and chowned in the builder, `HEALTHCHECK` via the binary. Compose with pinned tags (never `:latest`); Caddy in a separate overlay for plain VPS TLS. Traefik/Dokploy: 15 s SSE heartbeats, no compression on the stream route, `X-Accel-Buffering: no`, per-write deadlines instead of a server `WriteTimeout`. **Test through a real Dokploy deploy in week 1**, not at the end.
- SQLite with one connection: handlers wait on an in-process notifier, never while holding a tx or rows. Load test with 20 simultaneous streams.
- Upgrades: forward-only migrations, refuse to start on a newer schema, `VACUUM INTO` snapshot to `/data/backups/pre-vN.db` before migrating; backups via the SQLite backup API, never copying the live db and WAL; restore test in CI.

### Security checklist for A (must all be true to ship)
- [ ] Setup wizard requires a one-time code printed in the container log; closes permanently once an owner exists (enforced in the DB); concurrent double-submit makes one owner
- [ ] argon2id, hashed server-side sessions, `__Host-` cookie, CSRF token + Origin check on every non-GET, no state change on GET
- [ ] Login and join rate limits applied before the DB lookup; constant-time response for unknown users
- [ ] Autoescaped templates only (never `template.HTML` on agent text), strict CSP (`default-src 'none'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`), `nosniff`, transcripts and blobs served as `text/plain` attachments from a cookieless path
- [ ] Step-up: password re-entry for device-trust changes (and later profile and skill edits)
- [ ] Join token TTL 15 min, single use; device revoke; device trust mode defaults to **approve each profile hash** (approved on the device via CLI, not in the browser); "trust this hub" is explicit per-device opt-in with a warning
- [ ] HTTP server timeouts (`ReadTimeout`, `IdleTimeout`, `MaxHeaderBytes`), SSE per-user and global connection caps, body limits
- [ ] Owner-only `handloom hub reset-password`; no web reset
- [ ] Logs and audit scrubbed (actor and IP, never bodies, tokens or env values; redact `Authorization`)
- [ ] Refuse to start over plain HTTP unless `HANDLOOM_BASE_URL` is https or localhost; `HANDLOOM_SECRET` mandatory, 32+ bytes
- [ ] Docs state: no lead/worker boundary on a shared device until B

### Security tests in CI
1. **XSS corpus**: `<script>`, `<img onerror>`, `javascript:` links in task body, note and transcript render inert; CSP header present on every page.
2. **Auth matrix**: every route as anonymous, viewer, member, owner, device and agent token returns exactly the expected 200/401/403; POST without CSRF or with a foreign Origin is 403; an agent token cannot write profiles, skills or the library.
3. **Setup takeover**: wizard unavailable after the owner exists; wrong code fails; double-submit yields one owner.
4. **Join and rate limits**: join token single use and expiring; N bad logins or joins give 429; revoked device and its run tokens get 401; a run token replayed after the run ends gets 401.
5. **Materializer safety** (from C onward): skill paths with `../`, absolute paths or symlinks rejected; git URLs to `file://`, loopback or `169.254.169.254` rejected; launch of an unapproved profile hash refused on an approve-mode device.

## 3. Later-milestone security requirements (recorded now)

- **Per-run tokens (B):** bound to (run, device, job); expiry equals the run lease; revoked at run end and on device revoke; hashed in the DB; 0600 file in a per-run dir, never in argv or env, never logged; never carries spawn, profile, skill or library write.
- **Skill and profile supply chain (C):** only the owner publishes or imports; members propose. Git import: https only, no private or link-local IPs, size and file-count caps, no symlinks, resolve and pin a commit hash, full diff on every version bump, never auto-update. The materializer writes only under fixed roots. Reject secret-looking values in profiles at save time.
- **Remote MCP (after B):** own token and role, no human-only verbs (accept, answer escalation, profile write, trust change), confirm-in-web for spawn.
- **Confidential mode (F):** encrypted backups (age) or at minimum 0600; the device-local policy from v0.3 §6.

## 4. Adapter manifest v1 (replaces the sketch in v0.3 §2)

Fixed schema plus **named built-in hooks** for the cases data cannot express. No shell strings, no scripting language.

```yaml
schema: 1
kind: claude
detect: [claude]
version_probe: "{bin} --version"
launch:
  argv: [claude]                         # argv list only; placeholders {model} {dir} {profile_prompt_file} {session_id}
  model_flag: [--model, "{model}"]       # format differs per CLI (opencode wants provider/id)
  env: {}                                # secrets by reference only
  trust: { mode: keys, keys: [Down, Enter] }   # keys | flag | file | none: pre-clears the trust dialog
  yolo_flag: []                          # unattended-approval equivalent
instructions: { file: CLAUDE.md, mode: block }  # marker-delimited block, preserves user text
skills:
  mode: dir                              # dir | flag | inline | none
  path: .claude/skills
  isolate: [--setting-sources, "project,local"]   # hides global skills/config; required
mcp: { mode: flag, argv: [--strict-mcp-config, --mcp-config, "{file}"] }   # flag | file | cmd | none
tools:                                   # portable tool -> argv fragment
  shell: { allow: [--allowedTools, Bash], deny: [--disallowedTools, Bash] }
  web:   { deny: [--disallowedTools, "WebFetch,WebSearch"] }
state: { method: hook, template: tmpl/claude-hooks.json.tmpl }   # hook|plugin|extension|scrape|none
scrape: { idle_regex: "", blocked_regex: "", working_regex: "" } # L0 only
wake: { terminal: true, headless: [-p, --resume, "{session_id}", "{prompt}"] }
session_id: { from: hook }               # hook | filename | none
cleanup: [".handloom/"]
```

**Code escape hatches, as named built-ins** (never user-supplied code): merge hook JSON into settings files; build codex TOML and `-c` overrides; derive the session id per CLI; codex hook-review and `[projects]` trust entries in `~/.codex/config.toml` (Handloom never writes these today); pi `--approve`; claude trust dialog by keys.

**Verified per-CLI facts** (help output, `strings` and files; no model turns run):

| CLI | Project skills dir | Isolation off-switches | Tool enforcement | Headless resume |
|---|---|---|---|---|
| claude | `.claude/skills` | `--setting-sources`, `--strict-mcp-config`, `--bare` | `--tools`, allow/deny, permission modes; project allow rules ignored until the folder is trusted | `-p --resume ID` (only one actually run) |
| codex (0.160.0 on Mantis, `/usr/local/bin/codex`) | `.codex/skills`, `.agents/skills` (loader unconfirmed) | `--ignore-user-config`, `--ignore-rules`, `-c` | `-s` sandbox, `-a`, `--search` | `codex exec resume ID` |
| pi | project path likely `.pi/skills` (unconfirmed); `--skill <path>` is the cleaner option | `--no-skills`, `--no-context-files`, `-ne`, `--approve` | `--tools`, `--exclude-tools`, `--no-tools` | `-p --session ID` |
| omp | `--skills glob`; paths unverified, may read other tools' dirs | `--profile` isolates auth, sessions, settings; `--no-extensions`, `--no-rules` | `--tools`, `--no-tools`, `--approval-mode`, `--max-time` | `-p --resume` |
| opencode | reads `.claude/skills` and `.agents/skills`, so global `~/.claude/skills` bleeds in | `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS`, `..._EXTERNAL_SKILLS`, `..._PROJECT_CONFIG`; `OPENCODE_CONFIG*`, `--pure` | config files only; `--auto` approves everything | `run -s ID` |

**Portable tools, enforceable per CLI:** read (claude, pi, omp yes; codex via sandbox; opencode via config) · edit (directory scope only; claude also path rules) · shell (claude, pi, omp by tool name; codex sandbox) · web (claude, codex `--search`, pi/omp by tool name) · handloom-mcp (claude, codex yes; pi via `pi mcp` config; omp unverified).

**Skill portability.** The five CLIs reference `<name>/SKILL.md` under `.claude/skills` or `.agents/skills`. Frontmatter was not compared across CLIs: treat only `name` and `description` as portable and strip other keys (Claude's `allowed-tools` etc.) at materialization. For CLIs without skills, inline only name, description and a file pointer, never the whole body (it would bloat context and change on-demand semantics).

**L0 custom CLI, honestly.** Pane scraping breaks on TUI redraws, spinners, themes and upgrades, and cannot tell blocked from idle. So L0 reports state as `unknown` and never claims `idle`; wake is driven by output-quiet time; completion is an explicit `handloom done` call (CLI or MCP), not a state change; every run has a timeout.

**`handloom adapter test <kind>` conformance checks:**
1. Binary found, version probe parses. 2. Manifest validates, argv has no shell metacharacters. 3. Scratch worktree starts the CLI with the intended flags and exits 0 on a no-op prompt. 4. Instructions file written and the CLI repeats a nonce phrase from it. 5. A nonce skill is visible and a **decoy global skill is not**. 6. A Handloom MCP tool call succeeds. 7. Trust dialog passed or reported. 8. Working then idle transitions seen (L1). 9. A nudge turns idle into working. 10. Headless resume with a stored id returns a reply (L1). 11. A denied tool call fails (L2). 12. Cleanup removes everything Handloom wrote.

A manifest from a git URL or the UI is untrusted: require owner approval of its hash, argv-only launch.

## 5. Milestone changes

- **A** now has the concrete scope and checklist above.
- **B** adds per-run tokens (§3) and the one-trust-domain-per-device docs until it lands.
- **C** gains manifest v1, the conformance command, skill materialization with isolation, and the supply-chain controls (§3). First targets stay claude and codex plus one L0 custom CLI; pi, omp and opencode are "L1, prompt-only limits" until each passes conformance.
- Everything else as in v0.3 §10.

## 6. Still unverified

Dokploy template format, Traefik and Dokploy timeouts and compression settings, Dokploy volume-backup semantics; `VACUUM INTO` in the pinned modernc build; pi and omp project skill paths; codex skill loading and transcript format; frontmatter differences across CLIs; omp MCP; headless forms for every CLI except claude; any runtime behaviour at all.

## 7. Name (decided)

**Handloom** (owner decision, 2026-10-06). A loom worked by hand: the human at the loom, agents weaving, and "hand" for handoff. Chosen from a shortlist checked against npm, PyPI, GitHub and DNS: free on npm, PyPI, `.dev` and `.sh`; the top GitHub repo is a 24-star unrelated ML project. `.ai` and `.io` are taken. Short command: **`hl`**. Known clash: Homebrew has a formula `hl` (pamburus/hl, a Rust log viewer, about 41 installs in 30 days); no `hl` binary on this box or in apt. Mitigation: ship `handloom` as the real binary and install `hl` as an optional alias that the installer skips when `hl` already exists. Deferred by the owner: domain registration. Still to do: trademark check. Runners-up: Loomwright, Weftwright. Rejected: Weaver (TaskWeaver, Service Weaver, OpenTelemetry `weaver`), Heddle and Weft (agent tools use them), Treadle, Muster, Bobbin, Skein.
