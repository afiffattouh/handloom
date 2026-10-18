# Running the agent for {name}

This guide is for the developer or agency who runs a coding agent on behalf of the person who owns {name}.
The owner's own guide is `MAINTAIN.md`. Ask {contact} about anything that is not here.

## Run the agent

1. Open a branch for the change. Never work on the live branch.
2. Give the agent the request in plain words, one small change at a time. It reads `AGENTS.md`.
3. While it works, the agent runs `./check --fast`. Before you open the pull request, run
   `./check --mode maintain` yourself. It must print PASS.

## What the check refuses

- A change to a protected file (the agent cannot make the check pass by editing the check):

{protected}

- More changed files than `[maintain] max_changed_files` in `factory.toml`.
- Any stage in `factory.toml` that fails, or a `BLOCKED.md` file in the project.

## Read the result

Every run writes `runs/<time>-<mode>/`. Open `summary.json` there:

- `result` and `exit_code` say what happened, `reason` says why in one sentence.
- `stages` lists each check with `status` (pass, fail, skip, blocked) and a `log` file with the details.
- `refused` lists each protected file the change touched. `changed_files` lists what changed.

## Exit codes

- **0:** passed. Open the pull request.
- **1:** the work does not meet the check. Give the agent the reason and the log and let it try again.
- **3, blocked:** the agent wrote `BLOCKED.md` because it cannot do the request as written. Do not retry.
  Read the note, then change the request or tell {contact}.
- **2, broken checks:** `factory.toml` or the spec is wrong, not the work. Do not retry and do not edit
  `factory.toml` to get past it. Tell {contact}.

## Ask for approval

Open a pull request. The check runs on it automatically. {approver_line} Do not merge a change with a red cross.
{preview_note}

## Undo

Open the merged pull request in GitHub, press **Revert**, and get the revert approved like any other change.
