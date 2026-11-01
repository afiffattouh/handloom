# Handloom: from agent coordination to autonomous product delivery

Review date: 2026-11-01. Based on the current `/workspace/handloom` checkout, source inspection, and the existing design and durability documents. This is a proposed direction, not an implemented system or a live agent benchmark.

## Recommendation

Keep the Go hub, SQLite store, task graph, Git integration, profile versions, handoff records, and machine-run verification. Rework the execution lifecycle and the user experience around a single outcome: describe a product, receive a deployed and verified MVP. Do not begin with a whole-repository rewrite or a larger library of role prompts.

The present design combines several products: a fleet console, a general agent coordination protocol, a human review queue, and a software acceptance kit. The desired product needs one opinionated delivery path through those components.

## What the implementation actually does

| Finding | Evidence | Implication |
| --- | --- | --- |
| Leads still manage task creation, assignment, and review through commands. | `internal/hub/jobs.go`, `internal/hub/spawns.go` | Routine scheduling depends on a model correctly operating Handloom. |
| Lead loss sends a notification; replacing a lead is human-only. | `internal/hub/supervisor.go:jobResume` | Durable task storage does not yet mean autonomous recovery. |
| Job status detects several stuck conditions and advises manual action. | `internal/hub/jobstate.go` | Useful diagnosis exists, but it is not an execution controller. |
| Limits already exist for concurrent agents, total lead-requested starts, and repeated rejection; cost is measured. | `internal/hub/spawns.go`, `internal/hub/hub.go`, `internal/hub/usage.go` | Extend these controls rather than claiming there are no limits. Persist budgets and attempts at the workflow level. |
| A lead can accept a submitted task when configured verification passes. Starting/closing jobs and recovery retain human boundaries. | `internal/hub/tasks.go:taskAccept`, `internal/hub/jobs.go`, `internal/hub/supervisor.go` | Documentation saying only humans approve is too broad. Separate task acceptance, release approval, and business authorization. |
| GitHub repository creation and publishing already exist through `gh`. | `internal/link/project.go:createProject`, `internal/link/publish.go` | Harden and integrate these paths; do not rebuild GitHub access from scratch. |
| The Dokploy script deploys Handloom itself to a known compose application. | `scripts/dokploy-deploy.sh` | It is not a provisioning and release connector for arbitrary generated products. |
| Factory-kit protects acceptance tests and check configuration, runs checks, and produces evidence. | `kit/README.md`, `kit/kit/check.py` | Preserve this foundation; move routine test-authoring responsibility to a separate authorized QA role. |
| Spawned agents normally run through tmux; headless wake commands exist, with uneven live verification documented. | `internal/link/spawn.go`, `internal/link/headless.go` | Introduce a structured executor interface incrementally and certify one provider first. |
| Only native execution is implemented; container isolation is explicitly unbuilt. | `internal/link/isolation.go` | Repository write scopes are not process isolation, especially when generated code and deployment credentials meet. |
| Existing workflow-v2 already proposes plan-as-data and system-owned workers; later committee advice parks much of it. | `docs/direction/workflow-v2.md` | Resolve the product direction explicitly instead of layering another contradictory proposal on top. |

The durability document records useful restart, lease, handoff, and concurrent-action coverage. It also explicitly leaves gaps around headless expiry, cost caps, and crashes during checks/merges. The full Go suite now passes in the development environment; real agent failure and deployment scenarios still require their own validation.

## The user experience

One-time setup connects an agent provider, GitHub organization, Dokploy destination, and default stack. Validate capabilities separately: reading a Git repository does not prove repository creation, pushing, or deployment access.

Everyday use has one primary action: **Build a product**. Enter the idea, optionally attach references, and start. Defaults choose the MVP playbook, stack, budget, and deployment destination. Advanced options allow changing those defaults without requiring agent names, machines, profiles, or verification commands.

Show one project page with the current phase, what has been produced, spend/time versus limits, preview when available, and the latest meaningful result. Controls: pause, stop, redirect, retry the failed phase. An exception must explain the blocker and the smallest action needed.

At completion show the deployed product, repository, tested user journeys, limitations, and rollback target. Completion requires release checks; it cannot mean merely that every worker said it was done.

## Default MVP playbook

