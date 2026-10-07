# Handloom

> Formerly "handloom". The command is `handloom`; `hl` is the short alias. Old names (`handloom`, `HANDLOOM_*`, `Handloom-Agent`, `~/.config/handloom`) are still accepted. The project is not public yet.

Handloom runs jobs made of coding agents on your own machines. You describe a job; a lead agent plans it and starts workers; each worker gets its own copy of the repository; the machine that holds the files checks the work; you review and close. One hub keeps the record, and every machine only calls out to it.

See `docs/` for the design, [DECISIONS.md](DECISIONS.md) for choices and evidence, and [docs/deploy.md](docs/deploy.md) for deployment details.

## Quick start

Four stages. You do each one once.

**1. Run the hub (once, on a VPS or any server).** It is one container with one volume.

```
export HANDLOOM_BASE_URL=https://handloom.example.com    # the public https address
docker compose up -d --build
docker compose logs handloom | grep -E "Admin token|setup code"
```

Open `$HANDLOOM_BASE_URL/setup`, enter the setup code and make your owner account. On Dokploy, see [docs/deploy.md](docs/deploy.md). Keep the admin token somewhere safe.

**2. Join each machine that will run agents (once per machine).** A machine needs `git`, `tmux`, and at least one agent CLI that is logged in (Claude Code, Codex, Pi, OMP or OpenCode).

- Put the `handloom` binary on the machine: `scripts/setup.sh device <ssh-name> --hub <hub-url>` from this repository does it over ssh, or build it (`go build -o bin/handloom ./cmd/handloom`) and copy it.
- In the web UI open **Devices, Add a device**, and copy the join command it shows.
- On the machine run it (`handloom link join <hub-url> <join-token>`), then `handloom link install` so the link starts at boot.
- Run `handloom doctor`. It checks the link, git, tmux and the agent CLIs, and says what to fix.

**3. Make profiles (once per kind of agent).** In the web UI, **Profiles, New profile**. A profile says which CLI an agent uses, what it may do, its prompt, and (for workers) which paths it may change. Make one for a lead (usually read-only) and one or more for workers.

**4. Start a job.** **Jobs, New job**: a title, a brief, the device, optionally the repository path and a check command, and the lead's profile. The lead starts workers by itself. Questions and finished jobs arrive in your inbox. When every task is done, review `job/<id>/integration` in the repository and merge it yourself.

The inbox shows a **Get started** checklist until all four stages are done. Everything above also works from the command line: `handloom help`.

**Security.** Anyone who can post to your hub can in effect run code on every connected device, and agents often run without approval prompts. Use https (or a private network), a strong owner password, and revoke devices you no longer use. Only a signed-in human can start or close jobs, answer questions or change profiles.

## What is here

- **Hub** (`handloom hub`): HTTP API, web UI and one SQLite file. No AI, no git, no shell in it.
- **Link** (`handloom link`): one per device. Starts agents in tmux windows with their own git worktrees, wakes them, refuses submits outside the profile's paths, runs the job's check, merges accepted work.
- **CLI** (`handloom <verb>`, short `hl`): what agents and people type. Agents never hold a credential.
- **Jobs and tasks**: a job is a root task with its own lead; tasks have owners, leases, dependencies and need evidence to be submitted.
- **Profiles**: versioned, pinned by hash, with tools, denied commands, skills and write scope.
- **Adapters**: Claude Code and Codex (tested), Pi, OMP and OpenCode (less tested).
- **MCP server** (`handloom mcp`): the agent verbs as tools, for agents whose sandbox cannot reach the link.
- **Audit log**: every action, append-only.

## Build

```
go build -o bin/handloom ./cmd/handloom
go test ./...
```

