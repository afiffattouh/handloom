# Workflow v2: one ask, one review

Status: proposal for discussion, 2026-10-14. Nothing here is built yet.

How this relates to the other documents: `docs/direction/README.md` is about who Handloom is for and what to prove first, and the `WORKSPACE-DESIGN` files are about architecture. Neither describes how a person uses it day to day. This one does, and does not change their security decisions, except one deliberate revision of a design principle (below).

## Why a step back

Handloom works, but it is hard to use well. Starting a job takes decisions a person should not have to make, and when something goes wrong nobody says so. What we saw on job #4, started from Claude Desktop:

- The form asked for a repository path, a lead profile and a machine. The job was one small change.
- A lead agent was started for a job that said "one task for one coder". The lead then had to run the system by hand: name a worker, start it, assign it.
- Starting the worker failed because the name was taken by an agent of an older job. The lead did not understand the error, assumed the worker existed, and assigned the task to an agent that was offline. Then it went idle.
- Nothing on screen said "stuck". The person had to ask someone to look at the audit log.

The mechanical parts of orchestration (names, starting workers, assigning, noticing silence) were left to a language model reading a prompt. They should be done by the system. The model should decide what to do, not how to operate Handloom.

The job's own brief shows it: it said "Keep it to one task for one coder". The person was doing the orchestration in prose because the system had no way to be told.

## The idea in one sentence

**You say what you want. Handloom hands back a branch (or a document) that was checked, and you decide whether it lands.**

A person sees three things: **Ask**, **Progress**, **Review**. Everything else (links, spawns, leases, worktrees, integration branches, tokens) is the system's business.

## Personas

| Who | What they want | What they must not need to know |
| --- | --- | --- |
| **Owner** (runs the hub, may also work) | Set up once, then hand out work and review it without watching. Trust that nothing lands without them. | Tmux, worktrees, tokens, agent names. |
| **Team member** (often not technical) | Add their own machine in one step, ask for work in their project, see their results. | Git, agents, profiles, "link". |
| **Reviewer / stakeholder** (viewer) | See what is happening and what was produced. | Anything; read only. |
| **Agents** (not people, but they have roles) | Lead: decides the plan. Worker: does one task. The machine: checks the work. | n/a |

## Journeys

**1. First fifteen minutes (owner).** Install the hub, add this machine, add a project, run a guided first job, see its result. Today these are separate pages and commands. Target: one checklist that ends with a finished first job.

**2. Everyday (owner or member).** Type one sentence ("add a --version flag") from the web or from Claude. Pick the project (the last one is the default). See a plan if the job is big, or nothing if it is small. Get a message only when needed. Read the result. Land it with one button.

**3. Joining (team member).** Open an invite, set a password, copy one line onto their machine. Their machine appears as theirs. They start asking for work.

**4. When something goes wrong (anyone).** The job says in plain words what is wrong and offers one action: "The machine went offline. Start it again / Wait". "The check failed twice. Look at it / Send back with a note." No log reading.

**5. Two people, one project.** Before a job starts, show other open jobs on the same project that touch the same files. Landing order is the owner's call.

## The flow

```
Ask  ->  (Plan)  ->  Work  ->  Review  ->  Land
```

1. **Ask.** One box: what do you want done. Project defaults to the last one. The machine is chosen for you (the one that holds the project and is online). The check command is a property of the *project*, set once, not asked again for every job. Machine, role and base branch live under "Options".
2. **Size decides the team.** Small asks run as **one worker, no lead**. Larger asks get a lead that writes a plan. The hub has no model, so it does not judge size: the Ask box has a plain choice, "One worker" (the default) or "A lead with a team", and the project can set its own default. This removes the most fragile part for the most common case.
3. **Plan.** The lead *submits a plan as data*: tasks, who does each (a role such as "coder"), what depends on what. It does not start or name anyone. The person sees the plan as a short card. Default: small and medium jobs run at once; "Ask me first" is a setting per project.
4. **Work.** For each task in the plan, the link starts one worker with a generated name (`coder-<task id>`) in its own worktree. A worker lives for its task and ends when the task is accepted. Names can never collide and an old agent can never be picked by mistake.
5. **Review.** One page: what was asked, what changed (files and a short summary written by the lead or worker), the checks and whether they passed. Actions: **Land it**, **Send back with a note**, **Close**.
6. **Land.** One button. The project decides how: hosted project merges, a GitHub project opens a pull request.

