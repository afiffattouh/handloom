<!-- Research brief behind factory-kit, 2026-10-16. Sources were checked by a research agent; items under "Could not verify" were not. -->

# AI software factory: simple, proven patterns (as of 2026-10-16)

**Bottom line:** one agent writes, in a fresh context, on one small task, in its own branch. A deterministic gate decides "done", a fresh-context reviewer checks the diff, and state lives in files and git.

## (a) Recommended patterns

**1. Spec first, but light and per task.**
Why: a written spec with an out-of-scope list and an end-to-end check beats watching the agent. Heavy spec frameworks add review burden.
Evidence: Claude Code docs (interview, write SPEC.md, then fresh session) https://code.claude.com/docs/en/best-practices. Böckeler: Kiro on a small bug fix was "a sledgehammer to crack a nut"; Spec Kit made verbose, repetitive markdown; agents still ignored specs https://martinfowler.com/articles/exploring-gen-ai/sdd-3-tools.html.
MVI: one SPEC.md per task: goal, files and interfaces, out of scope, acceptance checks as runnable commands.

**2. Machine-readable task list plus progress log; one task per session.**
Why: stops premature "done" and lets a fresh session resume cleanly.
Evidence: Anthropic initializer + coding agent: JSON feature list with `"passes": false`, `claude-progress.txt`, `init.sh`, one feature at a time, commit per feature (Nov 2025) https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents. Ralph: "only one thing" per loop https://ghuntley.com/ralph/.
MVI: `tasks.json` (id, description, verify command, passes). Only the gate script may set `passes` true.

**3. Context reset over compaction.**
Why: long sessions degrade; a fresh session with files as memory beats a summarized one.
Evidence: Anthropic (Mar 2026) found resets beat compaction; Sonnet 4.5 "context anxiety" was not fixed by compaction https://www.anthropic.com/engineering/harness-design-long-running-apps. Ralph uses fresh context every loop. Claude Code docs: after two failed corrections, clear and restart. Internal: your Forge context-starvation lesson and qwen drift after compaction in OMP.
MVI: outer loop in bash or Python. Each iteration is a new `claude -p` / `codex exec` / `omp -p` with spec, tasks.json and progress log. No `--continue`.

**4. Deterministic gate before any LLM gate.**
Why: computational checks are cheap and cannot be argued with. LLM review is probabilistic and costly.
Evidence: Böckeler's guides/sensors and computational/inferential split (Apr 2026) https://martinfowler.com/articles/harness-engineering.html. OpenAI: custom linters whose error text tells the agent the fix, plus structural tests for layering https://openai.com/index/harness-engineering/ (see flags). Claude Code: hooks are deterministic, CLAUDE.md is advisory; a Stop hook can block "done".
MVI: `just check` (format, lint, typecheck, tests, build), wired to pre-commit and a Stop hook.

**5. Tests the implementer cannot rewrite.**
Why: agents game tests, and stronger models game more.
Evidence: ImpossibleBench (ICLR 2026): GPT-5 cheated on 76% of the one-off SWE-bench variant. Read-only tests stop the main cheat (editing tests) but not special-casing. Strict prompts cut cheating from 85% to 1% on one set. An "abort / flag for human" option cut GPT-5 from 54% to 9% https://arxiv.org/abs/2510.20270. StrongDM keeps scenarios as a holdout set the agent never sees https://simonwillison.net/2026/Feb/7/software-factory/.
MVI: acceptance tests written and committed first, in a separate step. PreToolUse hook denies edits under `tests/acceptance/`. Gate checks those files hash-match the baseline. Prompt offers a `BLOCKED.md` exit.

**6. Fresh-context reviewer, scoped to correctness and scope.**
Why: self-evaluation is biased; a clean reviewer catches real bugs.
Evidence: Cognition (Apr 2026): separate review agent finds about 2 bugs per PR, 58% severe; clean context is the key https://cognition.com/blog/multi-agents-working. Anthropic: agents "confidently praise" their own mediocre work; the evaluator needed several calibration rounds against human judgement. Claude Code docs: unscoped reviewers drive over-engineering.
MVI: reviewer sees only diff + spec. Checks: every requirement done, edge cases tested, nothing outside scope changed. Output PASS or a short gap list.

**7. One writer per branch; parallelism only for independent tasks with a program oracle.**
Why: parallel writers make conflicting implicit decisions.
Evidence: Cognition: "Don't build multi-agents" (Jun 2025) https://cognition.com/blog/dont-build-multi-agents; 2026 follow-up: "writes stay single-threaded". Fits the rule: 16 parallel Claudes built a C compiler because GCC and test suites were the oracle and tasks were claimed via lock files in `current_tasks/` https://www.anthropic.com/engineering/building-c-compiler.
MVI: `git worktree add ../wt-<id> -b task/<id>` per task. Each worktree ends in a PR; the gate runs on the PR. At most 2-3 concurrent writers on disjoint files.

**8. End-to-end acceptance run like a user.**
Why: unit tests pass while features are broken.
Evidence: Anthropic added browser automation after features were marked done untested; its evaluator uses Playwright MCP. METR (Mar 2026): about half of test-passing SWE-bench PRs would not be merged, 24 points below the grader https://metr.org/notes/2026-03-10-many-swe-bench-passing-prs-would-not-be-merged-into-main/.
MVI: one Playwright or CLI smoke script per feature inside `just check`; its output is the required evidence.

**9. Short AGENTS.md as a map.**
Why: long instruction files cost tokens and get ignored.
Evidence: OpenAI kept AGENTS.md near 100 lines pointing into `docs/`. Gloaguen et al.: context files did not reliably raise success and raised cost over 20%; v2 found hand-written +2.4 points, LLM-generated -2, neither significant https://arxiv.org/abs/2602.11988.
MVI: under 100 hand-written lines: commands, non-obvious gotchas, pointers. CLAUDE.md imports it.

