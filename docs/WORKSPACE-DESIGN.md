# Handloom workspace: design sketch

Status: draft v0.1, 2026-10-06. Builds on DESIGN.md (coordination layer, M0–M2 done). Nothing here is built yet.

> **Naming (2026-10-06):** the product is now called **Handloom**. Until the code is renamed, the CLI binary and wire identifiers are still `handloom` (commands, `handloom/1`, the `Handloom-Agent` header, `.handloom/`, `handloom-mcp`); read those as placeholders for Handloom's. The short command is **`hl`** (owner decision, 2026-10-06; `handloom` is the long form). Domain registration is deferred. `DESIGN.md`, `README.md`, `DECISIONS.md` and `REPORT.md` describe the shipped M0-M2 code and still say handloom.

## 1. What changes

Today the tool (then called handloom) is a **coordination plane**: agents that already exist message each other, share a task board and get woken. You start the agents yourself.

The workspace turns it into a **work plane**: you describe a job, Handloom launches the right agents with the right profile, tools, rules and memory, you watch them work, and when one agent ends or runs out of context the next one picks up from a structured handoff.

```
            you ── brief ──▶ JOB ──▶ orchestrator ──▶ tasks ──▶ workers
                              │           │               │
                              │           └── handoffs ───┘
                              ▼
   PERSONAS + SKILLS (files)   MEMORY (graph over notes)   FILES (worktrees, artifacts)
                              ▲
                  handloom watch (TUI) / web board  ◀── event stream
```

Principle kept from v1: **dumb hub, smart agents.** The hub stores, indexes, enforces scopes and delivers. Anything that needs judgement (planning, summarising, consolidating memory) is done by a persona (orchestrator, scribe), never by server code.

Principle added: **files are config, the database is state, the vault is memory.** Personas, skills and briefs are plain files in a git repo you own. Runtime state lives in the hub. Memory is markdown with an index.

## 2. Concepts

| Concept | One line | Stored as |
|---|---|---|
| **Workspace** | A directory (`~/handloom-ws` or `.handloom/` in a repo) holding your personas, skills, templates, rules, vault | files, git |
| **Persona** | A reusable agent profile: role, prompt, skills, tool access, limits, output contract | `personas/<name>.md` |
| **Skill** | A capability bundle: instructions plus optional tools/MCP servers. SKILL.md format, so existing skills work | `skills/<name>/SKILL.md` |
| **Job** | One unit of work: brief, team, task graph, budget, expected output | `jobs/<id>/` plus hub rows |
| **Session** | One running instance of a persona on a job: agent CLI, session id, worktree, transcript | hub row plus transcript file |
| **Handoff** | Structured packet that moves work between sessions | `jobs/<id>/handoffs/<n>.md` plus hub row |
| **Memory** | Durable notes and a typed link graph, scoped job → project → persona → global | `vault/*.md` plus SQLite index |
| **Schedule** | A cron that instantiates a job template | hub row |

An **orchestrator** is a persona with `role: orchestrator`. Every job has exactly one. It is the existing `lead` role, scoped per job instead of per project.

## 3. Personas

```yaml
# personas/researcher.md
---
name: researcher
role: worker                    # orchestrator | worker | reviewer | scribe
runtime: { kind: claude, model: sonnet }   # or codex, pi, omp, opencode; or "local:qwen3.8-27b"
skills: [web-research, source-check]
tools:
  allow: [Read, Grep, WebSearch, WebFetch, "mcp:handloom"]
  deny:  [Bash(rm *), Write(src/**)]
files:
  read:  ["**"]
  write: ["jobs/{job}/out/research/**"]
limits: { max_minutes: 45, max_cost_usd: 3, max_context_pct: 70 }
memory: { read: [job, project], write: [job], sensitivity: internal }
output: { type: markdown, path: "out/research/{task}.md", must_cite_sources: true }
rules:
  - Never state a fact without a source link in the note.
  - If two sources disagree, report both.
---
You are a careful research analyst. ...   # persona prompt body
```

**Compile step.** `handloom persona compile <name> --runtime claude` turns the file into what that agent CLI understands: an instructions block (CLAUDE.md / AGENTS.md), a permission allowlist (`settings.json` / config), an MCP server list, env vars. The adapter owns this translation, as it already owns hooks.

**Honest enforcement levels.** Each rule is tagged by how it is enforced, and `handloom persona check` prints the table:

| Level | Examples | Strength |
|---|---|---|
| Hard (CLI sandbox) | tool allow/deny, permission mode | as strong as the agent CLI |
| Hard (handloom) | handloom verbs by role, who can spawn, who can accept | server scopes |
| Hard (post-check) | file write scope: diff is checked at `task submit`; out-of-scope change rejects the submit | reliable, after the fact |
| Soft (prompt) | style rules, "never do X" | best effort |

