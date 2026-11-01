# Managed task plans (initial implementation)

A job can now accept a structured plan. The hub creates the task graph once and
starts workers as dependencies become ready. Existing jobs keep their current
manual coordination workflow. This is the first execution foundation for the
product-delivery playbook, not the complete autonomous product builder.

Create an ordinary job with a lead and a machine, then submit a JSON file:

```
handloom job plan 12 --file plan.json
handloom job plan 12
handloom job plan 12 --json
```

Example (replace profile names with installed profiles):

```json
{
  "version": 1,
  "max_attempts": 2,
  "steps": [
    {"key": "build", "title": "Implement the first user journey", "body": "Implement the brief and record how to test it.", "profile": "coder"},
    {"key": "qa", "title": "Check the first user journey", "body": "Exercise success and failure cases; record evidence and defects.", "profile": "tester", "depends_on": ["build"]}
  ]
}
```

A human may supply `device` when the job has no machine. A lead uses its own
machine and may submit only its own job's plan. Worker and observer agents cannot
submit plans. The plan must be submitted before any tasks are created.

The HTTP endpoints are `POST /v1/jobs/{id}/plan` and `GET /v1/jobs/{id}/plan`.
Repeated identical submissions return the same recorded tasks. A different plan
is refused; live replanning/version updates are not implemented in version 1.
Every referenced profile is pinned to its exact version on first submission.

Each step gets a unique worker name derived from job, task, and attempt IDs.
Dispatch intent, assignment, and attempt count commit in one transaction.
Dependencies must be accepted; in repository jobs their integration merge must
also pass. Dependent worktrees start from that integration branch. The normal
verification and review mechanisms still apply: this change does not automatically
accept work, replace the lead, close jobs, publish repositories, or deploy products.

Failed starts and offline workers whose task has returned to `open` are retried
up to the plan's attempt limit (default 2, maximum 3), within the existing job
start and concurrency limits. A claimed task first waits for its lease to expire.
A live worker is never replaced merely because its task is open. An older attempt
cannot claim the task while its replacement registers. Unsubmitted edits remain
in the previous worktree; replacement agents must use the existing handoff note
and inspect previous work before repeating external actions. This is task-level
recovery, not automatic restoration of an agent's context or uncommitted changes.

The link retires completed plan workers after successful integration, cancelled
workers, and superseded attempts, using only its own tmux session. It reports a
worker offline after stopping its window. Retiring workers releases capacity;
process state is not assumed to survive restarts. Missing tmux sessions are
handled by the existing liveness leases.

Use `job plan` to see waiting dependencies, capacity limits, unavailable machines,
and exhausted attempts. The existing task and job pages show the generated tasks.
Do not manually reassign a managed task: its plan owns the worker identity.
There is no retry-counter reset endpoint in this initial version.

The broader product direction is: compiled brief, specialist artifacts and reviews,
independent QA, bounded repair, and verified GitHub/Dokploy delivery. Remaining
work includes run-level authorization and budgets, automatic lead recovery,
structured noninteractive executors, versioned replanning, deployment reconciliation and the simplified product UI. The built-in MVP recipe
and one-command launch are now available; see [MVP playbook](mvp-playbook.md).

## Local development and validation

The implementation passes `go test ./...`, `go vet ./...`, and
`CGO_ENABLED=0 go build -trimpath -o bin/handloom ./cmd/handloom`. The 66 Python
kit tests also pass. Tests cover persisted plans restored into a new hub,
dependency and integration gates, profile pinning, bounded retries, stale worker
claims, launch cancellation, worker cleanup, and upgrading existing databases.

Start a development hub with an explicit local base URL (without it the web UI
returns 503 even though the health check succeeds):

```sh
HANDLOOM_BASE_URL=http://127.0.0.1:7420 bin/handloom hub serve \
  --auto-init --data /workspace/.handloom-dev/data --addr 127.0.0.1:7420
```

Keep the initialization output private: it contains local bootstrap credentials.
The compiled binary was smoke-tested on local port 17420: `healthcheck` succeeded,
and both `/healthz` and `/setup` returned 200 with the expected health response
and account-setup form. No external deployment was made.

The worker lifecycle tests use controlled executors. A real authenticated agent
run with tmux, and GitHub/Dokploy product delivery, remain to be validated before
claiming unattended product delivery.
