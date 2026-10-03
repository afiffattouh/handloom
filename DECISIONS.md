# Decisions

Choices made while building M0 and M1, and places where the design was unclear or did not match what the machines do. Each entry says what the design or brief said, what was found, and what was done.

## Environment findings

**E1. Claude Code on Mantis runs as root, and root cannot use bypass mode.**
`ssh mantis '/root/.local/bin/claude -p "say ok" --dangerously-skip-permissions'` prints `--dangerously-skip-permissions cannot be used with root/sudo privileges for security reasons` and exits 1. Root is the only user on Mantis. No user was created on the shared VPS. Test agents on both machines get the same scoped allowlist instead (`test/e2e/agent.sh`): `defaultMode: dontAsk` plus file tools and a few read-only commands in the test project's `.claude/settings.json`, and the adapter's own allow rule for `handloom`. In `dontAsk` mode a tool call outside the list is denied with an error the agent reads; it never opens an approval prompt. The first rehearsal used `acceptEdits` and showed why this matters: the lead tried `find` to double-check a file, stopped at an approval prompt, the adapter reported `blocked`, and the job hung until the timeout.

**E1b. Claude Code shows a "trust this folder" dialog in a new directory.** When the folder pre-approves permissions, the dialog's default answer is "No, exit". `test/e2e/m1.sh` answers it (Down, Enter) during setup, before the brief, and counts those keys in its output. Without trust, Claude Code ignores the project's allow rules.

**E2. Claude Code on Mantis was not logged in.**
`claude auth status` on Mantis returned `"loggedIn": false`, and `claude -p "say ok"` printed `Not logged in · Please run /login`. Logging in needs the owner. Credentials were not copied from GB10. `test/e2e/m1.sh` checks this first and stops with a clear message.

**E3. Two Claude Code versions on Mantis.**
A non-interactive shell finds `/usr/local/bin/claude` (2.1.202). The newer one is `/root/.local/bin/claude` (2.1.220). GB10 has 2.1.288. The e2e script uses `/root/.local/bin/claude` by absolute path (`REMOTE_CLAUDE`).

**E4. Different CPU architectures.**
GB10 is arm64, Mantis is amd64. Go 1.27.1 is installed in `~/.local/go` on GB10 (checksum verified against go.dev). The Mantis binary is cross-compiled with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`; the pure-Go SQLite driver makes that work.

**E5. Herdr's Claude hook reports the session, not the state.**
`~/.claude/hooks/herdr-agent-state.sh` handles `SessionStart` only and sends the session id to Herdr's socket. It does not report idle, working or blocked. So handloom cannot learn Claude Code's state from that hook and installs its own. No Herdr code was copied, so its licence was not a question. handloom never edits `~/.claude/settings.json`; its hooks go in the project.

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

**D12. A wake failure is reported once per episode.** The first M0 run reported "cannot be woken" to the lead for every new message to a plain shell. Now the link reports a given reason once and again only after the agent's state has changed.

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
6. Section 10, adapter table: add that Claude Code refuses bypass mode as root, so headless and unattended agents on root-only machines need an allowlist (E1).