Do not promise more than this. Use worktrees (§7) so out-of-scope writes are cheap to discard.

## 4. Jobs

A job starts from a **brief**, a file with a fixed shape:

```yaml
# jobs/0042/brief.md
---
title: Competitor pricing scan
type: research                  # technical | research | business | cron | custom
orchestrator: lead-research
team: [researcher, analyst, reviewer]
inputs:   [vault:clients/acme, file:inputs/competitors.csv]
constraints: [no paid APIs, public sources only]
deliverables:
  - { path: out/report.md, accept: "covers all 12 competitors, each claim sourced" }
budget:   { cost_usd: 15, hours: 3 }
confidentiality: internal       # internal | client-confidential (local-model personas only)
---
Free-text goal and context.
```

**Templates.** `handloom job new --template research` pre-fills team, rules and deliverable shape. Ship four: technical, research, business, scheduled. They are just briefs with blanks.

**Lifecycle.** `draft → planning → running → review → done | failed | cancelled`.

1. You write or edit the brief. `handloom job start` validates it (an orchestrator exists, personas resolve, budget set).
2. The link launches the orchestrator session with the brief. It plans: creates tasks (existing task model, `depends_on`, evidence) and assigns personas.
3. For each assigned task the orchestrator calls `handloom spawn <persona> --task <id>`. The link starts the agent in a pane (herdr/tmux driver) with the compiled persona and a context pack (§6).
4. Workers claim, work, submit with evidence. Orchestrator accepts or rejects. Existing lifecycle, unchanged.
5. At the end the orchestrator checks the brief's deliverables, the scribe consolidates memory (§5), and the job moves to `review`. You accept it. Only humans close a job.

Spawn limits: workers cannot spawn in v1; the orchestrator is capped at N concurrent sessions and a depth of 1. Budget exhaustion pauses the job and escalates to you.

## 5. Memory

**Recommendation: markdown notes as source of truth, a property graph in SQLite as the index, retrieval by full-text first, embeddings later. Not a graph database.**

Why a graph at all: three things need links, not just search.
1. **Provenance.** Every fact points to the session, task and evidence that produced it.
2. **Supersession.** "The client changed the deadline" must retire the old fact without deleting it (`supersedes` edge, validity interval).
3. **Cross-job entities.** Client, repo, person, product appear across jobs; a link makes "everything about Acme" one traversal.

Why not more: graph databases and LLM entity extraction on every write add cost, errors and a service to run. Typed links written by agents plus FTS5 cover the need at your scale.

```
nodes:  note(id, scope, type, title, body_path, sensitivity, confidence,
             valid_from, valid_to, created_by_session, status)   -- status: staged|active|retired
        type: fact | decision | entity | episode | procedure | artifact
edges:  (src, kind, dst)    kind: about | derived_from | supersedes | decided_in
                                  | produced_by | blocks | relates_to
index:  FTS5 over title+body; optional vector column (sqlite-vec) later
```

**Scopes.** `job` (working notes, dies with the job unless promoted) → `project` (durable facts about a client/repo) → `persona` (what this role learned) → `global` (about you). A persona's `memory.read/write` lists the scopes it may touch.

**Write path (this is the part most designs get wrong).**
- Agents *propose*: `handloom mem add --scope job --type decision --about entity:acme --source task:7 "..."`. It lands as `staged`.
- Staged notes are visible to the job but not promoted.
- At job end the **scribe** persona consolidates: merges duplicates, adds `supersedes` edges, writes one `episode` note summarising the job, proposes promotions to `project`/`global`.
- Promotion to `global` and any deletion goes to your review queue. Retire, never delete silently.

**Read path.** Pull, not push. Agents call `handloom mem search "..." --scope project` or `handloom mem node <id> --depth 1`. The only automatic injection is the context pack (§6), which is budgeted.

**Memory types map to cognition:** episodes (what happened), facts/decisions/entities (what is true), procedures (how to do it; these can graduate into skills).

**Confidentiality.** Every note carries `sensitivity`. A client-confidential note is only retrievable by sessions whose runtime is a local model. This is enforced in the hub's search, not by prompt. It matches your rule that client material never reaches a cloud model.

## 6. Context management

Goal: each session starts with the smallest context that lets it work, and can always get more.

**Context pack**, built by the hub at spawn from a recipe, fixed token budget per persona:

| Slot | Content | Rule |
|---|---|---|
| 1 | Persona prompt + rules | always, verbatim |
| 2 | Brief (compact form) | always |
| 3 | Task spec + acceptance criteria | always |
| 4 | Incoming handoff packet | if any |
| 5 | Retrieved memory | top-k by FTS + 1-hop graph from the task's entities, within budget |
| 6 | Pointers | file paths, node ids, "run `handloom mem search`" |

