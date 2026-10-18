# The contract with callers (version 1)

For Handloom, CI, a person, or any other tool that runs a project's check. Nothing else about the
kit is needed to call it. Handloom's side is described in `docs/direction/modular-design.md`.

## Calling `./check`

Plain arguments work as before. A caller that runs the same command for every task sets variables
instead. For each setting, an argument beats `HANDLOOM_*`, which beats `FACTORY_*`.

| Variable | Argument | Meaning |
| --- | --- | --- |
| `HANDLOOM_BASE` / `FACTORY_BASE` | `--base` | The branch or commit the work started from. Default `[gate] base`, else `main`. |
| `HANDLOOM_SPEC` / `FACTORY_SPEC` | `--spec` | The spec file for this piece of work. |
| `HANDLOOM_MODE` / `FACTORY_MODE` | `--mode` | What to run (below). Unknown value: exit 2. |

| Mode | Meaning | What runs |
| --- | --- | --- |
| `submit` | A worker has finished. | The gate (`BLOCKED.md`, protected files, scope, the cap), then every stage, then the spec's `## Checks`. |
| `merge` | Work was merged into an integration branch. | Every stage. No scope check, and the spec is ignored, because the branch holds more than one task. |
| `maintain` | A small change to a live project. | The gate without a spec, capped by `[maintain] max_changed_files` (default 10). |

`--gate` and `--fast` still win when given.

## Answering

| Exit | Meaning | The caller shows |
| --- | --- | --- |
| 0 | Passed. | Passed. |
| 1 | The work does not meet the check. | Failed, with the reason. The worker may try again. |
| 2 | The check itself is misconfigured (bad `factory.toml`, unknown mode, spec without a `## Files` list). | A problem with the project, not the work. Nobody retries. |
| 3 | `BLOCKED.md`: the agent says it cannot be done as specified. | Needs a person, with the agent's note. Never retried. |

The last lines of output always say why, in words:

```
FAIL: unit: exit 1 -> runs/20261018T101500Z-gate/unit.log
evidence: runs/20261018T101500Z-gate/summary.json
```

## `runs/<time>-<mode>/summary.json` (schema 1)

`schema`, `kit_version`, `mode` (`gate`, `fast` or `full`: what ran), `requested_mode` (as asked, when
given), `started`, `result`, `exit_code`, `reason` (the sentence above), `seconds`, `slowest_stage`,
`git_sha`, `base`, `changed_files`, `blocked` (the contents of `BLOCKED.md`), `refused` (each file the
gate refused and why), `notes`, and `stages`: for each, `name`, `status` (pass, fail, skip, blocked),
`seconds`, `detail`, `log`, and `timed_out` when a stage hit its limit. New fields may be added;
existing ones keep their meaning within a schema version.

## Handing a project over

`factory handover [DIR] [--approver USER]` makes a green project ready to be looked after without
Handloom. It adds, and never overwrites:

- `.github/workflows/check.yml`: runs `./check` in `maintain` mode on every pull request and in
  `merge` mode on every change to the main branch.
- `.github/CODEOWNERS`: the protected files need the approver (only with `--approver`).
- `MAINTAIN.md`: how to ask an agent for a change, what to look at before approving, how to undo.
- `stage = "maintained"` in `factory.toml`.

It prints a checklist, including the branch-protection command for a person with admin rights to run.
It does not run it. It refuses a project whose `./check` fails.
