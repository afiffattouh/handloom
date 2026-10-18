# factory-kit

The project side of a software factory: one way for every repo to say what "done" means, and
a gate that coding agents cannot talk their way past.

This kit is part of Handloom and ships inside the `handloom` program (`handloom kit init`, `handloom kit handover`;
every command below also works as `handloom kit <command>`). It does not need Handloom: a project that has been set
up carries its own copy, so its check runs on any machine, in CI, and with any agent or person, with no hub and
nothing installed but Python 3.8+. From a checkout of this repository, `python3 kit/bin/factory ...` does the same.

It does not run agents. Handloom runs them (task, worktree, review, land). factory-kit is what
Handloom, CI, pre-commit, a plain Claude Code session and a person all call the same way:

```
./check                              every stage, stop at the first failure
./check --fast                       the quick pass (agents use this while working)
./check --gate --spec specs/x.md     the gate: what a machine runs before a person reviews
```

Exit codes: `0` pass, `1` fail, `2` configuration error, `3` blocked (the agent says honestly
that the task cannot be done as specified). Every run writes `runs/<time>-<mode>/`: one log per
stage and `summary.json`. The terminal gets one line per stage and the tail of the first failure.

A caller can also set `HANDLOOM_BASE`, `HANDLOOM_SPEC` and `HANDLOOM_MODE` (`submit`, `merge`, `maintain`)
and run the same `./check` for every task; see `docs/contract.md`. The last lines of output always give the reason in words.

## What it needs

Python 3.8 or newer and a POSIX shell (Linux, macOS, WSL; on plain Windows run `python .factory/check.py`).
A stock Mac's Python 3.9 is enough. Before Python 3.11 the kit reads `factory.toml` with its own small
reader (`.factory/_toml.py`), tested against `tomllib`. With no suitable Python, `./check` says what to
install and exits 2. Nothing else is installed: no packages, no network.

## Set a project up

```
factory init [DIR]
```

It adds, and never overwrites:

| File | What it is |
|---|---|
| `factory.toml` | The stages, in order (detected from package.json, pytest/unittest, go.mod, Cargo.toml, an existing `check.sh` or `make check`). A person owns it. |
| `./check`, `.factory/` | The runner, gate and hooks, vendored. Standard library Python only, so any machine or fresh worktree runs them with nothing installed. |
| `AGENTS.md`, `CLAUDE.md` | A short guide for agents (a map, not a manual; `./check` fails it past 100 lines). `CLAUDE.md` imports it. |
| `specs/TEMPLATE.md` | Goal, Files (the only paths that may change), Out of scope, Checks (commands that prove it). |
| `tests/acceptance/` | Tests that define done. A person owns them; `.manifest` holds their hashes. |
| `LESSONS.md` | What went wrong, with dates. Seen twice means it becomes a check. |
| `.claude/settings.json` | Two project-local Claude Code hooks (merged with what is there). |

`factory init --preset web` adds link, image-alt, page-title and size checks for a site (`.factory/checks/web.py`,
configured under `[web]`). Without a preset it detects the stages as above.

Then review `factory.toml`, run `./check`, and commit on the base branch.

## A task, start to finish

The owner defines done; the agent does the work; the machine decides.

```
factory task new parser-fix          # branch task/parser-fix + specs/parser-fix.md
# fill in the spec, add tests under tests/acceptance/ that fail today
factory task seal parser-fix         # commit spec + tests as the owner; that commit is the gate's base
git worktree add ../repo-wt-parser-fix task/parser-fix    # or let Handloom make the worktree
# the agent works there and runs:
./check --gate --spec specs/parser-fix.md --base task/parser-fix-base
```

Acceptance tests for unfinished work cannot sit on `main` (it would go red), so they live in the
task branch's first commit. Measured against that commit, the agent can neither skip them nor
edit them. `seal` refuses a spec that still has the template's placeholders.

A fresh worktree has no `venv` or `node_modules`. List them under `[worktree] link` in
`factory.toml`; `./check` links them from the main checkout and keeps them out of git.

## What the gate checks

In order, stopping at the first problem:

1. **`BLOCKED.md`** exists: exit 3. The agent could not do the task as specified and says why.
   An honest stop beats a forced pass; agents fabricate when "done" is impossible.
2. **Protected files** unchanged against the base branch: `tests/acceptance/`, `factory.toml`,
   `.factory/`, `./check`, `.claude/settings.json`. Each of these can make the gate pass without
   the work. This compares with git, so a regenerated manifest does not hide an edited test.
