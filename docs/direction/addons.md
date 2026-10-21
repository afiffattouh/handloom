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

## Build notes

Written while building phase 1a (the package, local trust, the service, the echo add-on). One bullet each: what was unclear, what was chosen.

- **Manifest hash.** The spec does not say what it covers. It is the sha256 of the exact text of `addon.yaml`. A comment-only edit therefore counts as a change, which is harmless because the package hash changes with it anyway.
- **Package hash layout.** Each regular file gives the line `PATH SHA256` plus a newline, with PATH relative and written with slashes; the lines are sorted by path and hashed together. Links and other special files make the folder unhashable (an error), because what they point to is outside the hash. Empty folders and the executable bit are not part of the hash; the service is started by name (`python3 service.py`), so the bit does not decide what runs.
- **`leaves_machine` when left out.** The spec shows it as a field of a tool but not whether it is required. When left out it counts as "leaves this computer", the cautious value. The owner's value at grant time still wins, as in the spec.
- **`timeout` and `max_output` when left out.** 30 seconds and 1MB. Sizes are written as B, KB or MB (powers of 1024). `hosts` may be empty (an add-on with no network, such as echo); `tools` may not.
- **Tool and input names.** Tool names are lower case with digits, dashes and underscores; a leading underscore is reserved for the protocol (`_health`). Input names are lower case with digits and underscores. The spec only gives the rule for add-on names.
- **Approval store.** One file, `addons/approvals.json`, next to the add-on folders in the link's Handloom home, written with a temporary file and a rename. It is not a valid add-on name, so it cannot clash with one. A damaged file is reported, never silently replaced. Like everything the link holds as a file, an agent on the same computer can edit it (see "What is not built"); the hash check protects against changed add-on files and a changed hub, not against a hostile local user.
- **Who approved.** The operating-system user name of whoever ran `handloom addon add`, or "someone" when it cannot be read.
- **Running add-ons and the command line.** `add` and `remove` run in their own process and cannot ask the link's supervisor. The supervisor therefore writes the service's process id to `addons/.running/NAME` while it runs, and the command line refuses to replace or remove an add-on whose id is alive. A stale file (the link was killed) is ignored once the process is gone.
- **Replacing an add-on.** `add` on a name that is already installed and not running replaces it with the new version after the usual reading and confirmation, and says which version it replaces. The copy is checked against the hash that was shown, so a folder edited between the reading and the copy is refused.
- **`add` runs a health check.** After copying, the add-on is started once and asked `_health`, as the spec says ("at install"). If it fails, the add-on stays installed and approved (the files are what the person read) and the command exits with an error that says so.
- **Crash accounting.** "At most three restarts in ten minutes" is counted as: the first start is free; after a stop that was not asked for, each start counts; a fourth stop inside ten minutes marks the add-on down. A command that cannot be started counts the same way. A timeout, an output overflow, a changed package and an idle stop are not crashes. Down is kept for the life of the supervisor (the link); a new link process or `handloom addon doctor` starts fresh.
- **One call at a time per add-on.** The protocol has ids, so concurrency is possible later; version 1 serialises calls to one add-on, which keeps timeout handling to "kill the service".
- **Output cap.** The cap is checked as the reply arrives (a line without an end is cut off at the cap plus 64KB for the surrounding JSON) and again on the reply's text. Over it, the service is killed and the code is `plugin-error` with the limit in the sentence; `timeout` is used only for time. The 20-files and total-size checks on files belong to phase 3 and are not done here; the supervisor only passes on the file names the add-on reports.
- **Stray output.** Lines on the service's output that are not a reply to the current call (not JSON, or another id) are ignored, so an add-on that prints a log line does not break a call.
- **Environment.** Only PATH, LANG, TMPDIR, `PYTHONDONTWRITEBYTECODE`, `PYTHONUNBUFFERED`, `HANDLOOM_ADDON` and `HANDLOOM_ADDON_API` (and SYSTEMROOT where it exists). No home folder: the service would otherwise write into a place the owner did not look at. Python's byte-code cache is switched off so that running the add-on never changes the folder whose bytes are approved.
- **Arguments are checked before the call** against the tool's `input` (required, type, no unknown names) and a mismatch is a caller error, not an add-on result code.
- **Local only.** `handloom addon` refuses to run through a hub connection. The hub-side verbs (`status`, `grants`, `grant`, `revoke`) are phase 2.
- **Echo.** Two tools, `say` (arguments `text`, optional `file`: a plain file name saved into the results folder) and `wait` (`seconds`). The manifest allows `wait` the full five minutes so that the timeout can be tried with a lowered value; the tests do that on a copy.