Rules: **pointers over payloads** (a path, not a pasted file); the pack is **content-hashed and stored**, so `handloom watch` can show exactly what each session was given; it is a file the agent reads (`handloom context`), not text typed into a terminal (keeps the existing "wake with a nudge" principle).

**Context rollover.** The adapter reports context usage where the CLI exposes it. At `max_context_pct` the link asks the session to write a **self-handoff** and end; the next session of the same persona starts from it. This is the durable-session mechanism: a job outlives any one context window.

## 7. File system

```
jobs/0042/
  brief.md
  tasks/            snapshot of task graph (read-only export)
  handoffs/         0001-lead-to-researcher.md ...
  out/              deliverables; each file registered in manifest.json
  sessions/<id>/    transcript.jsonl, context-pack.md, worktree/ (if repo job)
  memory/           job-scope notes
```

- **One git worktree per writing session** on a job branch. The orchestrator merges. No two sessions share a working directory.
- **File leases** (open question 4 in DESIGN.md): advisory lock on paths a task declares it will write. Workers see conflicts at claim time. Cheap, and needed once several writers run in parallel.
- **Artifact manifest**: path, sha256, producing session/task, time. `handloom task submit --evidence file:<path>` checks the file exists in the manifest.
- **Scope check at submit**: diff against the persona's `files.write` globs. Violations reject the submit with the offending paths.

## 8. Handoffs

Handoff is a typed packet, not a chat message. The hub validates the shape; the receiver reads it with `handloom handoff show`.

```yaml
id: 0007
kind: delegate            # delegate | return | relief | escalate | fork
from: { persona: lead-research, session: s3 }
to:   { persona: analyst }
task: 12
done:        [Collected 9 of 12 competitor price pages (notes n41–n49)]
not_done:    [Competitors 10–12 behind login]
decisions:   [{ what: Use list price not street price, why: client asked, node: n50 }]
open_questions: [Is bundled pricing in scope?]
state:       { branch: job/0042, commit: a1b2c3, files: [out/data.csv] }
memory_refs: [n41, n50, entity:acme]
next_steps:  [Normalise currency, then compare tiers]
acceptance:  Table with 12 rows, each cell sourced
```

| Kind | When | Created by |
|---|---|---|
| delegate | orchestrator gives a task to a worker | orchestrator |
| return | worker submits; `task submit` is a return handoff (evidence + note become the packet) | worker |
| relief | context or time limit; same persona, fresh session | the ending session |
| escalate | blocked; goes to orchestrator, then to you | worker / orchestrator |
| fork | split one task into parallel sessions | orchestrator |

Handoffs are append-only and linked into memory (`decided_in` edges), so the reasoning survives the session that produced it. A handoff the receiver rejects ("insufficient: no commit ref") is a first-class event, not a chat.

## 9. Schedules

`handloom schedule add "0 7 * * 1-5" --template research --param topic=... --device gb10`. The link on that device fires it: new job from template, fresh sessions, but project memory carries over, so run N sees what run N-1 learned (an `episode` note per run). Output policy per schedule: keep, notify (ntfy/webhook, existing notifiers), or escalate on failure. Crons never auto-approve anything; they stop at `review` like any job unless the template says `auto_accept: true` and has a reviewer persona.

## 10. Watching

Two views over the same event stream (`/events`, already in the hub): `handloom watch` (TUI) and the web board (M3). Both read-only by default, with explicit intervention actions.

```
┌ handloom ─ ws:main ─ job 0042 Competitor pricing scan ─ running ─ $4.10/$15 ─ 1h12/3h ──────────────┐
│ SESSIONS                 │ s3 researcher ● working  task #12  ctx 54%  $1.20        [t]ranscript │
│ ◆ lead-research   ● work │ ─────────────────────────────────────────────────────────────────── │
│   ├ researcher    ● work │ 14:02:11  tool  WebFetch competitor-c.com/pricing                   │
│   ├ analyst       ◌ idle │ 14:02:14  mem   + fact n51 "Tier C list price $49" (staged)         │
│   └ reviewer      ○ wait │ 14:02:40  say   Page 2 of 3 has the enterprise tiers, fetching.     │
│                          │ 14:03:02  tool  Write out/research/12.md                            │
│ TASKS                    │                                                                     │
│  #10 ✓ scope list        ├─────────────────────────────────────────────────────────────────────┤
│  #11 ✓ fetch 1–9         │ EVENTS                                                              │
│  #12 ▶ fetch 10–12  s3   │ 14:03:05 s3 submit #12 evidence file:out/research/12.md             │
│  #13 ⧖ normalise    dep12│ 14:03:05 → lead-research woken (hook)                               │
│  #14 ⧖ compare           │ 14:03:20 lead accepted #12; spawn analyst for #13                   │
│ ASK (0)   HANDOFFS (4)   │ 14:03:21 handoff 0008 delegate lead→analyst                         │
├──────────────────────────┴─────────────────────────────────────────────────────────────────────┤
│ > _                                                                                            │
│ tab pane · j/k move · enter open · m message · p pause · a attach pane · h handoffs · M memory  │
└────────────────────────────────────────────────────────────────────────────────────────────────┘
```

