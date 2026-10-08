# Backlog: things decided to do later

Notes kept so they are not lost. Each says why it matters and what is known.

## Profile creation is too technical

Done: the web form and `handloom profile new --adapt` fit a profile to its CLI instead of refusing it (denied commands a CLI cannot refuse become a standing rule in the agent's instructions, a shell the CLI always has is listed, a missing web tool is left out), and say what they did; the paths and denied commands sit under an optional "Limits" section. Still open: plain-language intents ("a researcher that never changes files") that derive the tools, paths and limits; a form that asks only what the chosen starter needs.

## CLI parity

Done: everything the web UI does is also an API call and a command: `handloom prices`, `people`, `notifications`, `token new`, `screen <agent>`, `metrics`, and `hub create-owner` for the first owner without the web setup page. The web pages and the commands share the same rules (owner only, the same checks). Only the web's sign-in itself (password, cookie) stays web-only.

## Using Handloom from an agent app through MCP

Done over stdio: `handloom mcp --operator` for a person's own app, and `handloom mcp` with a pull agent for an app session that takes part (docs/mcp.md). Still open: a remote MCP endpoint on the hub for connector-style clients (needs OAuth), and a token limited to the operator tools instead of the person's full token.
