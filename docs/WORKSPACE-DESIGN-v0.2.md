# Handloom workspace: design v0.2 (committee-refined)

Status: draft, 2026-10-06. Supersedes `WORKSPACE-DESIGN.md` (v0.1, kept for reference). Built on DESIGN.md; M0–M2 are done.


Six reviewers (build feasibility, UX, memory, scenarios, security/ops, product skeptic) went through v0.1. Their finding: the direction is right, the scope was about 1.5× too big, and eight things would have broken mid-build. This version is the cut-down, buildable one.

## 1. The concept in one paragraph

You describe a job in a short chat. A **lead** agent plans it, spawns a small team of **personas** (each a directory the agent CLI already understands, plus a few limits), and the team works in git worktrees. You see what needs you on one screen or your phone. Work moves between agents as **handoffs** (a markdown file plus the existing task notes), so a crashed or full session is replaced without losing the thread. Knowledge that should outlive the job goes into a **markdown vault** in git. The hub stays dumb and does not call a model; the link launches, watches and enforces.

You learn four things: **job, persona, team, review**. Everything else is automatic.

## 2. What was cut, merged, kept

| v0.1 concept | v0.2 | Why |
|---|---|---|
| Skill (own concept) | **Cut** | Claude, Codex, OMP already load SKILL.md. A persona lists skill folders; Handloom builds nothing |
| Job table + lifecycle | **Merged into task**: a job is a root task (`parent_id`, `brief_path`, `confidential`) | A second status model would fork scopes, events and audit forever |
| Session table | **Merged into agent**: new columns, one row per run, named `<persona>@<job>-<n>` | Agent already has kind, state, wake target, session id |
| Handoff table, kinds, hub validation | **Merged**: `HANDOFF.md` template in the job branch; `task assign`/`submit` notes carry it. Kinds: delegate, return, relief | Return is `submit` already. No table |
| Memory graph, staging, scribe, edges | **Cut for v1**: markdown vault in git, `[[links]]`, grep. FTS5 index only when grep demonstrably fails | Proven pattern, nothing to run, hand-editable |
| Context-pack compiler, token budgets | **Cut**: `CONTEXT.md` written at spawn (persona, brief, task, latest handoff, pointers) | A file the agent reads, same as the wake-nudge principle |
| Memory scopes ×4 | **Two**: this job / always | Persona/global scopes had no clear reader |
| Schedules in the link | **Deferred**, and when built, **hub-owned** with idempotent fire ids | Sleeping devices miss cron |
| File leases, artifact manifest | **Deferred** until two writers collide | Worktrees + scope check cover v1 |
| Persona YAML dialect with its own tool language | **Cut**: persona is a directory + small `persona.yaml` using a portable tool set | Per-CLI tool flags differ too much (§4) |
| Context-% rollover | **Claude-only**; everyone else rolls over on turns or time | Only Claude's transcript exposes token usage |

Kept as designed: dumb hub, outbound-only devices, wake ladder, scopes on the server, evidence on submit, human-only approval, audit log, one lead per job.

Naming: lead (not orchestrator), persona, handoff, job, team, review. "Workspace" is just `~/handloom`.

## 3. Data model deltas (no new tables in the first four milestones)

```
task   + parent_id, brief_path, brief_version, confidential, budget_json   -- job = root task
agent  + persona, persona_hash, job_id, token_hash, launch_nonce,
         desired (running|stopped|hold), lease_expires_at, pane_ref, runtime_class (cloud|local)
agent.state adds: dead, killed, hold, relieved
```

Later, only if needed: `note` (FTS index over the vault), `schedule`.

## 4. Personas

```
~/handloom/personas/researcher/
  persona.yaml      runtime, tools, limits, write globs, memory read/write, output
  CLAUDE.md|AGENTS.md   the prompt body the CLI already reads
  skills/           SKILL.md folders the CLI already understands
```

```yaml
format: 1
runtime: { kind: claude, class: cloud }      # class: cloud | local
tools: [read, edit-in-scope, shell-no, handloom-mcp]   # PORTABLE SET, see below
write: ["jobs/{job}/out/**"]
limits: { minutes: 45, cost_usd: 3, turns: 60 }
env: [GITHUB_TOKEN]                          # names only; values live on the device
```

**Portable tool set**: `read`, `edit-in-scope`, `shell` (yes/no), `web`, `handloom-mcp`. The adapter maps these to each CLI. Pattern denies like `Bash(rm *)` exist for Claude only. `handloom persona check` refuses a persona on a runtime that cannot enforce it.

