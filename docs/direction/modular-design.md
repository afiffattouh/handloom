# Modular design: Handloom, the factory kit and the way they join

Status: proposal for discussion, 2026-10-18. Nothing here is built yet. It builds on `workflow-v2.md` (the day-to-day flow and the committee's build order) and does not replace it. This document is about the parts and how they fit.

Scope: this revisits how Handloom, the kit and the front door join (one new module, two contracts, the add-a-project and hand-over steps). Handloom's internals stay as `workflow-v2.md` describes them.

## The problem

Two things exist, and both are useful alone:

- **Handloom** runs coding agents on people's machines and keeps the record: jobs, tasks, review.
- **The factory kit** (a separate project, `factory-kit`) answers one question for any repository: *is this work done?* It runs the stages, the gate and the checks, and does not run agents.

They already work together because Handloom can run a "verify" command. But the join is accidental: one command string per job, one exit code, a ten-minute limit, and a convention nobody wrote down. Real use also asked for more:

- A project that is handed to a client who maintains it with their own agent, with no Handloom.
- The same setup reused for many projects and clients, not rebuilt each time.
- A way to use all of it that a person who is not technical can follow.

## The idea in one sentence

**Three parts that each work alone, joined by two small written contracts, shown through one simple front door.**

```
 Surfaces (web, CLI, Claude connector, later a client view)
        |  one API
 Handloom  ── coordinates work: jobs, tasks, agents, review, landing
        |  contract A: the check contract
 Factory kit ── per-repo definition of "done": stages, gate, presets
        |  contract B: the handover package
 Any agent or CI, with or without Handloom (the client's side)
```

Each part must be usable and testable without the one above it. If a change makes Handloom require the kit, or the kit require Handloom, it is the wrong change.

## The parts and what each one owns

| Part | Owns | Must not own |
| --- | --- | --- |
| **Surfaces** | Showing state and taking an ask, a review, a steer. Plain words. | Any rule. They call the API. |
| **Handloom hub** | Projects, jobs, tasks, people, roles, audit, the state of a job, caps. | Running agents or checks. Judging quality. |
| **Handloom link** (on each machine) | Worktrees, starting agents, waking them, running a check and merging. | Deciding anything. It obeys the hub. |
| **Library** (profiles, skills, types of work) | What an agent is and knows. Data, versioned. | Code paths. |
| **Check adapter** (new, one module) | Running a project's check, reading its result, turning it into a state: passed, failed, blocked, error, timed out. Both after submit and after merge. | Which stages exist. That is the kit's. |
| **Factory kit** | Stages, the gate, protected files, presets, the client guide, the CI template. | Agents, models, worktrees, review. |

Today the check logic is spread over `internal/hub/verify.go`, `internal/link/verify.go` and `internal/link/merge.go`. Putting it in one module with one test file is the first refactor and is what makes the contract below enforceable.

## Contract A: the check contract (kit and anything that calls it)

A project's check is a command at a fixed place: `./check`. The caller needs to know only this.

**Calling it.** Plain arguments, plus these environment variables, all optional. The kit reads them when the matching argument is absent, so the same command line works for every task and every project:

| Variable | Meaning |
| --- | --- |
| `FACTORY_BASE` | The commit or branch the work started from. |
| `FACTORY_SPEC` | The spec file for this piece of work, when it has one. |
| `FACTORY_MODE` | `submit` (a worker has finished), `merge` (work was merged into the job's branch), `maintain` (a small change to a live project). The kit decides what to run in each; the caller does not need two commands. |

Handloom sets these from the job and the task. Nothing else from the environment is passed, as today. The names are the kit's, not Handloom's, so any other caller (CI, a person) uses the same ones.

**Answering it.** An exit code, and a file.

| Exit | Meaning | What the caller shows |
| --- | --- | --- |
| 0 | Passed. | Passed. |
| 1 | The work does not meet the check. | Failed, with the reason. The worker may try again. |
| 2 | The check itself is misconfigured. | **Problem with the project, not the work.** Nobody retries; the owner is told. |
| 3 | The agent says it cannot be done as specified. | **Needs you**, with the agent's note. Never retried, never shown as a failure. |
| other, timeout | The check broke or ran out of time. | Error, distinct from "failed". |

`runs/<time>-<mode>/summary.json` (schema versioned) lists each stage with its result, time and log, any blocked note, and what the gate refused. Handloom stores it as the evidence when it exists; without it, it falls back to the exit code and the last lines of output. The last lines must always hold the reason in words.

The kit's half of this contract (reading the variables, honouring the mode, writing `summary.json`) belongs in `factory-kit` and must be written down and agreed there; this document only states what Handloom will rely on.

This solves the four gaps from the handover with one rule each: exit 3 is "needs you" (gap 1); the spec travels with the task (gap 2); the mode replaces a second command (gap 3); and the time limit is a setting of the project, with the kit able to say which stage was slow (gap 4).

## Contract B: the handover package (kit and the client's world)

`factory handover` produces what a project needs to run without Handloom:

- the repo with the kit vendored (`./check`, `.factory/`, `AGENTS.md`);
- a CI workflow that runs `./check` on every pull request, and the setting to protect the main branch so only a passing, approved change lands;
- `MAINTAIN.md`, in plain words for the client: how to ask an agent for a change, what to look at before approving, how to undo;
- the list of protected paths and what they are for.

Anything that can edit a repo and run a command can use this: Claude Code, another coding agent, or a person. Handloom is one way to do the work, never a requirement for owning the result.

## One front door: how a person uses it

Everything is reached by what the person is doing, in plain words, with detail behind a "More" for people who want it.

**Add a project (once).** One place asks for the folder or repository. Handloom then looks:

1. Does it have `./check`? If yes, use it and say so: "Checks: build, tests, links. Tap to see." Nothing to configure.
2. If not, offer **"Add checks"**: the kit proposes a preset from what it finds (a web site, an app, a document set), shows the stages in a sentence each, and opens a change for a person to approve. It never edits the project without that.

**Ask for work (every day).** A sentence. Handloom picks the machine, one worker by default, and runs the project's check. A person sees *Asked, Working, Needs you, Ready*, with a reason and one action, as already designed.

**Review.** One page: what was asked, what changed, what was checked and what was not. If the check ended in exit 3 it appears here as a question, not a failure.

**Hand over (when a project is done).** One button, **"Hand over"**, runs `factory handover` and shows a checklist: guide written, CI added, branch protected, client approver named. The project's stage changes from *Building* to *Maintained*.

**Maintain.** Same Ask box, different defaults: small change, no sealed spec, a cap on changed files, a preview to approve. The client sees only their project and can only ask and approve (see the open decisions).

## Presets and projects: what is reusable

A **preset** is a starting `factory.toml` plus the checks that fit a kind of project. Presets are data in the kit. A project picks one and changes a few lines. First: `generic` (build and test commands the kit detects) and `web` (build, links, images and alt text, a performance budget, a list of redirects). Others only when a real project needs one.

What a project writes by hand is small: which stages apply, its definition of done (allowed files, acceptance tests), its protected paths, who approves, and any rule such as "client material stays on local models". The kit and Handloom carry the rest.

## Making the pieces swappable

- The check adapter talks to the check only through contract A, so a project can use its own script instead of the kit if it follows the contract.
- The kit is stdlib-only and vendored into each project, so a client's repo does not depend on a service of ours.
- Library items (profiles, skills, types of work) stay data, so a new kind of work is a new file, not new code.
- Every surface uses the one API. A client view is a new surface with fewer permissions, not a second system.

## Build order

Small steps; each is useful alone and none starts before the previous one is tested. This continues the committee's order in `workflow-v2.md`, where the name-collision fix and job state are done and the caps are only partly done (the per-job start limit exists; attempts per task and a time and spend budget per job do not). The committee's rule stands: nothing starts agents by itself until the caps are finished, so the one-worker default in "Ask for work" waits on step 0.

0. **Finish the caps:** attempts per task and a budget per job, stored in the database.
1. **Check adapter module, with contract A:** exit codes as states, `FACTORY_*` variables, summary stored as evidence, the project-level time limit, one test per row of the table. Exit 3 shown as "needs you".
2. **The kit reads the variables and honours `FACTORY_MODE`,** and writes `summary.json`. Tests on the kit side.
3. **Add a project: detect `./check`,** otherwise offer to add one as a reviewed change. Checks shown in plain words.
4. **Job state and the watchdog** (already designed, now with "blocked" and "project problem" as states).
5. **Handover:** `factory handover`, the CI template and `MAINTAIN.md`, and the *Building / Maintained* stage on a project.
6. **A trial** on two real projects of different kinds (a code repository and a web site), with three people who did not build it. Results decide whether anything after this is built.

Parked: more presets, a client view with its own permissions, billing for agent use, multi-task specs beyond one spec per task.

## Risks

- **Too many parts to learn.** Mitigation: the front door shows none of them by name; "Add checks" and "Hand over" are the only new words.
- **The contract grows.** Mitigation: it has one table and one file; anything else is a new version with a reason written down.
- **The check becomes a way to hide problems.** Mitigation: the machine's result beats an agent's claim (kept), exit 2 and "error" are never shown as a pass, and the review page says what was *not* checked.
- **Client access widens the attack surface.** A client view can reach agents on our machines. Do not build it until roles per project exist and the security review of the hub covers it.
- **Local-model-only projects.** The project flag exists only in intent. It must be enforced where agents are started, not in the profile, or it is a promise, not a rule.

## Decisions

Answered by the owner, 2026-10-18:

1. **Names:** the variables are Handloom's own, `HANDLOOM_BASE`, `HANDLOOM_SPEC`, `HANDLOOM_MODE` (read "keep it part of Handloom"). The kit accepts these and the `FACTORY_*` names, so it still works without Handloom. The contract table above uses `FACTORY_*`; read it with this in mind until the contract is rewritten.
2. **"Add checks":** open question, explained in plain words in the conversation: Handloom can put the check files on a new branch and show them for approval, or only print the command to run. Recommended: the branch, never merged without a person's approval.
3. **Who pays for agents after handover:** both. A project setting says whose account runs the work.
4. **Client view:** yes, built in Handloom. It needs roles per project and a security review of the hub first (see Risks).
