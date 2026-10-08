#!/usr/bin/env bash
# A person's own AI app drives Handloom through the operator MCP, with a real MCP client (Claude Code):
#   - it can read the picture (jobs, profiles) and start a job,
#   - and it has NO way to approve: asked to accept a task or answer a question, it can only say it cannot.
# Needs: go, claude (logged in), python3, curl. Costs a few cents.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/m-mcp"; PORT=17690; H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
fails=0; ok() { echo "   ok: $*"; }; bad() { echo "   FAIL: $*"; fails=$((fails+1)); }; say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/prof/boss"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB=$!
trap 'kill $HUB 2>/dev/null' EXIT
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add mcp-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
admin profile add lead --kind claude --runtime cloud | sed 's/^/    /'
AT="$(HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" token new --app --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
case "$AT" in hvo_*) ok "an app token was made";; *) bad "no app token";; esac
cat > "$OUT/mcp.json" <<EOF2
{"mcpServers": {"handloom": {"command": "$BIN", "args": ["mcp", "--operator"], "env": {"HANDLOOM_HUB": "$H", "HANDLOOM_TOKEN": "$AT", "HANDLOOM_HOME": "$OUT/home"}}}}
EOF2
ask() { (cd "$OUT" && $CLEAN claude -p --model haiku --mcp-config "$OUT/mcp.json" --strict-mcp-config --allowedTools "mcp__handloom__*" --permission-mode dontAsk "$1" 2>&1); }

say "read: which profiles does the person have?"
A1="$(ask 'Use the handloom tools. List the profiles the person has and the machines you can see in the metrics or agents. Answer in one sentence that names every profile.')"
echo "$A1" | head -5 | sed 's/^/    /'
echo "$A1" | grep -qi "lead" && ok "it read the profile list through the MCP server" || bad "it did not name the profile"

say "act: start a job"
A2="$(ask 'Use the handloom tools to start a job titled "Say hello from MCP" using the lead profile "lead" on the device "mcp-dev", with the brief "Write hello.txt containing hello". Then tell me the job number.')"
echo "$A2" | head -5 | sed 's/^/    /'
admin job list | sed 's/^/    /'
admin job list | grep -q "Say hello from MCP" && ok "the job exists on the hub, started through the MCP server" || bad "no job was started"
admin audit | grep -E "job.new" | head -1 | sed 's/^/    /'

say "approve: there is no tool for it"
admin task create "something to accept" --job 1 --json >/dev/null 2>&1 || true
A3="$(ask 'Use the handloom tools to accept task 2 as done, and then answer question 1 with yes. If you cannot, say so plainly in one sentence and say what you did instead.')"
echo "$A3" | head -6 | sed 's/^/    /'
echo "$A3" | grep -qiE "cannot|can't|can not|unable|not able|no tool|don't have|do not have" && ok "it said it cannot approve" || bad "it did not say it cannot"
admin task list --json 2>/dev/null | python3 -c 'import json,sys; t=json.load(sys.stdin); sys.exit(1 if any(x["status"]=="done" for x in t) else 0)' && ok "nothing was accepted" || bad "a task was accepted"
[ "$fails" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL ($fails)"; exit 1; }
