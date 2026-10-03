# Report: handloom M0, M1 and M2

Date: 2026-10-03. Builder: Claude Code on GB10.

## Status

| Milestone | Result |
|---|---|
| **M0** | **Passes** on GB10 plus Mantis. |
| **M1** | **Passes** on GB10 plus Mantis: three of four runs. The failed run is explained below and led to a fix. |
| **M2** | **Partly passes.** Claude Code lead, Codex worker and a headless Claude Code worker finish the job (two runs of two). The check also asks for a Pi worker, which has not run: Pi has no working cloud model on either machine. |

The M0 and M1 sections below are as written for those milestones. M2 is in its own section at the end.

## What works

- **Hub.** HTTP API, SQLite (WAL), token auth for admin, humans and devices, tokens stored hashed, device join and revoke.
- **Scopes.** The section 6 table is enforced on every request. A rejected action gets 403 and a `denied` row in the audit log.
- **Task board.** Create, assign, claim, heartbeat, release, block, submit, accept, reject, cancel. Leases, dependencies, assignment, evidence required on submit.
- **Messages.** To an agent, `role:lead` or `task:<id>`. The sender is set by the hub; an agent cannot forge a `human:` sender.
- **Audit log.** Every action, append-only (the database rejects updates and deletes).
- **Link.** Unix socket (mode 0600) for the CLI and hooks, long-poll to the hub, wake ladder with its guards.
- **Wake ladder.** End-of-turn hook, terminal nudge through tmux and Herdr, report to the lead when an agent cannot be woken. All three ran live with real Claude Code on GB10.
- **Claude Code adapter.** State hooks, Stop-hook delivery, instructions block in `CLAUDE.md`. `handloom adapter install claude` and `remove`.
- **CLI.** One binary: `handloom hub`, `handloom link`, agent and admin verbs. `handloom help` lists them.

## What does not work, or is not done

- **M1 is not fully reliable.** One of four two-machine runs failed (run 2, below). An agent that stops without claiming its assigned work is not detected: the lead waits and nobody is told. Leases only cover claimed tasks.
- **An agent may decline a message.** In the hook check the worker read a human's message asking for a reply and chose not to send it, because it was unrelated to what it was doing. Delivery worked; acting on mail is the agent's judgement.
- **Out of scope, not built:** other adapters, MCP server, headless resume, web board, escalations (`handloom ask`), notifiers, packaging, TLS.
- **Device-offline detection** is not built: an agent goes `offline` only when its session ends cleanly.
- `handloom link install` (service unit) is not built; the link runs in the foreground.

## Evidence

Full transcripts are in `docs/evidence/`.

### Unit tests

```
$ go test ./... -count=1
ok  	handloom/internal/cli	0.075s
ok  	handloom/internal/drivers	0.003s
ok  	handloom/internal/hub	0.648s
ok  	handloom/internal/link	0.099s
```

At M1: 97 tests and subtests pass, none fail (105 after M2). `TestScopeTable` checks every cell of the section 6 table: 15 verbs times 4 callers, 60 subtests. Each forbidden cell must return 403 and leave a `denied` audit row; each permitted cell must succeed.

### M0: GB10 plus Mantis (`test/e2e/m0.sh`, transcript `docs/evidence/m0-gb10-mantis.txt`)

The lead's shell is on GB10 (`thinkstationpgx-2be0`), the worker's on Mantis. Excerpt:

