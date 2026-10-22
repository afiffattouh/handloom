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

## Add-ons (D111)

Released in v0.1.14 (2026-10-22), phases 1 to 3 of `docs/direction/addons.md`. Built: the add-on host, the tool gateway with the audit log, the Add-ons page, grants on profiles and the job notice, `docs/addons.md`, and the examples `echo` and `web-fetch`. Still to do, in this order:

- Run `web-fetch` through a real agent program on a real job, and fix what shows (the Codex MCP token handoff, for one, was not tried; the other kinds use the command line).
- A revoke that reaches helpers that are already running (the link would ask the hub for revocations in its ladder).
- Set an add-on to Not running in the report when its service crashes (today only the call's result says so).
- A block list for `web-fetch` (addresses on this computer or a private network).
- Waiting on isolation (a container or a separate user for the link, see `docs/isolation.md`): add-ons that keep secrets, and shared services with their own credentials and TLS.
- Waiting on a project that fails in a way they would catch: installable kits, adapter packs, a catalogue, installing from a web address, a rules table from task label to profile (a router).
- Not designed: teams.

## Updating computers (D112)

Built and released in v0.1.14: version on the Computers page, the Update button, `handloom device update`, the link's own checked download and restart. Still to do:

- A computer on v0.1.13 or older has no updater: run `install.sh` on it once. After that every release can be installed from the page.
- Prove it on a real computer: Linux with systemd, a background start, and macOS with launchd (only a fake release host and a stand-in program were tested), including the case of a running job.
- Update several computers at once ("Update all"), and a way to cancel a waiting request.
- Check the Sigstore signature of `checksums.txt` as `install.sh` does (needs a verifier in the link or a call to `cosign` when present).
- Tell the hub which release it is itself, and show it, so a person can see hub and computers side by side.
- Tool tokens live in the link's memory, so an update (or any restart) during a job leaves a running helper with "unknown token" on its next tool call until it is started again; keep the tokens across a restart.

## Projects (D104)

Stage one is built: a project is a name and a shared remote; attach or create it, and any machine gets its own copy under its own login. Stage two is built (D105): a job of a project starts from a fast-forwarded base (fresh, stale or diverged, recorded on the job) and a published pull request's status is followed. Planned, not built:

- Left from stage two: pull request checks (the CI result) are not shown, only state, review and mergeability; the status is on the job, not yet on the project page; let a person say which branch is the base when a repository has no usable default.
- Stage three: warn across machines. "The base moved" while a job runs (the remote's main has new commits), and shared-file warnings between open jobs of one project on different machines (compare changed files, not repository paths). Needs a periodic fetch by the link and a small report of changed paths per job, never file contents.
- Left undone from stage one: removing a project or forgetting one machine's copy; a different folder than `<link folder>/projects/<name>`; GitLab and other hosts for *creating* repositories (attaching and cloning work with any git host); a project page that refreshes by itself while a machine works.

## Hosted projects (D106)

Built: a project the hub holds itself (a bare repository per project under the data folder, served over git's HTTP protocol to machines with their device credential), a hook that lets machines add branches named `handloom/...` and nothing else, and a merge by a person (`handloom job merge`, the Merge button). Planned, not built:

- Mirroring a hosted project to GitHub or another host from the hub (today a team that outgrows the hub clones it once and pushes it elsewhere by hand, then attaches the new address as a new project).
- Deleting a hosted project, and shrinking its repository (`git gc` runs on git's own schedule only).
- Access that differs between people or machines of one hub: every machine of the hub can reach every hosted project.
- Windows machines (the credential helper is a shell command written for sh).
- A merge that asks the author to resolve conflicts, or a merge queue; a merge preview (the files a job changes) on the job page.
- Hub backups that include the git folder in one command (today they are separate, see deploy).

## Working on one project together (D103)

Left undone: pull a teammate's published branch into a clone from Handloom; run one job's agents on several machines; warn on overlap by changed files rather than by repository; an MCP tool for publishing (kept out on purpose: it is an approval-class action).

## Where the build stands (2026-10-20)

The kit, solo asks, drafted tests, checks added or updated from the hub, and the rebuilt job page are live. The ordered list of what comes next, with the reason for each, is under "Next" in `docs/direction/modular-design.md`. New since the last note here: who accepted a task is shown on the job page; deleting projects, agents and jobs is still not possible.
