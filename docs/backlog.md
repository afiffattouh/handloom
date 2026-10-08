# Backlog: things decided to do later

Notes kept so they are not lost. Each says why it matters and what is known.

## Profile creation is too technical

Done: the web form and `handloom profile new --adapt` fit a profile to its CLI instead of refusing it (denied commands a CLI cannot refuse become a standing rule in the agent's instructions, a shell the CLI always has is listed, a missing web tool is left out), and say what they did; the paths and denied commands sit under an optional "Limits" section. The form now asks "What should it do?" in plain words (look and report, research the web, write files, build and test; or choose the tools yourself) with a "never run risky commands" box, and `handloom profile make NAME --can ...` does the same from the command line; exact tools and limits are under "Fine tune".

## CLI parity

Done: everything the web UI does is also an API call and a command: `handloom prices`, `people`, `notifications`, `token new`, `screen <agent>`, `metrics`, and `hub create-owner` for the first owner without the web setup page. The web pages and the commands share the same rules (owner only, the same checks). Only the web's sign-in itself (password, cookie) stays web-only.

## Using Handloom from an agent app through MCP

Done over stdio: `handloom mcp --operator` for a person's own app, and `handloom mcp` with a pull agent for an app session that takes part (docs/mcp.md). Still open: a remote MCP endpoint on the hub for connector-style clients (needs OAuth), The token limited to the operator tools exists (`handloom token new --app`, the "Connect an AI app" page).

## Future plan

- **Remote MCP endpoint on the hub** (decided 2026-10-08: skip for now). Lets connector-style clients (the Claude web or phone app, "add a custom connector by URL") use Handloom without a local `handloom` program. Start with the operator tools served over HTTP with the app token (`hvo_`) as a bearer header, for clients that accept one; OAuth (consent page, token issue and refresh, client registration, revocation) second. Build it only when someone needs Handloom from the web or phone app. The local `handloom mcp --operator` and the "Connect an AI app" page cover Claude Code, Claude Desktop and Codex.

