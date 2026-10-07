# Isolation: running agents in a box (designed, not built)

Today an agent runs directly on the machine, as the user who runs the link, in a tmux window the link opens. What limits it is its CLI (tool limits, and for Claude Code and OpenCode named shell commands), its own git worktree, the scope check at submit, the device's check command, and the fact that only a human merges. Nothing separates it from the rest of that user's files.

A **container mode** would run each agent CLI in a container that sees only its work folder. This page is the design, so that adding it later is a contained piece of work and not a redesign. It is not built, and `handloom link run --isolation container` says so.

## The seam that exists now

`internal/link/isolation.go` defines `Isolator`: given a `LaunchPlan` (the spawn, its work directory, Handloom's home, the binary, and the `spawn-exec` command line) it returns the command line the agent's tmux window runs. The only isolator today is `none`, which returns the line unchanged. A container isolator returns `docker run ... <image> <that same line>` instead. Everything else in the link keeps working because the agent still lives in a tmux window:

- the wake ladder types nudges into the window, and sees that the pane exists (a container in the foreground is not a shell, so the shell guard does not trip);
- the terminal view reads the window with `capture-pane`;
- the scope check, verification and merges run on the host against the work folder, which the container sees through a bind mount;
- the lease and "offline" logic watch the pane.

To add it: write the isolator, register it in `isolators`, remove its name from `planned`. Choosing it per link is `--isolation`; choosing it per job or per profile later needs a field on the spawn (not on the profile spec: the spec is hashed and pinned, and where something runs is not part of what an agent may do).

## What the container mode has to solve

These are the places the current code assumes the host. Each is a task, not a surprise.

1. **The link socket.** `spawn-exec` and the agent's `handloom` commands talk to the link through `$HANDLOOM_HOME/link.sock`. Mount the socket and set `HANDLOOM_HOME` to the mounted path. The device credential must never enter the box: only the socket does, as now.
2. **The CLI's login.** Claude Code, Codex, OMP, Pi and OpenCode each keep credentials in the user's home (`~/.claude/.credentials.json` and `~/.claude.json`, `~/.codex/auth.json`, `~/.omp/agent`, `~/.pi/agent`, `~/.config/opencode` and `~/.local/share/opencode`). Give each agent its own home directory under `<handloom home>/boxes/<agent>/home`, seeded with the minimum of those files, read-only where the CLI allows. `trustClaudeDir` and `trustCodexDir` then write into that home instead of the real one.
3. **Git worktrees.** A worktree's `.git` is a file that points into the repository's own `.git/worktrees/<name>`, outside the work folder. Mount the repository's `.git` read-write (and the knowledge repository's, for jobs that have one). This is the one place the box can still damage the repository's history, so the scope check and the integration branch remain the real protection.
4. **The model.** A local model is a network address (for example `model-host.example:8083` over Tailscale). The container needs a route to exactly that, and nothing else if the profile allows no web: `--network none` plus a proxy, or a network that only reaches the model.
5. **Usage and session logs.** The link reads token counts from the CLI's own logs. With a per-agent home they are in `boxes/<agent>/home`, which the link can read as before; point the readers at it.
6. **The CLI itself.** The image needs the five CLIs installed and the `handloom` binary. Either one image per CLI built by the owner (`handloom-agent:omp`) with a documented Dockerfile, or the host's installs mounted read-only. One image per CLI is simpler and keeps versions explicit.
7. **File ownership.** Run as the host user's uid so files the agent writes in the work folder belong to the user.
8. **Limits and hardening.** No Docker socket. Drop all capabilities, read-only root filesystem with a tmpfs for `/tmp`, memory, CPU and process limits. A container that outlives its agent is removed when the window closes (`--rm`).
9. **Stopping.** Closing the tmux window stops `docker run -it`; make sure the container is stopped and removed too.

## What it does not change

The scope check, the device check, per-agent worktrees, human-only approval and the audit log are the same in both modes. The container is a second wall, not a replacement for those. It matters most where the machine is shared, the model or its tools are not trusted, or the CLI cannot refuse specific commands (OMP, Pi, Codex).

## Until then

Use a dedicated machine or user for agents and join that as the device, and prefer Codex (its sandbox limits files and network) for work that touches something sensitive.
