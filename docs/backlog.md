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

Done: everything the web UI does is also an API call and a command: `handloom prices`, `people`, `notifications`, `token new`, `screen <agent>`, `metrics`, and `hub create-owner` for the first owner without the web setup page. The web pages and the commands share the same rules (owner only, the same checks). Only the web's sign-in itself (password, cookie) stays web-only.

## Using Handloom from an agent app through MCP

Done over stdio: `handloom mcp --operator` for a person's own app, and `handloom mcp` with a pull agent for an app session that takes part (docs/mcp.md). Still open: a remote MCP endpoint on the hub for connector-style clients (needs OAuth), and a token limited to the operator tools instead of the person's full token.