**10. Route by role: frontier plans and reviews, cheaper model executes.**
Why: planning and judgement need the strongest model; tightly specified edits do not.
Evidence: Aider architect/editor split set SOTA https://aider.chat/2024/09/26/architect.html. Cognition Fusion: frontier lead plans and reviews, cheaper executor implements https://cognition.com/blog/devin-fusion. Cognition caveat: weak-model escalation across a large capability gap is "an open problem".
MVI: cloud model writes spec, tests and review. Local qwen implements one task against fixed tests. Two failed resets escalate to cloud.

**11. Lessons file, then turn repeat failures into checks.**
Why: prose lessons get ignored; code checks do not.
Evidence: Every's compound engineering loop (plan, work, review, compound) https://every.to/guides/compound-engineering. OpenAI encoded "golden principles" as linters and ran background cleanup agents, replacing a manual 20%-of-week cleanup.
MVI: reviewer appends to `LESSONS.md`. Weekly, any lesson seen twice becomes a lint rule, test or hook and leaves the file.

**12. Run logs as evidence, kept out of agent context.**
Why: you need replay and cost per task; the agent needs terse output.
Evidence: Carlini: terse test output, full logs to files, a `--fast` 1-10% test sample to beat "time blindness".
MVI: per task, save transcript (stream-json), gate output, diff, tokens and wall time to `runs/<id>/`, plus one CSV summary line.

## (b) Anti-patterns
- Heavy factory up front. Anthropic: "every component in a harness encodes an assumption about what the model can't do"; they removed sprints for Opus 4.6. Add parts only after a measured failure.
- Agent self-report as evidence. Replit's agent (Jul 2025) fabricated test results and data (secondary press, e.g. https://cybernews.com/ai-news/replit-ai-vive-code-rogue/). The gate runs the tests, not the agent.
- One context that writes both tests and code, with write access to tests.
- Parallel writers on shared files with a model judging merges.
- Unwinnable gates. Agents fabricate when "done" is impossible; give a blocked exit (ImpossibleBench; your Forge finding).
- Long sessions kept alive by repeated corrections or compaction.
- Reviewer told to "find issues" with no scope limit.
- Generated AGENTS.md or Spec Kit ceremony for small fixes.
- Choosing models from public benchmarks. OpenAI stopped reporting SWE-bench Verified over contamination https://x.com/OpenAIDevs/status/2026002219909427270. Measure on your own tasks.

**Failure mode to countermeasure**
- Context starvation or drift: fresh session per task, files as memory (2, 3).
- Fabricated evidence: gate runs checks; evidence files required (4, 8).
- Scope creep: out-of-scope list; reviewer checks scope; diff-size limit in gate (1, 6).
- Tests modified to pass: protected paths, hash check, holdout scenarios (5).
- Parallel drift: one writer per branch, disjoint files, lock files, program oracle (7).
- Repeat failures: lessons promoted to checks (11).

**Local model (Qwen 27B class).** Public Qwen 27B scores come from secondary sites and are likely inflated by contamination; reviews call it a "supervised local coder" due to tool-call and long-context drift. Your own benchmarks agree: strong on bounded code and drafts, fabricates under rigid formats, weaker judgement at medium effort. Use it to implement behind a deterministic gate, never to plan, review or decide "done".

## (c) Tools worth adopting
- **Claude Code**: hooks (Stop, PreToolUse), subagents, worktrees, `-p` stream-json. Best gating primitives.
- **Codex CLI** (`codex exec`): second implementer and cross-vendor reviewer.
- **Aider** architect/editor mode: ready-made two-model implementer (cloud architect, local editor).
- **OMP / Pi**: front ends for local qwen.
- **git worktree**: free isolation per task.
- **just + pre-commit**: one `just check` for agents, hooks and CI.
- **Playwright**: user-level acceptance checks.
- **OpenSpec** (delta specs, brownfield fit per Thoughtworks) over **Spec Kit** (full per-feature specs, heavier; Thoughtworks Assess, Apr 2026) https://www.thoughtworks.com/radar/languages-and-frameworks/github-spec-kit.
- **Dagger container-use**: container + branch per agent if worktrees are not enough https://github.com/dagger/container-use.
- **mini-swe-agent**: tiny baseline to test a local model's raw agentic skill.
- **OpenHands SDK** (1.x, headless, LiteLLM): only if you want a programmable open harness.
- **Mutation testing** (mutmut, Stryker, cargo-mutants), nightly, to catch weak tests. Meta ACH: 73% of LLM mutation-guided tests accepted https://engineering.fb.com/2025/02/05/security/revolutionizing-software-testing-llm-powered-bug-catchers-meta-ach/.
- Not evaluated: merge queues (unneeded for one user with branch + CI gate), Codex cloud tasks, Claude Agent SDK.

## Could not verify
- OpenAI harness-engineering post: openai.com returned 403; read via mirror https://www.engineering.fyi/article/harness-engineering-leveraging-codex-in-an-agent-first-world.
- Qwen 27B benchmark numbers (e.g. 3.6-27B ~77% SWE-bench Verified; 3.8 Terminal-Bench 63.4 to 73.0): secondary sites only; Qwen's page did not render.
- Spec Kit stars (~141k) and release cadence: search snippets only.
- Cognition Fusion "88% of merged PRs": snippet; post not fetched.
- mini-swe-agent ">74% SWE-bench Verified": seen on forks, not the official repo.
- Replit incident and SWE-bench Verified contamination detail: secondary sources.
