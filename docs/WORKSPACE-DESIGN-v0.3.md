# Handloom workspace: design v0.3 (product direction)

Status: draft, 2026-10-06. This is a delta on `WORKSPACE-DESIGN-v0.2.md`. Anything not mentioned here is unchanged from v0.2. Where v0.2 and this file disagree, this file wins.


## 0. What the owner changed

1. Handloom is a **product for other people**, not one person's tool.
2. **Any CLI**: users choose which agent CLIs to use, including ones we did not write adapters for.
3. **Skill management and profile management** are first-class, picked up in the UI.
4. A **web UI** for settings and day-to-day use, including a **web inbox**. The TUI stays only if it can do the job well.
5. Deployable on **Dokploy as its own project**, and on any VPS.

These reverse five v0.2 decisions. They are listed in §9 with the reason each flips.

## 1. Product shape

**One self-hosted hub per team (or person), reached over HTTPS. Devices connect outbound.** Not multi-tenant SaaS. Several human accounts share one hub and one project in v1.

```
 Browser (primary UI)  ──HTTPS──▶  Hub container (Dokploy / any VPS)
 Phone push (ntfy/web push)          web UI · API · SQLite · library (profiles, skills, notes)
 TUI / CLI (secondary)               ▲ outbound HTTPS from each device
                                     │
                    link on laptop · workstation · VPS  →  agent CLIs in tmux/herdr panes
```

Surfaces, in order of importance:

| Surface | Job |
|---|---|
| **Web UI** | Everything: inbox, jobs, review, profiles, skills, library, devices and CLIs, settings, members |
| **Phone** | Push for needs-you and job-done, one-tap answers (opens web inbox) |
| **CLI** (`handloom …`) | Agents' own verbs; scripting; `handloom new` chat for terminal people |
| **TUI** (`handloom watch`) | Optional power view: Inbox and Job, quick actions (reply, pause, attach). **No settings, no profile or skill editing.** Built after the web UI, kept small, and dropped if nobody uses it |

The TUI cannot handle settings or editing well (forms, long prompts, diffs, file pickers). The decision is: web for configuration, TUI for watching.

## 2. Any CLI: the adapter contract

Adapters become data, not code, so users can add a CLI without us.

```yaml
# adapters/<kind>/adapter.yaml   (installable from a git URL or the UI)
kind: mycli
detect: [mycli]
launch: "mycli --model {model} --prompt-file {context_file}"   # argv template, never a shell string
instructions_file: AGENTS.md        # where profile prompt + protocol block is written
skills_dir: .mycli/skills           # or "inline" = skills are appended to the instructions file
mcp: { method: flag|config-file|none, ... }
tools: { portable: [read, edit, shell, web], map: {...} }   # which portable tools it can enforce
state:  { method: hook|plugin|pane-scrape|none }
wake:   { terminal: true, headless: "mycli resume {session_id} {prompt}" }
transcript: { path: "~/.mycli/sessions/{session_id}.jsonl", format: jsonl|none }
usage: none|transcript
```

**Capability levels**, shown as a badge on every CLI in the UI:

| Level | You get | Needs |
|---|---|---|
| **L0 any CLI** | Runs in a pane, receives its instructions file and `CONTEXT.md`, wakes by terminal nudge, state by pane scraping | only `launch` + `instructions_file` |
| **L1 state** | Reliable idle/working/blocked, end-of-turn delivery | hook or plugin |
| **L2 enforced** | Tool limits enforced by the CLI itself | per-launch tool flags or config |
| **L3 observable** | Live transcript feed, context usage, rollover by tokens | transcript source |

A profile declares what it requires; `check` refuses a profile on a CLI below that level instead of silently weakening it. Built-in adapters: claude (L3), codex (L2, transcript later), pi, omp, opencode (L1 to start). A "custom CLI" form in the UI creates an L0 adapter from three fields.

What stays out: API-only agents with no CLI. Possible later as a built-in runner, not v1.

## 3. Profiles (personas) and skills, managed in the UI