## While it runs: talking to a job

A person can do three things, all from the job page or from Claude:

- **Add a note** to the job. The lead (or the lone worker) reads it at its next step.
- **Redirect:** "stop, do it this way". This is a note marked as a change of plan; the lead rewrites the plan and the board shows what changed.
- **Answer a question** an agent asked, with options and room for context.

Anything else (pausing, cancelling one task) is a button on the task. Notes always go to the lead, never to a worker behind its back, so the plan stays one thing.

## A deliberate change to a design principle

`DESIGN.md` principle 1 says "dumb hub, smart agents: the lead is an agent, not part of the server". This proposal revises it in one place. The hub still has no model and never decides *what* to build. But starting, naming, assigning and restarting workers become the system's job, done by fixed rules, because a model reading a prompt did them badly. The lead's power shrinks to one thing: write the plan. That is safer (a lead can no longer start the wrong agent) and simpler to explain.

## The job always says where it is

A job has one state, computed by the hub, with a reason and a next step:

| State | Means | Offered |
| --- | --- | --- |
| Planning | the lead is writing the plan | wait |
| Working | N of M tasks, who is on each | watch |
| Needs you | a question, or work to review | the question / the review |
| Stuck: why | no worker running, a machine offline, the lead silent, the check failing again and again | one fix, in words |
| Ready | all tasks done and checked | Land it |
| Done | landed or closed | open result |

The hub also acts before it asks: a worker that went offline is started again once, a lead that is idle while tasks wait is woken with the plain fact, and only then does it become "Stuck: why" for a person.

## What a person sees, and what they do not

Visible: **Projects**, **Jobs** (asks), **Machines**, **Roles** (what profiles are called to a person), **Inbox**.

Hidden by default: link, spawn, lease, worktree, integration branch, tokens, agent names. They stay available under "Details" for the owner who wants to look.

## Collaboration between agents

Keep what works: one board per job, a short handoff note on every task, file ownership decided by the plan. Make messages between agents rare: the plan carries the dependencies, so workers need to talk only when something is genuinely unclear, and then the question goes to the lead, then to a person.

## Different kinds of work: playbooks

Software is one kind of job. Others are knowledge work (research, deliverables, consulting), video production (script, images and graphics, clips, voice-over, assembly) and whatever a team invents. They differ in four ways, and a **playbook** is a ready-made job type that bundles them:

- **Team:** the roles, each with its profile and skills.
- **Deliverable:** a branch, documents, or a folder of files.
- **Checks:** what proves the work acceptable.
- **Gates:** where a person is asked.

The person types an idea; Handloom proposes a playbook from the sentence (by rules on words and the project, not by a model in the hub) and the person changes it only if they disagree. Roles, skills and gates live in the playbook, not in the job form.

| | Dev | Knowledge work | Video |
| --- | --- | --- | --- |
| Team | lead, coder, tester, reviewer | lead, researcher, analyst, writer, fact-checker | director, scriptwriter, image maker, clip maker, voice, editor |
| Deliverable | a branch | documents in the client's folder | a folder of assets and a final cut |
| Machine check | the project's test command | every claim has a source, figures match the data | durations match the script, audio plays, resolution is right |
| Second opinion | reviewer agent | fact-checker agent, never the author | QA agent compares the cut with the script |
| The person steps in | the diff | the framing, then the draft | script, then look and voice, then rough cut |
| Land | merge or pull request | merge into the client's knowledge, or export | save the final files |

What Handloom lacks for this today:

1. **Deliverables that are not code.** The hub keeps only the record; "evidence" is a note naming a file. A deliverable becomes a folder of files that stays on the machine that made it, with a list (names, sizes, types, thumbnails) the hub keeps. The review page previews images, audio, video and documents; the files are pulled only when a person asks.
2. **Checks beyond one command.** A check is one of three kinds: a command the machine runs; a rubric scored by a *different* agent than the author; a human gate.
3. **Gates.** A pause with something to look at and options ("three looks for the series, pick one", "approve the script"). It builds on the questions agents can already ask, with attachments and choices. This is where a person's ideas and steering go.
4. **What a machine can do.** Image, voice and video tools are set up once per machine, and the machine reports them. A playbook says what it needs and the job goes to a machine that has it. The job form never lists tools.
5. **A cost cap per job,** visible, which stops the work when spent. Generating media costs money.
6. **A style guide.** For video, a series bible (voice, characters, palette) kept as a knowledge folder that agents read first. The mechanism exists.

Playbooks are data over what exists (profiles, skills, knowledge folders). A person writes their own by answering questions: what is the outcome, what does done look like, who reviews it. Not by editing files.

Not yet verified: which image, voice and video tools can be driven from the agent CLIs, and how well. That must be checked before the video playbook is promised. A local model cannot make media, so confidential video work would need local media models.

## What stays as it is

The hub holds records and never runs a model. Machines only call out. Only people approve. The device's check beats an agent's claim. Confidential jobs stay on local models.

## Build order

Each step is useful alone and fixes something we saw.

1. **Job state, reason and next action** on every job page and in the Claude connector ("what needs me"). Pure reading of data we already have.
2. **Workers made by the system:** generated names, one per task, ended with the task. The lead's "spawn" and "assign" calls become one `plan submit`.
3. **Watchdog:** start a lost worker again, wake a lead that sleeps while work waits, then escalate with a reason.
4. **Check command per project;** machine chosen automatically; the Ask box reduced to one sentence plus Options.
5. **Solo mode:** one worker and no lead for small asks, chosen in the Ask box.
6. **Playbooks for dev and knowledge work,** as data over today's profiles and skills, chosen from the Ask box.
7. **Gates and the three kinds of check.**
8. **File deliverables with previews, machine capabilities and cost caps;** the video playbook is the test of these.
9. **First-run checklist** that ends in a finished practice job.
10. **Overlap warning** between open jobs of one project (stage three of projects).

## Decisions so far

1. The system starts workers, from the lead's plan. The lead only plans.
2. Small asks run as one worker with no lead, by default.
3. Plans run at once for small and medium jobs; "ask me first" is a setting per project.
4. After dev, knowledge work comes next; video is the test of file deliverables.
5. Large files stay on the machine that made them; the hub keeps the list and thumbnails.
6. People write their own playbooks by answering questions.

## Committee review, 2026-10-14

Six reviewers read this document against the code: security, a non-technical user, distributed systems, multi-agent practice, product strategy and video production. Their findings change the plan below. **The decisions above stand until the owner says otherwise; the changes here are recommendations.**

### Where they agreed

