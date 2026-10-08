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
- **Container isolation** (decided 2026-10-08: skip for now). Each agent CLI runs in a container that sees only its work folder. The design and the nine problems to solve are in `docs/isolation.md`; the seam (`Isolator` in `internal/link/isolation.go`) is already in place, and `handloom link run --isolation container` says it is not built. Build it when a machine is shared, the model or its tools are not trusted, or work touches something sensitive on a CLI that cannot refuse commands (OMP, Pi, Codex). Until then: use a dedicated machine or user for agents.
- **Schedules and the librarian** (decided 2026-10-08: future plan). The librarian is an agent that looks after a client's knowledge repository: it reads the pending `job/<id>` proposal branches (D68), removes duplicates, flags contradictions, keeps the folder structure consistent, and prepares one reviewed change for the person to approve; it never merges on its own. Schedules start it (and other recurring jobs such as a weekly report) without a click: by time ("every Friday"), by event ("when five proposals are waiting"), or both. Open questions: merge or only prepare; time, event or both. It also needs a web view of proposed notes for the review. Build it when a client's knowledge repository starts filling up.
- **A smoother first install** (decided 2026-10-08: future plan). A new person today does: run the hub container, open `/setup` with the one-time code, sign in, add a machine (`handloom link join`, `link install`, `doctor`), and optionally `handloom login` plus a token from Settings for the console. Not yet tested on a clean machine. In order:
  1. **Published binaries and a one-line install script.** There are no releases: a user without Go has to build from source or run `scripts/setup.sh` from a machine that has Go. Build for Linux and macOS (amd64 and arm64), attach checksums, and add an install script. This is the biggest gap.
  2. **Name-and-password console login.** `handloom login` asks for a name and password and the hub issues the token itself (a new endpoint with the same step-up and rate limits as the web), instead of copying a token from Settings.
  3. **Checklist and recovery docs.** Add "sign in on your terminal" and "connect an AI app" to the inbox's Get started checklist; document the recovery commands (`hub reset-password`, `reset-human-token`, `reset-admin-token`, `create-owner`), which only work on the hub's own machine, for people who only have the web UI.

