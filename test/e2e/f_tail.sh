#!/usr/bin/env bash
# The terminal view, end to end on one machine with real tmux: a person opens an
# agent's page, the link reads the agent's pane, secrets are removed on the
# machine, the text appears in the page, and it is in no database table.
#
# Needs: go, tmux, curl, python3.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/f"; PORT="${PORT:-17550}"; H="http://127.0.0.1:$PORT"; SOCK=hltest-f; T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
fails=0; ok() { echo "   ok: $*"; }; bad() { echo "   FAIL: $*"; fails=$((fails+1)); }; say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
HANDLOOM_ADDR="127.0.0.1:$PORT" HANDLOOM_BASE_URL="$H" HANDLOOM_INSECURE=1 $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
cleanup() { $T kill-server 2>/dev/null; kill $HUB $LINK 2>/dev/null; }; trap cleanup EXIT
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"; CODE="$(grep -o 'setup code: .*' "$OUT/hub.log" | awk '{print $3}')"
curl -s -o /dev/null -H "Origin: $H" -d "code=$CODE&name=Afif&password=correct+horse+battery&password2=correct+horse+battery" "$H/setup"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add f-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done

say "an agent in a real tmux pane that prints a secret"
SECRET="hva_Zq9SecretSecretSecret12345"
$T new-session -d -s f -x 120 -y 30 "HANDLOOM_HOME=$OUT/home $BIN run tty --kind shell -- sh -c 'echo SCREEN-OK-4471; echo token=$SECRET; sleep 600'"
for i in $(seq 1 30); do admin agents --json 2>/dev/null | grep -q '"tty"' && break; sleep 0.5; done
admin agents --json | grep -q '"wake_target": "tmux' && ok "the agent registered with a tmux wake target" || bad "no wake target"

say "a person opens its page"
JAR="$OUT/jar"; curl -s -c "$JAR" -o /dev/null -H "Origin: $H" -d "name=Afif&password=correct+horse+battery" "$H/login"
PAGE="$(curl -s -b "$JAR" "$H/agents/tty")"
TOKEN="$(echo "$PAGE" | grep -o 'name="csrf-token" content="[^"]*"' | sed 's/.*content="//; s/"$//')"
echo "$PAGE" | grep -q 'data-tail="/agents/tty/tail"' && ok "the agent page has the terminal" || bad "no terminal on the page"
curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -H "X-CSRF-Token: $TOKEN" -H "Origin: $H" -X POST "$H/agents/tty/watch" | grep -q 204 && ok "the page asked for the screen" || bad "watch refused"
got=""
for i in $(seq 1 20); do got="$(curl -s -b "$JAR" "$H/agents/tty/tail")"; echo "$got" | grep -q SCREEN-OK-4471 && break; curl -s -o /dev/null -b "$JAR" -H "X-CSRF-Token: $TOKEN" -H "Origin: $H" -X POST "$H/agents/tty/watch"; sleep 1; done
echo "$got" | sed 's/^/    | /'
echo "$got" | grep -q SCREEN-OK-4471 && ok "the agent's screen reached the page" || bad "the screen never arrived"
echo "$got" | grep -q "$SECRET" && bad "the secret reached the page" || ok "the token on the screen was removed on the machine"
echo "$got" | grep -q "\[removed\]" && ok "and says that something was removed" || bad "no removal marker"

say "nothing was stored"
python3 - "$OUT/data" <<'PY' && ok "the screen is in no database table" || bad "the screen is in the database"
import glob, sqlite3, sys
db = glob.glob(sys.argv[1] + "/*.db")[0]
c = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
bad = 0
for (t,) in c.execute("select name from sqlite_master where type='table'").fetchall():
    cols = [r[1] for r in c.execute(f"pragma table_info({t})")]
    for col in cols:
        n = c.execute(f"select count(*) from {t} where cast({col} as text) like ?", ("%SCREEN-OK-4471%",)).fetchone()[0]
        bad += n
sys.exit(1 if bad else 0)
PY
say "wanting ends by itself"
sleep 50
curl -s -b "$JAR" "$H/agents/tty/tail" | grep -q "Waiting for the machine" && ok "the screen is forgotten when nobody looks" || bad "the screen is still served"
[ "$fails" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL ($fails)"; exit 1; }
