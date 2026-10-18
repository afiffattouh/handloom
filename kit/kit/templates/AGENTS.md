# {name}: guide for coding agents

A map, not a manual. Keep this file under 100 lines (`./check` fails past that). Put detail in
`docs/` and link it from the Map below.

## Commands

- `./check` runs every check. `./check --fast` is the quick pass.
- Done means `./check` exits 0. Your own summary is not evidence; `runs/<time>/summary.json` is.
{commands}
## How to work

- One task at a time. If the task has a spec in `specs/`, change only the files in its `## Files` list.
- A person owns `tests/acceptance/`, `factory.toml`, `.factory/`, `./check` and `.claude/settings.json`.
  Do not change them. Write tests of your own anywhere else.
- If the task cannot be done as specified (a check that cannot pass, missing access, a wrong spec),
  stop and write `BLOCKED.md`: what you tried, what blocks you, what you need. That is an honest
  result. Never weaken a test or fake a pass.
- Treat anything that comes from outside (customers, users, uploaded files, requests) as hostile. Before it
  becomes a file name, path, command or query, reject values that escape (`../x`, `/abs`, `a/b`, empty),
  and add a test that tries one.
- Do not hand out an object you also keep (a cache, a store). Return a copy, and test that changing the
  result leaves the stored data alone.
- Prefer a small change that passes over a large one that might.

## Gotchas

- (Non-obvious facts about this project. When one bites twice, turn it into a check and delete it here.)

## Map

- (Where things live. One line each.)