Written while building phase 1b (the add-on table, the link's report, the hub's reads).

- **Who reads.** The task asked for reads open to anyone with read access, while the spec's `addon.manage` (view all add-ons) is owner only and arrives with the grants in phase 2. Phase 1b therefore uses the ordinary read action and adds no row to the scope table. What a person sees is cut by the machine rule that already holds: an owner sees every computer, a member only their own, a viewer none. Revoked computers are not listed. Phase 2 should decide whether viewing narrows to `addon.manage`.
- **Reads.** `GET /v1/addons` lists every add-on on every computer in view, without the manifest text. `GET /v1/addons/{name}` returns the same rows for one name with each computer's manifest text, and is a 404 when no computer in view has it. The CLI verb is `handloom addon status [NAME]`; it is the one add-on verb that talks to the hub.
- **The report.** `POST /v1/device/addons` carries the whole list and replaces that computer's rows in one step; a refused report changes nothing. The hub checks names, status words, hashes (64 hex or empty), sizes (100 add-ons, 64KB of manifest each) and refuses a name twice. Only a device credential may report. The link's local socket refuses the route, like the other link-only routes. At the hub, an agent's request that carries its computer's credential is that computer, as with usage; the socket is what keeps agents out.
- **When the link reports.** Installing is done by the `handloom addon` command in another process, so the link cannot be told. It compares what is in the add-on folder with what the hub last accepted each time its ladder runs (every ten seconds, and at once on any hub event), on start included, and sends only when they differ. A report the hub refused, or could not be reached for, is sent again on the next run. If the approvals file cannot be read the link logs it and keeps the hub's last rows rather than reporting an empty computer.
- **Status.** The link reports `ready`, `changed` and `missing` as the local list computes them. `down` is accepted and shown by the hub (the CLI says "Not running") but nothing sets it yet: the service supervisor is not started by the link until phase 2.
- **Manifest text.** Sent only for an add-on whose manifest parses, and left out when over 64KB. A `changed` add-on reports the text on disk, not the approved text.
- **Audit.** None. The spec asks for audit entries only for calls; an install report is not an act the spec names.
- **Table.** `addon(device_id, name, version, package_hash, manifest_hash, manifest, status, detail, reported_at)`, keyed by computer and name, status checked against the four words in the database itself. Rows are removed by the report that leaves them out; revoking a computer leaves its rows (hidden, and replaced if it ever reports again).
- **Report size.** The ordinary 256KB body cap made the report limits (100 add-ons, 64KB of manifest each) unreachable. The report route now accepts up to 16MB, and the link sends at most 4MB of manifest text per report (later add-ons go without their text but keep their status). Not covered by a link-side test.

Written while building phase 2a (the hub side of the gateway: grants, the spawn snapshot, the call journal).

