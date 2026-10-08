#!/usr/bin/env bash
# A demo hub with believable work in it, for screenshots (the README's pictures). Real hub, real link, real tmux;
# the "agents" are shell panes that stand in for agent CLIs, so nothing here calls a model.
#   PLAYWRIGHT_MODULE=/path/to/playwright test/e2e/demo.sh      -> docs/img/*.png
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/demo"; PORT="${PORT:-17790}"; H="http://127.0.0.1:$PORT"; SOCK=hltest-demo; T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$ROOT/docs/img"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
ADMIN="$($CLEAN "$BIN" hub init --data "$OUT/data" 2>&1 | grep -o 'hva_[A-Za-z0-9_-]*' | head -1)"
PW="$($CLEAN "$BIN" hub create-owner Maya --data "$OUT/data" | sed -n 2p)"
HT="$($CLEAN "$BIN" hub reset-human-token Maya --data "$OUT/data" | sed -n 2p)"
HANDLOOM_BASE_URL="$H" HANDLOOM_INSECURE=1 HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB=$!
cleanup() { $T kill-server 2>/dev/null; kill $HUB $LINK 2>/dev/null; true; }; trap cleanup EXIT
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
agent() { local a="$1"; shift; $CLEAN env HANDLOOM_AGENT="$a" "$BIN" "$@"; }
JT="$(admin device add studio-mac --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
admin device add build-box >/dev/null
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
for p in "lead-engineering --kind claude --runtime cloud --name lead" "coder --kind claude --runtime cloud" "researcher --kind omp --runtime local --model qwen3.8-27b" "qa-tester --kind opencode --runtime local --model qwen3.8-27b"; do admin profile add $p --adapt >/dev/null; done
admin people add Jonas --role member >/dev/null; admin people add Priya --role viewer >/dev/null
# a lead whose terminal has something to read
$T new-session -d -s lead -x 110 -y 28 "HANDLOOM_HOME=$OUT/home $BIN run lead-ui --kind shell -- sh -c 'printf \"\\033[2m\$ handloom inbox\\033[0m\\n2 messages\\n  worker-1: sign-in form submitted (task #2)\\n  worker-2: session API submitted (task #3)\\n\\033[2m\$ handloom task show 2\\033[0m\\n#2 submitted  Write the sign-in form\\n  evidence: commit:3fa9c2e, test:go test ./... -> ok\\n  check: passed on build-box (1.4s)\\n\\nReviewing both before I accept. One question for you first.\\n\"; sleep 900'"
for i in $(seq 1 30); do admin agents --json 2>/dev/null | grep -q '"lead-ui"' && break; sleep 0.5; done
human agent role lead-ui lead >/dev/null
for w in worker-1 worker-2 worker-3; do agent "$w" register "$w" --kind shell >/dev/null; done
human job new "Ship the login page" --body "Build the sign-in form and wire it to the session API. Done means a person can sign in and out, and bad passwords show an error under the field." --lead lead-ui >/dev/null
human task create "Write the sign-in form" --job 1 --assign worker-1 --body "Fields: email and password. Show errors under the field." >/dev/null
human task create "Wire the session API" --job 1 --assign worker-2 >/dev/null
human task create "Add rate limiting" --job 1 --assign worker-3 >/dev/null
human task create "Write the sign-in tests" --job 1 --assign worker-3 >/dev/null
for t in "2 worker-1" "3 worker-2"; do set -- $t; agent "$2" task claim "$1" >/dev/null; done
agent worker-1 task submit 2 --evidence "commit:3fa9c2e" --evidence "test:go test ./... -> ok" --note "Form done, errors show under each field." >/dev/null
agent worker-2 task submit 3 --evidence "commit:81b44d0" --note "Session API wired; sign-out clears the cookie." >/dev/null
agent worker-3 task claim 4 >/dev/null; agent worker-1 task submit 2 --evidence "commit:3fa9c2e" >/dev/null 2>&1; human task reject 3 --reason "The cookie should be HttpOnly and SameSite=Lax." >/dev/null 2>&1; agent worker-2 task claim 3 >/dev/null 2>&1; agent worker-2 task submit 3 --evidence "commit:c19e0a4" --note "Cookie is now HttpOnly, SameSite=Lax." >/dev/null 2>&1
agent lead-ui ask "Which store should sessions use?" --option "SQLite (simple, one file)" --option "Postgres (shared)" >/dev/null
human job new "Fix the flaky CSV parser" --body "The parser drops the last row when the file has no trailing newline." --lead lead-ui >/dev/null
human task create "Reproduce the dropped row" --job 6 --assign worker-1 >/dev/null; human task create "Fix and add a regression test" --job 6 --assign worker-2 >/dev/null
agent worker-1 task claim 7 >/dev/null; agent worker-1 task submit 7 --evidence "test:go test ./csv -run TestNoTrailingNewline -> FAIL (reproduced)" >/dev/null
human task accept 7 >/dev/null
agent worker-2 task claim 8 >/dev/null; agent worker-2 task submit 8 --evidence "commit:b7a21c9" --evidence "test:go test ./... -> ok" >/dev/null
human task accept 8 >/dev/null
human job new "Write the onboarding docs" --body "Draft docs/onboarding.md for new engineers." --lead lead-ui >/dev/null
human digest | sed 's/^/    /'
# the web pictures
HANDLOOM_TOKEN="" PLAYWRIGHT_MODULE="${PLAYWRIGHT_MODULE:-playwright}" node "$ROOT/test/e2e/demo_shots.js" "$H" Maya "$PW" "$ROOT/docs/img" || exit 1
# the terminal picture: capture the console with its colours and draw it
$T new-session -d -s tui -x 118 -y 32 -e HANDLOOM_HUB="$H" -e HANDLOOM_TOKEN="$HT" -e COLORTERM=truecolor -e TERM=xterm-256color "$BIN" tui
sleep 4; $T send-keys -t tui 2; sleep 1; $T capture-pane -e -p -t tui > "$OUT/tui-inbox.ansi"
$T send-keys -t tui 1; sleep 1; $T capture-pane -e -p -t tui > "$OUT/tui-overview.ansi"
python3 "$ROOT/test/e2e/ansi2html.py" "$OUT/tui-overview.ansi" "$OUT/tui-overview.html" "Handloom console"
python3 "$ROOT/test/e2e/ansi2html.py" "$OUT/tui-inbox.ansi" "$OUT/tui-inbox.html" "Handloom console"
HANDLOOM_TOKEN="" PLAYWRIGHT_MODULE="${PLAYWRIGHT_MODULE:-playwright}" node "$ROOT/test/e2e/demo_shots.js" --html "$OUT/tui-overview.html" "$ROOT/docs/img/tui-overview.png"
HANDLOOM_TOKEN="" PLAYWRIGHT_MODULE="${PLAYWRIGHT_MODULE:-playwright}" node "$ROOT/test/e2e/demo_shots.js" --html "$OUT/tui-inbox.html" "$ROOT/docs/img/tui-inbox.png"
ls -la "$ROOT/docs/img"
