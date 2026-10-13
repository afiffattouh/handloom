# Using Handloom from an AI app (MCP)

Your own AI app can use Handloom in three ways. The first needs nothing installed: you paste one address into Claude and sign in. The other two run the `handloom` program (`handloom mcp`, over stdio) on the computer where the app runs, so they work in any client that can start a local MCP server (Claude Code, Claude Desktop, Codex, and others).

## Connect from Claude with nothing to install

Handloom is also a remote MCP connector. You add it to Claude by its address and sign in with your Handloom account. No program, no config file, no token to copy.

The address is your hub's public address followed by `/mcp`, for example `https://handloom.example.com/mcp`. The **Connect an AI app** page shows it, filled in, at the top.

- **Claude on the web, and Claude Desktop:** Settings, Connectors, Add custom connector. Paste the address and save. Press Connect, sign in to Handloom, read what the app asks for, enter your password and press Allow.
- **Claude Code:** `claude mcp add --transport http handloom https://handloom.example.com/mcp`, then run `/mcp` inside Claude Code, choose handloom and sign in.

What the app can do is the same as for the app token below: look at jobs, tasks, agents, the inbox and the numbers; start jobs; message agents. It cannot accept work, answer an agent's question, close a job or change settings, and the hub refuses those for it however Claude is set up. The tools are the same ones as `handloom mcp --operator` offers, without the approvals (there is no way to turn those on over the connector).

To disconnect, press Disconnect next to the app under **Connected apps** on the Connect page. Every token that connection ever got stops working at once. An access token lasts an hour and the app refreshes it by itself; the refresh token lasts 60 days from its last use.

What the hub needs:

- `HANDLOOM_BASE_URL` set to the hub's public https address. Without it the connector is off. It is the issuer in the sign-in documents, so it must be the address Claude uses.
- The hub reachable from the internet. Claude on the web connects from Anthropic's servers, not from your computer, so a hub only on a private network (Tailscale, a home network) cannot be added there. Claude Code connects from your own computer, so it also works with a hub you can reach from there. Claude Desktop's custom connectors may also go through Anthropic's servers (this has not been checked), so keep the hub reachable from the internet to be safe.
- A reverse proxy in front of it passes the paths `/mcp`, `/oauth/` and `/.well-known/` like any other; nothing is streamed and no special settings are needed (`docker-compose.caddy.yml` already does this).
- `HANDLOOM_OAUTH_REDIRECTS` (optional): a comma-separated list of extra https addresses an app may be sent back to after you allow it. Claude's own address (`https://claude.ai/api/mcp/auth_callback`) and the `http://localhost` / `http://127.0.0.1` addresses Claude Code uses are built in; add another product's callback here, for example `https://claude.com/api/mcp/auth_callback`.

How it is built, for people who check: the hub is its own OAuth 2.1 authorization server. The app finds it from the 401 on `/mcp` (`WWW-Authenticate: Bearer resource_metadata=...`), reads `/.well-known/oauth-protected-resource` and `/.well-known/oauth-authorization-server`, registers itself at `/oauth/register` (a public client, no secret), sends you to `/oauth/authorize` (PKCE with S256 is required), and trades the code at `/oauth/token`. Refresh tokens rotate: using an old one again ends the whole connection. The transport is streamable HTTP with plain JSON answers (protocol versions 2026-07-28, 2025-11-25, 2025-06-18 and 2025-03-26); there is no event stream, so a GET on `/mcp` is answered with 405. See D109 in DECISIONS.md for what is deliberately not done.

## Advanced: run a program on your computer

### You drive Handloom from your own AI app: `handloom mcp --operator`

Your app can look at the picture and start work for you: what needs you, the jobs and their tasks, agents, profiles and the starter library, the command center's numbers, an agent's terminal, and it can start a job or message an agent. Everything runs as you, with your token, so the hub's rules and audit log apply.

**It cannot approve.** Accepting work, answering an agent's question and closing a job are not tools unless you turn them on, because an app set to auto-approve would otherwise approve its own agents' work. When something needs your decision, the app tells you what it is and where to decide it (the Inbox). Read tools are marked read-only so a client may let them through; the tools that start things are marked as changing data, so a client can ask you first.

Set it up. The easiest way is the **Connect an AI app** page in the web UI: it makes a token for the app and shows these settings with your hub address and the token already filled in. From the command line, `handloom token new --app` makes the token.

The app token is not your own token. The hub lets it do only what the tools above do (read, start a job, message an agent) and refuses everything else, however the app is configured: accepting, answering, closing, settings. Making a new one replaces the old; `handloom token remove-app` (or the page) disconnects the app.

```
# Claude Code
claude mcp add handloom --env HANDLOOM_HUB=https://your-hub --env HANDLOOM_TOKEN=hvo_... -- handloom mcp --operator
```

```json
// Claude Desktop: claude_desktop_config.json
{ "mcpServers": { "handloom": {
    "command": "handloom", "args": ["mcp", "--operator"],
    "env": { "HANDLOOM_HUB": "https://your-hub", "HANDLOOM_TOKEN": "hvo_..." } } } }
```

```toml
# Codex: ~/.codex/config.toml
[mcp_servers.handloom]
command = "handloom"
args = ["mcp", "--operator"]
env = { HANDLOOM_HUB = "https://your-hub", HANDLOOM_TOKEN = "hvo_..." }
```

`handloom` has to be installed on the computer your app runs on (it is one binary: see [add a machine](add-a-machine.md) for the install steps). The operator tools do not need the link. Claude Desktop does not use your shell's PATH, so a bare `"command": "handloom"` fails with "no such file" even when `handloom` works in your terminal: put the full path in the command field, the output of `command -v handloom` (for example `/opt/homebrew/bin/handloom`). If you do not want to install anything, use the connector above instead.

If you really want the app to approve (accept, send back, answer, close), give it your own token (`handloom token new`, not `--app`) and add `--allow-approvals`: an app token is refused those calls by the hub. Each of those tools is then marked destructive and described as an approval, so a client asks every time. Do not combine it with auto-approve.

Tools: `handloom_digest`, `handloom_jobs`, `handloom_job`, `handloom_tasks`, `handloom_task`, `handloom_agents`, `handloom_questions`, `handloom_profiles`, `handloom_starters`, `handloom_metrics`, `handloom_screen`, `handloom_job_new`, `handloom_send`; with `--allow-approvals` also `handloom_task_accept`, `handloom_task_reject`, `handloom_answer`, `handloom_job_close`.

### An app session takes part as an agent: `handloom mcp`

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