One static binary. Cross-compile with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ...`.

## Set up a server with a script

`scripts/setup.sh` does it over ssh from this repo: it builds handloom for the server's CPU, copies the binary, and runs the hub or the link as a systemd service that starts at boot. `local` instead of an ssh name sets up this machine.

```
scripts/setup.sh hub vps                                   # prints the admin token once: store it
export HANDLOOM_TOKEN=hva_...
scripts/setup.sh device vps    --hub http://100.x.y.z:7420   # the hub's own machine
scripts/setup.sh device laptop --hub http://100.x.y.z:7420   # every other machine
scripts/setup.sh status laptop
scripts/setup.sh remove laptop --purge
```

- The hub listens on the server's Tailscale address by default (`--addr` to change). It is plain HTTP: keep it on a private network.
- `device` needs a join token. With `HANDLOOM_TOKEN` set to the admin token the script creates the device itself; otherwise pass `--join-token`.
- Linux with systemd only. Root gets a system service and `/usr/local/bin/handloom`; other users get a user service and `~/.local/bin/handloom`.
- Running it again is safe: it replaces the binary, keeps the credential and restarts the service.
- `remove` never deletes the hub's data.

After that, add agents on each machine (below). The script needs Go on the machine you run it from, or `--binary` with a prebuilt handloom.

## By hand

On the machine that runs the hub (bind it to a private address):

```
handloom hub init --data ./handloom-data              # prints the admin token once
handloom hub serve --data ./handloom-data --addr 100.x.y.z:7420
```

As admin, from anywhere that reaches the hub:

```
export HANDLOOM_HUB=http://100.x.y.z:7420 HANDLOOM_TOKEN=hva_...
handloom device add laptop                        # prints a one-time join token
handloom human add you                            # prints your human token
```

On each device:

```
handloom link join http://100.x.y.z:7420 hvj_...
handloom link run                                 # keep it running, or: handloom link install
```

In a project directory on a device, for a Claude Code agent:

```
handloom adapter install claude --name alice --dir .
tmux new -s alice claude                      # inside tmux or Herdr, so it can be woken
```

Make one agent the lead (admin or human): `handloom agent role alice lead`, then run the adapter install again so its instructions say so.

For a Codex agent (its sandbox blocks the link's socket, so it gets the handloom verbs as MCP tools):

```
handloom adapter install codex --name bob --dir .
tmux new -s bob handloom run bob -- codex -c 'mcp_servers.handloom.command="/path/to/handloom"' -c 'mcp_servers.handloom.args=["mcp"]'
```

For an agent with no terminal: `handloom register carol --kind claude --wake-target headless`, install the adapter, and create its session once with `HANDLOOM_HEADLESS=1 claude -p --session-id <uuid> "..."`. The link then wakes it with `claude -p --resume`.

From then on agents use `handloom inbox`, `handloom send`, `handloom task ...`. Run `handloom help` for the full list.

## Tests

- `go test ./...`: unit tests. Every cell of the scope table has a test that proves a forbidden action is rejected.
- `test/e2e/m0.sh`: two shells on two machines run a task from creation to acceptance with `handloom` commands only, and a lease expires.
- `test/e2e/m1.sh`: a lead Claude Code and a worker Claude Code on two machines finish a three-task job after one brief.
- `test/e2e/m2.sh`: the same with a Claude Code lead, a Codex worker and a headless Claude Code worker (and a Pi worker when `PI_MODEL` is set).

The e2e scripts need a second machine reachable by ssh (see `test/e2e/lib.sh`).

## Layout

```
cmd/handloom/            main
internal/hub/        HTTP API, scopes, leases, events, audit
internal/store/      SQLite schema and migrations
internal/link/       daemon, local socket, wake ladder
internal/drivers/    tmux and herdr nudge drivers
internal/cli/        verbs, hooks, adapter installer
internal/mcp/        stdio MCP server
internal/client/     HTTP client and link configuration
internal/api/        wire types
adapters/            instruction snippets; per kind: manifest and, for pi/omp/opencode, the shim
docs/protocol.md     the API, version handloom/1
scripts/setup.sh     set up a server: hub or device, as a service
test/e2e/            two-machine scenarios
```

## Licence

Apache-2.0 (proposed). See [LICENSE](LICENSE).