```
[hltest-lead@thinkstationpgx-2be0] $ handloom task create Report the kernel version --body ... --assign hltest-worker
    Created #1   open      Report the kernel version  (assigned to hltest-worker)
[hltest-worker@mantis] $ handloom inbox
    [1] from hub, about task #1, 18:40:22
        Task #1 is assigned to you: Report the kernel version. ...
[hltest-worker@mantis] $ handloom task claim 1
    #1   claimed   Report the kernel version  (owner hltest-worker, lease until 18:40:43)
[hltest-worker@mantis] $ handloom send role:lead Claimed task 1, running it now. --task 1
    Sent to hltest-lead.
[hltest-worker@mantis] $ handloom task submit 1 --evidence test:uname -r -> 6.8.0-139-generic --note Output of uname -r on mantis.
    #1   submitted Report the kernel version  (owner hltest-worker)
[hltest-lead@thinkstationpgx-2be0] $ handloom task accept 1
    #1   done      Report the kernel version  (owner hltest-worker)

[hltest-worker@mantis] $ handloom task create Workers cannot create tasks
    handloom: role worker may not task.manage
[hltest-lead@mantis (imposter)] $ handloom task create A device cannot speak for an agent of another device
    handloom: agent "hltest-lead" is not registered on device hltest-mantis

[hltest-worker@mantis] $ handloom task claim 2
    #2   claimed   A task whose owner goes silent  (owner hltest-worker, lease until 18:40:44)
    waiting 23s without a heartbeat...
    task #2 is open
[hltest-worker@mantis] $ handloom task heartbeat 2
    handloom: task 2 is not yours (status open, owner none)
[hltest-lead@thinkstationpgx-2be0] $ handloom inbox
    [7] from hub, about task #2, 20:40:45
        Task #2 (A task whose owner goes silent): the lease held by hltest-worker expired. The task is open again.

M0 PASS: lead on thinkstationpgx-2be0 and worker on mantis completed the loop with handloom commands only.
```

Audit log of that run (rows 10 to 28):

```
10  18:40:22.944Z  agent:hltest-lead    task.create        task:1  {"assigned_to":"hltest-worker","depends_on":[],"title":"Report the kernel version"}
14  18:40:23.255Z  agent:hltest-worker  task.claim         task:1  {"owner":"hltest-worker"}
15  18:40:23.433Z  agent:hltest-worker  message.send       message:3  {"recipients":["hltest-lead"],"task_id":1,"to":"role:lead"}
16  18:40:23.697Z  agent:hltest-worker  task.submit        task:1  {"evidence":["test:uname -r -> 6.8.0-139-generic"],...}
19  18:40:23.783Z  agent:hltest-lead    task.accept        task:1  {"owner":"hltest-worker"}
21  18:40:24.088Z  agent:hltest-worker  denied             POST /v1/tasks  {"reason":"role worker may not task.manage"}
23  18:40:24.436Z  device:hltest-mantis denied             POST /v1/tasks  {"reason":"agent \"hltest-lead\" is not registered on device hltest-mantis"}
25  18:40:24.608Z  agent:hltest-worker  task.claim         task:2  {"owner":"hltest-worker"}
26  18:40:45.541Z  hub                    task.lease_expired task:2  {"owner":"hltest-worker"}
27  18:40:47.807Z  agent:hltest-worker  denied             POST /v1/tasks/2/heartbeat  {"reason":"task 2 is not yours (status open, owner none)"}
```

### M1: lead Claude Code on GB10, worker Claude Code on Mantis (`test/e2e/m1.sh`)

Both on Claude Code 2.1.288, model Sonnet. The script types one thing into an agent, the brief into the lead, and then only reads. The worker gets no human input: its first prompt is handloom's nudge. During setup the script answers Claude Code's trust-this-folder dialog on a new directory (two keys on Mantis in run 1), before the brief.

| Run | Result | Transcript |
|---|---|---|
| 1 | M1 checks pass, job done in 91 s. Script exit 1: my extra hook check wrongly required the worker to obey the test message. | `docs/evidence/m1-gb10-mantis-run1.txt` |
| 2 | **Fail.** The worker's command used a shell redirect (`uname -r > kernel.txt`), which `dontAsk` mode denies. It stopped and asked "the user" instead of trying another way or telling the lead. Nothing was claimed; timeout. | `docs/evidence/m1-gb10-mantis-run2-failed.txt` |
| 3 | Pass, exit 0. | `docs/evidence/m1-gb10-mantis-run3.txt` |
| 4 | Pass, exit 0. | `docs/evidence/m1-gb10-mantis-run4.txt` |

After run 2 I changed two things, neither of which weakens the M1 check: the adapter's instructions now tell an unattended agent that nobody is watching, to retry a denied command another permitted way, and to block the task and message the lead if still stuck; and the extra hook check now tests delivery (hook wake, then inbox read), not obedience.

Audit log after the brief, run 4:

