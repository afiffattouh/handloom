# Modular design, version 2: kits, Handloom and the surfaces

Status: revised 2026-10-18 after the journey review; the kit moved into this repository the same day (`kit/`, shipped inside the program as `handloom kit`). Version 1 (earlier the same day) described the parts and two contracts. This version keeps them, adds what the review found, and states what was validated. Built items are marked. It builds on `workflow-v2.md` (the day-to-day flow) and does not replace it.

## The idea in one paragraph

**Kits** define "done" for a kind of project and can be dropped into any repository. **Handloom** runs agents and keeps the record, and works with or without a kit. **Surfaces** show it to people, one per audience. Each layer works alone. The only coupling is a short written contract, so a client can be given a kit with no Handloom, a team can use Handloom with no kit, and a project can go through both end to end.

```
 Surfaces      owner console   ask box (team)   Claude connector   client view (later)
                      \              |                /                  /
 Handloom       jobs, tasks, agents, review, caps  ──  check adapter ──┐
                                                                       │ contract
 Kits          factory kit (built)   media / research / content / data  │ (calls ./check)
               (each: ./check, presets, handover)  <───────────────────┘
                         │
 The project   repo + ./check + .factory/   works for any agent, CI or person, with or without Handloom
```

## What was validated, 2026-10-18

- **Handloom does not depend on the kit.** The Handloom code has no reference to the kit. It runs whatever check command the job names.
- **The kit does not depend on Handloom.** Its only mention is the names of three optional environment variables, and it accepts `FACTORY_*` as well.
- **The kit alone works end to end.** On a throwaway client website, with no Handloom: `factory init --preset web`, a passing check, an agent change with a dead link and a missing image caught by the maintain-mode check, the fix passing, an attempt to empty a check refused by the gate, a handover that wrote the CI workflow, CODEOWNERS and `MAINTAIN.md`, and a fresh clone passing with no `factory` command on the path.
- **Two blockers were found.** Nobody else could install the kit (it existed only on one machine). It needed Python 3.11 or newer, which a stock Mac does not have. Both are fixed: the kit now lives in `kit/` in this repository and ships inside the `handloom` program (`handloom kit init`), and it runs on Python 3.8+.

## Three ways to work

| Way | For | They install | They get |
| --- | --- | --- | --- |
| **Kit alone** | An agency or a client who maintains a project with their own agent. | The kit once (to scaffold). The project then carries its own copy in `.factory/`, so no install is needed to run it. | Checks, a gate agents cannot talk past, CI on every pull request, `MAINTAIN.md`. |
| **Handloom alone** | A team that wants agents on its machines and a review step, on a project with no kit. | The hub and a link on each machine. | Jobs, review, history. The job names any check command. |
| **End to end** | An owner who runs work for clients and hands the result over. | Both. | Everything above, plus the check shown in plain words, evidence stored with the job, and the handover as a button. |

The same project moves between ways without change: a project the kit scaffolded can later be added to Handloom, and a project run through Handloom can be handed over.

## The layers and what each owns

| Layer | Owns | Must not own |
| --- | --- | --- |
| **Surfaces** | Showing state; taking an ask, a review, a steer; plain words. | Any rule. They call the API. |
| **Handloom hub** | Projects, jobs, tasks, people, roles, audit, job state, caps (attempts, time, spend). | Running agents or checks; judging quality. |
| **Handloom link** (each machine) | Worktrees, starting agents, waking them, running the check, merging. | Deciding anything. |
| **Check adapter** (one module, planned) | Running a project's check; turning the exit code into a state; storing the evidence. | Which stages exist. |
| **Library** | Profiles, skills, starters. Data only. | Code paths. |
| **Kit** | Stages, the gate, protected files, presets, the handover package. | Agents, models, worktrees, review. |

## The kit contract (version 1, written down in `factory-kit/docs/contract.md`)

A **kit** is anything that offers this, for one kind of project:

1. **A command, `./check`,** that answers "is this work done?" with exit codes 0 pass, 1 fail, 2 the project's checks are broken, 3 an agent says it cannot be done as specified. A last line says why, in words. Built.
2. **Inputs by variable,** `HANDLOOM_BASE`, `HANDLOOM_SPEC`, `HANDLOOM_MODE` (`submit`, `merge`, `maintain`), with `FACTORY_*` accepted. Built.
3. **Evidence,** `runs/<time>-<mode>/summary.json`, schema versioned, with stages, reason, refusals, and the agent's note. Built.
4. **A preset** to start from, and a **handover**: CI on every pull request, owners for the files that define correct, a guide for the person who maintains it. Built for the factory kit and its web preset.
5. **A runtime floor:** Python 3.8 or newer and a POSIX shell, nothing installed, no network. Built and tested on 3.8, 3.9 and 3.12. Before Python 3.11 the kit reads `factory.toml` with its own small reader. With no suitable Python, `./check` says what to install and exits 2. On plain Windows run `python .factory/check.py`.
6. **A way to describe itself in plain words,** so a surface can show "Checks: links, images, titles, size". Planned: `factory describe --json`.

Why Python 3.8 and not a hosted installer: there is no way to install a newer Python that needs no technical skill on a stock Mac, and asking a client to do it is the kind of friction this design exists to remove. Supporting what is already there was the better choice.

**Other kits** (media, research, content, data, security) follow the same contract. The runner, gate, summary and handover are generic but live inside one package. They become a shared core when the second kit is built, and not before. A kit is added after a real project has failed in the way it would catch.

## Two tracks for the person who looks after a project

You said the maintainer can be either an agency or the client. They need different things, and they use the same kit.

| | **Agency track** | **Client track** |
| --- | --- | --- |
| Who | A developer or agency maintains the site on the client's behalf. | The client's own staff ask for changes. |
| How they reach an agent | Their own tools, or Handloom. | A web page that takes a sentence (the client view) or the client's own AI app. |
| What they see | The pull request, the check result, a preview link. | The sentence they asked, a preview, one Approve button, and Undo. |
| What exists | The kit, handover, CI. A preview and an agency version of `MAINTAIN.md` are still to build. | Nothing yet. It needs roles per project and a security review of the hub. |
| Honest status | Buildable now. | Parked until its prerequisites exist. |

Until the client track exists, the handover checklist asks the owner to name who operates the agent, and `MAINTAIN.md` is written for that person (a short version for the client says what to ask for and how to approve).

**Preview.** Every preset's handover should produce a link a client can open before approving. The lowest-friction route is not building one: hosting such as Cloudflare Pages, Netlify or Vercel gives every branch a preview address once the repository is connected. The kit supplies the build output folder (`[web] dir`) and the handover checklist gets one line, "connect preview hosting", so `MAINTAIN.md` can point to "the preview link on the pull request" truthfully. Open decision: which host to recommend.

## What the review found, and the change for each

| Finding (review) | Change | Layer | Status |
| --- | --- | --- | --- |
| 1. Only the author has used it. | Watch three people who did not build it, doing four fixed tasks. | process | Next, and cheap. |
| 2. The ask is a four-section form with a required lead profile. | One sentence. The project stores its check; the machine is chosen; one worker by default; "Get steps first" as an option. | Handloom, surfaces | Designed (workflow-v2 steps 4 and 5). |
| 3. The client journey assumes a technical client. | Two tracks (above), a preview, and `MAINTAIN.md` for the right reader. | kit, surfaces | Preview and guide: next. Client view: parked. |
| 4. Acceptance tests by hand are the hidden cost. | In the job form, choose a sealed task and fill spec, base and check from it. Draft acceptance tests from the spec's Risks with a different agent than the implementer, for the author to read before sealing. | kit, Handloom | Designed. |
| 5. Review lacks cost, "checked / not checked", visible Stop, and there are no caps. | Show `summary.json` on the review page; cost per ask; Stop on the Overview; caps on attempts, time and spend stored in the database. | Handloom | Caps first. |
| 6. Exit 3 and 2 read "failed". | The check adapter maps the exit codes: 3 is a question for a person, 2 is a project problem. It passes the environment variables. | Handloom | Next after caps. |
| 7. Words. | Overview, Helpers, Computers, Use this result, Finish. Hide lead and profile unless "a team" is chosen. | surfaces | Designed. |
| 8. Three tools at handover. | A "Use the project's checks" choice in the job form; offer to apply branch protection through the owner's `gh` login after confirmation. | Handloom, kit | Designed. |
| 9. Setup code in a log. | Show it where the owner already is, with the hub's start message. | Handloom | Idea. |
| 10. Overview leads with a monitor. | Inbox first. | surfaces | Easy. |

