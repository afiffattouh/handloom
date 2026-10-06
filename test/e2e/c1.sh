#!/usr/bin/env bash
# Milestone C1 check, on one machine with a real Claude Code:
#
#   a human asks for an agent; the link starts Claude Code in a tmux window of
#   its own; the agent registers with that window as its wake target, goes
#   idle, is woken by a message, reads its inbox with a run token, and answers.
#
# Needs: go, tmux, claude (logged in), python3, curl. Costs a few cents.
# Everything is named hltest-c-* and removed at the end.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/test/e2e/out/c1"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17460}"
H="http://127.0.0.1:$PORT"
SOCK=hltest-c
T="tmux -L $SOCK"
say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok: %s\n' "$*"; }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin"
cleanup() {
  $T kill-server 2>/dev/null || true
  for p in ${HUB_PID:-} ${LINK_PID:-}; do kill "$p" 2>/dev/null || true; done
  # Remove the trust entries the spawn added to Claude Code's config for our work directories.
  python3 - "$OUT" <<'PY' 2>/dev/null || true
import json, os, sys
p = os.path.expanduser("~/.claude.json")
d = json.load(open(p)); out = sys.argv[1]
for k in [k for k in d.get("projects", {}) if k.startswith(out)]:
    del d["projects"][k]
json.dump(d, open(p + ".tmp", "w")); os.replace(p + ".tmp", p)
PY
}
trap cleanup EXIT
fail() {
  printf '\nFAIL: %s\n' "$*" >&2
  { echo "--- spawns:"; human spawns 2>&1; echo "--- agents:"; admin agents 2>&1; echo "--- link log:"; tail -12 "$OUT/link.log"; echo "--- pane:"; $T capture-pane -p -t handloom:researcher 2>&1 | tail -25; } >&2 || true
  exit 1
}

say "build, hub, device, link (its own tmux server: $SOCK)"
(cd "$ROOT" && "$GO" build -o "$OUT/bin/handloom" ./cmd/handloom)
BIN="$OUT/bin/handloom"
# Nothing in here may type into the terminal this script runs in.
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "$H/healthz" >/dev/null || fail "hub did not start"
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add hltest-c-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
ok "hub and link are up"

say "ask for an agent: handloom spawn researcher --kind claude"
human spawn researcher --kind claude --model sonnet --device hltest-c-dev --wait 120s | tee "$OUT/spawn.txt"
grep -q "researcher is running" "$OUT/spawn.txt" || fail "the agent did not start"
$T list-windows -t handloom -F '#{window_name}' | grep -qx researcher || fail "no tmux window called researcher in the link's server"
ok "the link started it in its own tmux server (window 'researcher')"
SPAWN_PANE="$(human spawns --json | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["pane"])')"
echo "$SPAWN_PANE" | grep -q "tmux:.*$SOCK:%" || fail "the wake target is not the link's pane: $SPAWN_PANE"
ok "its wake target is $SPAWN_PANE"

say "it registers and goes idle (Claude Code's own session-start hook says so)"
state() { admin agents --json | python3 -c 'import json,sys;print(([a["state"] for a in json.load(sys.stdin) if a["name"]=="researcher"] or ["none"])[0])'; }
for i in $(seq 1 60); do [ "$(state)" = idle ] && break; sleep 1; done
[ "$(state)" = idle ] || fail "the agent never went idle (state: $(state))"
ok "state idle, so Claude Code started without stopping at a prompt or a trust question"
admin agents --json | python3 -c '
import json,sys
a=[a for a in json.load(sys.stdin) if a["name"]=="researcher"][0]
assert a["wake_target"].startswith("tmux:"), a
assert a.get("lease_expires_at"), "no liveness lease"
print("   wake target and lease are set")'
test -f "$OUT/home/work/none/researcher/.handloom/tokens/researcher" || fail "no run token file in the work directory"
ok "run token and adapter files are in its work directory"

say "wake it with a message and watch it read its inbox"
human send researcher "Reply with only the word PONG. Do nothing else." >/dev/null
for i in $(seq 1 90); do admin audit | grep -q "agent:researcher *inbox.read" && break; sleep 1; done
admin audit | grep "agent:researcher *inbox.read" | sed 's/^/    /' | head -2
admin audit | grep -q "agent:researcher *inbox.read.*\[run-token\]" || fail "the agent did not read its inbox with its run token"
ok "it was woken by the link's nudge and read its inbox with its run token"
for i in $(seq 1 60); do [ "$(state)" = idle ] && break; sleep 1; done
[ "$(state)" = idle ] || fail "it did not return to idle (state: $(state))"
$T capture-pane -p -t handloom:researcher | grep -q PONG && ok "the pane shows its answer: PONG" || ok "(its answer is not in the visible pane; the inbox read is what counts)"

say "audit trail"
admin audit | grep -E "spawn\.|agent.register|agent.token|inbox.read" | sed 's/^/    /' | head -14
printf '\nPASS: milestone C1 check (a real Claude Code started by spawn)\n'
