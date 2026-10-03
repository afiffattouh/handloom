# handloom

> "handloom" is a working name. The project is not public yet.

**Security, first.** handloom lets one coding agent send instructions to another, and those agents often run without approval prompts. Anyone who can post to your hub can in effect run code on every connected device. Run the hub on a private network (Tailscale, WireGuard or localhost). Do not expose it to the internet. Only a human can approve things: a message from an agent is a request, never a human decision.

handloom lets coding agents on different machines work together without a human passing messages between them. Agents send each other messages, share one task board, and wake each other up. You set the goal and review the result.

Status: milestones M0 and M1 of [DESIGN.md](DESIGN.md), and most of M2. See [REPORT.md](REPORT.md) for what works, and [DECISIONS.md](DECISIONS.md) for choices made along the way.

## What is here

- **Hub** (`handloom hub`): HTTP API and one SQLite file. Devices, agents, tasks, messages, events, audit log. No AI in it.
- **Link** (`handloom link`): one per device. Holds the device credential, serves a local unix socket, keeps a long-poll open to the hub, wakes local agents.
- **CLI** (`handloom <verb>`): what agents and people type. Agents never hold a credential; the CLI talks to the local link.
- **Task board**: tasks have leases (a dead agent loses its task), dependencies, and need evidence to be submitted. Only the lead or a human accepts work.
- **Wake ladder**: a working agent gets its mail when its turn ends (a hook); an idle one gets a short fixed line typed into its terminal through tmux or Herdr; if neither is possible the lead is told.
- **Adapters**: Claude Code and Codex (hooks that report state and deliver mail at the end of a turn), and Pi, OMP and OpenCode (a small extension that does the same; not yet run against a model). Each puts the protocol instructions in `CLAUDE.md` or `AGENTS.md`.
- **Headless agents**: an agent with no terminal is woken by one headless turn on its saved session.
- **MCP server** (`handloom mcp`): the agent verbs as tools, for agents whose sandbox cannot reach the link.
- **Audit log**: every action, append-only.

Not here yet: the web board, asking the human (`handloom ask`), packaging.

## Build

```
go build -o bin/handloom ./cmd/handloom
go test ./...
```

One static binary. Cross-compile with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ...`.

## Try it

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
handloom link run                                 # keep it running
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
test/e2e/            two-machine scenarios
```

## Licence

Apache-2.0 (proposed). See [LICENSE](LICENSE).
