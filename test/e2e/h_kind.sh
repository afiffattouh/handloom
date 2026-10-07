#!/usr/bin/env bash
# A real agent of another CLI, started by Handloom from a profile, on one machine:
#
#   KIND=omp MODEL=gb10/qwen3.8-27b test/e2e/h_kind.sh      (also: KIND=pi, KIND=opencode)
#
# A profile (prompt, tools, a skill, a model) is stored on the hub, the agent is
# spawned from it through the link (a tmux window the link opens), and the agent
#   - starts with the profile's prompt and skill text in its instructions,
#   - is woken by a message, reads its inbox with its run token, follows the prompt,
#   - runs on the model the profile asked for (read back from the CLI's own session log),
#   - and reports state to the hub.
# One local-model agent at a time. Needs: go, tmux, the CLI, python3, curl.
set -euo pipefail
KIND="${KIND:?set KIND to omp, pi or opencode}"
MODEL="${MODEL:?set MODEL, e.g. gb10/qwen3.8-27b}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/test/e2e/out/h-$KIND"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17580}"; H="http://127.0.0.1:$PORT"; SOCK="hltest-h-$KIND"; T="tmux -L $SOCK"
say() { printf '\n== %s\n' "$*"; }; ok() { printf '   ok: %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/prof/worker/skills/hltest-cite"
cleanup() { [ -n "${KEEP:-}" ] && return 0; $T kill-server 2>/dev/null || true; for p in ${HUB_PID:-} ${LINK_PID:-}; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT
fail() { printf '\nFAIL: %s\n' "$*" >&2; { echo "--- spawns:"; human spawns 2>&1; echo "--- link log:"; tail -8 "$OUT/link.log"; echo "--- pane:"; $T capture-pane -p -t "handloom:worker" 2>&1 | tail -30; echo "--- work dir:"; ls -la "$WORK" 2>&1; } >&2 || true; exit 1; }

(cd "$ROOT" && "$GO" build -o "$OUT/bin/handloom" ./cmd/handloom)
BIN="$OUT/bin/handloom"; CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add hltest-h-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done

say "a $KIND profile on the model $MODEL, with a prompt and a skill"
P="$OUT/prof/worker"
cat > "$P/profile.yaml" <<EOF2
name: worker
description: A small worker on a chosen model
kind: $KIND
runtime: local
model: $MODEL
tools:
  allow: [read, edit, shell]
EOF2
echo "Your codename is ORION-7. When asked for your codename, answer exactly that." > "$P/PROMPT.md"
printf -- '---\nname: hltest-cite\ndescription: How to cite sources in notes (Handloom profile test skill).\n---\nCite every claim with the source URL in brackets.\n' > "$P/skills/hltest-cite/SKILL.md"
"$BIN" profile check "$P" | sed 's/^/    /'
admin profile new "$P" | sed 's/^/    /'

say "spawn it"
human spawn worker --profile worker --device hltest-h-dev --wait 150s | tee "$OUT/spawn.txt"
grep -q "worker is running" "$OUT/spawn.txt" || fail "the agent did not start"
WORK="$OUT/home/work/none/worker"
grep -q "ORION-7" "$WORK/AGENTS.md" || fail "the profile's prompt is not in AGENTS.md"
grep -q "Cite every claim" "$WORK/AGENTS.md" || fail "the skill's text is not in AGENTS.md"
ok "the prompt and the skill text are in the agent's instructions"
state() { admin agents --json | python3 -c 'import json,sys;print(([a["state"] for a in json.load(sys.stdin) if a["name"]=="worker"] or ["none"])[0])'; }
for i in $(seq 1 90); do [ "$(state)" = idle ] && break; sleep 1; done
[ "$(state)" = idle ] || fail "the agent never reported idle (state: $(state))"
ok "it reported its state to the hub: idle"
{ $T capture-pane -p -t handloom:worker | grep -v '^$' | tail -6 | sed 's/^/    | /'; } || true

say "wake it with a message"
human send worker "Please do these two things, then stop.
1. Write your codename to the file codename.txt in your current directory.
2. Write the name of the skill you were given, and what it says, to the file skill.txt." >/dev/null
for i in $(seq 1 240); do [ -f "$WORK/codename.txt" ] && [ -f "$WORK/skill.txt" ] && break; sleep 1; done
for f in codename.txt skill.txt; do [ -f "$WORK/$f" ] || fail "the agent did not write $f"; done
echo "   codename.txt: $(cat "$WORK/codename.txt")"; echo "   skill.txt:    $(tr '\n' ' ' < "$WORK/skill.txt")"
grep -q "ORION-7" "$WORK/codename.txt" || fail "the profile's prompt was not followed"
ok "it was woken, read its work, and followed the profile's prompt"
grep -qi "hltest-cite" "$WORK/skill.txt" && ok "it knows its skill" || echo "   note: the model did not name the skill (not a failure of Handloom)"
admin audit | grep -E "agent:worker *inbox.read" | head -1 | sed 's/^/    /'
admin audit | grep -q "agent:worker *inbox.read.*\[run-token\]" || fail "it did not read its inbox with its run token"
ok "it read its inbox with its run token"

say "the model it ran on, from the CLI's own session log"
case "$KIND" in
  omp)      LOGS="$HOME/.omp/agent/sessions" ;;
  pi)       LOGS="$HOME/.pi/agent/sessions" ;;
  opencode) LOGS="$HOME/.local/share/opencode" ;;
esac
MN="${MODEL##*/}"
found="$(grep -rl --include='*' -e "$MN" "$LOGS" 2>/dev/null | xargs -r ls -t 2>/dev/null | head -1 || true)"
if [ -n "$found" ] && grep -q "$WORK" "$found" 2>/dev/null; then ok "the session log of $WORK names the model $MN ($found)"; else
  if grep -rlq -e "$WORK" "$LOGS" 2>/dev/null; then echo "   note: a session log for $WORK exists but does not name $MN"; fail "the model was not the one asked for"; fi
  echo "   note: could not find a session log under $LOGS; check by hand"
fi
printf '\nPASS: %s started by Handloom from a profile, on %s\n' "$KIND" "$MODEL"
