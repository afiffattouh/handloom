#!/usr/bin/env bash
# The terminal console (handloom tui), end to end with a real hub, a real link and a real tmux:
# a person opens the console, reads every screen, answers a question, accepts and sends back work,
# starts a job from the form, watches an agent's terminal, and sees the offline and bad-token states.
# Every screen is captured with tmux capture-pane into test/e2e/out/n/snap-*.txt.
#
# Needs: go, tmux, curl, python3.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/n"; PORT="${PORT:-17700}"; H="http://127.0.0.1:$PORT"; SOCK=hltest-n; T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
fails=0; ok() { echo "   ok: $*"; }; bad() { echo "   FAIL: $*"; fails=$((fails+1)); }; say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
HUB=""; LINK=""
start_hub() {
  HANDLOOM_ADDR="127.0.0.1:$PORT" HANDLOOM_BASE_URL="$H" HANDLOOM_INSECURE=1 $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" >> "$OUT/hub.log" 2>&1 & HUB=$!
  for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
}
cleanup() { $T kill-server 2>/dev/null; [ -n "$HUB" ] && kill "$HUB" 2>/dev/null; [ -n "$LINK" ] && kill "$LINK" 2>/dev/null; true; }
trap cleanup EXIT
start_hub
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
HT="$(admin human add afif --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
agent() { local a="$1"; shift; $CLEAN env HANDLOOM_AGENT="$a" "$BIN" "$@"; }

say "a hub, a machine, a profile, and some work to look at"
JT="$(admin device add n-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
admin profile add lead --kind claude --runtime cloud >/dev/null
admin profile add coder --kind claude --runtime cloud >/dev/null
# lead-ui lives in a real tmux pane that prints something to look at
$T new-session -d -s lead -x 120 -y 30 "HANDLOOM_HOME=$OUT/home $BIN run lead-ui --kind shell -- sh -c 'echo SCREEN-OK-4471; echo second line of the terminal; sleep 900'"
for i in $(seq 1 30); do admin agents --json 2>/dev/null | grep -q '"lead-ui"' && break; sleep 0.5; done
human agent role lead-ui lead >/dev/null
for w in worker-1 worker-2; do agent "$w" register "$w" --kind shell >/dev/null; done
human job new "Ship the login page" --body "Build the sign-in form and wire it to the session API. Done means a person can sign in and out." --lead lead-ui >/dev/null
human task create "Write the sign-in form" --job 1 --assign worker-1 --body "Fields: email and password. Show errors under the field." >/dev/null
human task create "Wire the session API" --job 1 --assign worker-2 >/dev/null
human task create "Add rate limiting" --job 1 >/dev/null
agent worker-1 task claim 2 >/dev/null; agent worker-2 task claim 3 >/dev/null
agent worker-1 task submit 2 --evidence "commit:3fa9c2e" --evidence "test:go test ./... -> ok" --note "Form done, errors show under each field." >/dev/null
agent worker-2 task submit 3 --evidence "commit:81b44d0" --note "Session API wired; sign-out clears the cookie." >/dev/null
agent lead-ui ask "Which database should the login page use for sessions?" --option sqlite --option postgres >/dev/null
agent lead-ui ask "May I add a dependency for rate limiting?" >/dev/null
human digest | sed 's/^/    /'

# ---- driving tmux ----
snap() { $T capture-pane -p -t tui > "$OUT/snap-$1.txt"; }
keys() { $T send-keys -t tui "$@"; }
see() { # see <text>: wait up to 10 s for text on screen
  for i in $(seq 1 40); do $T capture-pane -p -t tui | grep -qF -- "$1" && return 0; sleep 0.25; done; return 1; }
expect() { see "$1" && ok "$2" || { bad "$2 (never saw: $1)"; snap "fail-$fails"; }; }
open_tui() { $T kill-session -t tui 2>/dev/null; $T new-session -d -s tui -x "${W:-120}" -y "${HGT:-40}" -e HANDLOOM_HUB="$H" -e HANDLOOM_TOKEN="${TOK:-$HT}" "$@" "$BIN"; }

say "bare handloom in a terminal opens the console"
open_tui
expect "Needs you 2" "the header shows the count of things that need you"
expect "afif" "the header names the signed-in person"
snap overview
cat "$OUT/snap-overview.txt"

say "inbox: open a question and answer it"
keys 2; expect "Which database" "the inbox lists the question"
snap inbox-list
keys Enter; expect "Press e to answer" "the question opens"
snap inbox-question
keys e; expect "picks an option" "the answer line offers the options"
keys 1 Enter; expect "Answer question #1?" "it asks before answering"
snap inbox-answer-confirm
keys n; expect "Cancelled" "n cancels"
[ "$(human escalations --json | python3 -c 'import json,sys;print(sum(1 for e in json.load(sys.stdin) if e.get("answer") is None))')" = 2 ] && ok "no answer was sent" || bad "an answer was sent on n"
keys e; see "picks an option"; keys 1 Enter; see "Answer question #1?"; keys y
expect "Answered question #1" "y sends the answer"
human escalations --status answered | grep -q "sqlite" && ok "the hub has the answer: sqlite" || bad "the answer is not on the hub"