| Phase | Responsibility | Required output and exit condition |
| --- | --- | --- |
| Frame the request | Product orchestrator | Preserve the original request; produce a structured brief, assumptions, users, core journeys, scope exclusions, and acceptance criteria. Ask only about decisions that materially block delivery. |
| Review the plan | Product, architecture, and security reviewers | Independent findings with severity, evidence, and suggested resolution; orchestrator records the decision and a dependency graph. One review round by default, one remediation round. |
| Design the experience | UX specialist, then UI designer | Journey flows, navigation, screen inventory, loading/empty/error states, accessibility requirements, and reusable visual tokens/components. UI review includes rendered screens. |
| Prepare the foundation | Architect/platform engineer and independent QA | Versioned stack template, repository, environment setup, API/data contracts, CI, and acceptance tests authored separately from implementation. Tests are versioned and sealed for the implementation attempt. |
| Build vertical slices | Frontend/backend specialists as needed | One complete user journey at a time, with explicit ownership and interface contracts. Parallelize independent work, with bounded concurrency. |
| Verify and repair | QA, UX/UI reviewer, security reviewer | Browser journeys, integration tests, authorization boundaries, accessibility checks, and visual inspection against the design. Findings become targeted repair tasks and are rechecked. |
| Release | Deployment executor | Immutable artifact tied to a commit, migrations/preflight, Dokploy deployment, health and browser smoke checks against the deployed revision. Record release ID, URL, logs, and rollback strategy. |
| Deliver | Orchestrator | Product link, repository, credentials/setup guidance without exposing secrets, test evidence, known limitations, and operating notes. |

Roles are responsibilities, not permanently running agents. A small product should not pay for ten agents waiting around. Use short bounded specialist tasks; bring a committee together at meaningful decisions, not every edit.

Prompt optimization should compile a request into a better task contract. It must preserve intent, label assumptions, attach relevant artifacts, and select a versioned role prompt. Measure prompt changes against repeatable product-building benchmarks; do not create recursive prompt-improvement committees.

## Execution architecture

The orchestrator model proposes plans and resolves product tradeoffs. A deterministic controller validates plans, persists transitions, schedules ready tasks, chooses eligible workers, and applies retry and review policy. Agents do not own their own scheduling authority.

Keep this controller inside the existing Go service initially. Use transactional state changes and a durable dispatch/outbox mechanism. An external workflow engine is an option if requirements later justify its operational cost; it is not a prerequisite for fixing the current lifecycle.

Minimum new concepts:

- **Run:** original request, compiled brief, pinned playbook/stack/prompt versions, policy, budget, deadline, status.
- **Step:** stable ID, dependencies, role, input/output artifact contracts, checks, timeout, retry policy.
- **Attempt:** unique ID, lease generation, worker, heartbeat/progress checkpoint, started/finished timestamps, result, resource usage.
- **Artifact:** type, hash/version, source commit, author, provenance, and access scope. Handoffs reference artifacts rather than copying entire conversations.
- **Review finding:** reviewer, criterion, evidence, severity, resolution, and revision reviewed. Any relevant change invalidates stale approval.
- **External operation:** intended action, durable operation key, remote resource ID, observed state, and reconciliation result.

Crash recovery must reconcile intent with reality. If a deployment request times out, query Dokploy before issuing another deployment. If repository creation succeeds but its response is lost, adopt the correct repository after checking identity and ownership. Do not promise exactly-once remote effects: implement idempotent requests where supported and reconciliation elsewhere.

Retry transient network/provider failures with backoff; send invalid code to a repair task; replace a lost worker from a checkpoint; stop and report permanent missing access. Bound retries, total starts, wall time, and concurrency across restarts. Reserve budget before dispatch; when provider usage is delayed, disclose that a strict monetary cap cannot be guaranteed and bound calls/time instead.

For agent execution, introduce `start`, `poll`, `cancel`, `checkpoint`, and structured result events. Preserve tmux for observation or compatible providers while certifying one noninteractive backend end to end. A process existing is not proof of progress.

## Autonomy and review policy

The product promise should be **no routine intervention after launch**, with explicit exceptions. A run cannot recover missing credentials, an unavailable deployment target, or a contradictory business requirement by pretending it succeeded.

Configure authority once per workspace/project: allowed GitHub owner, repository visibility, approved stack templates, Dokploy project/server/environment, permitted resource types, budget, and release rules. The MVP preset can automatically create private repositories and deploy into its assigned environment. Routine actions within that policy should not repeatedly ask for permission.

Do not implement this by giving an agent a human token or globally removing authorization checks. Add a workflow service identity constrained by the launch policy. Changes to permissions and deployment destinations remain outside a worker's authority.

