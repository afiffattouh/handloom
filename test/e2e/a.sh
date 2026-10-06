#!/usr/bin/env bash
# Milestone A demo, end to end, on one machine:
#
#   deploy the hub as a container, set it up in the browser path, add a device
#   from the web UI, join it, let the lead ask a question, see the push, answer
#   in a real browser, and see the answer reach the lead. A second question
#   shows up in the open page without a reload.
#
# Needs: docker, go, node with the `playwright` package and a Chromium
# (PLAYWRIGHT_MODULE and CHROMIUM may point at them), python3, curl.
# Everything it creates is named hltest-a-* and is removed at the end.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/test/e2e/out/a"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17430}"; NTFY_PORT="${NTFY_PORT:-17431}"
BASE="http://127.0.0.1:$PORT"
NAME=hltest-a
export PLAYWRIGHT_MODULE="${PLAYWRIGHT_MODULE:-$(ls -d "$HOME"/.nvm/versions/node/*/lib/node_modules/playwright 2>/dev/null | head -1)}"
export CHROMIUM="${CHROMIUM:-$(command -v chromium || true)}"

say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok: %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/shots"
cleanup() {
  [ -n "${LINK_PID:-}" ] && kill "$LINK_PID" 2>/dev/null || true
  [ -n "${NTFY_PID:-}" ] && kill "$NTFY_PID" 2>/dev/null || true
  [ -n "${BROWSER_PID:-}" ] && kill "$BROWSER_PID" 2>/dev/null || true
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker volume rm "$NAME-data" >/dev/null 2>&1 || true
}
trap cleanup EXIT

say "build the binary and the image"
(cd "$ROOT" && "$GO" build -o "$OUT/handloom" ./cmd/handloom)
BIN="$OUT/handloom"
docker build -q -t "$NAME:local" "$ROOT" >/dev/null
ok "image built ($(docker image inspect "$NAME:local" --format '{{.Size}}') bytes)"

say "a fake ntfy server that records what the hub pushes"
cat > "$OUT/ntfy.py" <<PY
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', 0)); body = self.rfile.read(n).decode()
        with open('$OUT/ntfy.log', 'a') as f:
            f.write(json.dumps({'path': self.path, 'title': self.headers.get('Title'), 'click': self.headers.get('Click'), 'body': body}) + '\n')
        self.send_response(200); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', $NTFY_PORT), H).serve_forever()
PY
python3 "$OUT/ntfy.py" & NTFY_PID=$!

say "deploy: start the hub container (host network, own volume)"
docker run -d --name "$NAME" --network host -v "$NAME-data:/data" \
  -e HANDLOOM_ADDR="127.0.0.1:$PORT" -e HANDLOOM_BASE_URL="$BASE" \
  -e HANDLOOM_NTFY_URL="http://127.0.0.1:$NTFY_PORT" -e HANDLOOM_NTFY_TOPIC=hltest-a-topic \
  "$NAME:local" >/dev/null
for i in $(seq 1 30); do curl -sf "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf "$BASE/healthz" >/dev/null || fail "hub did not come up: $(docker logs "$NAME" 2>&1 | tail -5)"
ok "hub answers /healthz"
for i in $(seq 1 20); do [ "$(docker inspect --format '{{.State.Health.Status}}' "$NAME")" = healthy ] && break; sleep 2; done
[ "$(docker inspect --format '{{.State.Health.Status}}' "$NAME")" = healthy ] || fail "container never became healthy"
ok "container healthcheck is healthy"
LOG="$(docker logs "$NAME" 2>&1)"
CODE="$(echo "$LOG" | grep -o 'setup code: [A-Z0-9-]*' | awk '{print $3}')"
ADMIN="$(echo "$LOG" | grep -o 'hva_[A-Za-z0-9_-]*' | head -1)"
[ -n "$CODE" ] && [ -n "$ADMIN" ] || fail "no setup code or admin token in the log"
ok "log shows a setup code and, once, the admin token"

say "set up the owner through the setup form"
PW="correct horse battery staple"
JAR="$OUT/jar"; : > "$JAR"
code=$(curl -s -o /dev/null -w '%{http_code}' -d "code=$CODE&name=afif&password=$PW&password2=$PW" "$BASE/setup")
[ "$code" = 403 ] || fail "setup without an Origin should be refused (got $code)"
ok "a cross-site style post (no Origin) is refused"
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -H "Origin: $BASE" -d "code=$CODE&name=afif&password=$PW&password2=$PW" "$BASE/setup")
[ "$code" = 303 ] || fail "setup answered $code"
ok "owner created and signed in"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/setup")" = 404 ] || fail "the setup page is still open"
ok "setup page is closed afterwards"