- **Left:** session tree (orchestrator at the root) with state glyphs from the existing `idle|working|blocked|offline|unknown`, plus task graph with dependencies.
- **Right top:** the selected session's live feed: tool calls, memory writes, messages, text. Source: the adapter's transcript file where the CLI writes one, else pane capture through the herdr/tmux driver.
- **Right bottom:** merged event log for the job (wakes, claims, submits, accepts, handoffs).
- **Views by key:** `h` handoff packet viewer (diff of what was passed), `M` memory graph browser (node, neighbours, provenance, staged queue for review), `c` context pack of the selected session, `b` brief.
- **Actions:** message an agent or the orchestrator, pause/resume a session, **attach** (jump into the real pane to take over), answer an escalation, approve a staged-memory promotion, cancel.
- Built with Bubble Tea. Needs no new server features beyond a job-filtered event stream and a transcript tail endpoint.

First cut, shippable on today's code: `handloom watch` with the agents list, task list and event log (everything exists); session feed and handoffs arrive with later milestones.

## 11. Architecture deltas

New hub tables: `job`, `persona_snapshot`, `session`, `handoff`, `note`, `edge`, `artifact`, `schedule`, `context_pack`. New link duties: **spawn** (compile persona, create worktree, launch pane through the driver, register the session) and **transcript tail**. New adapter duties: `compile` (persona → CLI config), context-usage reporting, transcript path. New CLI verb groups: `job`, `persona`, `skill`, `spawn`, `handoff`, `mem`, `schedule`, `watch`, `context`.

A job **pins a snapshot** of each persona and skill at start, so editing a persona later never changes a running job and every run is reproducible.

Not changed: outbound-only devices, wake ladder, scopes-on-server, evidence on submit, human-only approval, audit log.

## 12. Build order

| Milestone | Delivers | Done when |
|---|---|---|
| M3 (planned) | Human channel: web board, `handloom ask`, notifiers | as in DESIGN.md |
| **W1** | `handloom watch` TUI over existing agents/tasks/events | you can watch the M2 scenario live |
| **W2** | Personas, skills, `compile`, `handloom spawn`, sessions | a persona with a tool deny-list launches on claude and codex and is refused the denied tool |
| **W3** | Jobs: brief, templates, orchestrator-led planning, worktrees, scope check at submit | a technical job runs end to end from a brief with no human input after `job start` |
| **W4** | Handoffs (delegate, return, relief), context pack, rollover | a session is ended at 70% context and a fresh one finishes the task from the packet |
| **W5** | Memory v1: notes, links, FTS, staging, scribe consolidation, sensitivity gate | a second job on the same client retrieves the first job's decisions; a confidential note is refused to a cloud session |
| **W6** | Schedules, research and business templates | a daily research job runs unattended and run 2 uses run 1's episode |
| **W7** | Embeddings, graph retrieval tuning, memory browser | retrieval beats FTS-only on a fixed question set |

Each milestone gets an e2e script like M0–M2, with evidence in the report.

## 13. Risks and open questions

1. **Scope.** This is several products. W1–W4 are the core; W5+ can wait until real jobs show what memory is missing. Cut here first.
2. **Enforcement is only as strong as each CLI.** Tool limits differ per agent. The `persona check` table must stay honest.
3. **Context usage reporting** may not exist in every CLI; rollover falls back to a time or turn limit.
4. **Memory quality** depends on the scribe and on you reviewing promotions. A bad write path poisons every later job. Staging plus supersession is the mitigation; measure with a fixed question set before trusting it.
5. **Orchestrator reliability.** It is a single point of failure for a job. Mitigation: it is itself durable through relief handoffs, and the hub already alerts you when the lead goes silent.
6. **Local-model personas** (qwen on GB10) fit confidential jobs but are weaker at planning; allow a cloud orchestrator for non-confidential jobs and a local one otherwise, set per job.
7. **Name and positioning.** Herdr manages panes; this manages work. Resolved: the name is Handloom (see the naming note at the top).
8. **Hub placement.** Memory and transcripts are heavier than tasks. Keep the hub on one trusted box (GB10) rather than a public VPS for anything confidential.