Separate implementation from acceptance-test ownership. A QA/specification role can seal tests under controller policy; a builder cannot weaken them. Legitimate requirement changes create a new spec/test revision, invalidate affected evidence, and rerun checks. The present kit's human-only ownership convention needs this deliberate revision for autonomy.

Review committees contribute evidence and dissent. They are not majority-vote truth machines. Require independently produced findings, deterministic checks where possible, and explicit severity/rubric rules. Critical unresolved findings block release; subjective design disagreements get a bounded decision by the designated product/UX owner.

## Repeatable technology and deployment

Begin with one supported product stack rather than asking every architect agent to choose. Suggested default: TypeScript, Next.js, PostgreSQL, a chosen migration layer, established authentication, a shared accessible component library, Playwright, and container deployment through Dokploy. This is a proposal, not a claim about the best stack for every product; choose exact supported versions during implementation.

A template must include CI, tests, auth/authorization patterns, database migration rules, health/readiness endpoints, structured logs, environment-variable schema, seeded test data, Docker packaging, and operating notes. Add a Python API/worker variant only when a real product needs it. Templates need release versions and upgrade tests.

GitHub and Dokploy should be explicit connectors with validated inputs and narrow credentials available to the executor, not pasted into agent prompts. Track remote IDs and the exact commit/image deployed. Rollback of application code does not automatically reverse database changes: use backward-compatible migrations and explicit recovery policy.

## Build order and proof

1. **Prove one complete delivery path.** One provider, one machine, one stack, one GitHub owner, one Dokploy destination. Deliver a small database-backed product with a browser-tested user journey. Define measured acceptance criteria before work starts.
2. **Build durable execution.** Versioned plans, task attempts, system-owned scheduling, checkpoints, bounded retries, cancellation, budgets, and reconciliation. Keep the existing protocol available while moving the default path onto the controller.
3. **Add specialist quality gates.** Structured product/UX/UI artifacts, independent QA, security findings, protected acceptance evidence, and bounded repair loops.
4. **Make deployment an ordinary recoverable step.** Repository provisioning, CI observation, build artifacts, Dokploy provisioning/deployment, smoke checks, and rollback records.
5. **Replace the default UI.** Start/progress/result, with setup and advanced diagnostics separate. Do not make a user operate the old fleet console to complete the new flow.
6. **Add enterprise as another playbook.** Threat-model depth, data handling, audit retention, performance/load, backup restoration, accessibility depth, environment separation, approval policy, and operating runbooks. These requirements need demonstrated evidence; a playbook label does not establish compliance.

The first benchmark should be a frozen set of representative MVPs, with repeated runs and a single-agent baseline. Measure successful deployment with no intervention, user-journey pass rate, intervention count, elapsed time, cost, retries, and escaped defects. Report failures and exclusions. Do not optimize for agent count or volume of generated code.

Fault tests must kill the lead, worker, link, and hub at different phases; interrupt checks and merges; simulate provider throttling; lose the response to repository creation/deployment; submit stale results; exhaust budget; and fail a migration. Verify bounded recovery, no duplicate external resources, cancellation, and correct invalidation of stale evidence.

## Implementation progress and validation

The first execution slice is implemented in `internal/plan`, `internal/hub/plans.go`,
and the CLI/link integrations. `handloom job plan` submits an immutable task graph;
the hub pins profiles, schedules dependency-ready workers, persists bounded
attempts, fences stale claims, and refuses cancelled launch requests. Dependent
repository workers wait for verified integration and start from its branch.
The link retires completed, cancelled, and superseded plan workers.

Passed: full Go test suite, Go vet, static binary build, 66 Python kit tests,
and health/setup-page HTTP smoke checks against the built hub. The new migration
preserves existing jobs. Go dependencies downloaded with checksum verification
after the published network policy became active.

This is the execution foundation, not a completed product-delivery system. Automatic
lead recovery, run budgets/authorization, versioned replanning, automated repair/re-review, the simplified product UI, and GitHub/Dokploy release
reconciliation remain subsequent milestones. Real authenticated agent processes
and external deployment have not been exercised in this environment.

No environment configuration was reapplied or published during this resumed work.
No remote repository or deployment was created. See `docs/plans.md` for current usage.

The next implementation milestone is now available: `mvp-v1` compiles ten
specialist stages, `handloom build` atomically launches a job/lead/plan with
idempotent request handling, and the job page shows stage progress. Profiles are
installed once in an isolated namespace, preserving existing customization.
See `docs/mvp-playbook.md` for commands and explicit limits. Artifact/rubric
assessment remains model-mediated; real-agent product delivery and deployment
connectors are not validated by the local regression suite.
