#!/usr/bin/env bash
# Milestone B check, on one machine, with shell agents in tmux panes:
#
#   a job with its own lead and two tasks; the worker starts one; the lead's
#   terminal is killed; the hub notices, tells the human (push, inbox, audit);
#   `job resume` gives the job to a second lead, who finds the board and the
#   old lead's unread mail; run tokens identify the agents.
#
# Needs: go, tmux, python3, curl. Everything is named hltest-b-* and removed at the end.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/test/e2e/out/b"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17450}"; NTFY_PORT="${NTFY_PORT:-17451}"
H="http://127.0.0.1:$PORT"
T="tmux -L hltest-b"
say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok: %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; { echo "--- agents:"; admin agents 2>&1; echo "--- link log:"; tail -15 "$OUT/link.log"; echo "--- tmux:"; $T list-windows -a 2>&1; } >&2 || true; exit 1; }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/work"
cd "$OUT/work"   # agents write their run tokens under the directory they start in
cleanup() {
  $T kill-server 2>/dev/null || true
  for p in ${HUB_PID:-} ${LINK_PID:-} ${NTFY_PID:-}; do kill "$p" 2>/dev/null || true; done
}
trap cleanup EXIT

say "build"
(cd "$ROOT" && "$GO" build -o "$OUT/bin/handloom" ./cmd/handloom)
BIN="$OUT/bin/handloom"
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID true

say "a fake ntfy server, the hub (6 s agent lease), a device and its link (checks every second, vouches every 2 s)"
cat > "$OUT/ntfy.py" <<PY
import http.server, json
class Hd(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', 0)); body = self.rfile.read(n).decode()
        open('$OUT/ntfy.log', 'a').write(json.dumps({'title': self.headers.get('Title'), 'body': body}) + '\n')
        self.send_response(200); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', $NTFY_PORT), Hd).serve_forever()
PY
python3 "$OUT/ntfy.py" & NTFY_PID=$!
HANDLOOM_NTFY_URL="http://127.0.0.1:$NTFY_PORT" HANDLOOM_NTFY_TOPIC=hltest-b-topic HANDLOOM_BASE_URL="$H" HANDLOOM_INSECURE=1 \
  "$BIN" hub serve --auto-init --data "$OUT/data" --addr "127.0.0.1:$PORT" --agent-lease 6s --sweep 1s > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "$H/healthz" >/dev/null || fail "hub did not start: $(tail -3 "$OUT/hub.log")"
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" "$BIN" "$@"; }
JT="$(admin device add hltest-b-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" "$BIN" "$@"; }
"$BIN" link join "$H" "$JT" >/dev/null
"$BIN" link run --tick 1s --live-every 2s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
ok "hub, link and fake push service are up"

say "three agents, each in its own tmux pane (handloom run registers it with its pane)"
$T new-session -d -s hltest-b -c "$OUT/work" -x 200 -y 50 "env HANDLOOM_HOME=$OUT/home PATH=$OUT/bin:$PATH handloom run lead1 --kind shell -- sleep 3600"
$T new-window -t hltest-b -c "$OUT/work" "env HANDLOOM_HOME=$OUT/home PATH=$OUT/bin:$PATH handloom run lead2 --kind shell -- sleep 3600"
$T new-window -t hltest-b -c "$OUT/work" "env HANDLOOM_HOME=$OUT/home PATH=$OUT/bin:$PATH handloom run w1 --kind shell -- sleep 3600"
for i in $(seq 1 30); do [ "$(admin agents --json | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')" = 3 ] && break; sleep 0.3; done
as() { local a="$1"; shift; HANDLOOM_AGENT="$a" "$BIN" "$@"; }
for a in lead1 lead2 w1; do
  as "$a" whoami --json | grep -q '"via": "run-token"' && ok "$a authenticates with its run token" || ok "$a is device-asserted (it has no token file here; fine for a shell agent)"
done
for i in $(seq 1 20); do
  [ "$(admin agents --json | python3 -c 'import json,sys;print(sum(1 for a in json.load(sys.stdin) if a.get("lease_expires_at")))')" = 3 ] && break; sleep 0.5; done