say "inbox: accept one task, send one back"
keys Down; see "To review"; keys Enter; expect "Press a to accept" "the work waiting for review opens"
expect "go test ./... -> ok" "its evidence is shown"
snap inbox-review
keys a; expect "Accept task #" "it asks before accepting"
snap inbox-accept-confirm
keys n; sleep 0.5
human task show 2 | grep -q "submitted" && ok "nothing changed on n" || bad "task changed on n"
keys a; see "Accept task #"; keys y; expect "Accepted task #2" "y accepts"
human task show 2 | grep -q "^#2 *done" && ok "task 2 is done on the hub" || bad "task 2 is not done"
sleep 3.5; see "To review"
keys Enter; see "Press a to accept"; keys x; expect "Why send it back" "x asks for a reason"
keys "Add a test for a wrong password" Enter; expect "Send task #3 back?" "it asks before sending back"
snap inbox-reject-confirm
keys y; expect "Sent task #3 back" "y sends it back"
human task show 3 | grep -q "Add a test for a wrong password" && ok "the hub has the reason" || human task show 3 | sed 's/^/    /'

say "jobs"
keys 3; expect "Ship the login page" "the job is listed"
snap jobs
keys Enter; expect "Brief" "the job opens"; expect "Write the sign-in form" "its tasks are listed"
snap job-detail
keys Escape
keys n; expect "New job" "the form opens"
keys "Fix the billing page" Tab "Totals are wrong when a coupon is used." Tab; keys Tab Tab Tab "./check.sh"
keys C-s; expect "Start this job?" "it shows what it will do before starting"
snap form-confirm
keys y; expect "needs a repository" "the hub's own words are shown when it refuses"
snap form-error
keys Tab; keys C-u 2>/dev/null; keys C-s; see "Start this job?"; keys n
keys Escape; sleep 0.3
keys n; see "New job"; keys "Fix the billing page" Tab "Totals are wrong when a coupon is used." Tab C-s; see "Start this job?"; keys y
expect "Started job #" "a valid form starts the job"
human job list | grep -q "Fix the billing page" && ok "the job exists on the hub" || bad "no such job on the hub"
snap jobs-after

say "agents and the terminal view"
keys 4; expect "lead-ui" "the agents are listed"
snap agents
keys Enter; expect "SCREEN-OK-4471" "the agent's terminal appears"
snap agents-terminal
cat "$OUT/snap-agents-terminal.txt"
keys f; expect "follow: off" "f turns follow off"; keys f; expect "follow: on" "and on again"
keys Escape

say "library"
keys 5; expect "coder" "the profiles are listed"
expect "This agent can" "the plain summary is shown"
snap library
keys s; expect "Starters" "s switches to starters"; sleep 1; snap library-starters

say "help"
keys '?'; expect "Everywhere" "the help sheet opens"; snap help; keys q 2>/dev/null; keys Escape

say "sizes"
$T resize-window -t tui -x 80 -y 24; sleep 0.5; keys 1; sleep 1; snap size-80-overview; keys 2; sleep 1; snap size-80-inbox; keys 4; sleep 1; snap size-80-agents
$T resize-window -t tui -x 160 -y 50; sleep 1; keys 1; sleep 1; snap size-160-overview; keys 2; sleep 1; snap size-160-inbox; keys 4; sleep 1; snap size-160-agents
$T resize-window -t tui -x 70 -y 20; sleep 1; snap size-70
grep -q "at least 80 columns" "$OUT/snap-size-70.txt" && ok "a tiny window gets a plain message" || bad "no message in a tiny window"
$T resize-window -t tui -x 120 -y 40; sleep 1
for f in "$OUT"/snap-size-80-*.txt; do
  [ "$(wc -l < "$f")" -le 24 ] || bad "$(basename "$f") is taller than 24 lines"
  awk 'length($0) > 80 { exit 1 }' "$f" || bad "$(basename "$f") has a line wider than 80"
done

say "the hub goes away and comes back"
kill "$HUB"; wait "$HUB" 2>/dev/null; HUB=""
expect "Offline, retrying" "the status line says offline"; snap offline
keys 3; expect "Ship the login page" "the last data is still shown"
start_hub
expect "Updated" "it recovers by itself" ; keys 3; see "Ship the login page"

say "a wrong token, and no colour"
W=100 HGT=30 TOK="hvh_wrongwrongwrongwrong" open_tui
expect "handloom token new" "a bad token says what to do"; snap token-error
$T kill-session -t tui
open_tui -e NO_COLOR=1
see "Needs you"; sleep 1
$T capture-pane -p -e -t tui | grep -q $'\033\\[[0-9;]*[34][0-9;]*m' && bad "colour codes with NO_COLOR" || ok "NO_COLOR draws no colour"
snap nocolor
$T kill-session -t tui

[ "$fails" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL ($fails)"; exit 1; }