```
17  19:05:01.879Z  agent:hltest-lead    task.create  task:1  {"assigned_to":"hltest-worker","depends_on":[],"title":"Write kernel version to kernel.txt"}
18  19:05:02.402Z  device:hltest-mantis wake         agent:hltest-worker  {"messages":[1],"method":"tmux"}
20  19:05:03.347Z  agent:hltest-lead    task.create  task:2  {"assigned_to":"hltest-worker","depends_on":[],"title":"Write hostname to hostname.txt"}
22  19:05:04.961Z  agent:hltest-lead    task.create  task:3  {"assigned_to":"hltest-worker","depends_on":[1],"title":"Write summary.txt"}
24  19:05:08.564Z  agent:hltest-worker  task.claim   task:1  {"owner":"hltest-worker"}
25  19:05:08.759Z  agent:hltest-worker  task.claim   task:2  {"owner":"hltest-worker"}
26  19:05:17.185Z  agent:hltest-worker  task.submit  task:1  {"evidence":["test:cat kernel.txt -> 6.8.0-139-generic"],...}
27  19:05:17.412Z  agent:hltest-worker  task.submit  task:2  {"evidence":["test:cat hostname.txt -> ubuntu-4gb-hel1-1"],...}
28  19:05:17.596Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[4,5],"method":"tmux"}
31  19:05:20.170Z  device:hltest-mantis wake         agent:hltest-worker  {"messages":[3],"method":"hook"}
32  19:05:21.309Z  agent:hltest-lead    task.accept  task:1  {"owner":"hltest-worker"}
33  19:05:21.313Z  agent:hltest-lead    task.accept  task:2  {"owner":"hltest-worker"}
36  19:05:25.317Z  agent:hltest-worker  task.claim   task:3  {"owner":"hltest-worker"}
37  19:05:27.965Z  agent:hltest-worker  task.submit  task:3  {"evidence":["test:cat summary.txt -> 6.8.0-139-generic / ubuntu-4gb-hel1-1"],...}
39  19:06:18.704Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[9],"method":"tmux"}
42  19:06:21.835Z  agent:hltest-lead    task.accept  task:3  {"owner":"hltest-worker"}
```

The script's checks on that log, all ok: three tasks created by the lead and assigned to the worker; each claimed and submitted by the worker and accepted by the lead; task 3 claimed only after task 1 was accepted; the worker's first wake was a typed nudge; both agents were woken by handloom (worker: tmux, hook, tmux; lead: tmux, tmux); no human or admin action after the brief.

The 51 seconds between rows 37 and 39 are the nudge rate limit (one per agent per 60 seconds).

Before the Mantis login, the same script ran with both agents on GB10 as two devices (`WORKER_HOST=local`): `docs/evidence/m1-rehearsal-gb10-only.txt`.

### The other wake paths, live

- **End-of-turn hook** (also an extra phase of `m1.sh`). Mail was sent while the worker on Mantis was mid-turn. Nothing was typed. The Stop hook held the turn and the worker read the mail (run 4):
  ```
  49  19:06:34.570Z  human:hltest-human   message.send  message:11
  50  19:06:43.944Z  device:hltest-mantis wake          agent:hltest-worker  {"messages":[11],"method":"hook"}
  51  19:06:45.601Z  agent:hltest-worker  inbox.read    agent:hltest-worker  {"messages":[11]}
  ```
- **Herdr driver** (`test/e2e/herdr.sh`, transcript `docs/evidence/herdr-driver-gb10.txt`). A Claude Code agent in a new Herdr pane was woken with `herdr agent prompt`:
  ```
  10  18:37:57.162Z  device:hltest-herdr-device wake  agent:hltest-herdr  {"messages":[1],"method":"herdr"}
  12  18:37:59.090Z  agent:hltest-herdr  inbox.read   agent:hltest-herdr  {"messages":[1]}
  ```
- **Blocked agent** (GB10). In the first rehearsal the lead stopped at an approval prompt. The adapter reported `blocked` (`agent.state ... {"state":"blocked","was":"working"}`) and the link typed nothing. That run failed at the timeout, which led to the `dontAsk` setting for test agents (DECISIONS.md E1). Report-on-failure itself is covered by unit tests, not by a live run.

## Decisions

