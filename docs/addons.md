# Add-ons

An add-on gives helpers a tool they do not have, such as saving a web page as a file. It is a small program that you install on one of your computers. Handloom works without any, and nothing in the first setup asks for one.

Words used here: an **add-on** is the program you install; a **tool** is one thing a helper can ask it to do; a **computer** is a machine that runs a link.

## Install one on a computer

Installing is done on the computer that will run the add-on, by the person who runs it. The hub never sends an add-on anywhere and never starts one.

You need the add-on's folder (it holds a file called `addon.yaml`) and Python 3.8 or newer for the two examples that come with Handloom, which are in `examples/addons/`.

1. Read what it is, and compare its hash with the one its author published:

   ```
   handloom addon hash examples/addons/web-fetch
   ```

   The hash covers every file in the folder. If one byte differs, the hash differs.

2. Install it. Without `--yes` Handloom shows the name, version, tools, the places it says it contacts, and whether each tool says it leaves the computer, then asks you to confirm. In a script, pin the hash instead:

   ```
   handloom addon add examples/addons/web-fetch --sha256 <the hash from step 1>
   ```

   The folder is copied into Handloom's home on this computer, your approval is recorded, and the add-on is started once to check that it answers. It stays installed if that check fails, and the command says so.

3. See it: `handloom addon list` on the computer, or the **Add-ons** page (Admin menu) on the hub. It says **Ready** within a few seconds.

If you change any file of an installed add-on, or install a different version, it shows **Changed since approved** and does not run until you approve it again with `handloom addon add`. `handloom addon remove NAME` removes it. If the add-on is running (it keeps running for ten idle minutes after a helper uses it), both refuse and change nothing: wait up to ten idle minutes, or stop the link, then run the command again.

## Let a profile use it

