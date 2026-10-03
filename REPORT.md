# Report: handloom M0 and M1

Date: 2026-10-03. Builder: Claude Code on GB10.

## Status

| Milestone | Result |
|---|---|
| **M0** | **Passes** on GB10 plus Mantis. |
| **M1** | **Not passed yet.** Built and passing in a rehearsal with both agents on GB10. The real run needs Claude Code on Mantis, which is not logged in. |

M1 is blocked by one thing only the owner can do: log in to Claude Code on Mantis.

```
ssh -t mantis /root/.local/bin/claude      # then /login
cd /home/user/projects/handloom && test/e2e/m1.sh
```

`claude auth status` on Mantis returns `"loggedIn": false`. I did not copy credentials from GB10, and I did not count the rehearsal as M1.

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

- **M1 on two machines** has not run (see Status).
- **Claude Code 2.1.220 on Mantis is untested with the adapter.** GB10 has 2.1.288. The adapter uses the `PermissionRequest` hook event and the test agents use `dontAsk` mode; I could not check either on the older version without a login. If one is missing there, the M1 run will show it.
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

97 tests and subtests pass, none fail. `TestScopeTable` checks every cell of the section 6 table: 15 verbs times 4 callers, 60 subtests. Each forbidden cell must return 403 and leave a `denied` audit row; each permitted cell must succeed.

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

### M1 rehearsal: both agents on GB10, as two devices (`WORKER_HOST=local test/e2e/m1.sh`)

This is not the M1 check. It is the same script and the same job with the worker's Claude Code on GB10 behind a second link, because Mantis could not run Claude Code. Transcript: `docs/evidence/m1-rehearsal-gb10-only.txt`; the agents' terminals: `m1-rehearsal-lead-pane.txt`, `m1-rehearsal-worker-pane.txt`.

The script typed one thing into an agent, the brief into the lead, and then only read. The worker received no human input: its first prompt was handloom's nudge. Audit log after the brief:

```
19  18:38:19.844Z  agent:hltest-lead    task.create  task:1  {"assigned_to":"hltest-worker","depends_on":[],"title":"Write kernel version to kernel.txt"}
20  18:38:20.254Z  device:hltest-local2 wake         agent:hltest-worker  {"messages":[1],"method":"tmux"}
22  18:38:21.566Z  agent:hltest-lead    task.create  task:2  {"assigned_to":"hltest-worker","depends_on":[],"title":"Write hostname to hostname.txt"}
23  18:38:21.569Z  agent:hltest-lead    task.create  task:3  {"assigned_to":"hltest-worker","depends_on":[1],"title":"Write summary.txt"}
26  18:38:26.203Z  agent:hltest-worker  task.claim   task:1  {"owner":"hltest-worker"}
27  18:38:26.424Z  agent:hltest-worker  task.claim   task:2  {"owner":"hltest-worker"}
28  18:38:30.916Z  agent:hltest-worker  task.submit  task:1  {"evidence":["test:cat kernel.txt -> 6.17.0-1029-nvidia"],...}
29  18:38:31.239Z  agent:hltest-worker  task.submit  task:2  {"evidence":["test:cat hostname.txt -> thinkstationpgx-2be0"],...}
30  18:38:31.325Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[4,5],"method":"tmux"}
34  18:38:36.468Z  agent:hltest-lead    task.accept  task:1  {"owner":"hltest-worker"}
35  18:38:36.472Z  agent:hltest-lead    task.accept  task:2  {"owner":"hltest-worker"}
37  18:39:21.660Z  device:hltest-local2 wake         agent:hltest-worker  {"messages":[6,7,8],"method":"tmux"}
40  18:39:24.175Z  agent:hltest-worker  task.claim   task:3  {"owner":"hltest-worker"}
41  18:39:26.933Z  agent:hltest-worker  task.submit  task:3  {"evidence":["test:cat summary.txt -> 6.17.0-1029-nvidia / thinkstationpgx-2be0"],...}
43  18:39:41.017Z  device:hltest-thinkstationpgx-2be0 wake  agent:hltest-lead  {"messages":[9],"method":"tmux"}
46  18:39:46.004Z  agent:hltest-lead    task.accept  task:3  {"owner":"hltest-worker"}
```

The script's checks all passed: three tasks created by the lead, each claimed and submitted by the worker and accepted by the lead, task 3 claimed only after task 1 was accepted, both agents woken by handloom, no human or admin action after the brief. The job took 91 seconds.

### The other wake paths, live on GB10

- **End-of-turn hook** (extra phase of `m1.sh`). Mail was sent while the worker was mid-turn. Nothing was typed. The Stop hook held the turn and the worker read and answered the mail:
  ```
  49  18:39:57.192Z  human:hltest-human   message.send  message:11
  50  18:40:03.677Z  device:hltest-local2 wake          agent:hltest-worker  {"messages":[10,11],"method":"hook"}
  51  18:40:04.920Z  agent:hltest-worker  inbox.read    agent:hltest-worker  {"messages":[10,11]}
  52  18:40:06.875Z  agent:hltest-worker  message.send  message:12  {"recipients":["hltest-lead"],"to":"role:lead"}
  ```
- **Herdr driver** (`test/e2e/herdr.sh`, transcript `docs/evidence/herdr-driver-gb10.txt`). A Claude Code agent in a new Herdr pane was woken with `herdr agent prompt`:
  ```
  10  18:37:57.162Z  device:hltest-herdr-device wake  agent:hltest-herdr  {"messages":[1],"method":"herdr"}
  12  18:37:59.090Z  agent:hltest-herdr  inbox.read   agent:hltest-herdr  {"messages":[1]}
  ```
- **Blocked agent.** In the first rehearsal the lead stopped at an approval prompt. The adapter reported `blocked` (`agent.state ... {"state":"blocked","was":"working"}`) and the link typed nothing. That run failed at the timeout, which led to the `dontAsk` setting for test agents (DECISIONS.md E1). Report-on-failure itself is covered by unit tests, not by a live run.

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

- GB10: Go 1.27.1 in `~/.local/go`. The repo in `/home/user/projects/handloom` with 6 local commits; no remote, nothing pushed.
- Mantis: `/root/hltest` (the test binary and link files). Remove with `ssh mantis rm -rf /root/hltest`. The next e2e run recreates it.
- No tmux session, Herdr pane or process from the tests is still running. Ports 7420 and 7421 are free.
