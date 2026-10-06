# Decisions

> **Naming:** this file was written when the project was called handloom. The product is now **Handloom** (command `handloom`, short alias `hl`). In the code, the wire and the settings, `handloom` still works: `HANDLOOM_*` environment variables, the `Handloom-Agent` header, `.handloom/agent`, `handloom.db`, `~/.config/handloom`, the `handloom_*` MCP tool names and a `handloom` symlink to the binary are all accepted, and the new names win. Read `handloom` below as `handloom`.

Choices made while building M0 and M1, and places where the design was unclear or did not match what the machines do. Each entry says what the design or brief said, what was found, and what was done.

## Environment findings

**E1. Claude Code on Mantis runs as root, and root cannot use bypass mode.**
`ssh mantis '/root/.local/bin/claude -p "say ok" --dangerously-skip-permissions'` prints `--dangerously-skip-permissions cannot be used with root/sudo privileges for security reasons` and exits 1. Root is the only user on Mantis. No user was created on the shared VPS. Test agents on both machines get the same scoped allowlist instead (`test/e2e/agent.sh`): `defaultMode: dontAsk` plus file tools and a few read-only commands in the test project's `.claude/settings.json`, and the adapter's own allow rule for `handloom`. In `dontAsk` mode a tool call outside the list is denied with an error the agent reads; it never opens an approval prompt. The first rehearsal used `acceptEdits` and showed why this matters: the lead tried `find` to double-check a file, stopped at an approval prompt, the adapter reported `blocked`, and the job hung until the timeout.

**E1b. Claude Code shows a "trust this folder" dialog in a new directory.** When the folder pre-approves permissions, the dialog's default answer is "No, exit". `test/e2e/m1.sh` answers it (Down, Enter) during setup, before the brief, and counts those keys in its output. Without trust, Claude Code ignores the project's allow rules.

**E2. Claude Code on Mantis was not logged in.**
`claude auth status` on Mantis returned `"loggedIn": false`. Credentials were not copied from GB10. The owner logged in later the same day and M1 then ran. `test/e2e/m1.sh` checks the login first and stops with a clear message.

**E3. Two Claude Code versions on Mantis.**
A non-interactive shell finds `/usr/local/bin/claude` (2.1.202). The newer one is `/root/.local/bin/claude` (2.1.220). GB10 has 2.1.288. The e2e script uses `/root/.local/bin/claude` by absolute path (`REMOTE_CLAUDE`). After the login that one had updated itself to 2.1.288, so the M1 runs used 2.1.288 on both machines.