## Build order

Small steps; each is useful alone. The committee's rule stands: nothing starts agents by itself until the caps exist.

Done:
- **Kit contract version 1:** variables, modes, exit codes, reason line, `summary.json`, web preset, handover, runs on Python 3.8+.
- **The kit is part of Handloom:** `kit/` in this repository, tested in CI, embedded in the program; `handloom kit init|handover|task|upgrade` unpacks it once per version and runs it. A set-up project still carries its own copy, so it needs neither the program nor a hub.
- **Check adapter:** the link passes `HANDLOOM_MODE` (`submit` or `merge`) and `HANDLOOM_BASE` to the check, reads the `summary.json` the kit names on its last line (inside the worktree only, schema 1 only), and the hub keeps it with the check. Exit 3 shows as "blocked: a person decides" with the agent's own note, exit 2 as "the project's own checks are broken", and a lead is told not to send either back to be retried. Any other command keeps "failed, exit N".
- **Cap on attempts:** a task whose work is sent back 3 times (`--max-rejects`) stops, and the owner is asked in the inbox what to do (rewrite, change helper, give up). Already there: agents per job (`--max-job-agents`) and time per task (`--max-task-time`).
- **Spec reaches the check:** a base named `task/<slug>-base` with `specs/<slug>.md` in the worktree (what `handloom kit task seal` leaves) sets `HANDLOOM_SPEC`, so the scope check runs.
- **Seen on the screens:** a task shows "sent back N×"; the job page lists the stages the kit's check looked at, what it refused and the agent's note; the Overview has a Stop on every open job (a confirm step, then it stops the job and its unfinished tasks).
- **The ask in one sentence:** a project can carry its own check (`handloom project check NAME "./check"`, or the Projects page; only someone who manages projects can set it). The New job page opens with "Just ask": a sentence, a project and "Show me the steps first". The machine is the one that has the project, the check is the project's, the lead is the usual one and is told to keep the plan small; with "steps first" it must send its plan and wait. The full form stays below it. A sealed kit task can be named on the full form (base `task/<slug>-base`, check `./check`).
- **Kit extras:** `factory describe [--json]` (what the check covers, in plain words, schema 1); `factory handover --preview cloudflare|netlify|vercel` (the checklist step, and `MAINTAIN.md` only promises a preview link when a host was chosen); `--for operator` writes an operator edition of the guide for an agency; the kit alone is a release asset (`factory-kit_<version>.tar.gz` with `install.sh`, which puts `factory` on the path, no Handloom needed).
- **The job page, rebuilt around tasks (2026-10-18, after trying the live hub):** one card per task with what changed (files with lines added and removed, the whole diff, and any changed web page opened in a sandbox that gives it no scripts and no network), the check result, the helper's terminal while it works, and the actions on the page: Use this result, Send back with a note, Try again with another profile, Stop this task. The machine reports what changed on submit (`POST /v1/tasks/{id}/result`; `handloom task result ID`). Giving a task to another profile starts a new helper named for the profile and the task, tells the old one to stop, and assigns the task when the new one registers (`handloom task reassign ID --profile P`).
- **Solo asks (no lead):** "Just ask" now starts one helper and no lead unless "Have a lead plan it" is ticked. The job's title and brief become its one task (with a short instruction not to invent facts), the hub starts one helper from the `coder` profile (else the first profile that is not a lead) and gives it the task, and the person accepts or sends back the result on the job page. The job page says "Starting the helper" / "A helper is working on it" / "waiting for you", not "No lead is running". Same from the CLI: `handloom job new TITLE --project P --worker-profile PROFILE`. A job is solo when it has no lead and a person made its steps.
- **Drafting acceptance tests (CLI):** `handloom job draft-tests TASK --project P --worker-profile X` reads `specs/TASK.md` from the local `task/TASK` branch, refuses a spec still holding the template or listing no risks, and starts a solo job from that branch whose one helper (a profile other than the builder's) writes a failing test per risk into `specs/TASK/drafts/`. Drafts go there, not into `tests/acceptance/`, because the gate protects that folder (a helper writing there fails `./check` by design); `specs/` is always in scope and never run. `factory task seal` leaves drafts uncommitted and says so. The person reads them, moves the ones they want into `tests/acceptance/`, runs `factory accept-tests` and seals. Not covered: a web button (the hub cannot read a spec that lives on a machine's local branch), and hosted projects refuse a push of `task/*`, so the sealed-task flow is for a machine's own copy.
- **Words and order:** Inbox is first and is where sign-in lands; "Command center" is now "Overview"; the job form offers `./check (the kit)`.