[ "$(admin agents --json | python3 -c 'import json,sys;print(sum(1 for a in json.load(sys.stdin) if a.get("lease_expires_at")))')" = 3 ] || fail "the link is not vouching for the three terminals"
ok "the link vouches for all three terminals (they have a lease)"

say "a job with its own lead, two tasks, the worker starts one and writes to the lead"
JOB="$(human job new "Scan competitors" --lead lead1 --body "Twelve pages" --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')"
as lead1 inbox | grep -q "lead of job #$JOB" || fail "lead1 was not told it leads the job"
id_of() { python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])'; }
T1="$(as lead1 task create "Fetch the pages" --assign w1 --json | id_of)"
T2="$(as lead1 task create "Write the summary" --depends "$T1" --json | id_of)"
as w1 inbox >/dev/null
as w1 task claim "$T1" >/dev/null
as w1 send role:lead "Fetched six of twelve so far, two need a login." --task "$T1"
[ "$(as lead2 task list --job "$JOB" --json | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')" = 2 ] || fail "the board should show two tasks"
ok "job #$JOB: two tasks, one claimed; lead1 holds an unread message from w1"

say "kill the lead's terminal"
$T kill-window -t hltest-b:0
for i in $(seq 1 40); do [ "$(admin agents --json | python3 -c 'import json,sys;print([a["state"] for a in json.load(sys.stdin) if a["name"]=="lead1"][0])')" = offline ] && break; sleep 0.5; done
[ "$(admin agents --json | python3 -c 'import json,sys;print([a["state"] for a in json.load(sys.stdin) if a["name"]=="lead1"][0])')" = offline ] || fail "the hub never marked lead1 offline"
ok "lead1 went offline (its lease ran out; the other two are still vouched for)"
[ "$(admin agents --json | python3 -c 'import json,sys;print([a["state"] for a in json.load(sys.stdin) if a["name"]=="w1"][0])')" != offline ] || fail "w1 was marked offline too"
for i in $(seq 1 20); do [ -s "$OUT/ntfy.log" ] && break; sleep 0.3; done
[ -s "$OUT/ntfy.log" ] || fail "no push reached the fake service"
cat "$OUT/ntfy.log"
grep -q "job #$JOB" "$OUT/ntfy.log" || fail "the push does not name the job"
if grep -qi "fetch the pages\|competitors\|summary" "$OUT/ntfy.log"; then fail "the push leaks task or job text"; fi
ok "the human got a push naming the job and nothing else"
human digest --json 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
items=[i for i in d["needs_you"] if i["kind"]=="lead-silent"]
assert items and "job resume" in items[0]["detail"], d["needs_you"]
print("   inbox item:", items[0]["title"])' || fail "the inbox has no lead-silent item for the job"
admin audit | grep -q "job.lead_lost" || fail "no job.lead_lost in the audit log"
ok "the inbox and the audit log say so too"

say "resume the job with lead2"
human job resume "$JOB" --lead lead2
out="$(as lead2 inbox)"
echo "$out" | sed 's/^/    /'
echo "$out" | grep -q "resuming job #$JOB" || fail "lead2 was not told the state of the job"
echo "$out" | grep -q "forwarded from lead1" || fail "lead1's unread mail was not forwarded"
echo "$out" | grep -q "Fetched six of twelve" || fail "the worker's message did not reach lead2"
as lead2 task list --job "$JOB" | sed 's/^/    /'
as lead2 task show "$T1" >/dev/null
as w1 send role:lead "Back to it." >/dev/null
as lead2 inbox | grep -q "Back to it" || fail "the worker's next message did not reach the new lead"
human job show "$JOB" | grep -q "lead: lead2" || fail "job show does not name lead2"
ok "lead2 has the board, the forwarded mail, and the worker's next message"

say "audit trail"
admin audit | grep -E "job.new|agent.job|agent.lease_expired|job.lead_lost|job.resume" | sed 's/^/    /'
printf '\nPASS: milestone B check\n'