3. **Scope:** every changed file is in the spec's `## Files` list (plus `specs/`, `LESSONS.md`,
   `BLOCKED.md`), and no more than `max_changed_files` changed. Build caches are ignored.
4. **The stages**, then the spec's `## Checks` commands as `spec-1`, `spec-2`, ...

Plain `./check` also fails when an acceptance test no longer matches `.manifest`, and when
`AGENTS.md` grows past its limit.

## The hooks (Claude Code only)

- **PreToolUse** refuses Edit/Write on the files a person owns and tells the agent to write
  `BLOCKED.md` if it cannot do the task without them.
- **Stop** runs `./check --fast` when the session changed files and refuses to let the agent
  finish while it fails. It never traps an agent: a second stop in a row, `BLOCKED.md`, a clean
  tree or `FACTORY_STOP_CHECK=0` lets it stop.

Verified live with Claude Code 2.1.299: the agent was refused an edit to an acceptance test and
did not route around it; asked to make a change that broke a test, it was stopped twice and
wrote `BLOCKED.md` instead of weakening the test.

A shell command can still write a protected file, and OMP, Pi, OpenCode and Codex have no such
hooks. The gate is the real protection; the hooks make the honest path the easy one.

## Use it from

- **Handloom:** set the job's check command to `./check`. Handloom runs it on the machine that holds the files,
  tells it what it is checking (`HANDLOOM_MODE` is `submit` after a submit and `merge` after a merge;
  `HANDLOOM_BASE` is the job's base), keeps the run's `summary.json` with the job, and shows exit 3 as
  "the agent says this cannot be done as specified, so a person decides" and exit 2 as "the project's own checks
  are broken". The agent's own claim never decides. (`./check --gate --spec specs/<task>.md --base <ref>` still
  works if you want the scope check for one sealed task.)
- **CI / pre-commit:** `./check` (or `./check --fast` in pre-commit).
- **Any agent session:** the agent runs `./check --fast` while working; `AGENTS.md` tells it so.

## Hand a project over

```
factory handover [DIR] --approver <github user>
```

For a finished, green project that someone else will look after with any agent: a CI workflow that runs
the check on every pull request, owners for the protected files, `MAINTAIN.md` in plain words for the
person who maintains it, and a checklist (with the branch-protection command for an admin to run).

- `--preview {cloudflare,netlify,vercel,none}` (default none): records the preview host in `factory.toml`. `MAINTAIN.md`
  only talks about a preview link when one was chosen; the checklist says how to connect the host (it needs an
  account, so the kit writes no host files).
- `--for {client,operator}` (default client): `operator` writes `MAINTAIN-operator.md`, the short guide for whoever
  runs the agent on the owner's behalf: running `./check`, what it refuses, reading `runs/*/summary.json`, exit 3
  and 2, approval, undo.

## Other commands

```
factory describe [DIR]       what this project's check covers, in plain words (--json for programs)
factory accept-tests [DIR]   record the acceptance tests in .manifest (a person, on the base branch)
factory upgrade [DIR]        refresh the vendored .factory/ files and ./check (commit on the base branch)
factory lessons [DIR]        list lessons seen twice that are not yet a check
```

## Without Handloom

```
sh install.sh                # from this folder (or KIT_DIR=/path/to/kit sh install.sh)
sh install.sh --uninstall
```

Copies the kit to `${FACTORY_HOME:-$HOME/.local/share/factory-kit}` and puts a `factory` command in
`${BIN_DIR:-$HOME/.local/bin}` (it finds Python 3.8+ itself). If that folder is not on your `PATH`, it prints the
line to add. Running it again is safe; it will not overwrite a folder or command it did not make.

## Why these pieces

From research on what works for agent coding in 2025-2026 (sources in `docs/research.md`):
deterministic checks before any model review; tests the implementing agent cannot change (agents
game tests, stronger models more so, and an honest "blocked" exit cuts cheating sharply); a short
spec with an explicit file list; a short `AGENTS.md`; evidence written by the machine, not the
agent; and lessons that become checks, because agents ignore prose but not a failing check.

Two lessons from a local-model trial (Oct 2026), now in `AGENTS.md` and the spec template's `## Risks`:
agents write tests for the normal case only. Given "SKUs come from customers' orders", one wrote a
file named by the SKU with no path check, and one returned a cached object the caller could change.
Both passed their own tests. Acceptance tests written by the owner, for the risks, catch both.

Out of scope on purpose: running agents, choosing models, parallel worktrees, reviewer agents.
Those belong to Handloom. Add a part here only after a measured failure.

## Tests

```
python3 -m unittest discover -s kit/tests      # from the repository root
```