User-facing word: **Profile**. Internal and in files: persona. They are the same thing.

**Library** (hub-stored, versioned, editable in the web UI; optional git export/import for people who want files):

| Object | Fields | UI |
|---|---|---|
| **Profile** | name, purpose, CLI (or "any with L≥n"), model, prompt, skills, portable tool set, write scope, limits (minutes, turns, cost), memory read/write, confidential-capable (local runtime only) | create from preset or fork; form editor; **Try** button runs the 3-minute probe job and shows pass/fail; shows capability warnings per CLI; version history; "used by N jobs" |
| **Skill** | a SKILL.md folder (the existing standard), source (upload, git URL, or written in the editor), version | import, edit, attach to profiles, see which profiles and CLIs use it |
| **Preset** | shipped profiles (Coder, Researcher, Analyst, Reviewer, Writer) and skill packs | clone-to-edit only |

**Skills do not run inside Handloom.** Handloom stores, versions and *materializes* them: at spawn the adapter copies attached skills into the CLI's skill directory (`skills_dir`), or appends them to the instructions file when the CLI has no skill mechanism. That restores skill management (which v0.2 cut) without building a skill runtime.

Rules kept from v0.2: a job pins profile and skill **versions** at start; edits affect the next job only; untried profiles show a warning.

## 4. Web UI

Server-rendered Go templates with htmx and server-sent events. **No SPA, no JS build step.** A solo builder can ship and maintain it, it is one binary, and live updates (inbox, job feed) ride on SSE through Dokploy's proxy.

| Page | Contents |
|---|---|
| **Inbox** (home) | needs-you (escalations, budget, blocked), to-review, running, "since you left" digest; reply/approve inline |
| **Jobs** | list, filter, **New job** (chat or form → brief summary card → start) |
| **Job** | team and states, task graph, event timeline, live feed per run, brief and amend, hold/attach instructions, cost and caps |
| **Review** | deliverables, "done when" checks, diff or report, cost, "things learned" checkboxes, accept / request changes / discard |
| **Profiles**, **Skills** | the library above |
| **Library** | notes and decisions (search, view, edit, retire); the "things learned" history |
| **Devices & CLIs** | devices online, installed CLIs with L-badges, add device (shows the one-line `handloom link join …`), add custom CLI, device trust policy |
| **Settings** | members and roles, notifiers (ntfy, web push, webhook, later email/Slack), default budgets, confidential policy, backups, base URL, API tokens |

Auth: email-less accounts with password (argon2) plus session cookie, CSRF protection, login rate limits, owner created by a **first-run setup wizard**. Roles: **owner, member, viewer**. Optional OIDC later. Agents never use web sessions; they use per-run tokens (v0.2 §7).

All surfaces read the same `GET /v1/digest` and event stream, so web, TUI and push cannot disagree.

## 5. Deploying on Dokploy (and any VPS)

- **One container image**, `handloom hub`. Dokploy: create a project, add the image or the repo's `docker-compose.yml`, set a domain (Traefik issues TLS), attach one volume at `/data`. Same compose file works with plain `docker compose up` on any VPS.
- **Config by env**: `HANDLOOM_BASE_URL`, `HANDLOOM_DATA_DIR=/data`, `HANDLOOM_SECRET` (cookie/token signing), optional `HANDLOOM_NTFY_URL`, `HANDLOOM_ALLOW_CONFIDENTIAL`. No config file needed.
- **First run**: open the URL, the wizard creates the owner and prints the device join command. Sub-10-minute quickstart is a release criterion.
- **Health and proxy**: `/healthz`; SSE and long-poll endpoints send keep-alives and set `X-Accel-Buffering: no`. Verified against Traefik before release.
- **Storage**: SQLite (WAL) plus blobs under `/data`. Backups: Dokploy volume backup, or `handloom hub backup` (SQLite backup API plus blobs) to a path or S3-compatible target, with a restore test in CI. Optional Litestream later.
- **Scale limit, stated honestly**: one container, one SQLite writer. Right-sized for a team of up to ~10 humans and ~20 concurrent runs. Past that, the migration path is Postgres, which the store layer should keep possible (no SQLite-only SQL outside one package).
- **Ship**: multi-arch image, `docker-compose.yml`, and a Dokploy template, plus install script for links.

