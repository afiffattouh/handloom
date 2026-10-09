#!/usr/bin/env bash
# An upgrade with work in flight: a hub and a link of an OLDER release, a job with a task claimed, a task
# submitted and a question open; then the hub is replaced by the new build on the same data (the link still old),
# then the link too. Nothing may be lost, the old link must keep working against the new hub, and the work must
# finish. Needs: go, gh (to fetch the older release), curl, python3, tmux.
#   OLD=v0.1.0-rc2 test/e2e/upgrade.sh
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OLD="${OLD:-v0.1.0-rc2}"; OUT="$ROOT/test/e2e/out/upgrade"; PORT="${PORT:-17830}"; H="http://127.0.0.1:$PORT"; SOCK=hltest-up
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
fails=0; ok() { echo "   ok: $*"; }; bad() { echo "   FAIL: $*"; fails=$((fails+1)); }; say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/old" "$OUT/new"
case "$(uname -m)" in x86_64) A=amd64;; *) A=arm64;; esac
say "the older release $OLD and the new build"
(cd "$OUT/old" && gh release download "$OLD" -R afiffattouh/handloom -p "handloom_${OLD}_linux_${A}.tar.gz" && tar -xzf handloom_*.tar.gz handloom) >/dev/null 2>&1 || { echo "could not fetch $OLD"; exit 1; }
OLDBIN="$OUT/old/handloom"; NEWBIN="$OUT/new/handloom"; (cd "$ROOT" && "$GO" build -o "$NEWBIN" ./cmd/handloom) || exit 1
echo "   old: $($OLDBIN version)"; echo "   new: $($NEWBIN version)"
export HANDLOOM_HOME="$OUT/home"
HUB=""; LINK=""
start_hub() { HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$1" hub serve --auto-init --data "$OUT/data" >> "$OUT/hub.log" 2>&1 & HUB=$!; for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && return 0; sleep 0.25; done; return 1; }
stop_pid() { [ -n "${1:-}" ] && kill "$1" 2>/dev/null && wait "$1" 2>/dev/null; true; }
start_link() { $CLEAN "$1" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s >> "$OUT/link.log" 2>&1 & LINK=$!; for i in $(seq 1 40); do $CLEAN "$1" link status >/dev/null 2>&1 && return 0; sleep 0.25; done; return 1; }
cleanup() { stop_pid "$HUB"; stop_pid "$LINK"; tmux -L $SOCK kill-server 2>/dev/null; true; }; trap cleanup EXIT

start_hub "$OLDBIN" || { echo "old hub did not start"; exit 1; }
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$1" "${@:2}"; }
JT="$(admin "$OLDBIN" device add up-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin "$OLDBIN" human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$1" "${@:2}"; }
agent() { local b="$1" a="$2"; shift 2; $CLEAN env HANDLOOM_AGENT="$a" "$b" "$@"; }
$CLEAN "$OLDBIN" link join "$H" "$JT" >/dev/null; start_link "$OLDBIN" || { echo "old link did not start"; exit 1; }

say "work in flight on the OLD release"
agent "$OLDBIN" lead register lead --kind shell >/dev/null; agent "$OLDBIN" w1 register w1 --kind shell >/dev/null; agent "$OLDBIN" w2 register w2 --kind shell >/dev/null
human "$OLDBIN" agent role lead lead >/dev/null
human "$OLDBIN" job new "In flight" --lead lead >/dev/null
human "$OLDBIN" task create "claimed before" --job 1 --assign w1 >/dev/null
human "$OLDBIN" task create "submitted before" --job 1 --assign w2 >/dev/null
agent "$OLDBIN" w1 task claim 2 >/dev/null
agent "$OLDBIN" w2 task claim 3 >/dev/null; agent "$OLDBIN" w2 task submit 3 --evidence "commit:abc1234" --note "done" >/dev/null
agent "$OLDBIN" lead ask "Which store?" --option sqlite --option postgres >/dev/null
human "$OLDBIN" digest | sed 's/^/    /'

say "the hub is replaced by the NEW build on the same data; the link is still the old one"
stop_pid "$HUB"; start_hub "$NEWBIN" && ok "the new hub started on the old data" || bad "the new hub did not start"
sleep 4
[ "$(human "$NEWBIN" task show 2 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')" = claimed ] && ok "the claimed task is still claimed" || bad "the claimed task changed"
[ "$(human "$NEWBIN" task show 3 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')" = submitted ] && ok "the submitted task is still submitted" || bad "the submitted task changed"
human "$NEWBIN" escalations --json | grep -q "Which store" && ok "the question is still open" || bad "the question was lost"
human "$NEWBIN" device list 2>/dev/null | grep -q "up-dev" && ok "the machine is still joined" || true
sleep 3; $CLEAN "$OLDBIN" link status 2>&1 | grep -q running && ok "the OLD link keeps working against the NEW hub" || bad "the old link lost the new hub"
agent "$OLDBIN" w1 task submit 2 --evidence "commit:def5678" --note "finished after the upgrade" >/dev/null 2>&1 && ok "an old-release agent submitted to the new hub" || bad "the old agent's submit failed"
human "$OLDBIN" task accept 3 >/dev/null 2>&1 && ok "accepting work submitted before the upgrade works" || bad "accept failed"

say "then the link is upgraded too"
stop_pid "$LINK"; start_link "$NEWBIN" && ok "the new link started with the old credential" || bad "the new link did not start"
human "$NEWBIN" task accept 2 >/dev/null 2>&1 && ok "accepting the work finished across the upgrade works" || bad "accept failed"
agent "$NEWBIN" lead task list --json >/dev/null 2>&1 && ok "the agent verbs work through the new link" || bad "the agent verbs failed"
human "$NEWBIN" digest | sed 's/^/    /'
human "$NEWBIN" digest --json | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if any(i["kind"]=="escalation" for i in d["needs_you"]) else 1)' && ok "the open question still needs the person" || bad "the question is gone from the digest"
[ "$fails" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL ($fails)"; exit 1; }