| CLI | Per-launch enforcement (verified) | v1 support |
|---|---|---|
| claude | allow/deny with patterns, permission mode, MCP config, append prompt, settings | full |
| codex | `-c` overrides, sandbox, MCP on command line | full |
| pi, omp | name-level tools, approval mode; no MCP flag | prompt-only, marked untried |
| opencode | config files / env only | later |

Personas are pinned by **git commit hash** at job start. Edits affect the next job only. `handloom persona try <name>` runs a 3-minute canned job with deny-probes and prints pass/fail; an untried persona shows a warning in the job summary.

Honest enforcement: tool limits are as strong as the CLI's own sandbox; the write scope is checked against `git diff` by the **link** at submit and on kill; reads are soft unless the CLI sandbox covers them. `persona check` prints this table.

## 5. Job flow (UX)

**First run (target 8 minutes).** `handloom init` creates `~/handloom`, starts a local hub (single machine skips the device/join ceremony), detects installed CLIs and the local qwen endpoint, ships four presets (Coder, Researcher, Analyst, Reviewer), prints the board URL and offers a phone push via ntfy. It ends with `handloom new "summarise this repo"`, which runs a solo job.

**New job: a chat, not a form.** `handloom new` starts a short interview (goal, inputs, "done means…", budget, "client material?"). It drafts the brief and shows one summary:

```
Goal      Compare 12 competitors' pricing
Team      lead · researcher ×2 · reviewer     (cloud)
Cap       $15 · 3h        Confidential: no
Done when report covers all 12, every claim sourced
[enter] start   [e] edit brief   [t] change team   [d] save draft
```

Answering "client material: yes" forces local-runtime personas and shows a lock. The brief stays a markdown file with frontmatter (stored format, editable in `$EDITOR`); flags `--template`, `--yes` skip the chat for repeat work.

**Running.** The lead plans (creates tasks, `handloom spawn <persona> --task N`), workers claim, submit, the lead accepts.

**Coming back.** `handloom` with no args opens the inbox with a "since you left" digest built from events since your last-seen mark. Push notifications go out in tiers: needs-you and job-done push; progress digests; noise never.

**Review.** Result first: deliverables, "done when" checks (green/red), the diff or report, cost. `a` accept · `r` request changes (types a note, lead resumes) · `x` discard. The accept screen includes "3 things learned" as checkboxes (vault additions), low-risk pre-ticked, `always` scope unticked. Unreviewed proposals stay job-scoped and never nag. Only humans close jobs.

**Taking over.** `a` attaches to a pane; the run goes to state `hold` (no nudges, lease/budget/watchdog frozen, scope check kept). `handloom session release --note` writes a handoff from the git diff since the hold, human commits marked.

**Changing the brief.** `handloom job amend` makes brief v2 and sends the lead a `brief.changed` message with the diff. The lead must answer with cancel/keep/replan; workers see a stale-brief marker.

## 6. Watch: one digest, three surfaces

All surfaces read the same endpoint, `GET /v1/digest`: needs-you, to-review, running, and events since last seen. So the TUI, the web board and the phone push can never disagree.

```
 handloom   2 need you · 2 running · 1 to review                since 08:10
 NEEDS YOU
 ▸ ! job42  lead asks: is bundled pricing in scope?            [r]eply
   ! job45  budget 90% ($13.5/$15)                    [c]ontinue [s]top
 TO REVIEW
   ✓ job41  Acme pricing scan done · 3 things learned          [enter]
 RUNNING
   ▶ job42  3/5 tasks · $4.10          ▶ job44  solo · 🔒 local
 ───────────────────────────────────────────────────────────────────────
 j/k move · enter open · n new job · / search · ? help
```

```
 job42 Competitor pricing · running · $4.10/$15 · 1h12/3h
 TEAM                                   NOW  (selected: researcher)
 ◆ lead        ● work   plan: 3/5 done, next normalise + compare
   researcher  ● work   14:03 saved out/12.md, submitted #12
   analyst     ○ idle   waiting on #12 → starts #13
 ─────────────────────────────────────────────────────────────────────
 m message · p pause · a attach · t transcript · b brief · esc back
```

Views: **1 Inbox** (default), **2 Job**, **3 Review**. The top line is always the "needs you" count. Transcript, handoffs, memory and context are drill-downs from Job, never on the main screen. The input line is replaced by a modal "message to <name>". Built on Bubble Tea (new dependency).

