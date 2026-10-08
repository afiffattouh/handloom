# The terminal console

`handloom tui` opens a full-screen console for the person who is signed in: what needs you, your jobs, your agents and the library, in one terminal window. It shows the same picture as the web UI and calls the same API as the command line, so what you do here shows up everywhere else.

Typing `handloom` with nothing after it does the same when you are in a terminal and `HANDLOOM_HUB` and `HANDLOOM_TOKEN` are set. Otherwise it prints the list of commands as before.

## Getting started

You need the hub address and a personal token for your own account (not the admin token, which can only look).

1. In the web UI open **Settings** and create an API token, or, if you already have one that works, run `handloom token new`. The new token is shown once and replaces the one you used.
2. Set both variables, for example in your shell profile:

   ```
   export HANDLOOM_HUB=https://handloom.example.com
   export HANDLOOM_TOKEN=hvh_...
   ```
3. Run `handloom tui` (or just `handloom`).

If the hub does not accept the token, the console says so and tells you to run `handloom token new`. If the hub cannot be reached it keeps showing the last data it had, with "Offline, retrying" at the bottom, and recovers by itself.

The window needs at least 80 columns by 24 rows. Smaller than that, it asks you to make it larger. `NO_COLOR=1` turns colour off; everything still reads, because state is always shown as an icon and a word as well as a colour, and the active tab is marked with brackets.

## Layout

The top line shows the hub, who you are and a filled pill with how many things **need you**. Under it are the five screens. The bottom line shows the keys that work right now; the line above it shows what just happened, or the connection state. Data refreshes by itself every 3 seconds.

Changes are never made on a single key. Every action (accept, send back, answer, close, start a job) first shows exactly what will happen and waits for `y`. `n` or `esc` cancels and nothing is sent.

## Keys everywhere

| Key | What it does |
|---|---|
| `1` to `5`, `tab`, `shift+tab` | go to a screen |
| `↑ ↓` or `k j`, `pgup pgdn`, `home end` | move, scroll |
| `enter` | open the selected row |
| `esc` | go back, or clear the filter |
| `/` | filter the list on this screen (`enter` keeps it, `esc` clears it) |
| `ctrl+r` | refresh now |
| `?` | the key sheet |
| `q`, `ctrl+c` | quit |

## 1 Overview

The command center in text, for the last 24 hours, 7 days or 30 days (`r` changes the range). It shows how many things need you and how long the oldest has waited; agents working, idle, blocked and offline; tasks in progress, waiting for review and waiting to start; the share of checks that passed; the median task time; how fast you answer; and usage. A cost appears only when the models have a price (`handloom prices`); with none, usage shows tokens and says no prices are set. Small bars show accepted and sent-back work and checks per day (or per hour), and how much of the range each agent spent working. Insights are the hub's rule-based observations, each with a "Based on" line saying which numbers led to it.

| Key | |
|---|---|
| `r` | change the range |

## 2 Inbox ("Needs you")

Questions from agents, blocked work, merge problems and silent leads first, then work waiting for review and finished jobs. `enter` opens an item: a question shows its text and options; work to review shows what was asked, the evidence, the note from the agent, the check the machine ran (not the agent), and what became of the branch (merged, waiting, or why not).

| Key | |
|---|---|
| `a` | accept work waiting for review |
| `x` | send it back; you are asked for a reason first |
| `e` | answer a question; typing `1` or `2` picks the matching option |
| `c` | close a finished job |

These are the same calls as `handloom task accept`, `task reject --reason`, `answer` and `job close`. If the hub refuses (a viewer cannot accept work, for instance) its own words are shown at the bottom.

## 3 Jobs

All jobs with state, title, lead and how many tasks are open, claimed, waiting for review and done. `enter` opens a job: its brief, machine, repository and check command, and its tasks with owner, check result and merge state.

| Key | |
|---|---|
| `n` | start a new job |
| `c` | close the job as finished |
| `C` | cancel the job and its unfinished tasks |

The new-job form asks for a title, a brief, a lead profile (chosen from the hub's profiles), a machine (from the machines your agents are on, plus the device list when your account may see it), a repository and a check command. `tab` and `shift+tab` move between fields, `←` and `→` choose a profile or machine, `ctrl+s` (or `enter` on Start job) shows what will be started and waits for `y`. It calls the same endpoint as `handloom job new`; if the hub refuses, for example because a check command needs a repository, the reason is shown in the form and what you typed stays.

## 4 Agents

A table of every agent (name, role, kind, machine, state, current task) with a detail pane for the selected one (beside the table on a wide terminal, under it on a narrow one).

`enter` opens the agent's terminal: the end of its screen, read by its machine and held by the hub for a minute, never stored. The first answer can take a few seconds ("Waiting for the machine"); the console asks again every 2 seconds while the view is open. Viewers are not allowed to look at terminals, and the hub's refusal is shown as a plain message.

| Key | |
|---|---|
| `enter` | watch the terminal |
| `f` | follow the end of the terminal on or off; scrolling turns it off, `end` turns it on |
| `esc` | back to the table |

## 5 Library

Profiles (what an agent may do and know) and starters (ready-made profiles), read only, each with the plain "what it can do" sentence. Adding a profile is done in the web UI or with `handloom profile add`.

| Key | |
|---|---|
| `s` | switch between profiles and starters |

## What it does not do

- It does not edit profiles, people, prices or settings; use the web UI or the commands.
- It does not resume a silent lead or fix a merge conflict; the item says which command to run.
- The role next to your name appears when the hub reports it (owners always get it). A hub that does not report it shows only the name.
- Mouse support is not built; the keyboard does everything.