- **Owner only.** `addon.manage` is a row of the scope table with a new subject, `human:owner`, so the table can say "the owner role and nobody else". The four existing subjects all read "no" for it. The admin token is refused, like every non-read action in the table (profiles accept the admin token; grants do not, because the spec says the owner role). A person's AI app is refused by the operator allowlist, as for every call not on it. Listing grants is the same action: "view all add-ons" in the spec stays with reading add-ons (phase 1b, `GET /v1/addons`), and only the grants need the owner.
- **Routes.** `POST /v1/profiles/{name}/addon-grants`, `DELETE /v1/profiles/{name}/addon-grants/{addon}/{tool}` (no body), `GET /v1/addon-grants[?profile=P]`, and the link-only `POST /v1/device/addon-calls`. The CLI adds `--computer` to `grant`, which the spec does not list: see the next bullet.
- **Which manifest a grant is for.** The spec says the hash of "the manifest" without saying whose, and computers can hold different versions. The grant takes the manifest reported by a computer that has the add-on approved (status ready or down, manifest reported, tool in it). If those computers disagree the grant is refused with "name the computer", and the optional `computer` field (CLI `--computer`) picks one. A `changed` add-on cannot be granted: nobody approved what it is now.
- **Grants are by profile name, not version.** A profile gets a new version on every change, and a grant that vanished with each edit would surprise the owner. The grant follows the name; a spawn uses the grants that exist when the link asks for it.
- **A grant replaces itself.** Granting a tool that is already granted overwrites the row (new manifest hash, new values, who and when). That is how a grant is renewed after a manifest change. Each grant, including a renewal, is an `addon.grant` audit entry with the profile as target; a revoke is `addon.revoke`. A revoke of a tool that is not granted is a 404 and writes nothing.
- **When the snapshot is made.** In the answer to a link's `GET /v1/device/spawns`, for requests still `pending` only, from what the computer last reported. A spawn the link already started keeps what it was given. The link gets `tools`, `tools_unavailable` and `confidential` on the spawn; `confidential` is now on every spawn the API returns, off when false.
- **Order of causes.** not-installed (missing, or never reported), changed (status changed, another manifest hash than the grant's, or a manifest the hub cannot read), denied-confidential, plugin-down. A tool can have one cause only; the one named first wins. Confidential comes before down because a down add-on can come back and a confidential job still may not use that tool. A `changed` cause has two actions: "approve the changed add-on again on this computer" when the computer reports it changed, and "check the new version, then grant the tool again" when the computer approved a new manifest the grant does not match.
- **`Why` as well as the cause.** Each unavailable tool carries the cause code, a plain sentence for it and the one action, so a page or a link notice need not keep its own table.
- **Job notice function.** `jobToolsUnavailable(job)` takes every spawn of the job that has a profile and did not fail to start, computes the snapshot against the computer's current report, and returns the distinct tools with cause and action. It is therefore live, not a record of what the spawn was given; a tool that has since come back no longer shows.
- **Journal lines.** The hub checks that the spawn is the calling computer's, that the agent name and job are the spawn's own, and fixed shapes for every other field: call id (64 characters of letters, digits and `._:-`), add-on and tool names (the manifest rules), phase, one of the nine result codes (a call line has none, a denied line is never `ok`), size at most 50MB, time taken at most a day, and a time that is set, after 2020 and not more than a day ahead. Anything else, an unknown field in a line or beside the list, or more than 200 lines, refuses the whole batch and stores nothing. The code, name and phase fields cannot hold a sentence, so there is no field a query could ride in.
- **Idempotence.** A new table `addon_call_seen` (computer, spawn, call id, phase) records what was stored; a line already there is skipped. It is never pruned: a row is a few dozen bytes per call, and pruning would let a very late resend duplicate. Revisit if it ever matters.
- **Audit shape.** The action is `addon.NAME.PHASE`, the target is `job:ID` or `spawn:ID`, and the payload holds call id, spawn, agent, job, tool, code, size, milliseconds and the reported time. The actor is the computer (device principal), as with other reports from a link. No event is sent to agents.
- **Not done in 2a.** The link does not read the snapshot or send a journal yet, and nothing sets `down` in the report yet (phases 2b and later). The web pages that use `jobToolsUnavailable` come with phase 3.

Written while building phase 2b (the link side of the gateway: tokens, the deciding function, the journal, the commands).

- **Where the snapshot is read.** The hub fills the snapshot only for a request still `pending`. The link therefore takes it from that answer, keeps it in memory under the spawn's token, and does not ask the hub again. A helper's own start-up (`spawn-exec`, which sees the spawn as `launching`) reads its tools from the link with `GET /local/tools` and its token, so the instructions and the call come from one record.
- **Tokens.** Random, 32 bytes, kept only as a sha256 (the link never holds a token it could print). Every spawn gets one, also one with no tools, so that its call is answered "not granted" and journaled instead of "unknown token". The token goes into the spawn's terminal command as `HANDLOOM_TOOL_TOKEN`, which means it can be seen in the process list by the same user; this is the "same user" limit the design already states. A token is held only in memory: a link that is restarted forgets all tokens, and a helper that was running then gets "the link does not know this helper's tool token" until it is started again. A headless wake-up turn (no terminal) does not carry the token and has the same effect.
- **When a token ends.** At spawn start it replaces any earlier token of the same agent name. In the ladder, after the first two minutes, it ends when the agent is not on the hub's list for this computer, or when the link's terminal server answers and has no window of that name; when the terminal server cannot be asked, no token is dropped on a guess. At the latest after a day.
- **The call limit is per spawn.** The spec's grant text says "per job"; the task for this phase says per spawn and per tool, and that is what is built. A job with two helpers allows each of them the limit. A call counts once it is let through (also when the add-on then fails); a call refused, or not made because the journal could not be written, does not count. The counter ends with the spawn's token.
- **What the one function sees.** `decide` takes the snapshot's entry for the tool (or the hub's reason it is unavailable), the job's confidential flag, the calls used, and what the computer says now (not here, changed, cannot be read). The computer's answer is asked of the supervisor's own check, so a file changed after the spawn started is `changed` at the call. A tool that the add-on's manifest no longer has counts as not installed ("this computer has no such tool"). A snapshot entry that says plugin-down is refused as plugin-down: it carries no limits to forward with. The `down` state the supervisor reaches during a call comes back as the result code `plugin-down` of that call. A tool that leaves the computer in a confidential job is refused here even if a snapshot lets it through.
- **Arguments.** A call whose arguments do not fit the tool's input is a caller mistake (HTTP 400, `bad_arguments`) and is answered before the journal and the counter, like a name that is not `ADDON.TOOL`. Neither is journaled, since nothing was decided about a tool. A call with no known token is not journaled either: there is no spawn to put it under.
- **Journal.** `addon-calls.jsonl` in the link's home (override: `Options.Journal`), one `api.AddonCall` per line, nothing else, opened for appending and synced (the folder too, for a new file) before the call goes on. A line cut short by a crash is closed off by a newline before the next line, and passed over when shipping. If the call line cannot be written the call is not made (HTTP 500) and does not count. The result line carries the reply's size in bytes, never its text; a file name is not in any line.
- **Shipper.** Runs in the ladder (every ten seconds and after each call). The file `addon-calls.jsonl.sent` holds how many bytes the hub has accepted, written by rename after the hub's answer; lost, it only makes lines go again and the hub skips what it has. At most 200 lines per report. An unreachable hub or any answer except 400 leaves everything as it was, logged at most every five minutes. A 400 means the hub refuses a whole batch for one line, so the batch is sent line by line and a line that is refused alone is dropped with a note in the link's log (a spawn the hub no longer knows would otherwise block every later line). A fully shipped journal over 8MB is renamed to `addon-calls.jsonl.old` (one older copy is kept).
- **The call goes on if the caller leaves.** The call to the add-on is not tied to the HTTP request, so the result line is always written. The link's shutdown stops the add-on services, and a call in progress ends as `plugin-error`; `Supervisor.Close` used to wait for such a call (up to five minutes) and now ends it.
- **Result files.** The per-call folder `<worktree>/.handloom/results/ADDON/CALL/` is made and passed. The answer lists the files by full path; a name that is not a plain relative path is left out. The checks on count, size and links are phase 3.
- **`handloom tool call` and `--json`.** The spec uses `--json '{...}'` for the arguments while every verb already uses `--json` for output a program can read. A `--json` followed by a word starting with a brace is the arguments; a bare `--json` is output. `--args '{...}'` is the plain spelling and is what the MCP tools use. `--arg k=v` is typed by the tool's declared input (a number, true or false, else text). Exit code 1 for every answer except `ok`; the message begins with the code word. The command reads `HANDLOOM_TOOL_TOKEN` and the socket and never goes to the hub, even when `HANDLOOM_TOKEN` is set. `handloom tool promote` is phase 3 and says so.
- **Instructions.** `spawn-exec` appends two sections to the instructions it writes: "Tools from add-ons" (name, summary, arguments with required ones first, how to call, the limit) and "Tools that are not available" (the cause, and "this tool is not available, do not guess the answer"). The owner's action line is left out: the helper cannot do it. A helper with neither gets nothing new. This is the same for all four kinds that `spawn-exec` starts.
- **MCP.** `handloom mcp` adds one tool per granted tool (`handloom_tool_ADDON_TOOL`) when `HANDLOOM_TOOL_TOKEN` is set, and each runs `handloom tool call`, so the decision and the journal are the same. Of the kinds the link starts, only Codex has the Handloom MCP server configured by the link; whether Codex passes the token to a server it starts was not tried here. The other kinds (Claude Code, OMP, Pi) do not start `handloom mcp` for a spawn, so for them the command line is the way, and the instructions say so.
- **Not done.** Nothing sets the add-on's status to `down` in the report yet; the result code `plugin-down` of a call is the only place it shows. The audit entry carries `size` and `millis` of a `call` line as 0.

- Gateway review fixes. A tool in a spawn's snapshot carries the manifest hash its grant was made for, and the link refuses a call with `changed` when the installed, approved manifest hash differs. The package hash is not carried: a grant is for a manifest, so re-approving changed code under the same manifest keeps the grant, as the spec's wording says.
- When the link starts it closes every call line that has no result line with a `plugin-error` result line (the journal has no field for a message), and ends add-on processes an earlier link left behind. A leftover is found by the id file and ended only if its working folder is still the add-on's folder, because the lock proves nothing once the old link is gone and the number may belong to another program. Where `/proc` is not there, leftovers are not ended.
- **Revoking does not reach running helpers.** The link enforces the snapshot a helper was started with, so a tool revoked (or newly granted) at the hub changes only helpers started afterwards; a running helper keeps the tool until it ends. `handloom addon grant` and `revoke` say so in their output. Making a revoke take effect at once would need the link to ask the hub for revocations in its ladder and drop the tool from live sessions; not built.
