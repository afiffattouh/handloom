#!/usr/bin/env bash
# Live check of the herdr wake driver, on this machine only.
#
# It splits the calling Herdr pane (the new pane does not take focus), starts
# a Claude Code agent there, sends that agent a handloom message while it is
# idle, and passes if the link wakes it with `herdr agent prompt` and the
# agent reads its inbox. The pane it created is closed at the end.
#
# Run from inside a Herdr pane: test/e2e/herdr.sh
#   CLAUDE_MODEL=...   model for the agent (default sonnet)
#   PORT=7421          local port for the throwaway hub
set -euo pipefail

[ "${HERDR_ENV:-}" = 1 ] || { echo "run this inside a Herdr pane" >&2; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
MODEL="${CLAUDE_MODEL:-sonnet}"
PORT="${PORT:-7421}"
HUB_URL="http://127.0.0.1:$PORT"
OUT="$ROOT/test/e2e/out/herdr"
BIN="$ROOT/bin/handloom"
AGENT=hltest-herdr
PANE=""

say()  { printf '\n== %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }
cleanup() {
  [ -z "$PANE" ] || herdr pane close "$PANE" >/dev/null 2>&1 || true
  kill "${HUB_PID:-}" "${LINK_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT
human() { HANDLOOM_HUB="$HUB_URL" HANDLOOM_TOKEN="$HUMAN_TOKEN" "$BIN" "$@"; }
jget() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

say "build, hub and link (local, port $PORT)"
(cd "$ROOT" && CGO_ENABLED=0 "$GO" build -o bin/handloom ./cmd/handloom)
ss -ltn | grep -q ":$PORT\b" && fail "port $PORT is in use"
rm -rf "$OUT"
mkdir -p "$OUT/home" "$OUT/project/.claude"
export HANDLOOM_HOME="$OUT/home"
"$BIN" hub init --data "$OUT/hub" >"$OUT/hub-init.txt"
ADMIN_TOKEN="$(grep '^hva_' "$OUT/hub-init.txt")"
"$BIN" hub serve --data "$OUT/hub" --addr "127.0.0.1:$PORT" >"$OUT/hub.log" 2>&1 &
HUB_PID=$!
for _ in $(seq 50); do curl -fsS "$HUB_URL/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
admin() { HANDLOOM_HUB="$HUB_URL" HANDLOOM_TOKEN="$ADMIN_TOKEN" "$BIN" "$@"; }
join="$(admin device add hltest-herdr-device --json | jget 'd["token"]')"
"$BIN" link join "$HUB_URL" "$join" | head -1
"$BIN" link run >"$OUT/link.log" 2>&1 &
LINK_PID=$!
for _ in $(seq 50); do [ -S "$OUT/home/link.sock" ] && break; sleep 0.2; done
HUMAN_TOKEN="$(admin human add hltest-human --json | jget 'd["token"]')"

say "agent in a new Herdr pane"
env -u HERDR_ENV -u HERDR_PANE_ID "$BIN" register "$AGENT" --kind claude | head -1
"$BIN" adapter install claude --name "$AGENT" --dir "$OUT/project" | head -1
cat >"$OUT/project/.claude/settings.json" <<'JSON'
{"permissions": {"defaultMode": "dontAsk", "allow": ["Read", "Glob", "Grep"]}}
JSON
PANE="$(herdr pane split --current --direction down --cwd "$OUT/project" --no-focus | jget 'd["result"]["pane"]["pane_id"]')"
echo "    created pane $PANE"
sleep 1
herdr pane run "$PANE" "HANDLOOM_HOME='$OUT/home' PATH='$ROOT/bin':\"\$PATH\" claude --model '$MODEL'" >/dev/null

agent_field() { human agents --json | jget "[a.get('$1','') for a in d if a['name']=='$AGENT'][0]"; }
for i in $(seq 90); do
  text="$(herdr pane read "$PANE" --source visible --format text 2>/dev/null || true)"
  if grep -q 'Yes, I trust this folder' <<<"$text"; then
    if grep -qE '❯ *Yes, I trust this folder' <<<"$text"; then
      herdr pane send-keys "$PANE" enter >/dev/null; sleep 2
    else
      herdr pane send-keys "$PANE" down >/dev/null; sleep 1
    fi
    continue
  fi
  [ "$(agent_field state)" = idle ] && break
  sleep 1
done
[ "$(agent_field state)" = idle ] || fail "the agent did not become ready"
target="$(agent_field wake_target)"
echo "    $AGENT is idle, wake target $target"
[ "$target" = "herdr:$PANE" ] || fail "wake target is $target, want herdr:$PANE"
sleep 3

say "mail for the idle agent"
AT="$(human audit --json | jget 'd[-1]["seq"]')"
human send "$AGENT" "Herdr driver check. Please run: handloom whoami"
ok=""
for _ in $(seq 40); do
  ok="$(human audit --json | python3 -c '
import json, sys
rows = [r for r in json.load(sys.stdin) if r["seq"] > int(sys.argv[1])]
wake = [r for r in rows if r["action"] == "wake"]
read = [r for r in rows if r["action"] == "inbox.read"]
if wake and read and wake[0]["seq"] < read[0]["seq"]:
    print(wake[0]["payload"]["method"])' "$AT")"
  [ -n "$ok" ] && break
  sleep 3
done
human audit | awk -v after="$AT" '$1 > after'
echo
herdr pane read "$PANE" --source recent --format text 2>/dev/null | grep -v '^\s*$' | tail -12 | sed 's/^/    | /'
[ "$ok" = herdr ] || fail "expected a wake through herdr followed by an inbox read, got '${ok:-nothing}'"
printf '\nHERDR DRIVER PASS: the link woke %s in pane %s with `herdr agent prompt`, and it read its inbox.\n' "$AGENT" "$PANE"