## 6. Confidential work on a public hub

The hub may now sit on a public VPS, so v0.2's "hub lives on GB10" no longer holds. Replace it with a deployment policy, set by the owner in Settings:

| Policy | Behaviour |
|---|---|
| **Confidential off** (default on a public deployment) | Jobs cannot be marked confidential |
| **Device-local confidential** | A confidential job is pinned to one device with local-runtime profiles. The hub stores only status, titles and timestamps; bodies, notes, transcripts and handoffs stay on that device. The web inbox shows "🔒 needs you" and relays the reply, but never the content. Viewing content requires the device online (proxied on demand) |
| **Hub-trusted confidential** | For private deployments (Tailscale/LAN): the hub stores confidential content and enforces `canRead` (v0.2 §7) |

Everything else from v0.2 §7 stands: per-run tokens, link-side allowlist, idempotent spawn, budgets enforced by the link, tainted memory.

## 7. Multi-user and trust

- Humans: owner, member, viewer. A job belongs to its creator; escalations go to the creator (or any owner). Members see the whole project's jobs unless the job is marked private to its creator (cheap to add, not v1).
- Devices belong to the human who added them. A job's runs execute on that creator's devices by default; placement across a teammate's devices needs that teammate's device to be marked shared.
- **Spawn trust, per device** (chosen when adding the device; shown in Devices & CLIs):
  - **Approve each profile hash** on the device (default for unattended or confidential devices).
  - **Trust this hub** (convenient for personal devices): the link launches any profile the hub publishes. State the consequence in the UI: hub compromise means code execution on that device. This was already true of the original design; now it is a visible choice.
- Launch is always argv-templated, under fixed roots, never a shell string from the hub.

## 8. Memory moves hub-side

The vault cannot be "markdown in a git repo you own" when other people use Handloom from a VPS. v0.3:

- **Notes are hub-stored documents**: markdown with frontmatter, versioned, with **FTS5 search from day one** (cheap in SQLite; replaces grep). Agents use `handloom mem add|search|show` (CLI and MCP). Humans use the Library page.
- Export and import as a folder of markdown (and optional sync to a git remote) so nothing is locked in, and hand editing is possible.
- Scopes stay two: this job / always. Confidential notes follow §6.
- Handoffs stay markdown, stored as job documents and carried in task notes.
- Unchanged: research sources as files plus manifest, a rolling `state.md` per recurring job, tainted-from-web flag, "things learned" review at accept.

## 9. v0.2 decisions that flip

| v0.2 | v0.3 | Reason |
|---|---|---|
| Skills cut | Skills managed (store, version, materialize) | Product requirement; cheap because the SKILL.md format already exists |
| Web board later, TUI first | Web UI is the primary surface; TUI later and small | Others will use it; settings and editing need forms; "away" use needs the web inbox |
| Memory and handoffs as git markdown, hub holds pointers | Hub-stored documents with FTS and markdown export | Hub is now remote; web UI must edit them |
| Hub on GB10 for confidential work | Deployment policy (§6) | Hub is now on arbitrary VPSs |
| Single-user assumptions | Members, roles, per-device trust | Product for others |
| Persona is a directory in a repo | Profile is a hub-stored object, with optional git export/import, still pinned by version | Edited in the UI; the persona-as-directory shape is still what gets materialized onto devices |

## 10. Revised milestones

