#!/usr/bin/env bash
# Starts a hub with the web UI and a little data, then takes screenshots of every page (see ui_shots.js).
#   PLAYWRIGHT_MODULE=/home/user/projects/guardai/node_modules/playwright test/e2e/ui_shots.sh   -> test/e2e/out/ui-shots/*.png
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/ui-shots"; PORT="${PORT:-17780}"; H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
rm -rf "$OUT"; mkdir -p "$OUT/data" "$OUT/bin" "$OUT/home"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home"
ADMIN="$($CLEAN "$BIN" hub init --data "$OUT/data" 2>&1 | grep -o 'hva_[A-Za-z0-9_-]*' | head -1)"
PW="$($CLEAN "$BIN" hub create-owner Afif --data "$OUT/data" | sed -n 2p)"
HANDLOOM_BASE_URL="$H" HANDLOOM_INSECURE=1 HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB=$!
trap 'kill $HUB 2>/dev/null' EXIT
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
admin device add GB10 >/dev/null; admin device add Mantis >/dev/null
for s in lead-engineering coder researcher; do admin starters >/dev/null; done
admin profile add lead-engineering --kind claude --runtime cloud --name lead >/dev/null
admin profile add coder --kind claude --runtime cloud >/dev/null
admin profile add researcher --kind omp --runtime local --model gb10/qwen3.8-27b --adapt >/dev/null
admin job new "Fix the parser" --repo /tmp/none --device GB10 --lead-profile lead --body "Fix the off-by-one in the CSV parser." >/dev/null 2>&1
admin job new "Write the onboarding docs" --device Mantis --lead-profile lead --body "Draft docs/onboarding.md." >/dev/null 2>&1
admin people add mia --role member >/dev/null 2>&1
HANDLOOM_TOKEN="" PLAYWRIGHT_MODULE="${PLAYWRIGHT_MODULE:-playwright}" node "$ROOT/test/e2e/ui_shots.js" "$H" Afif "$PW" "$OUT"
