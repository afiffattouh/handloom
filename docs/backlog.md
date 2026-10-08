# Backlog: things decided to do later

Notes kept so they are not lost. Each says why it matters and what is known.

## Profile creation is too technical

Done: the web form and `handloom profile new --adapt` fit a profile to its CLI instead of refusing it (denied commands a CLI cannot refuse become a standing rule in the agent's instructions, a shell the CLI always has is listed, a missing web tool is left out), and say what they did; the paths and denied commands sit under an optional "Limits" section. The form now asks "What should it do?" in plain words (look and report, research the web, write files, build and test; or choose the tools yourself) with a "never run risky commands" box, and `handloom profile make NAME --can ...` does the same from the command line; exact tools and limits are under "Fine tune".

## CLI parity

Done: everything the web UI does is also an API call and a command: `handloom prices`, `people`, `notifications`, `token new`, `screen <agent>`, `metrics`, and `hub create-owner` for the first owner without the web setup page. The web pages and the commands share the same rules (owner only, the same checks). Only the web's sign-in itself (password, cookie) stays web-only.

## Using Handloom from an agent app through MCP

Done over stdio: `handloom mcp --operator` for a person's own app, and `handloom mcp` with a pull agent for an app session that takes part (docs/mcp.md). Still open: a remote MCP endpoint on the hub for connector-style clients (needs OAuth), The token limited to the operator tools exists (`handloom token new --app`, the "Connect an AI app" page).

## Future plan

The planned work is tracked as GitHub issues: https://github.com/afiffattouh/handloom/issues (label `planned`):

- #1 Published binaries and a one-line install script
- #2 Console login with name and password
- #3 Get started checklist: console, AI app, and recovery docs
- #4 Remote MCP endpoint for connector-style clients
- #5 Container isolation (design: `docs/isolation.md`)
- #6 Schedules for recurring jobs
- #7 Knowledge librarian and a web view of proposed notes
- #8 Test phone push against a real ntfy topic
- #9 Prove a clean install on a fresh machine (Linux and macOS)
- #10 Forced merge conflict with a local-model lead
- #11 Terminal console: mouse wheel, a viewer token, light-theme check
- #12 Web UI: tables on a phone, and a review with real data
- #13 Teams: per-person alerts and isolation inside one hub
- #14 Profile creation: more help, and whether to add a test run