Nothing can use a tool until an owner allows it. The profile must already exist (see **Add profiles** in the [README](../README.md#quick-start)). On the Add-ons page choose **Grant** next to the tool and pick a profile. On the command line, run it as the owner: set `HANDLOOM_HUB` to the hub address and `HANDLOOM_TOKEN` to the owner's personal token first. Get it from **Settings** on the hub, or run `handloom token new`; if the owner has lost it, run `handloom hub reset-human-token` on the hub's own computer. The admin token (the one the hub prints when it starts) and a member's token are refused with "use a human token"; that means the owner's personal token.

```
handloom addon grant researcher web-fetch.fetch --leaves yes --max-calls 20
```

- **Leaves this computer.** The add-on says whether a tool sends anything off the computer. You confirm it when you grant; your answer is the one used. If you do not say, the answer is yes.
- **Calls.** At most this many calls per helper, 50 if you do not say.
- Only an owner can grant or revoke. A grant is for the add-on as you approved it. If the add-on's description changes, grant again.
- A grant or a revoke reaches helpers started afterwards. A helper that is already running keeps the tools it started with.

`handloom addon grants` lists them; `handloom addon revoke PROFILE ADDON.TOOL` removes one.

## What a helper sees and does

A helper started from that profile gets a short section in its instructions with each tool: its name, what it does, what to give it, and how to call it. It can also ask at any time:

```
handloom tool list
handloom tool call web-fetch.fetch --arg url=https://example.org
```

The answer is printed. A tool that makes files (such as `web-fetch.fetch`) puts them in a folder for that call under the helper's working folder, in `.handloom/results/`, and prints their paths. That folder is scratch space: it is not part of the project and it is not submitted. To keep a file, the helper copies it into the project:

```
handloom tool promote <path printed by the call> --to notes/example.md
```

From there it is a change like any other. The profile's write scope is checked when the helper submits, and the project's check can read the file. At most 20 files, and no more than the tool's output limit, are kept per call; a call that goes over fails and says why.

If a call does not work, the helper is told in plain words that the tool is not available and that it must not guess the answer. The job page shows which tools a helper was started without, and also the calls that went wrong while the helpers worked (an add-on that failed, a call that took too long, a limit that was reached), each with the one thing to do about it.

## What is protected, and what is not

What Handloom does:

- An add-on runs only if its folder is exactly what its owner approved on that computer.
- A helper can call a tool only if its profile was granted that tool, and only up to the call limit. The link on the computer decides each call; the hub is not in the way.
- Every call is recorded before it is made and again when it ends: which job, which helper, which tool, how long, and the result code. The text of what was asked and what came back is not sent to the hub and is not in the audit or the hub's database. It does appear wherever the helper shows it: its terminal view (which the hub can show to someone watching, with secrets hidden), its own notes, and the model it works with, which may be a cloud model.
- A **confidential job** is never given a tool that leaves the computer (by your answer at the grant). The job page says which ones it was refused.

What it does not do. Please read this part.

- **Helpers and add-ons run as the same user as the link, on the same computer.** Anything that user can read, an agent can read: files in Handloom's home, the environment of other programs of that user, and what an add-on keeps in its folder. That is why an add-on cannot keep a secret such as a password or a key: a manifest that lists secrets is refused. If a tool needs a key, give it to the call as an argument.
- **A confidential job with a shell is not kept off the network.** The rule above stops a confidential job from using a tool that leaves the computer. It does not stop a helper that has a shell from using the network by itself. Keep confidential jobs on profiles that run a local model and have no shell, or accept that limit.
- **A shared computer is shared.** If several people's helpers run on one computer, they share the add-ons installed there, and anyone who can run commands as that user can use the add-on's files and its running program. Install add-ons only on computers where you trust everyone who runs helpers.
- **A tool that says it stays on the computer is taken at its word.** The grant screen shows it; Handloom does not watch the add-on's network use. Read an add-on before you install it, the same as any program.
- **Revoking is not instant** for helpers that are already running (see above).

Stronger separation (a container or a separate user for the link) is designed but not built; see [isolation.md](isolation.md).

## When something does not work

A helper that cannot use a tool gets a code word. Here is what each one means and what to do.

| Code | Meaning | What to do |
| --- | --- | --- |
| `not-installed` | This computer does not have the add-on | Install it on the computer the job runs on |
| `denied-grant` | The helper's profile may not use that tool | Grant the tool on the profile |
| `denied-confidential` | A confidential job may not use a tool that leaves the computer | Use a tool that stays, or a different job; or grant it with `--leaves no` if it really does stay |
| `denied-budget` | The call limit was reached | Raise the limit on the grant |
| `changed` | The add-on is not the version that was approved or granted | Approve it again with `handloom addon add` (if the add-on is running, wait up to ten idle minutes or stop the link first), then grant again if its tools changed |
| `plugin-down` | The add-on is not running and could not be started | Run `handloom addon doctor` on the computer; the reason is printed |
| `plugin-error` | The add-on reported an error | Read the message; it comes from the add-on |
| `timeout` | The call took longer than its limit | Try again, or use a tool with a longer limit |

Two more things to know:

- An add-on written for a different add-on API than your Handloom speaks (its `addon.yaml` says `api: 2` and your program speaks `api: 1`, or the other way) is refused. `handloom addon add` says so and installs nothing. If one was already installed and your program changed, it shows **Not running** with the reason, and a call answers `plugin-down`. Use a Handloom that speaks that API, or a version of the add-on written for yours.
- `handloom doctor` has one line for each add-on on the computer. `handloom addon doctor` starts each one and asks it if it is well.

## Write your own

An add-on is a folder with an `addon.yaml` and a program. Look at `examples/addons/echo` (no network; returns what it is given and can save it as a file) and `examples/addons/web-fetch` (fetches a page and saves its text). Both are one `addon.yaml` and one Python file that uses only the standard library.

### addon.yaml

```yaml
schema: 1                     # the format of this file
kind: tool                    # the only kind that can be installed
name: echo                    # lower-case letters, digits, dashes; starts with a letter; at most 32
version: 0.1.0
api: 1                        # the add-on API you wrote it for
title: Echo
summary: One sentence on what it does.
run: ["python3", "service.py"]   # started inside the folder; no shell, so no pipes
hosts: []                     # the places it contacts; shown when installing ("*" for any)
secrets: []                   # must be empty
tools:
  - name: say
    summary: What this tool does, in one sentence a helper can act on.
    input:                    # a flat list: name -> type (string, number or boolean), required, summary
      text: {type: string, required: true, summary: "The text to return"}
    leaves_machine: false     # say honestly whether it sends anything off the computer
    timeout: 10s              # per call, at most 5m (default 30s)
    max_output: 1MB           # the text plus all files of one call, at most 50MB (default 1MB)
```

Arguments are checked against `input` before your program sees them: a missing required one, a wrong type or an unknown name is refused. A manifest with `kind: kit`, a non-empty `secrets` list, or another `api` is refused.

### The protocol

Handloom starts `run` in the add-on's folder with almost no environment (PATH, a temporary folder, `HANDLOOM_ADDON`, `HANDLOOM_ADDON_API`) and talks over standard input and output, one JSON object per line each way. Nothing else is used: no port.

```
in:  {"id":"c1","tool":"say","args":{"text":"hi","file":"a.txt"},"job":412,"results":"/path/to/call/folder"}
out: {"id":"c1","ok":true,"text":"hi","files":["a.txt"]}
out: {"id":"c1","ok":false,"error":"what went wrong, in plain words"}
in:  {"id":"h1","tool":"_health"}
out: {"id":"h1","ok":true}
```

- Answer every request with the same `id`. Lines that are not an answer to the current call are ignored, so a log line does no harm. One call is made at a time.
- `_health` is asked when the add-on is installed and by `handloom addon doctor`. Answer `ok: true` when you are well.
- Write files only into the `results` folder you are given, and list them in `files` by plain relative name. A name that leaves the folder fails the call.
- It is started on first use, restarted after a crash (at most three times in ten minutes, then it is marked not running), and stopped after ten idle minutes. Do not keep anything you need only in memory between calls.

### Limits

- Time: the tool's `timeout`. The call is killed and the helper gets `timeout`.
- Output: the text plus the files may not pass `max_output`. Over it, the call fails with `plugin-error`.
- Files: regular files only, no links, at most 20 per call. Anything else fails the call and the offending files are deleted.
- No secrets, no kits, no installing from a web address, and no reaching the hub: an add-on can only answer calls.

Before you share it, run `handloom addon hash FOLDER` and publish the hash next to it, so that people can check what they install.