All in [DECISIONS.md](DECISIONS.md). The ones most worth a look:

- Test agents run in `dontAsk` mode with an allowlist, because root on Mantis cannot use bypass mode and an approval prompt hangs the job (E1).
- New agents are workers; only the admin or a human gives the lead role (D2).
- The wake nudge has no backticks and the tmux driver refuses to type into a shell (D13).
- tmux wins over Herdr when detecting where an agent lives, after a test shell picked up my own Herdr pane as its wake target (D14).
- Lease heartbeats follow tool activity, not the `working` state (D15).
- Plain HTTP on the Tailscale network for now (D1).

## What should change in DESIGN.md

Listed at the end of DECISIONS.md. In short: add the four API calls the wake ladder needed, state the default role and the admin's scopes, specify the re-nudge rule, drop the backticks from the nudge, and correct the claim that Herdr's hook gives Claude Code's state.

## Left on the machines

- GB10: Go 1.27.1 in `~/.local/go`. The repo in `/home/user/projects/handloom` with local commits only; no remote, nothing pushed.
- Mantis: `/root/hltest` (the test binary, link files and the worker's test project). Remove with `ssh mantis rm -rf /root/hltest`. The next e2e run recreates it.
- No tmux session, Herdr pane or process from the tests is still running. Ports 7420 and 7421 are free.

---

# M2

## Status

The M2 check: the M1 scenario with a mixed team (at least a Claude lead plus Codex and Pi workers), and with one worker that has no terminal.

| Part | Result |
|---|---|
| Claude Code lead, GB10 | passes |
| Codex worker, Mantis (sandbox on, handloom through MCP) | passes |
| Worker with no terminal (headless Claude Code, Mantis) | passes |
| Pi worker | **not run** |

**Why no Pi worker.** Pi's only working model is the local Qwen: on GB10 its OpenAI login is invalid (`pi auth check` says `invalid`), on Mantis its OpenRouter key answers `403 Key limit exceeded`. Your rule for the local model (work goes through OMP in a detached tmux run, no input typed into a running session) does not fit a handloom worker woken by typed nudges, so I did not run one without asking. To run the full check, either fix one cloud login for Pi, or allow the local model for this test:

```
PI_MODEL=<provider/model> test/e2e/m2.sh
```

## What was built

- **`handloom hook <kind>`**: one handler for Claude Code, Codex, and the Pi, OMP and OpenCode shims.
- **Codex adapter**: `.codex/hooks.json` and the instructions in `AGENTS.md`. Verified on Mantis: state hooks, Stop-hook delivery, terminal nudge.
- **`handloom mcp`**: MCP server over stdio with the agent verbs as tools. Codex's sandbox blocks the link's socket; through MCP it uses handloom with the sandbox left on.
- **`handloom run <name> -- <command>`**: starts an agent CLI as a registered, wakeable agent. Needed because Codex's session-start hook fires only at the first prompt.
- **Headless resume** (wake ladder step 3): an agent with wake target `headless` is woken by one headless turn on its saved session.
- **Pi, OMP and OpenCode adapters**: extension or plugin that forwards their events to `handloom hook`. Written from each CLI's extension API; installed and unit-tested; **not run against a model**.
- **Unclaimed-task notice**: the hub tells the lead when an assigned task is not claimed for 10 minutes (the gap found in M1).
- A blocked agent is reported to the lead after 30 seconds, not at once.

## What does not work, or is not verified

- **No Pi worker ran** (above).
- **Pi, OMP and OpenCode adapters are unverified live.** Their shims may need changes once run: in particular the end-of-turn delivery (`pi.sendUserMessage` at `agent_settled`; `client.session.prompt` at `session.idle`).
- **Headless commands for Codex, Pi, OMP and OpenCode** come from each CLI's `--help` and have not been run. Only `claude -p --resume` has.
- **Codex on GB10 is logged out**, so Codex 0.144.5 there is untested. Mantis has 0.160.0.
- **Codex records the test folder as trusted** in `~/.codex/config.toml` on Mantis (one entry, `/root/hltest/codex-project`). handloom does not write it; Codex does.
- Two runs is a small sample.

## Evidence

Unit tests: 105 tests and subtests pass, none fail.

`test/e2e/m2.sh`, two runs, both exit 0: `docs/evidence/m2-claude-codex-headless-run1.txt` and `-run2.txt`; the Codex terminal and the headless log of run 1 are beside them. M0 was rerun with the final code and passes.

Audit log after the brief, run 2:

```
24  19:54:30.021Z  agent:hltest-lead      task.create  task:1  {"assigned_to":"hltest-codex","depends_on":[],"title":"Collect kernel release"}
25  19:54:30.387Z  agent:hltest-lead      task.create  task:2  {"assigned_to":"hltest-headless","depends_on":[],"title":"Collect hostname"}
26  19:54:30.542Z  device:hltest-mantis   wake         agent:hltest-codex     {"messages":[1],"method":"tmux"}
27  19:54:30.629Z  device:hltest-mantis   wake         agent:hltest-headless  {"messages":[2],"method":"headless"}
30  19:54:32.022Z  agent:hltest-lead      task.create  task:3  {"assigned_to":"hltest-codex","depends_on":[1],"title":"Collect architecture"}
36  19:54:38.791Z  agent:hltest-headless  task.claim   task:2  {"owner":"hltest-headless"}
38  19:54:42.658Z  agent:hltest-headless  task.submit  task:2  {"evidence":["test:cat hostname.txt -> ubuntu-4gb-hel1-1"],...}
39  19:54:43.069Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[4],"method":"tmux"}
43  19:54:46.219Z  agent:hltest-codex     task.claim   task:1  {"owner":"hltest-codex"}
45  19:54:55.183Z  agent:hltest-lead      task.accept  task:2  {"owner":"hltest-headless"}
47  19:54:58.022Z  agent:hltest-codex     task.submit  task:1  {"evidence":["test:cat kernel.txt -> 6.8.0-139-generic"],...}
49  19:55:33.253Z  device:hltest-mantis   wake         agent:hltest-headless  {"messages":[5],"method":"headless"}
56  19:55:51.575Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[6],"method":"tmux"}
59  19:55:55.490Z  agent:hltest-lead      task.accept  task:1  {"owner":"hltest-codex"}
60  19:55:56.013Z  device:hltest-mantis   wake         agent:hltest-codex     {"messages":[7,8],"method":"tmux"}
64  19:56:03.505Z  agent:hltest-codex     task.claim   task:3  {"owner":"hltest-codex"}
65  19:56:12.905Z  agent:hltest-codex     task.submit  task:3  {"evidence":["test:cat arch.txt -> x86_64"],...}
67  19:56:51.581Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[9],"method":"tmux"}
70  19:56:55.487Z  agent:hltest-lead      task.accept  task:3  {"owner":"hltest-codex"}
```

The script's checks on that log, all ok: the lead created three tasks spread over the two workers; each was claimed and submitted by its assignee and accepted by the lead; task 3 was claimed only after task 1 was accepted; the Codex worker was woken by typed nudges; the headless worker was woken only by headless turns; no human or admin action after the brief.

Setup before the brief, done by the script: starting the agents, and one `claude -p` run that creates the headless worker's session, because a session must exist before it can be resumed.

## How the Codex worker is started (from the run)

```
handloom run hltest-codex -- codex --enable hooks --dangerously-bypass-hook-trust -a never -s workspace-write \
  -c 'mcp_servers.handloom.command="/root/hltest/bin/handloom"' -c 'mcp_servers.handloom.args=["mcp"]' \
  -c 'mcp_servers.handloom.default_tools_approval_mode="approve"' \
  -c 'mcp_servers.handloom.env={HANDLOOM_HOME="/root/hltest/home",HANDLOOM_AGENT="hltest-codex"}'
```

The sandbox and "never ask" stay on. `--dangerously-bypass-hook-trust` skips Codex's one-time review of the hooks handloom installed; it is not the approvals-and-sandbox bypass. The reasons are in DECISIONS.md (D23 to D25).

## Decisions and design changes

In DECISIONS.md under "M2 decisions". The main ones: Codex reaches handloom through MCP rather than by opening its sandbox to the network (D23); headless resume is opt-in, so an interactive agent that went offline is not resumed behind its owner's back (D26); the MCP server is hand-written with no new dependency (D29).