**E4. Different CPU architectures.**
GB10 is arm64, Mantis is amd64. Go 1.27.1 is installed in `~/.local/go` on GB10 (checksum verified against go.dev). The Mantis binary is cross-compiled with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`; the pure-Go SQLite driver makes that work.

**E5. Herdr's Claude hook reports the session, not the state.**
`~/.claude/hooks/herdr-agent-state.sh` handles `SessionStart` only and sends the session id to Herdr's socket. It does not report idle, working or blocked. So handloom cannot learn Claude Code's state from that hook and installs its own. No Herdr code was copied, so its licence was not a question. handloom never edits `~/.claude/settings.json`; its hooks go in the project.

**E6. `dontAsk` mode denies shell redirects to files.** Tested with the test allowlist: `uname -r && hostname` and `uname -r | cat` are allowed, `uname -r > k.txt` is denied even with `Write` allowed. In the second two-machine M1 run the worker hit this, stopped and asked "the user". The allowlist was not widened: allowing all of Bash for an unattended root agent would defeat the reason Claude Code refuses bypass mode as root. Instead the adapter's instructions now say that nobody is watching, that a denied command should be retried another permitted way, and that a stuck agent blocks the task and messages the lead.

## Findings from the M1 runs

**F1. A stuck agent that has claimed nothing is invisible.** Leases cover claimed tasks. In the failed run the worker ended its turn without claiming its assigned tasks, and the lead waited with no notice. A fix for M2: the hub tells the lead when an assigned task is still unclaimed some minutes after its notice was delivered.

**F2. Agents may decline mail that looks unrelated.** In the hook check the worker read a message from a human asking it to send a reply and declined: the text arrived in a tool result and had nothing to do with its current request. That is the cautious behaviour principle 6 wants for agent messages, but it also applied to a `human:` sender. handloom's notices about tasks were acted on in every run, because the instructions block says to. If humans are to direct agents through handloom, the instructions need to say how much weight a `human:` message carries (M3, with the human channel).

**F3. The nudge rate limit shapes the pace.** One nudge per agent per 60 seconds means a second wake inside a minute waits. The three-task job took about 90 seconds, most of it that wait.

## Claude Code facts, verified

Checked against the installed CLI (2.1.288 on GB10) with a logging hook, and against the hooks reference.

- Hooks run with the agent's environment (`HANDLOOM_AGENT`, `TMUX_PANE` reach the hook). They also receive `cwd` and `session_id` on stdin.
- A `Stop` hook that prints `{"decision":"block","reason":"..."}` makes Claude continue with the reason as its next instruction. Tested: Claude answered the reason's request, then stopped. The second `Stop` input has `stop_hook_active: true`.
- Hooks in `.claude/settings.local.json` of the project run in `-p` mode and interactive mode.
- `SessionStart` output on stdout is added to the agent's context.
- `PermissionRequest` exists as a hook event and is the prompt way to see "blocked". `Notification` with `permission_prompt` also fires, later.
- Not tested here: `claude -p --resume <id>` (headless resume is M2).

## Design gaps and choices

**D1. Transport is plain HTTP on the Tailscale network.** The design says HTTPS. Section 13 allows a private network by default and makes TLS the public-exposure setup. The hub in the tests listens on the Tailscale address only. TLS is left for M4.

**D2. New agents register as `worker`.** The design says roles are set by a human or the admin, not by agents, but does not say what role a new agent has. A register request cannot carry a role (unknown fields are rejected with 400). `lead` and `observer` are given with `handloom agent role`, which only the admin or a human may call.

**D3. The admin token is for administration only.** The scope table has no admin column. The admin may read, and may create devices, humans and projects, set roles and read the audit log. It may not create tasks or send messages; a human token does that.

**D4. `assign` and `block` are separate API calls.** Section 12 lists no `assign` endpoint though the scope table has the action. Added `POST /tasks/{id}/assign`. `block` falls under the "own task" row.

**D5. Escalations: the scope check is live, the feature is not.** `POST /escalations` and `/escalations/{id}/answer` check the scope table and then return 501. A forbidden caller gets 403 today, so the rule "only a human answers" is enforced and tested before M3 builds the feature. The design marks "open escalation" as n/a for humans; the hub rejects it.

**D6. Events carry a target agent.** The design's `event` table has no column that says which device should receive an event. Added `agent_id`. The long-poll returns events whose target agent lives on the calling device.

**D7. The link is level-triggered.** Events only tell the link to look. It then asks the hub for its agents' state and mail counters (`GET /device/agents`) and runs the ladder from that. A lost or repeated event cannot lose or repeat a wake. The link also looks every 10 seconds, which drives the timers.

**D8. Hub notices are messages from `hub`.** Assignment, submit, accept, reject, cancel, release, block, lease expiry and "task can be claimed now" are sent as messages with sender `hub`. This makes "unread mail" the single trigger for the ladder and covers "a newly assigned task". The sender is always set by the hub from the credentials; a request that contains a `from` field is rejected.

**D9. Wake reports go through `POST /agents/{name}/wake`.** The audit log must show every wake, and wakes happen on the link. The link reports each typed nudge (`method: tmux|herdr`) and each failure (`method: none` with a reason). The end-of-turn hook calls `POST /agents/{name}/turn-end`, where the hub decides in one transaction whether to hold the agent (and logs `wake` with `method: hook`) or mark it idle. `POST /messages/{id}/delivered` from the design is also there.

**D10. "Once per batch" for the end-of-turn hook is tracked on the hub.** The agent row keeps the newest message id the hook has handed over. The hook holds the stop only for unread mail newer than that. This does not rely on Claude Code's `stop_hook_active`.

**D11. Re-nudge and "unknown".** The design says: two nudges with no `handloom inbox` call within 10 minutes mark the agent unknown. It does not say when the second nudge is sent if no new mail arrives. Choice: if delivered mail stays unread for 5 minutes, nudge once more. After two nudges and 10 minutes with no inbox call, report unknown. The link sees inbox calls because they pass through its socket.

**D12. A wake failure is reported once per episode.** The first M0 run reported "cannot be woken" to the lead for every new message to a plain shell. Now the link reports a given reason once and again only after the agent's state has changed. Known quirk: an agent with no adapter (a plain shell) never reports a state, so the lead still gets one "cannot be woken (state_unknown)" notice for it. It is true, but a lead that is an LLM may read it as a problem. M2 could skip the notice for agents that have never reported a state.

**D13. The nudge has no backticks.** The typed line is `You have N new handloom messages. Run: handloom inbox`. The design's example has backticks, which a shell would execute if the pane had fallen back to a shell. The tmux driver also refuses to type when the pane's foreground program is a shell.

**D14. tmux wins over herdr when detecting the wake target.** `$TMUX_PANE` is set by tmux for exactly that pane. `$HERDR_PANE_ID` can be inherited from an outer terminal: in the first M0 run a shell started from a Herdr pane recorded that pane as its wake target. Plain `--kind shell` agents now record no wake target at all.

**D15. Lease heartbeats come from activity, not from the state.** The design says adapters heartbeat while the agent is `working`. A hung agent would stay `working` and keep its lease for ever. The Claude adapter reports each finished tool call (`PostToolUse`), and the link extends the agent's leases at most every 2 minutes while such reports arrive.

**D16. Agent identity on a device.** The CLI names its agent with `$HANDLOOM_AGENT`, or a `.handloom/agent` file in the working directory or a parent. The hub checks that the named agent belongs to the calling device. Any process on a device can therefore act as any agent of that device: the device is the trust boundary. The socket is mode 0600.

**D17. Claude hooks go in `.claude/settings.local.json`.** The hook command is an absolute path to the binary, which is per-machine, and this file is the per-machine one. The adapter also adds an allow rule for `handloom` commands (`--no-permissions` skips it); without it every `handloom` call would stop at an approval prompt.

**D18. `adapter.yaml` is a description, not yet a driver.** The Claude adapter's installer is Go code. The manifest is embedded and documents the adapter. A generic manifest-driven installer belongs to M2, when there is a second adapter to shape it.

**D19. Device-offline detection is left out.** Agents become `offline` from the `SessionEnd` hook. The hub does not yet mark agents offline when their link disappears. M3's lead-silence alert needs it.

**D20. One hub holds several projects; the CLI defaults to `default`.** (Open question 2.) Agent names are unique per hub, so an agent's project is known from its name.

**D21. Module path is `handloom`.** A working name. Rename with the project.

## Dependencies

- `modernc.org/sqlite`: pure-Go SQLite, as the brief asks, so the binary is static and cross-compiles without a C toolchain. It brings its own support modules (`modernc.org/libc`, `mathutil`, `memory` and a few small ones). Nothing else is imported outside the standard library.

## Should change in DESIGN.md

1. Section 12: add `assign`, `turn-end`, `wake` and `device/agents`; say that events carry a target agent (D4, D6, D7, D9).
2. Section 6: give the default role of a new agent, and the admin's scopes (D2, D3).
3. Section 9: specify the re-nudge rule and how often a failure is reported (D11, D12). Remove backticks from the nudge example (D13).
4. Section 10: the "If Herdr is running, the link can read agent state from Herdr" shortcut does not hold for Claude Code as Herdr's hook stands (E5). `herdr agent list` does expose a state, which M2 could use.
5. Section 8: "Adapters send heartbeats while the agent is `working`" should be tied to activity (D15).
6. Section 10, adapter table: add that Claude Code refuses bypass mode as root, so headless and unattended agents on root-only machines need an allowlist (E1), and that the instructions block must tell agents nobody is watching (E6).
7. Section 8: say what happens when an assigned task is never claimed (F1).

---

# M2 decisions

## Environment findings (M2)

**E7. Codex on GB10 is logged out.** `codex exec` there fails with "Your access token could not be refreshed. Please log out and sign in again" (codex-cli 0.144.5, only in `~/.nvm/versions/node/v20.20.2/bin`). Codex on Mantis (0.160.0) works. The Codex worker runs on Mantis.

**E8. Pi has no working cloud model on either machine.** GB10: `pi auth check --provider openai-codex` returns `invalid`; the default model is the local Qwen. Mantis: Pi's OpenRouter key answers `403 Key limit exceeded`. So the only model Pi can use is the local Qwen. The owner's rule for the local model (work goes to it through OMP in a detached tmux run, one at a time, no input typed into a running session) does not fit a handloom worker that is woken by typed nudges, so I asked. The owner chose the local Qwen for this test. The Pi worker ran as one session with `--thinking low` (`PI_MODEL=local-qwen/qwen3.8-27b test/e2e/m2.sh`). It did the job; once it sent the same reply twice.

**E10. Claude Code's login on Mantis expired overnight** ("OAuth session expired and could not be refreshed"). `m2.sh` now checks that the headless worker's Claude Code can run a turn, and `HEADLESS_HOST=local` puts that worker on this machine.

**E9. OMP exists only on GB10 and uses the local Qwen. OpenCode on Mantis has no credentials** (a free model did not answer within 60 seconds). Neither adapter ran against a model.

## Codex facts, verified on Mantis (codex-cli 0.160.0, as root)

- Hooks have the same shape as Claude Code's: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Stop`, `SessionEnd`; JSON on stdin with `session_id`, `cwd`, `hook_event_name`, `stop_hook_active`; the hook inherits the environment.
- A `Stop` hook that prints `{"decision":"block","reason":"..."}` makes Codex continue with the reason (tested: it answered the reason's request, then stopped).
- A project's `.codex/hooks.json` is loaded, with or without a git repository. Codex runs a hook only after the user has reviewed it: the TUI shows a "Hooks need review" dialog once and stores a hash in `~/.codex/config.toml`. `codex exec` has no dialog; there, and for unattended agents, `--dangerously-bypass-hook-trust` runs the hooks without the stored review.
- The same hooks given both in the project file and inline (`-c hooks.Stop=...`) fire twice. Register them in one place only.
- **In the TUI, `SessionStart` fires at the first prompt, not at launch.** Until then handloom would not know the agent exists.
- Hooks run outside the sandbox. The agent's shell commands run inside it. In `workspace-write` the sandbox blocks unix sockets, inside or outside the workspace, and `--add-dir` does not help; only `sandbox_workspace_write.network_access=true` opens them. Writes outside the workspace are refused, also as root.
- MCP servers run outside the sandbox. With approval policy `never`, an MCP tool call fails ("requires approval, but approval policy is never") unless the server has `default_tools_approval_mode = "approve"`.
- Codex adds a `[projects."<dir>"]` trust entry to `~/.codex/config.toml` for a directory it runs in. The probe entries were removed by hand; the e2e run leaves one entry for `/root/hltest/codex-project`.

## Design choices (M2)

**D22. One hook handler for every kind.** `handloom hook <kind>` reads the Claude-shaped hook JSON. Claude Code and Codex call it directly. Pi, OMP and OpenCode get a small shim (extension or plugin) that turns their own events into the same JSON and calls the same command. For the end-of-turn delivery the shim reads the handler's `decision: block` answer and gives the reason to the agent as a user message.

**D23. Codex uses handloom through MCP, with the sandbox left on.** The alternative, turning on network access in the sandbox so the `handloom` command can reach the link's socket, gives the agent's shell the whole network. The MCP server runs outside the sandbox and offers only the handloom verbs. The tools are pre-approved (`default_tools_approval_mode = "approve"`) because nobody is there to approve them; scopes are still enforced by the hub. Nothing is written to `~/.codex/config.toml` by handloom: the MCP server is given on the command line.

**D24. `handloom run <name> -- <command>`.** Because Codex's `SessionStart` comes late, the agent is started through `handloom run`, which records the terminal and directory, reports the agent idle and then becomes the command. It works for any kind.

**D25. `--dangerously-bypass-hook-trust` in the e2e script.** This skips Codex's review of hooks; it is not the approvals-and-sandbox bypass. The script uses it because the hooks were written a moment earlier by `handloom adapter install` and because the review would otherwise be stored in the owner's Codex config on every run. A person starting Codex by hand answers the review dialog once instead.

**D26. Headless resume is opt-in.** An agent is woken by a headless turn only if its wake target is `headless`: set by `handloom register --wake-target headless`, or by the hooks when the agent runs with `HANDLOOM_HEADLESS=1` (the link sets this for the turns it starts). An interactive agent that has gone offline is not resumed behind its owner's back; the lead is told instead. The design's wording ("no live terminal but a session id") would also cover that case; it can be widened later.

**D27. Headless commands are a table with overrides.** Defaults per kind are in `internal/link/headless.go`; `$HANDLOOM_HOME/headless.json` replaces one, for an absolute path or extra flags. Only the Claude Code line ran against the real CLI. A headless agent's project is not "trusted" by Claude Code, which then ignores the project's allow rules, so the e2e passes the allowlist on the command line through this file.

**D28. The agent's working directory is stored.** A headless turn must run in the agent's project. `register` and the session-start hook send `dir`.

**D29. MCP server written by hand.** About 300 lines: `initialize`, `ping`, `tools/list`, `tools/call` over newline-delimited JSON-RPC. No new dependency. Each tool call runs the same code as the command line, so scopes, errors and the audit log are identical. A failed command is a tool result with `isError`, so the agent can read the reason.

**D30. A blocked agent is reported after 30 seconds, not at once.** With approval policy `never`, Codex fires `PermissionRequest` and then denies the call itself; the agent is "blocked" for a moment. Reporting that to the lead was noise.

**D31. Unclaimed assigned tasks are reported** (finding F1). If an assigned task that could be claimed stays unclaimed for 10 minutes (`handloom hub serve --unclaimed`), the hub tells the lead once and logs `task.unclaimed`.

**D32. Adapter files.** Codex: `.codex/hooks.json`. Pi: `.pi/extensions/handloom.ts` (needs `pi --approve`). OMP: `.handloom/omp-extension.ts`, loaded with `omp -e`. OpenCode: `.opencode/plugins/handloom.js`. All put the protocol block in `AGENTS.md`. handloom never writes to `~/.codex`, `~/.pi`, `~/.omp` or `~/.config/opencode`.

## Should change in DESIGN.md (M2)

1. Section 10, Codex row: hooks exist and match Claude Code's; end-of-turn delivery works through the Stop hook; `notify` is not needed. Add the sandbox finding and that MCP is how a sandboxed Codex reaches handloom (D23), and the late `SessionStart` (D24).
2. Section 10, Pi row: turn-end delivery is `agent_settled` plus `pi.sendUserMessage` (verified on pi 1.0.0); project extensions load with `pi --approve`.
3. Section 9, step 3: say whether an interactive agent that went offline may be resumed headless (D26).
4. Section 12: `dir` on agents, wake method `headless`, `task.unclaimed`.

## Setup script

**D33. `handloom link install` and `handloom hub install` write systemd units; `scripts/setup.sh` drives them over ssh.** The design names `handloom link install` (section 14). Root gets a system unit, anyone else a user unit (with lingering, so it survives logout). The unit copies the installing shell's PATH, because the link starts agent CLIs for headless turns. The script builds on the machine it runs from and cross-compiles for the server, so the server needs nothing installed. Linux and systemd only; launchd, prebuilt releases and a Docker image stay in M4. Tested on GB10 (user service) and Mantis (system service): hub, two devices, a message between them, rerun, status, remove. The test install was removed afterwards.

## Milestone A (Handloom workspace, see docs/WORKSPACE-DESIGN-v0.4.md)

**D34. `handloom ask` does not block by default.** DESIGN.md section 11 says `handloom ask` blocks until answered or `--timeout`. A blocked agent holds a turn open (and a tool call that may time out) for as long as the human takes. Instead `handloom ask` opens the escalation and returns at once; the answer arrives as a message from `human:<name>` and the normal wake ladder wakes the lead. `--wait <duration>` polls `GET /v1/escalations/{id}` client-side for scripts and tests; the server never holds a request or transaction open for the wait.

**D35. Escalation scopes and shape.** Only the lead opens (`escalation.open`), only a human answers (`escalation.answer`; the admin token may not). Everyone with read access may fetch one escalation by id, which is how `--wait` polls. The answer must be one of the options when options were given, free text otherwise, and only once (409 after that). The asker receives the answer as an ordinary message whose sender is taken from the answering credentials, so an agent cannot forge `human:`. Open and answer are audited and emit events.

**D36. Notifications carry a link, never content.** A new escalation sends one generic line ("The lead has a question for you") and a link to the inbox through ntfy and/or a generic webhook. The question and the answer stay on the hub; a push service is a third party. Settings come from the environment, so secrets stay out of argv and service files' command lines: `HANDLOOM_BASE_URL`, `HANDLOOM_NTFY_URL`, `HANDLOOM_NTFY_TOPIC`, `HANDLOOM_NTFY_TOKEN`, `HANDLOOM_WEBHOOK_URL`. Sending runs after the request commits, in the background with a 15 s limit; a failure is logged, never returned to the lead, because the escalation is stored and shows in the inbox either way. Web push is deferred (needs a service worker and HTTPS; fragile on iOS).

**D37. The store boundary is scoped down.** v0.3 promised that SQLite-specific SQL would live in one package. The hub has about 55 raw SQL call sites (agents, tasks, messages, escalations), so a full move behind a store interface is a multi-day refactor that delivers nothing visible. Decision: all NEW persistence for the web UI (accounts, sessions, settings, tokens) goes through methods in `internal/store`, written dialect-neutral where cheap; the existing hub SQL stays. The Postgres migration path is therefore "possible but not prepared"; docs must not promise it. Revisit when there is a concrete reason to leave SQLite.

**D38. Container.** Multi-stage build; the builder runs on the build platform and cross-compiles (`GOARCH=$TARGETARCH`), so an arm64 box builds the amd64 image the VPS needs without emulation. The runtime is distroless static, non-root (65532), with `/data` created and owned in the build stage so a fresh named volume is writable. No shell in the image, so `HEALTHCHECK` is `handloom healthcheck`. `hub serve --auto-init` creates the database on first start and logs the admin token once; the setup-code flow in C4 replaces that for the web. Backups use `VACUUM INTO` (safe while running); `hub restore` checks integrity and schema version, refuses to overwrite without `--force`, and clears stale `-wal`/`-shm` files. Before a migration changes an existing database the hub writes `backups/pre-vN.db`; a database with a newer schema than the binary is refused. Verified on GB10: build, health, backup/restore round trip, restart persistence, and an amd64 cross-build. Not verified: Dokploy.

**D39. Web accounts and sessions.** Humans gain `password_hash` and `role` (owner, member, viewer; API-only humans are members). Passwords: argon2id (64 MiB, 3 passes, 2 threads), at most 2 hashes at once, a dummy hash for unknown names so timing does not reveal them, at least 12 characters. Sessions: random 256-bit id in the cookie, only its SHA-256 in the database, a new id at every login, idle timeout 12 h and absolute 7 days (checked on every request, configurable), purged at login. Cookie `__Host-handloom_session` (Secure, HttpOnly, SameSite=Lax, Path=/) over https; plain `handloom_session` on localhost or with `HANDLOOM_INSECURE=1`. Lax rather than Strict so a link from a phone notification opens signed in; state changes are POST-only and need the session's CSRF token (form field or `X-CSRF-Token`) plus a matching Origin. Login and setup have no session yet, so they require a same-origin Origin or `Sec-Fetch-Site`. Login is rate limited per address and per name before any lookup or hashing. Behind a proxy `HANDLOOM_TRUST_PROXY=1` uses the last `X-Forwarded-For` entry (the one our proxy appended); otherwise the socket address.

**D40. First-run setup.** The owner does not exist until someone presents a one-time setup code printed in the hub's log at start (stored only as a hash). The wizard is open exactly while no human has `role=owner` with a password, so an upgraded hub whose humans only have API tokens still gets a wizard, behind the code. Creation runs in one transaction that re-checks "no owner", so simultaneous submissions create one. A restart with an owner deletes the stored code. There is no web password reset and no email: `handloom hub reset-password <name>` (needs the database) prints a new password once and signs that human out everywhere.

**D41. The web UI is off unless it can be safe.** It needs `HANDLOOM_BASE_URL` set to https, to localhost, or `HANDLOOM_INSECURE=1`. Otherwise every web route answers 503 with the reason (and the log says so); the API is unaffected. This keeps existing Tailscale-over-http hubs working without silently serving a login form over plain http. `HANDLOOM_SECRET` (v0.4) is not introduced yet: nothing needs a signing key, because sessions and CSRF tokens are random values stored server-side. Add it when something needs one.

**D42. Viewer.** A viewer (`role=viewer`) may read and nothing else, through the API as well as the web UI (`human:viewer` in the scope table).

**D43. One digest, plain pages.** `GET /v1/digest` is the single source for the inbox, and for the TUI and phone later: needs-you (open escalations, blocked tasks, a lead offline or unknown for over 30 minutes while work is open), to-review (submitted tasks), running, agents, recent activity from the audit log (humans and admin only; the log spans projects), and the newest event number. The inbox is server-rendered HTML; no htmx or other script library was needed. Every action is a plain form post with a CSRF token (post, redirect, get), so the page works without script. `app.js` only opens an `EventSource`; when the hub says "inbox-changed" it refetches `/inbox/fragment` and swaps the body, unless the human is typing in a field, in which case it shows "New activity. Refresh" instead of throwing away their text. Events are hints only and carry no content; a reconnecting page gets a `resync` first, so a missed event heals itself.

**D44. Streams hold no database connection while they wait.** The hub has one SQLite connection. A stream waits on the in-process notifier and runs a short query only when it sends. Caps: 4 streams per human, 100 in all (429 beyond that); a heartbeat comment every 15 s keeps proxies from timing it out; the session is rechecked every minute so a signed-out browser's stream ends. `Cache-Control: no-transform` and `X-Accel-Buffering: no` keep proxies from compressing or buffering it. Answering an escalation and accepting or rejecting a task from the web share the code of the API verbs (`answerEscalation`, `rejectTask`), so the two paths cannot drift apart.

**D45. Found with a real browser: `Referrer-Policy: no-referrer` makes browsers send `Origin: null` on form posts.** The origin check refused every login from Chromium, and the unit tests (which set the Origin header by hand) could not see it. Fix: the policy is `same-origin` (nothing leaves the site, but same-site posts keep their Origin), and an `Origin: null` is accepted only together with `Sec-Fetch-Site: same-origin`. Lesson kept: UI changes get a screenshot and a click-through in Chromium, not only httptest. The inbox was checked at 1100 px and 390 px, light and dark.

**D46. Devices and settings pages.** Owners add and revoke devices and manage people; members and viewers can see the device list. Actions that widen what the hub can do need the signer's password again (add device, add person, change role, new invite, new API token), rate limited to five tries a minute: a stolen session cookie alone cannot add a machine that runs agents. Join tokens now expire (15 minutes, `JoinTTL`; rows created before this release have no expiry and keep working) and the unauthenticated join endpoint is rate limited by address before any lookup. People are invited with a one-time link (7 days) where they choose their own password; an invite to an existing person doubles as a password reset and signs them out. A role change signs the person out at once. Notification settings (ntfy server and topic) live in the database and take effect without a restart; the ntfy access token never does: it stays in `HANDLOOM_NTFY_TOKEN`. Settings add to the environment's notifiers, they do not replace them.

**D47. Milestone A check: `test/e2e/a.sh`.** One machine, about 25 seconds: builds the image, runs the hub in a container with its own volume, sets up the owner through the form (including a refused post without an Origin and a closed setup page afterwards), adds a device from the web UI, joins and starts a link, has the lead ask a question, checks the push reached a fake ntfy server with a link and without the question, answers by clicking in real Chromium, checks that a second question appears in the open page with no reload, checks the lead's inbox shows the answer from `human:afif` and that the audit log has every step, then backs up and restores the container's data. It cleans up everything it made (names `hltest-a-*`). Screenshots land in `test/e2e/out/a/shots`. Not covered: Dokploy itself (the Mantis VPS does not run it), a real ntfy server and phone, and the agent scenarios `m1.sh` and `m2.sh` (real Claude, Codex and Pi agents), which were not re-run after the escalation, auth and migration changes. `m0.sh` (GB10 plus Mantis, handloom commands only) was re-run on the new binary and passes.

## Rename to Handloom

**D48. The code is renamed from handloom to Handloom in one commit, with the old names still accepted.** Module `handloom`, binary `handloom` (`cmd/handloom`), short alias `hl`, MCP tools `handloom_*`, header `Handloom-Agent`, protocol id `handloom/1`, `HANDLOOM_*` settings, `.handloom/agent`, `handloom.db`, `~/.config/handloom`, service units `handloom-hub` and `handloom-link`, adapter shims `handloom.ts` and `handloom.js`. Nothing that exists in the field breaks: `HANDLOOM_*` settings are read when `HANDLOOM_*` is not set (`internal/setting`); the hub reads `Handloom-Agent` and clients send both headers; `.handloom/agent`, `handloom.db`, `handloom-data` and `~/.config/handloom` are used when the new name does not exist yet; the `handloom_*` MCP tool names still resolve; children of the link get both spellings of the settings. `adapter install` removes the old identity file and shim, and a `handloom` hook command keeps working because `scripts/setup.sh` installs a `handloom` symlink to the binary; `hl` is installed only if no other `hl` exists (Homebrew has a log viewer of that name). `hub install` and `link install` replace a service installed under the old name instead of running two. `docs/protocol.md` and the README use the new names; DESIGN, BRIEF, REPORT and DECISIONS keep their history with a note on top. Checked: Go tests (new tests for each compatibility path), `a.sh`, `m0.sh` (GB10 plus Mantis), and a real headless Claude Code turn that read its inbox with `handloom inbox` under the installed adapter. Not run: `m1.sh` and `m2.sh`, because Claude Code on Mantis is not logged in (`ssh -t mantis claude`, then `/login`); the Mantis binary in `/root/hltest/bin` was not touched.

## Dokploy deployment

**D49. Deployed on the owner's Dokploy as its own project.** Project "Handloom" (id `Ka0WzzTTkvNUGnY4Yynj7`), compose app `handloom-hub` (id `YOUR-COMPOSE-ID`, raw source), domain `handloom.example.com` with a Let's Encrypt certificate, on the Dokploy host `dokploy-host.example` (x86_64). The wildcard DNS for `example.com` already pointed there. The owner allowed using the API key in `~/.hermes/.env`; it was read into a shell variable for the calls and never printed or written anywhere. Image path: built for amd64 on GB10, loaded over ssh, run with `pull_policy: never`, because the repo has no remote. Checks: `https://handloom.example.com/healthz` 200, HSTS and the CSP present, certificate issued by Let's Encrypt, container health `healthy`, the setup page reachable and the setup code in the log. `HANDLOOM_TRUST_PROXY=1` is set because Traefik is the only proxy in front. Not done by me: creating the owner account (that is the owner's password), real phone push (no ntfy topic yet), and Dokploy's own volume backups for `/data` (use `hub backup` meanwhile). Other apps and projects on that Dokploy were not touched.

