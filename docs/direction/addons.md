# Add-ons: a small core and things that plug into it

Status: design, version 1, 2026-10-21. Replaces the "plugins" sketch. It was reviewed by five specialists (security, extension contract, core and data, the self-hosting owner, operations), each cross-checked by a second one; this version applies what survived. Nothing here depends on any particular machine, provider or tool: Handloom is run by anyone, on their own server, with their own agent programs and models, cloud or local.

## The idea

Handloom keeps a small **core**: who may ask for what, which agent does which task, where the work happens, what was checked, and the record. Everything else is an **add-on**: something an owner installs that gives agents a new ability. Zero add-ons is a complete, supported state, and the first-run path never mentions them.

Words people see: **Add-on** for anything installable, **Tool** for one thing an agent can call. "Computer" is the word for a machine that runs a link. The developer words (manifest, gateway, grant, kit) stay in developer docs.

## The core

| Module | What it is | State |
| --- | --- | --- |
| Orchestration | Jobs, tasks, spawns, the wake ladder, leases, caps, messages, escalations | built |
| Profiles | Agent program, runtime, model, tools, write scope, skills; starters | built |
| Projects | Repositories, checks, merge, publish, a per-project check command | built |
| Users and access | People, roles, invites, sessions, OAuth for AI apps, the scope table | built |
| Knowledge and context | A notes folder searched where it lives, lessons, handoff notes, search | pieces built; no single "context" given to an agent yet |
| Audit and notify | Who did what; push with no content | built |
| **Add-on host** | Registry of installed add-ons, grants, the tool gateway, budgets | **this design** |
| Teams | Groups and per-project roles | **not designed here**: no add-on depends on it, and it touches the scope table, sessions and every list query |

Layering, stated honestly: the hub never runs a shell, a model, an agent or a project's check. It uses git only on the repositories it holds itself (hosted projects, and the side ref for checks). The link does everything else on a computer.

## Two kinds of package

| | **Kit** | **Tool add-on** |
| --- | --- | --- |
| What it is | Files that go into a project, and a check | A program an agent can call through the link |
| Has a runtime, keys, health | No | Yes |
| Installed on | A project | A computer (a link) |
| Today | The factory kit, built into the program | Nothing |
| This design | Left exactly as it is. A manifest of `kind: kit` is refused with "kits are not installable yet; the factory kit is built in". | The whole of version 1 |

Why not one thing: a kit is inert and needs no approval beyond "put these files in"; a tool add-on can reach the network and hold keys, so it needs approval, health, grants and limits. One manifest with every part optional gives neither a clear check nor a clear approval screen.

## What is not built, on purpose

- **Add-ons that hold secrets** (an API key the add-on keeps). Until a real isolator exists (container, or a separate operating-system user for the link), anything the link or an add-on holds as a file, environment variable or process memory can be read by an agent on the same computer. Version 1 refuses a manifest that lists `secrets`, and says why. A key the owner types for one call is fine.
- **Shared add-on services** (one service used by many links). It needs authentication, tenancy and confidentiality rules that do not exist. Each link runs its own.
- **Add-on-declared hub actions, per-add-on default grants, declarative pages and commands.** The only permission is the owner granting a tool to a profile.
- **Installable kits** (research, prompt engineering and others), **adapter packs**, **teams**, **context packs**, **a catalogue**, **installing from a URL**.
- **Router.** Choosing a profile or model for a task is a profile question, not an add-on. A profile can already point its model at any OpenAI-compatible address. A rules table from task label to profile may come later in the hub, and any version must fall back to the profile the person chose.

## The package

A folder with `addon.yaml`:

```yaml
schema: 1                     # manifest format
kind: tool                    # tool (kit is refused for now)
name: web-fetch               # [a-z][a-z0-9-]{0,31}
version: 0.1.0
api: 1                        # the add-on API of the core it was written for
title: Fetch a web page
summary: Downloads a page and saves its text.
run: ["python3", "service.py"]   # started inside the add-on's folder; no shell
hosts: ["*"]                  # domains it contacts; shown at approval, informational
secrets: []                   # must be empty in version 1
tools:
  - name: fetch
    summary: Fetch one web page and save its text as a file.
    input:                    # a flat object: names to {type: string|number|boolean, required: bool, summary}
      url: {type: string, required: true, summary: The address to fetch}
    leaves_machine: true      # declared; the owner confirms it when granting, and the owner's value is the one used
    timeout: 30s              # per call, at most 5m
    max_output: 1MB           # per call: text reply plus all files, at most 50MB
```

