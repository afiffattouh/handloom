#!/usr/bin/env bash
# Starts one Claude Code test agent in its own tmux session on the dedicated
# "hltest" tmux server. Runs on the machine the agent lives on.
#
# usage: agent.sh <name> <handloom-home> <project-dir> <handloom-bin> <claude-bin> <model>
#
# The agent must already be registered (so its role is set). This installs
# the adapter, writes the test project's permission allowlist and starts
# `claude` with no prompt: the first thing the agent sees is whatever is
# typed into its terminal.
set -euo pipefail
name="$1" home="$2" dir="$3" handloom="$4" claude="$5" model="$6"

# The agent must not inherit wake variables from the terminal that runs this.
unset HERDR_ENV HERDR_PANE_ID HERDR_SOCKET_PATH TMUX TMUX_PANE
export HANDLOOM_HOME="$home"

mkdir -p "$dir/.claude"
"$handloom" adapter install claude --name "$name" --dir "$dir"

# Test agents must never stop at an approval prompt: the wake ladder would
# see "blocked" and the job would hang. Bypass mode is refused for root, so
# both machines run in "dontAsk" mode with the same allowlist: a tool call
# outside the list is denied with an error the agent can read, never asked
# about. handloom itself is allowed by the adapter.
cat >"$dir/.claude/settings.json" <<'JSON'
{
  "permissions": {
    "defaultMode": "dontAsk",
    "allow": [
      "Read", "Glob", "Grep", "Write", "Edit",
      "Bash(uname *)", "Bash(uname)", "Bash(hostname)", "Bash(hostname *)",
      "Bash(cat *)", "Bash(ls *)", "Bash(ls)", "Bash(echo *)", "Bash(printf *)", "Bash(wc *)"
    ]
  }
}
JSON

tmux -L hltest new-session -d -s "$name" -x 160 -y 50 -c "$dir" \
  "env -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH HANDLOOM_HOME='$home' PATH='$(dirname "$handloom")':\"\$PATH\" '$claude' --model '$model'"
echo "started $name in tmux session $name (tmux -L hltest attach -t $name)"