1. **Caps before automation.** Nothing may start agents by itself until the hub enforces limits: starts per task (two), agents at once, and a budget of time and spend per job. The counts live in the database so a restart cannot reset them. Without this, a watchdog or an auto-run plan can loop or multiply.
2. **A second agent's opinion never gates.** A rubric scored by another agent is advisory, labelled so, and cannot alone mark work ready or land it. It sees only the rubric and the work, and its text is shown to people, never passed on to other agents. Only a machine check or a person gates.
3. **Job #4 needed a smaller fix than the design.** A name collision and an unreadable error caused it. Unique names plus a clear error (built) and a stuck alert fix that. Plan-as-data and system-started workers are a mid-sized rewrite and must first prove themselves against the current way, with the local Qwen model as lead as well as Claude.
4. **"Plans run at once" is unsafe as a default.** A lead that is wrong or tricked could start many agents. Default to "ask me first"; "run at once" is an opt-in per project, and a plan may only use roles the project allows, within the caps.
5. **The job state is not a pure read of existing data.** Nothing records whether a plan was submitted, how many times a task was tried, or that a lead is silent. State needs a few new columns, a fixed precedence (Done, Needs you, Stuck, Ready, Working, Planning) and one test per row.
6. **The words and the setup still burden a non-technical person.** "Land", "gate", "playbook", "role", "machine", "plan" mean nothing to them. Team members should only ever do three things: accept an invite, paste one line, type a sentence. Plain words: "Use this result", "Needs your OK", "Type of work", "Helper", "Computer", "Steps".
7. **Missing for trust:** the cost estimate and actual cost on every ask; a **Stop** button on every job and **Undo** after a result is used; "what was checked, what was not" at the top of Review ("Tests passed. Nobody read it for tone."); what happens when nobody answers (the job waits, with a limit); phone approval.
8. **Nothing yet proves this helps anyone.** The only evidence is one job by its author. Before building further: watch three people who are not the author start and review a job, and run a fixed set of 15 to 20 small asks (Claude and Qwen, one worker and a team), measuring accepted-first-time rate, send-backs, stuck rate, minutes to a reviewable result and cost.

### Where they disagreed

- **Playbooks and video.** The strategist: cut video, defer knowledge work, because the earlier direction ruled out a business-workflow product and the checks for documents and media are not machine checks. The practitioner: ship exactly two playbooks, each with a test that runs in CI. The video producer: the structure and gates are sound, but promise only stills, voice-over, music and assembly now; consistent characters, rights and likeness, and cost per rerun are unsolved. **Resolution proposed:** keep playbooks as the idea, call them "types of work", ship only dev now, move knowledge work behind real demand, and park video as research with the producer's list (a "lock the look" gate with reference images, pick-one-of-N takes per shot, an editable timeline, subtitles, rights records, proxy previews only).
- **Solo mode and the choice in the Ask box.** The user reviewer: ask nothing, start one worker, and offer "Get steps first" if it says the job is bigger. The practitioner: a lone worker loses clarification and review, so give it a way to ask a person and a button that turns the job into a team job while keeping its work. **Resolution:** adopt both.

### Design details to add

- Workers are keyed by (task, attempt), and "make sure a worker exists for this task" is one transaction. A dead agent keeps its name, so a restart uses a new attempt, not the old name.
- A worker lives until its task is accepted or cancelled, so a send-back always finds it. A send-back with no live worker restarts it.
- A plan is versioned and submitted as a difference by task key. Done tasks are never touched, and a stale version is refused.
- The watchdog waits out a grace period for a sleeping laptop, never wakes a lead that is working, wakes at most once per state change, and escalates once per reason.
- After a hub restore, each link reports what it is running, and the hub adopts or ends the strays.
- Landing is serialised per repository, with the base recorded per job.
- Previews, if ever built: a separate origin with no cookies, raster images and plain audio and video only, documents rendered to images on the machine, and for confidential jobs previews stay on the machine. A deliverable's files are pulled only by a person's request, with a size cap, a hash and no path tricks.
- A machine's reported abilities are approved by the owner; only the owner publishes types of work.

### Revised build order (recommended)

1. **Done:** unique-name error, and refusing a task for an agent of another job. Deploy pending.
2. **Caps:** starts per task, agents at once, per-job budget, stored in the database.
3. **Job state v1** with reason and action, in plain words, plus **Stop**. Built from existing data and the few new columns.
4. **Watchdog:** restart once, only after step 2.
5. **Check command per project, default machine, one-sentence Ask box,** one worker by default with "Get steps first" and an ask-a-person path.
6. **Test with three people and the 15-to-20-ask set.** The results decide whether to build plan-as-data, types of work and anything after.

Parked until the results say otherwise: plan submit, types of work beyond dev, gates, file deliverables, previews, video, user-written playbooks.