## Milestone B (foundations)

**D50. B1: a job is a root task with its own lead.** `task.kind` is `task` or `job`; a job row is never claimed, never on the task board (`task list` hides it, `job list` shows it), and is closed only by a human (`job close`, or `--cancel`, which also cancels its unfinished tasks). Tasks carry `job_id` (and `parent_id`, equal to it for now); an agent can belong to a job (`agent.job_id`). The one-lead rule became "one lead per job, and one project-level lead": the unique index is `(project_id, COALESCE(job_id, 0)) WHERE role = 'lead'`. Everything that used to message "the lead" now asks `leadFor(task's job)`: the job's own lead, falling back to the project lead when the job has none. `role:lead` from an agent means the lead it reports to (its own job, else the job of the task it is working on, else the project lead); a human who says `role:lead` while there are several is asked to name one. Only humans start and close jobs and move agents between jobs, because those decide what an agent is allowed to see and do. A lead's new tasks go into its own job and it cannot choose another; dependencies must stay inside the job. Agent names stay unique per hub: the `<profile>@<job>-<n>` naming belongs to spawn (milestone C). No `brief_path`, `brief_version` or `budget_json` yet: nothing reads them until D.

**D51. B scope changes from v0.4.** (1) *Confidential* is cut to the column and an off switch: `job new --confidential` is refused unless the hub runs with `HANDLOOM_ALLOW_CONFIDENTIAL=1`, because v0.3 section 6 makes "confidential off" the default on a public hub, and a `canRead` gate over content that does not exist until milestone F would be untested code. Tasks of a confidential job inherit the mark. (2) *Member scopes* needed nothing: the viewer role and the owner-only pages were built in A. (3) The lease-based states `dead`, `killed`, `hold`, `relieved` are not added; B uses the existing `offline` (see B3).