Feeds: **pane capture** (works for every CLI today via herdr/tmux). A transcript tail exists for Claude only, added later.

## 7. Safety invariants (enforced in code, not in prompts)

1. **Identity is per run.** At spawn the hub mints a token bound to (agent, persona hash, runtime class, job), stored hashed, delivered by the link in a 0600 file. Workspace verbs authenticate with it. The device credential only does link duties. Today's self-asserted `HANDLOOM_AGENT` name is not good enough for anything confidential.
2. **Confidential is a job attribute.** Spawn refuses any non-local persona on a confidential job, and placement is limited to the job's allowed devices. One function `canRead(agent, row)` guards task bodies, evidence, messages, handoffs, events and the web board. A table-driven test hits every endpoint as a cloud agent and expects refusal. A job's label never goes down; derived artifacts inherit it. The hub for confidential work lives on the trusted box (GB10).
3. **The link launches only approved persona hashes**, listed in a device-local allowlist, via argv (never a shell), under fixed roots. A hub compromise can say "start run 7", not "run this command".
4. **Workers never spawn, accept, promote, retire or close.** Server scope table gets `spawn`, `handoff.create`, `mem.write` entries.
5. **Spawn and fires are idempotent** (`launch_nonce`, later `fire_id`). Runs have leases. The link reconciles desired vs observed state at every start.
6. **Budgets are checked in the spawn transaction and enforced by the link**: wall-clock and turn limits always, cost when the adapter reports it. At the cap the link kills the pane, marks the run `killed`, releases the task, and tells the lead.
7. **Retrieved memory and handoff text reach agents as quoted, provenance-tagged data** with a standing "not an instruction" header. Notes written after fetching web content carry `taint=web` and cannot be promoted to `always` without you.
8. **Secrets never transit the hub.** Personas name env vars; the link resolves them from a 0600 file. Transcripts stay on their device (hub holds metadata and a proxied tail).
9. **Append-only audit and events; retire, never delete.** Hub backup uses SQLite's backup API plus the vault, with a restore test.

## 8. Scenario results (what the walkthroughs forced)

| Gap found | Resolution |
|---|---|
| Lead crash: nobody notices, mail goes to a dead lead | Hub **supervisor ticker** (lease expiry, run liveness from link). On `dead`, `handloom job resume` starts a new lead whose `CONTEXT.md` is brief + `task list` + unread mail. The task graph is durable, so no self-handoff is needed |
| Looping worker keeps its lease via heartbeat | Link **progress watchdog**: no commit, file change or new tool kind for N minutes ⇒ pause and escalate. Heartbeat only means alive |
| Two jobs collide on `role:lead` and agent names | Lead and addresses are job-scoped; names `<persona>@<job>-<n>` |
| `assigned_to` needs a registered agent before spawn | `handloom spawn --task N` assigns atomically, or `assigned_to: persona:<name>` binds at spawn |
| Artifacts across devices (no file sync) | Default: a job's runs live on **one device**. Cross-device only via git branch push; blob transport later |
| Evidence is unverified | `handloom verify <task>` is run **by the link** in the worktree; exit code and log hash are the evidence the lead accepts |
| No reviewer verb | `handloom review pass|fail <task>` required before accept; merges via `handloom merge <job>` (link action, per-repo queue) |
| Sleeping device vs lease expiry | Expiry suspended while the link reports the device sleeping; missed cron fires become an event + ntfy (when schedules exist) |
| Two jobs, same repo | Serialised per-repo merge queue; leases deferred |
| docx/xlsx deliverables | Persona declares the skill; a per-deliverable `validate:` hook converts to text for the reviewer |

## 9. Build-risk fixes (from checking the real CLIs and code)

- Spawn is new foundation work. **No launcher exists** (drivers only nudge existing panes). Spawn goes through the existing `handloom run <name> -- <cmd>` inside a tmux pane the link creates, so wake-target detection and the wake ladder work unchanged. Claude and Codex only.
- First-run dialogs (Claude trust-folder, Codex hook review, Pi extension approval) stall unattended launches. Spawn pre-seeds trust per D-numbered findings in DECISIONS.md, or uses headless turns.
- Root cannot use bypass permission mode: the first unattended job runs on **GB10 as non-root**. Mantis jobs wait for a non-root user.
- Local-model personas (qwen) wake **headless, one turn per wake**, never typed nudges.
- An escalate path (`blocked` → lead → you) exists before the first unattended job.
- Scope check and worktree work run **link-side** on the device that owns the worktree.