The **package hash** is the sha256 over the folder's files (each file's relative path and its sha256, sorted by path, joined). `handloom addon hash DIR` prints it, so an owner can compare it with what the author published.

## Trust: install on the computer, approve the exact bytes

- Install is a local act on the computer that will run the add-on: `handloom addon add DIR [--sha256 HASH]`. It prints the name, version, tools, hosts and whether each tool says it leaves the computer, then asks the person to confirm (`--yes` after reading, or `--sha256` to pin). It copies the folder into the link's home and records the approval (package hash, manifest hash, who, when).
- The link runs only a package whose hash matches its local approval. A changed file, tool, host, secret list or `leaves_machine` is a new version: it must be approved again, and every grant to it stops working until the owner grants it again.
- The hub registers what a link reports and holds grants. It never sends program bytes and never starts an add-on. A compromised hub cannot make a computer run a new program.
- No fetching from a URL in version 1.

## How an agent reaches a tool

- Each spawn gets a **tool token**, minted by the link and given to that spawn only (`HANDLOOM_TOOL_TOKEN`). The gateway acts for the spawn the link launched, never for a name the caller claims. This stops one agent using another's grants by name. It does not stop an agent that is hostile and can read another's process environment as the same user: that needs isolation, and the docs say so.
- The gateway lives in the link, on the same local socket agents already use: `POST /local/tools/call`, `GET /local/tools`. Agents use `handloom tool list`, `handloom tool call ADDON.TOOL --arg k=v` (or `--json`), and `handloom tool promote FILE --to PATH`. Where an agent program supports it, the granted tools are also listed by its Handloom MCP server.
- The hub is not on the per-call path. When it starts a spawn it puts a **snapshot** into the spawn: the tools the profile may use on that computer (only those whose installed package matches the grant), whether each leaves the computer (the owner's value), the call limit, and whether the job is confidential. The link enforces the snapshot locally and reports calls to the hub in batches.
- **One function decides**, in the link, in this order: unknown or not installed, not granted, confidential and the tool leaves the computer, over the call limit, add-on down. Its result codes are fixed (below).
- A confidential job is never given a tool that leaves the computer. It is also listed as unavailable, with the cause, in the job's notice. A confidential job with a shell tool and no isolation is not protected from reaching the network; the docs say that in plain words.

### Result codes

| Code | In plain words | What the person can do |
| --- | --- | --- |
| `ok` | Done | |
| `not-installed` | This computer does not have the add-on | Install it on this computer |
| `denied-grant` | This helper's profile may not use that tool | Grant it on the profile |
| `denied-confidential` | A confidential job may not use a tool that leaves the computer | Use a tool that stays, or a different job |
| `denied-budget` | The call limit for this job was reached | Raise the limit on the grant |
| `changed` | The add-on changed since it was approved | Approve it again |
| `plugin-down` | The add-on is not running and could not be started | See `handloom addon doctor` |
| `plugin-error` | The add-on reported an error | Read the message |
| `timeout` | The call took longer than its limit | Try again or raise the limit |

An agent that gets anything but `ok` is told in plain words "this tool is not available, do not guess the answer". The job page shows the cause and the one action, at spawn time for the first six and at the call for the rest.

### Audit

The link writes an entry to its own append-only journal **before** it forwards a call, and a second when the call ends, then ships both to the hub in batches. The hub stores them as audit entries under the add-on's own prefix (`addon.NAME.call`, `addon.NAME.result`, `addon.NAME.denied`), which can never be a built-in name. An entry holds the job, spawn, agent, tool, size, time taken and result code. **Query text and results are not stored on the hub**; they stay on the computer with the job's notes.

## The add-on service

The link starts the add-on's `run` command in its folder, with a minimal environment, and talks to it over standard input and output: one JSON object per line each way. This works on every system Handloom runs on and needs no port.

```
→ {"id":"c1","tool":"fetch","args":{"url":"https://example.org"},"job":412,"results":"<dir>"}
← {"id":"c1","ok":true,"text":"Saved the page.","files":["page.md"]}
← {"id":"c1","ok":false,"error":"the address did not answer"}
→ {"id":"h1","tool":"_health"}
← {"id":"h1","ok":true}
```

- **Lifecycle.** Started on first use. Restarted after a crash, at most three times in ten minutes, then marked down. Stopped when idle for ten minutes. A `_health` call runs at install and from `handloom addon doctor`.
- **Limits per call.** The manifest's timeout and output cap; the link kills the call and answers `timeout`, or drops output past the cap.
- **Files.** The add-on writes files only into the `results` directory the link gives each call: `<worktree>/.handloom/results/ADDON/CALL/`. The link then checks them: regular files only, no links, no paths that leave the directory, at most 20 files and the output cap in total. The folder `.handloom/` is kept out of the project's changes. `handloom tool promote FILE --to PATH` copies a result file into the project tree, where the profile's write scope applies as for any other file, and the check can then read it.
- **Version skew.** Add-ons declare `api: 1`. A link whose core has a different add-on API refuses the add-on with a plain reason. The link reports every installed add-on's name, version, package hash and status on start and whenever it changes.

## What the hub holds

Two tables, and no other change to the core model.

- `addon`: one row per computer and name — version, package hash, manifest hash, the manifest text, status (`ready`, `changed`, `down`, `missing`), a status detail, when it was reported. Reported by the link; removed when the link reports it gone.
- `addon_grant`: one row per profile, add-on and tool — the manifest hash it was granted for, the owner's `leaves_machine` value (default: leaves), the call limit (default 50 per job), who and when. A grant works only where the installed manifest hash equals the granted one.

One new action in the scope table, `addon.manage` (grant, revoke, view all add-ons), for people with the owner role only. Calls need no hub action: the link enforces the snapshot.

## What people see

- **Add-ons page** (Admin menu), and a CLI twin. Lists each add-on across computers: name, version, a status pill with words (Ready, Changed since approved, Not running, Missing on this computer), its tools in plain words, "leaves this computer: yes or no", and the grants with Grant and Revoke (owner only, with the usual CSRF check). A short empty state explains what add-ons are and how to install one.
- **Profile page**: a section "Tools from add-ons" with what is granted and a form to grant.
- **Job page**: a notice when a helper was started without a tool it should have had, with the cause and the one action.
- **Help page**: a section on add-ons and a few questions. **Get started** is not changed.
- **`handloom doctor`**: a line per installed add-on on the computer.

## CLI

- On a computer (local): `handloom addon hash DIR`, `add DIR [--sha256 H] [--yes]`, `list`, `remove NAME`, `doctor`.
- At the hub (needs an owner's token): `handloom addon status`, `grants [--profile P]`, `grant PROFILE ADDON.TOOL [--leaves yes|no] [--max-calls N]`, `revoke PROFILE ADDON.TOOL`.
- For agents: `handloom tool list|call|promote`.

## The reference add-ons

Two are shipped as examples in `examples/addons/` and used by the tests: `echo` (returns its arguments and can write a file; no network) and `web-fetch` (fetches a page over HTTP and saves its text with a header: the address, the time, a hash of the body). Both are keyless. They need only Python 3.8 or newer, as the factory kit does.

## Build order

Each phase starts after the last one ran on a real task.

1. **Phase 0, this document**, and wording fixes: the layering sentence, and examples that name a particular machine or provider.
2. **Phase 1, the host.** The two tables, `handloom addon hash|add|list|remove|doctor`, hash-pinned local approval, the service lifecycle, the link reporting installs. Gate: install `echo` on a computer and see it listed with the right status on the hub.
3. **Phase 2, the gateway.** Tool token, snapshot into the spawn, the deciding function, result codes, audit before and after, `handloom tool list|call`, the grant verbs. Gate: a job whose helper uses a tool; a refused call and a failed call each show a plain cause; a confidential job is refused for a tool that leaves the computer.
4. **Phase 3, results and screens.** The results folder with limits and `promote`, the Add-ons page, the profile and job notices, doctor, Help text, the skew case in the upgrade test. Gate: a person who did not build it installs and uses `web-fetch` from the docs alone.
5. **Phase 4, only when isolation exists:** add-ons that hold secrets, and owner-registered shared services with per-link credentials and TLS. Kits as installable packages, and the research and prompt-engineering kits, come after a project fails in a way they would catch.

## Decisions taken in this design

- Kits stay as they are; tool add-ons are the whole of version 1.
- Add-ons install on the computer, approved by the exact package hash; the hub registers and grants but never pushes bytes.
- The link decides each call from a snapshot; the hub is off the per-call path.
- The confidential rule and the "leaves the computer" value are the owner's, never the add-on's.
- No add-on holds a secret until isolation exists. The docs say plainly what an agent on the same computer can read.
- Audit on the hub carries tool, size, time and code, never the query or the result.
- Words: Add-on, Tool, computer.
