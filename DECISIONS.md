# Decisions

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

**E8. Pi has no working cloud model on either machine.** GB10: `pi auth check --provider openai-codex` returns `invalid`; the default model is the local Qwen. Mantis: Pi's OpenRouter key answers `403 Key limit exceeded`. So the only model Pi can use is the local Qwen. The owner's rule for the local model (work goes to it through OMP in a detached tmux run, one at a time, no input typed into a running session) does not fit a handloom worker that is woken by typed nudges, so no Pi worker was run without the owner's say. `test/e2e/m2.sh` adds a Pi worker when `PI_MODEL` is set.

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
2. Section 10, Pi row: turn-end delivery is `agent_settled` plus `pi.sendUserMessage`; unverified against a model.
3. Section 9, step 3: say whether an interactive agent that went offline may be resumed headless (D26).
4. Section 12: `dir` on agents, wake method `headless`, `task.unclaimed`.
