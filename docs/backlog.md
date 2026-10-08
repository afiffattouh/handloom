# Backlog: things decided to do later

Notes kept so they are not lost. Each says why it matters and what is known.

## Profile creation is too technical

Raised by the owner after adding local profiles. Allowed paths, banned commands and "refused because the CLI cannot enforce this" are engineer words, and a refusal for OMP or Pi when a starter carries banned commands is a dead end for someone who just wants "a coder on my local model".

Known directions, not decided:
- Ask what the agent is for and what it may touch, in plain words, and derive tools, paths and denied commands from the answers.
- A profile has intentions ("must not delete or publish anything"); each CLI enforces what it can and the profile says so, instead of refusing. Today `--adapt` does this for starters only.
- Presets such as "careful (read only)", "builder (can change files)", "researcher (can browse)" as the first choice, with the detail folded away.
- Hide allowed paths unless the job is a code job; hide denied commands for CLIs that cannot enforce them.
- Never leave a person at a refusal: every refusal offers the one action that resolves it.
Already done: a guided form with quick settings and "Check before saving", the starter library, "add it without what that CLI cannot do".

## CLI parity

Everything an agent or a job does is in the CLI. These are web-only or missing a command: model prices (also no API), people (invite links, roles, listing), notification settings, creating your personal API token, creating the owner at first start, the terminal view, a command for `GET /v1/metrics`. Closing them means an API endpoint where there is none and a verb for each.

## Using Handloom from an agent app through MCP

Today `handloom mcp` (stdio) gives an agent the agent verbs as tools (used with Codex, whose sandbox cannot reach the link). Wanted: someone using the Claude app, Codex or another MCP client can drive Handloom, and an app session can take part as an agent.

- An operator MCP (read status, start jobs, list profiles and starters, read the inbox) as a mode of `handloom mcp`, over stdio first (works in Claude Code, Claude Desktop and Codex), then HTTP on the hub for clients that need a remote connector (which needs OAuth).
- Approvals stay human. Accepting work, answering a question and closing a job must not be tools an AI can call with the owner's token and auto-approve; they stay outside the operator MCP by default, or are marked so the client always asks.
- An app session as an agent: it registers through the link with no terminal to wake, and pulls its mail by calling `handloom_inbox` when the person tells it to. No wake ladder for it.