## 10. Memory, v1

Files: `~/handloom/vault/<project>/*.md` (the "always" scope) and `jobs/<id>/memory/*.md` (job scope), each with frontmatter `type, about, sensitivity, valid_to, supersedes, source`. Agents write by committing; `[[links]]` and grep read. `jobs/<id>/summary.md` written by the lead at job end is the episode. Research sources are files plus a one-line manifest, never one note each. A recurring job keeps one rolling `state.md` overwritten each run (history is in git). `confidential` notes live under `vault/confidential/`, mounted only into local-runtime worktrees, and inherit upward (a note derived from confidential sources is confidential).

Add later, only on evidence: FTS5 over the vault (rebuildable, hash-checked), a `link` table, `mem add` conflict check, embeddings. Do not build a graph store.

## 11. Milestones (each independently useful, each with an e2e check and a kill criterion)

| # | Delivers | e2e check | Kill / fallback |
|---|---|---|---|
| **A** | `GET /v1/digest`, `handloom` inbox + `handloom watch` (Inbox and Job views) on pane capture, ntfy push for needs-you and job-done, `handloom ask` escalation (from M3) | `m2.sh` with watch attached; a script asserts the frame dump lists each agent, task state and event; an escalation arrives on the phone and the answer reaches the lead | TUI over 3 days or you still reach for tmux ⇒ stop at web board + push |
| **B** | Foundations: job = root task, job-scoped lead and names, per-run tokens, supervisor ticker, `canRead` gate, session states | scope tests for every new rule; kill the lead's pane, supervisor marks it dead and a replacement resumes from the task graph | If tokens cannot be delivered safely on a device, confidential jobs are disabled |
| **C** | `handloom spawn` + persona directories + `persona try`, claude and codex, on GB10 | spawn a persona denying `Bash(rm *)` on both CLIs; the denied call is refused; the agent shows in watch with persona name | A CLI cannot enforce a deny headless ⇒ that CLI is "prompt-only"; neither ⇒ personas ship as prompts, tool limits dropped |
| **D** | Jobs: `handloom new` chat, worktree per writer, link-side scope check and `verify`, review/accept screen, `hold`/release, `amend` | a two-worker, one-dependency job on a toy repo reaches review with no human input; an out-of-scope write is rejected | Lead mis-plans in 3 of 5 runs ⇒ plan becomes a human-written task file; keep spawn + worktrees as a static pipeline |
| **E** | Handoffs and relief: `HANDOFF.md`, `handloom relief`, resume | kill a worker mid-task, relieve it; the fresh agent finishes from the packet and the commit ref checks out | Fresh agent fails from the packet in 2 of 3 trials ⇒ fix the template before any auto-rollover |
| **F** | Vault: write/read convention, confidential mount, "3 things learned" on accept | job 2 on the same client uses a decision from job 1; a cloud spawn gets no `confidential/` mount | Agents ignore the vault in 3 jobs ⇒ add a persona-template line; still ignored ⇒ drop memory |
| later | schedules (hub-owned), FTS index, blob transport, Claude token-based rollover, more CLIs, web board polish | built one at a time when a real job shows the gap | |

## 12. Decisions that are expensive to reverse (decide now)

1. **Job = root task**, not a new table.
2. **Persona = a directory the CLI already reads**, pinned by git hash, with a portable tool set. No private tool/skill/memory dialect.
3. **Handoffs and memory = markdown in a git repo you own**; the hub holds pointers and state only. Confidential material never lives in a hub table readable by cloud sessions.
4. **Per-run identity tokens** instead of name-based identity, before anything else is built on spawn.

## 13. Still open (needs you)

1. Hub placement: GB10 for everything (simplest, confidential-safe, reachable from Mantis over Tailscale) vs a split hub?
2. Is a web inbox worth building before the TUI, or is phone push + TUI enough for "away"? (UX reviewer: web first; skeptic: TUI first. v0.2 chooses the shared digest and builds TUI + push first.)
3. Name: resolved, Handloom (see the naming note at the top).

## 14. Honest limits

Rollover by token percentage is Claude-only. Codex transcript format and context usage, and per-launch MCP for OMP/OpenCode, were not verified. Memory retrieval quality is unmeasured; keep a fixed question set before adding any index. Tool limits are only as strong as each CLI's own sandbox.