| # | Delivers | e2e check | Kill / fallback |
|---|---|---|---|
| **A** | **Deployable hub + web inbox.** Image, compose file, Dokploy template, setup wizard, login and roles, `/v1/digest`, web Inbox with SSE, `handloom ask` escalations, ntfy and web push, devices page with join command | Deploy on Dokploy from scratch following the quickstart; add two devices; run the M2 scenario; the escalation arrives on a phone and the answer reaches the lead through the web inbox | Auth or SSE through Traefik unreliable ⇒ fix before anything else; nothing later is safe without it |
| **B** | Foundations: job = root task, job-scoped lead and names, per-run tokens, supervisor ticker, `canRead`, member scopes in the scope table | Scope tests for every rule as every role; kill a lead, a replacement resumes from the task graph | Tokens cannot be delivered safely ⇒ confidential disabled |
| **C** | **Adapter contract v2 + Profiles + Skills + spawn.** Data-driven adapters with L-badges, custom-CLI form, profile and skill library pages, Try button, materialization, spawn through `handloom run` in a link-made pane, claude and codex first, plus one L0 custom CLI | A profile with a tool deny list refuses the tool on claude and codex; a custom L0 CLI added from the UI runs a trivial job; a skill attached to a profile appears in the agent's skill dir | A CLI cannot enforce limits ⇒ it is capped at L1 and `check` says so |
| **D** | Jobs in the web UI: new-job chat/form, worktree per writer, link-side scope check and `verify`, Job and Review pages, hold/attach, amend | Two-worker one-dependency job reaches Review with no human input; out-of-scope write rejected | Lead mis-plans in 3 of 5 runs ⇒ human-written task file, keep spawn and worktrees as a static pipeline |
| **E** | Handoffs and relief | Kill a worker mid-task; relieved agent finishes from the packet | Fails 2 of 3 trials ⇒ fix template before any auto-rollover |
| **F** | Library (notes + FTS), "things learned" on accept, confidential policies (§6) | Job 2 uses a decision from job 1; confidential bodies never appear in hub storage under the device-local policy | Agents ignore the library in 3 jobs ⇒ strengthen the profile template; still ignored ⇒ drop it |
| **G** | `handloom watch` TUI (Inbox, Job) | Frame dump lists agents, tasks, events | Skip entirely if the web UI covers it |
| later | schedules (hub-owned), git sync of the library, OIDC, Postgres, API-only runner, per-job privacy, more adapters | one at a time on evidence | |

## 11. New risks from these changes

1. **Public attack surface.** Login, sessions, CSRF, join tokens and SSE are now internet-facing. Mitigation: secure defaults, rate limits, short-lived one-time join tokens, a threat-model pass before milestone A ships, and the `Trust this hub` choice shown plainly.
2. **Adapter sprawl.** Data-driven adapters shift the support burden to users' CLIs. Mitigation: L-levels, a conformance test (`handloom adapter test <kind>`), and L0 as the honest floor.
3. **Hub-stored config is a bigger target**, and a prompt-injected agent must not be able to edit profiles or skills. Only humans write the library; agents get read-only materialized copies by pinned version.
4. **Web UI scope creep.** Eight pages is a lot. Build A, then add pages only with the milestone that needs them.
5. **Unverified:** Dokploy template details and Traefik SSE behaviour, per-CLI skill directory conventions beyond Claude, Codex transcript format, and whether any non-Claude CLI exposes context usage.

## 12. Decisions and open questions

Decided (2026-10-06, owner):
- **Open source, self-host only.** No hosted service, so no multi-tenancy; one hub per team stays the model.

Open:
1. **Name.** "Weaver" is the candidate. Bare name is taken on npm and PyPI, the common domains are taken, and `weaver` is also an OpenTelemetry CLI, Service Weaver and Microsoft TaskWeaver (an agent framework). Decide before milestone A is public; keep the code name neutral until then.
2. **MCP.** Today `handloom mcp` (stdio) exposes the agent verbs. Undecided whether to also expose a remote MCP endpoint on the hub so an external agent (for example your own Claude Code session) can act as lead: create jobs, plan, spawn, read the inbox. Design rule until decided: the MCP server is a thin projection of the API, scoped by the same per-run token and role table, with lead-only tools (plan, spawn, accept, review, ask) gated server-side. Revisit after milestone B.
