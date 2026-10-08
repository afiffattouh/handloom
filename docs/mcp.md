# Using Handloom from an AI app (MCP)

Handloom speaks MCP in two ways. Both are one command, `handloom mcp`, over stdio, so they work in any client that can start a local MCP server (Claude Code, Claude Desktop, Codex, and others).

## 1. You drive Handloom from your own AI app: `handloom mcp --operator`

Your app can look at the picture and start work for you: what needs you, the jobs and their tasks, agents, profiles and the starter library, the command center's numbers, an agent's terminal, and it can start a job or message an agent. Everything runs as you, with your token, so the hub's rules and audit log apply.

**It cannot approve.** Accepting work, answering an agent's question and closing a job are not tools unless you turn them on, because an app set to auto-approve would otherwise approve its own agents' work. When something needs your decision, the app tells you what it is and where to decide it (the Inbox). Read tools are marked read-only so a client may let them through; the tools that start things are marked as changing data, so a client can ask you first.

Set it up (a personal API token: `handloom token new`, or Settings in the web UI):

```
# Claude Code
claude mcp add handloom --env HANDLOOM_HUB=https://your-hub --env HANDLOOM_TOKEN=hvh_... -- handloom mcp --operator
```

```json
// Claude Desktop: claude_desktop_config.json
{ "mcpServers": { "handloom": {
    "command": "handloom", "args": ["mcp", "--operator"],
    "env": { "HANDLOOM_HUB": "https://your-hub", "HANDLOOM_TOKEN": "hvh_..." } } } }
```

```toml
# Codex: ~/.codex/config.toml
[mcp_servers.handloom]
command = "handloom"
args = ["mcp", "--operator"]
env = { HANDLOOM_HUB = "https://your-hub", HANDLOOM_TOKEN = "hvh_..." }
```

`handloom` has to be on the machine your app runs on (it is one binary). The operator tools do not need the link.

If you really want the app to approve (accept, send back, answer, close), add `--allow-approvals`. Each of those tools is then marked destructive and described as an approval, so a client asks every time. Do not combine it with auto-approve.

Tools: `handloom_digest`, `handloom_jobs`, `handloom_job`, `handloom_tasks`, `handloom_task`, `handloom_agents`, `handloom_questions`, `handloom_profiles`, `handloom_starters`, `handloom_metrics`, `handloom_screen`, `handloom_job_new`, `handloom_send`; with `--allow-approvals` also `handloom_task_accept`, `handloom_task_reject`, `handloom_answer`, `handloom_job_close`.

## 2. An app session takes part as an agent: `handloom mcp`

A Claude or Codex app session on a machine can be an agent in a job: it receives tasks and messages and reports back. It has no terminal for Handloom to type into, so it is registered as a **pull** agent: Handloom never nudges it and never reports it unreachable; it reads its mail when you tell it to ("check your handloom inbox and work on what is assigned to you").

The machine needs the link (it holds the device credential), joined as usual. Then:

```
handloom register claude-app --kind app --wake-target pull
```

and give the app an MCP server for it, with the agent's name in the environment:

```
claude mcp add handloom --env HANDLOOM_AGENT=claude-app -- handloom mcp
```

(For Claude Desktop and Codex, the same `command`, `args = ["mcp"]` and `env = { HANDLOOM_AGENT = "claude-app" }` as above.) The agent's tools are the usual ones: inbox, tasks, submit, send. Its work is not checked until it submits, and a repository job's worktree and scope check apply only to agents Handloom started; an app agent works wherever you point it.

## What this is not

It is not a way to join a machine without the link: the link holds the device credential, starts agents, runs the checks and merges. MCP lets an app take part and lets you drive from the app.
