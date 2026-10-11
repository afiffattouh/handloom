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

- #1 Published binaries and a one-line install script (done, v0.1.0)
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

Context that survives a change of device, agent or person (tracking issue #20):

- #15 A handoff note for every task, so any agent on any machine can pick it up (done, v0.1.0)
- #16 Durability tests: crash, late submit, stuck agent, restore, upgrade in flight
- #17 Find and reuse knowledge: search across a client's notes and past jobs
- #18 Let a finished job teach: reviewed lessons become notes and skills
- #19 Join a machine in one step (done, v0.1.1)

## Projects (D104)

Stage one is built: a project is a name and a shared remote; attach or create it, and any machine gets its own copy under its own login. Stage two is built (D105): a job of a project starts from a fast-forwarded base (fresh, stale or diverged, recorded on the job) and a published pull request's status is followed. Planned, not built:

- Left from stage two: pull request checks (the CI result) are not shown, only state, review and mergeability; the status is on the job, not yet on the project page; let a person say which branch is the base when a repository has no usable default.
- Stage three: warn across machines. "The base moved" while a job runs (the remote's main has new commits), and shared-file warnings between open jobs of one project on different machines (compare changed files, not repository paths). Needs a periodic fetch by the link and a small report of changed paths per job, never file contents.
- Left undone from stage one: removing a project or forgetting one machine's copy; a different folder than `<link folder>/projects/<name>`; GitLab and other hosts for *creating* repositories (attaching and cloning work with any git host); a project page that refreshes by itself while a machine works.

## Working on one project together (D103)

Left undone: pull a teammate's published branch into a clone from Handloom; run one job's agents on several machines; warn on overlap by changed files rather than by repository; an MCP tool for publishing (kept out on purpose: it is an approval-class action).
