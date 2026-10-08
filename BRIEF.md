# Build brief: handloom, milestones M0 and M1


You are building the first two milestones of handloom, an open-source tool that lets coding agents on different machines coordinate without a human relaying messages.

## Read first

`DESIGN.md` in this folder. It is the spec. Sections 5 to 9 and 16 matter most for this job. If the design is wrong or unclear somewhere, write down the problem and your choice in `DECISIONS.md`. Do not silently change the design.

## Scope

Build M0 and M1 from DESIGN.md section 16:

- **M0:** hub (HTTP API + SQLite), device join, `handloom link` with local unix socket, CLI verbs for messages and tasks, server-side scopes, task leases, audit log.
- **M1:** event long-poll, wake ladder (end-of-turn hook, terminal nudge through herdr and tmux, report-on-failure), nudge guards, and the Claude Code adapter (state hooks, Stop-hook delivery, instructions block).

Out of scope: other adapters, MCP server, headless resume, web board, escalations, notifiers, packaging. Leave clean seams for them, nothing more.

## Technical choices (already made)

- Go, single binary `handloom` with subcommands. Pure-Go SQLite driver (`modernc.org/sqlite`) so the binary stays static.
- Standard library HTTP. Keep dependencies few. Justify each one in `DECISIONS.md`.
- Go is not installed on GB10. Install it in user space (`~/.local/go`, no sudo).

## Environment

- **GB10** (this machine, `/home/user/projects/handloom`): Herdr 0.9.0, Claude Code, Codex, Pi, OMP, OpenCode installed. Herdr's own Claude Code state hook is at `~/.claude/hooks/herdr-agent-state.sh`. Read it to learn the hook events. Check Herdr's license before reusing any code.
- **Mantis** (`ssh mantis`, a VPS on the same Tailscale network): Claude Code and tmux, no Herdr. Use it as the second device for the M1 test, and as the hub host if convenient. Use absolute paths in anything run over SSH; non-interactive shells there lack `~/.local/bin`.
- **Approval prompts.** Test agents must run with a scoped allowlist or bypass mode. If they stop for approvals, the wake ladder sees `blocked` and M1 cannot pass. Before building, verify that Claude Code on Mantis works non-interactively (`ssh mantis 'claude -p "say ok"'`), whether it runs as root there, and whether bypass mode is refused as root. If it is, use a non-root user or an allowlist and record the choice in `DECISIONS.md`.
- Other agents and sessions are running on both machines. Do not touch any tmux session, Herdr pane, service or file you did not create. Run the hub on a free port (default 7420; check it is free first). Use a test project and test agent names prefixed `hltest-`.

## How to work

1. Read DESIGN.md and the Herdr Claude hook. Verify the Claude Code facts in the adapter table (Stop hook `decision: block`, session id, `-p --resume`) against the installed CLI and its docs before coding the adapter.
2. Build M0 with tests. Every scope rule in section 6 gets a test that proves the forbidden action is rejected.
3. Build M1. Write the e2e scenario as a script in `test/e2e/` so it can be rerun.
4. Commit in small steps to a local git repo in this folder. Do not create a GitHub repo and do not push anywhere. The owner decides the name and when it goes public.

## Definition of done

Both must pass, and you must show the evidence, not describe it:

- **M0:** the M0 check in DESIGN.md section 16 passes on GB10 plus Mantis. Paste the test output.
- **M1:** a lead Claude Code on GB10 and a worker Claude Code on Mantis finish a three-task job (task 3 depends on task 1) with no human input after the first brief. Provide the audit log excerpt showing each wake, claim, submit and accept, and the e2e script that reproduces it.

If M1 cannot pass because of a real limit in Claude Code (for example, a hook cannot do what the design assumes), stop, write the finding with evidence in `DECISIONS.md`, and report. Do not fake the scenario or weaken the check to make it pass.

## Report back

When done, or blocked, write `REPORT.md` with: what works, what does not, test output, decisions you made, and anything in DESIGN.md that should change. Keep it short and factual.