say "add a device from the web UI, join it, start its link"
TOKEN="$(curl -s -b "$JAR" "$BASE/inbox" | grep -o 'name="csrf-token" content="[^"]*"' | sed 's/.*content="//;s/"//')"
PAGE="$(curl -s -b "$JAR" -H "Origin: $BASE" --data-urlencode "csrf=$TOKEN" --data-urlencode "name=hltest-a-dev" --data-urlencode "current_password=$PW" "$BASE/devices")"
JOIN="$(echo "$PAGE" | grep -o 'handloom link join [^ ]* hvj_[A-Za-z0-9_-]*' | head -1)"
[ -n "$JOIN" ] || fail "no join command on the devices page"
JT="${JOIN##* }"
export HANDLOOM_HOME="$OUT/home"
"$BIN" link join "$BASE" "$JT" >/dev/null
ok "device joined with the token the page showed"
if "$BIN" link join "$BASE" "$JT" >/dev/null 2>&1; then fail "a join token worked twice"; fi
ok "the join token is single use"
"$BIN" link run >"$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
"$BIN" link status >/dev/null || fail "link did not start"
ok "link is running"

say "the lead registers and asks a question"
HANDLOOM_AGENT=lead "$BIN" register lead --kind shell >/dev/null
HANDLOOM_HUB="$BASE" HANDLOOM_TOKEN="$ADMIN" "$BIN" agent role lead lead >/dev/null
QUESTION="Ship the release to production?"
HANDLOOM_AGENT=lead "$BIN" ask "$QUESTION" --option yes --option no | tee "$OUT/ask.txt"
for i in $(seq 1 30); do [ -s "$OUT/ntfy.log" ] && break; sleep 0.3; done
[ -s "$OUT/ntfy.log" ] || fail "no push reached the fake ntfy server"
cat "$OUT/ntfy.log"
grep -q "/hltest-a-topic" "$OUT/ntfy.log" || fail "push went to the wrong topic"
grep -q "$BASE/inbox" "$OUT/ntfy.log" || fail "push has no link to the inbox"
if grep -qi "production\|ship" "$OUT/ntfy.log"; then fail "the push leaks the question"; fi
ok "push arrived with a link and without the question"

say "answer in a real browser; a second question appears without a reload"
if [ -z "$PLAYWRIGHT_MODULE" ] || [ -z "$CHROMIUM" ]; then fail "no playwright/chromium found (set PLAYWRIGHT_MODULE and CHROMIUM)"; fi
mkfifo "$OUT/fifo"
node "$ROOT/test/e2e/a_browser.js" "$BASE" afif "$PW" yes "$OUT/shots" < "$OUT/fifo" > "$OUT/browser.log" 2>&1 & BROWSER_PID=$!
exec 3> "$OUT/fifo"
for i in $(seq 1 60); do grep -q READY-FOR-SECOND "$OUT/browser.log" 2>/dev/null && break; kill -0 $BROWSER_PID 2>/dev/null || break; sleep 0.5; done
grep -q READY-FOR-SECOND "$OUT/browser.log" || { cat "$OUT/browser.log"; fail "the browser did not get through the login and the answer"; }
ok "the browser signed in and clicked 'yes'"
HANDLOOM_AGENT=lead "$BIN" ask "Second question from the lead" >/dev/null
echo go >&3; exec 3>&-
wait $BROWSER_PID || { cat "$OUT/browser.log"; fail "the page did not update by itself"; }
BROWSER_PID=""
grep -q BROWSER-OK "$OUT/browser.log" || fail "browser check did not finish"
ok "the open page showed the second question without a reload"

say "the lead received the answer from the human"
INBOX="$(HANDLOOM_AGENT=lead "$BIN" inbox)"
echo "$INBOX" | sed 's/^/    /'
echo "$INBOX" | grep -q "from human:afif" || fail "the answer is not from human:afif"
echo "$INBOX" | grep -q "Answer to your question #1" || fail "no answer message"
ok "the lead has the answer, from human:afif"

say "audit trail"
HANDLOOM_HUB="$BASE" HANDLOOM_TOKEN="$ADMIN" "$BIN" audit | grep -E "setup.owner|device.add|device.join|escalation.open|escalation.answer|web.login" | sed 's/^/    /' | tee "$OUT/audit.txt"
for a in setup.owner device.add device.join escalation.open escalation.answer; do grep -q "$a" "$OUT/audit.txt" || fail "audit lacks $a"; done
ok "audit log has the setup, the device, and the question and answer"

say "backup and restore the container's data"
docker exec "$NAME" /handloom hub backup --to /data/backups/e2e.db >/dev/null
docker stop "$NAME" >/dev/null
docker run --rm --entrypoint /handloom -v "$NAME-data:/data" "$NAME:local" hub restore --from /data/backups/e2e.db --force >/dev/null
docker start "$NAME" >/dev/null
for i in $(seq 1 30); do curl -sf "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf "$BASE/healthz" >/dev/null || fail "hub did not come back after restore"
ok "hub is back after a restore from its own backup"

printf '\nPASS: milestone A demo (screenshots in %s)\n' "$OUT/shots"