Next:
0. **Solo asks, tried live** (the hub side is tested; a real helper has not yet run in a lead-less job).
1. **Trial with three people** (a team member, a second owner, someone playing the client). Four tasks: add a computer, ask for a small change, decide whether to use it, undo it. Record where each stops. The ratings in the review are replaced by what is seen.
2. **A kit download for people without Handloom** (a client or agency that wants only the scaffold): a release asset and a one-line install that puts `factory` on the path. Until then they use `python3 kit/bin/factory` from a clone, or `handloom kit init`.
3. **Spend cap** per job (needs prices entered). Parked by the owner, 2026-10-18.
4. **Cost per ask** on the review page.
5. **Ask in one sentence:** check stored on the project, default machine, one worker, "Get steps first".
6. **Plain words, the rest:** Overview, Helpers, Computers, Use this result and Finish the job are in; the other pages still use agent/machine words in places.
7. **Agency track:** preview step in the handover, and `MAINTAIN.md` for the operator.
8. **Spec author help:** acceptance tests drafted from the Risks by a different agent (done, CLI); a sealed-task picker (not built: it needs the link to report local branches).
9. **Client view,** only after roles per project and a security review of the hub.
10. **A second kit,** when a real project needs it. The shared core is split out then.

Closed: a project the hub holds cannot take a push to `main`, so the hub adds the kit itself. Projects page (or `handloom project checks NAME [--preset web]`) prepares the checks on a private side ref (`refs/handloom/checks`, which machines cannot push to), shows the files and `factory.toml`, and only a person's "Put on the main branch" (`--apply`) moves `main`, refused if `main` moved since. Applying also sets the project's check to `./check` when it had none. The hub has no Python, so `internal/hub/kitseed.go` is `factory init` written again in Go over the embedded kit files; `TestSeedKitMatchesFactoryInit` runs the real one and compares byte for byte, so they cannot drift. Not covered: a project held elsewhere (GitHub) still uses `handloom kit init` on a copy.

Parked: billing for agent use; more presets until a project needs one; multi-task specs.

## Risks

- **Too many parts to learn.** The front door shows none by name. "Checks" and "Hand over" are the only new words, and the ask box needs neither.
- **The contract grows.** One table of exit codes, one file of evidence. Anything else is a new version with a reason written down.
- **A promise nothing keeps.** `MAINTAIN.md` mentioned a preview that nothing made. Every sentence in a handover guide must be backed by something the handover produced.
- **The check hides problems.** The machine's result beats an agent's claim; exit 2 and errors are never shown as a pass; the review page says what was not checked.
- **Client access widens the attack surface.** A client view can reach agents on our machines. Do not build it before roles per project and the security review.
- **Local-model-only projects.** The flag must be enforced where agents are started, not in the profile.
- **The kit's stock-Mac path was tested only on Linux with Python 3.8 and 3.9.** It has not run on macOS itself or on Windows.

## Decisions

Answered by the owner, 2026-10-18:

1. **Names:** the variables are Handloom's own (`HANDLOOM_BASE`, `HANDLOOM_SPEC`, `HANDLOOM_MODE`); the kit also accepts `FACTORY_*`.
2. **"Add checks":** put the check files on a new branch and show them for approval; never merged without a person.
3. **Who pays for agents after handover:** both. A project setting says whose account runs the work.
4. **Client view:** yes, built in Handloom, after roles per project and a security review.
5. **Who looks after a handed-over project:** both an agency and the client. Two tracks, above.
6. **Python floor:** support older Pythons so a stock Mac works. Done: 3.8 and newer.

Open:
- Whether the kit also gets its own repository for people who only want the scaffold (it is under the same Apache 2.0 licence, inside this repository).
- Which preview host to recommend in the handover.
- Whether Handloom should offer to apply branch protection itself, using the owner's `gh` login.
