# Build an MVP from one brief

In the web UI, choose **Build a product** from the menu (or open `/build`). Enter
your brief and select a prepared project. A workspace owner can install the MVP
team from the same page; the brief is preserved during team setup. Projects must
already have a ready copy on an authorized, joined machine and a check command.
The form points to Projects when preparation is incomplete.

The build page shows stage progress, the original brief, and delivery status.
Progress refreshes automatically; choose a stage to load its current evidence.
Technical details and manual intervention controls remain available underneath.
See [the UX review](direction/product-ux-review.md) for the changes and remaining
work. The command-line flow follows below.

The built-in `mvp-v1` recipe creates a managed job from a product brief. The hub
creates the task graph, pins its profiles, schedules workers after dependencies
are accepted/integrated, and records bounded attempts. An orchestrator reviews
submissions and sends work back with concrete findings.

Install the specialist profiles once, as the workspace owner (choose an agent
CLI already installed and authenticated on the worker machine):

```sh
handloom playbook install mvp-v1 --kind claude --runtime cloud
```

Other supported agent CLIs can be selected with `--kind`; if their capabilities
require adapting a starter, explicitly pass `--adapt`. Existing `mvp-*` profiles
are preserved, including their runtime choices and customizations. Installation
is atomic and idempotent; it does not install or authenticate the agent CLI.
The product/design profiles retain their narrow write scopes and add shell access
for committing their artifacts, with destructive commands and pushing denied
where the selected provider supports those restrictions. Explicit `--adapt` can
remove unsupported restrictions; installation reports capability changes.

Inspect the plan without starting agents:

```sh
handloom build "An appointment booking tool for a small clinic" --dry-run
```

Start against an existing project with a configured check:

```sh
handloom build "An appointment booking tool for a small clinic" --project clinic
```

Or name an existing repository on a joined machine:

```sh
handloom build --brief-file brief.txt --repo /srv/clinic --device build-machine --verify ./check
```

The build prints its job ID and request ID. Use `handloom job plan <id>` or the
job page's **Build stages** section to follow it. If a network failure loses the
launch response, repeat the same command with `--request-id <printed-id>`; it
returns the existing job instead of spawning another team. Changing the brief
under the same request ID is refused.

The ten stages are:

| Stage | Required outcome |
| --- | --- |
| Product brief | Intent, users, scope/exclusions, assumptions and acceptance criteria |
| UX | Journeys, navigation, screen inventory and failure/empty/loading states |
| Architecture | Stack, API/data contracts, permissions, testing and environment requirements |
| UI | Visual tokens, responsive layouts, component states and visual acceptance criteria |
| Backend | Data layer, migrations, auth, APIs and integration tests |
| Frontend | Working user journeys wired to the integrated backend |
| QA | Independently derived browser/integration tests and usability/accessibility findings |
| Security | Independent findings with severity and evidence |
| Code review | Independent correctness and maintainability findings |
| Release handoff | Docker/CI files, configuration requirements, smoke checks and rollback/runbook |

UX and architecture can run together. The three independent review stages can
run together after frontend completion. Backend precedes frontend to avoid
concurrent initialization of dependency manifests and shared application files.
All review stages must be accepted before release preparation starts. The lead
cannot create extra tasks or start extra workers in a managed job.

The recipe recommends TypeScript/Next.js/PostgreSQL, established auth, accessible
components, Playwright and Docker for new web products. The architect pins exact
versions and preserves an existing project's stack. This is a stack policy in
the task contract; an automatically generated, tested stack scaffold is not yet
included.

The recipe names artifact locations and requires evidence in handoffs. Machine
checks and dependency readiness are enforced. Artifact completeness and the
severity of review findings are currently assessed by the lead; there is no
machine-readable review verdict or automatic repair/re-review graph yet. Failed
checks and repeated rejection still use the existing bounded task mechanisms.

Prerequisites: an existing repository/project, a joined worker with tmux and an
authenticated agent CLI, installed profiles, and a working project check command.
The check must already pass for baseline/documentation changes; this version does
not bootstrap a blank project's test infrastructure. Missing profiles, invalid
job configuration, and an absent check command fail before the job/lead/plan
transaction commits. Agent authentication, tmux availability, and a passing
baseline check still need to be verified on the worker machine.

Limits remain enforced across starts. The default hub cap is 12 total starts;
this recipe uses 11 for the lead and ten steps, leaving one retry. To allow up to
two attempts for each step, explicitly start the hub with `--max-job-starts 21`.
Concurrent workers still obey `--max-job-agents` (default 5). These are launch
limits, not a monetary budget.

The final stage is **release readiness, not deployment**. This recipe does not
create GitHub repositories, push, or call Dokploy. Existing publishing permissions
remain in effect. Missing credentials, unresolved decisions, exhausted attempts,
or a lost lead can still require intervention. Real-agent completion quality and
unattended product deployment have not yet been validated.

API: `POST /playbooks/mvp-v1/install` and `POST /products` under `/v1`.
The product request includes `request_id`, `title`, `brief`, and either `project`
or `repo` plus `device`/`verify` as needed. Requests from agents are refused;
workspace members can start builds using their authorized machines, and only the
owner/admin can install profiles.
