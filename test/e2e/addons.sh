#!/usr/bin/env bash
# Add-ons, end to end, with NO model and no network: a throwaway hub and link in a temporary home, the echo
# add-on installed on the computer and granted to a profile, and helpers that are a few lines of shell started
# in place of a real agent program (a fake `claude` that only runs the plan this script wrote for it).
# Shows: the audit entries of a call, a call that is refused, a confidential job refused for a tool that
# leaves the computer, an add-on that changed stopping its grants, and an add-on written for an add-on API
# this program does not speak being refused with a plain reason and reported as not running.
# Needs: go, tmux, git, python3 (3.8 or newer), curl. HOME is a temporary directory; nothing leaves this machine.
#   test/e2e/addons.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17951}"
SOCK=hltest-addons
T="tmux -L $SOCK"
OUT="$ROOT/test/e2e/out/addons"
H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
DEV=hltest-addons-desk
fails=0
say() { printf '\n== %s\n' "$*"; }
ok()  { printf '   ok: %s\n' "$*"; }
bad() { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }

command -v python3 >/dev/null && python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' || { echo "python3 3.8 or newer is needed"; exit 1; }
command -v tmux >/dev/null || { echo "tmux is needed"; exit 1; }

$T kill-server 2>/dev/null
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/fakebin" "$OUT/fakehome" "$OUT/plans" "$OUT/agents" "$OUT/skew"
BIN="$OUT/bin/handloom"
(cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || { echo "build failed"; exit 1; }

# The fake agent program: it runs the plan named after its working folder (the helper's name), then waits.
cat > "$OUT/fakebin/claude" <<EOF2
#!/bin/sh
name="\$(basename "\$PWD")"
[ -f "$OUT/plans/\$name.sh" ] && sh "$OUT/plans/\$name.sh" > "$OUT/agents/\$name.log" 2>&1
: > "$OUT/agents/\$name.done"
exec sleep 3600
EOF2
chmod +x "$OUT/fakebin/claude"

# A small git repository for the jobs to work in.
REPO="$OUT/repo"
git init -q -b main "$REPO" && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
printf 'hello\n' > "$REPO/README.md"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"

export HANDLOOM_HOME="$OUT/home" HOME="$OUT/fakehome" PATH="$OUT/fakebin:$OUT/bin:$PATH"
finish() { $T kill-server 2>/dev/null; kill ${HUB_PID:-} ${LINK_PID:-} 2>/dev/null; }
trap finish EXIT

say "a throwaway hub, one computer called $DEV, one owner"
HANDLOOM_ALLOW_CONFIDENTIAL=1 HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
[ -n "$ADMIN" ] || { echo "the hub did not start"; tail -5 "$OUT/hub.log"; exit 1; }
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" hub create-owner olive --data "$OUT/data" > "$OUT/owner.txt" 2>&1 || { echo "create-owner: $(cat "$OUT/owner.txt")"; exit 1; }
OT="$($CLEAN "$BIN" hub reset-human-token olive --data "$OUT/data" 2>/dev/null | tail -1)"
owner() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$OT" $CLEAN "$BIN" "$@"; }
JT="$(admin device add $DEV --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 40); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
$CLEAN "$BIN" link status >/dev/null 2>&1 && ok "the hub and the link are up" || { bad "the link did not start"; tail -5 "$OUT/link.log"; exit 1; }

# waitfor SECONDS COMMAND...: run the command until it succeeds.
waitfor() { local n="$1"; shift; for i in $(seq 1 "$n"); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
# a helper's plan finished (its log is complete)
plan_done() { [ -f "$OUT/agents/$1.done" ]; }
# start a helper from a profile in a job, with the plan it will run; prints nothing, waits for the plan
run_helper() { # name job profile plan-text
  printf '%s\n' "$4" > "$OUT/plans/$1.sh"
  owner spawn "$1" --job "$2" --profile "$3" --device $DEV --wait 60s > "$OUT/spawn-$1.txt" 2>&1 || { bad "spawn $1: $(cat "$OUT/spawn-$1.txt")"; return 1; }
  waitfor 40 plan_done "$1" || { bad "helper $1 did not finish its plan"; return 1; }
}

say "install echo on the computer: read it, pin the hash, confirm"
ECHO="$ROOT/examples/addons/echo"
HASH="$("$BIN" addon hash "$ECHO")"
echo "$HASH" | grep -Eq '^[0-9a-f]{64}$' && ok "the package hash is a sha256" || bad "hash: $HASH"
"$BIN" addon add "$ECHO" --sha256 0000000000000000000000000000000000000000000000000000000000000000 > "$OUT/add-wrong.txt" 2>&1 && bad "a wrong hash was accepted" || { grep -q "nothing was installed" "$OUT/add-wrong.txt" && ok "a wrong hash installs nothing, in words" || bad "wrong hash: $(cat "$OUT/add-wrong.txt")"; }
"$BIN" addon add "$ECHO" --sha256 "$HASH" > "$OUT/add.txt" 2>&1 && ok "installed with the pinned hash" || bad "add: $(cat "$OUT/add.txt")"
"$BIN" addon list | grep -q "^echo .*Ready" && ok "listed as Ready on the computer" || bad "list: $("$BIN" addon list)"
waitfor 30 sh -c "'$BIN' addon status | grep -q 'echo.*Ready'" 2>/dev/null
owner addon status | grep -q "echo.*Ready" && ok "the hub lists it as Ready" || bad "hub status: $(owner addon status)"

say "profiles and grants (only the owner grants)"
mkdir -p "$OUT/prof/coder" "$OUT/prof/vault"
printf 'name: coder\ndescription: A helper on a cloud model\nkind: claude\nruntime: cloud\ntools:\n  allow: [read, edit, shell]\n' > "$OUT/prof/coder/profile.yaml"
printf 'name: vault\ndescription: A helper on a model that runs here\nkind: claude\nruntime: local\nmodel: local-server/model-name\ntools:\n  allow: [read, edit, shell]\n' > "$OUT/prof/vault/profile.yaml"
owner profile new "$OUT/prof/coder" >/dev/null && owner profile new "$OUT/prof/vault" >/dev/null && ok "two profiles" || bad "profile new"
admin addon grant coder echo.say > "$OUT/grant-admin.txt" 2>&1 && bad "the admin token granted a tool" || ok "a grant by the admin token is refused: $(head -c 100 "$OUT/grant-admin.txt")"
owner addon grant coder echo.say --leaves no --max-calls 3 > "$OUT/grant1.txt" 2>&1 && ok "echo.say granted to coder (stays on the computer, 3 calls)" || bad "grant: $(cat "$OUT/grant1.txt")"
grep -q "reaches helpers started from now on\|reach helpers started from now on" "$OUT/grant1.txt" && ok "the grant says it reaches only helpers started afterwards" || bad "no note about running helpers: $(cat "$OUT/grant1.txt")"
owner addon grant vault echo.say --leaves yes >/dev/null && ok "echo.say granted to vault (declared as leaving the computer)" || bad "grant vault"
owner addon grants | grep -q "coder.*echo.say" && ok "the grants are listed" || bad "grants: $(owner addon grants)"

say "a job: a helper lists its tools, calls one, is refused another, and promotes a file"
owner job new "Use the echo add-on" --repo "$REPO" --verify true --device $DEV > "$OUT/job1.txt" 2>&1 || { bad "job new: $(cat "$OUT/job1.txt")"; exit 1; }
J1="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/job1.txt" | head -1)"
run_helper alpha "$J1" coder '
handloom tool list; echo "list exit $?"
handloom tool call echo.say --arg text=hello --arg file=note.txt; echo "say exit $?"
handloom tool call echo.wait --arg seconds=0; echo "wait exit $?"
handloom tool call nosuch.thing; echo "nosuch exit $?"
f="$(ls -d "$PWD"/.handloom/results/echo/*/note.txt 2>/dev/null | head -1)"
handloom tool promote "$f" --to docs/note.txt; echo "promote exit $?"
for n in 1 2 3; do handloom tool call echo.say --arg text=again$n >/dev/null; echo "again$n exit $?"; done
'
A="$OUT/agents/alpha.log"
grep -q "echo.say" "$A" && grep -q "list exit 0" "$A" && ok "tool list shows echo.say" || bad "tool list: $(head -20 "$A")"
grep -q "hello" "$A" && grep -q "say exit 0" "$A" && ok "tool call returned the text" || bad "tool call: $(cat "$A")"
grep -q "denied-grant" "$A" && grep -q "wait exit 1" "$A" && ok "a tool that was not granted is refused with denied-grant" || bad "refusal: $(cat "$A")"
grep -q "nosuch exit 1" "$A" && ok "a tool that is not installed is refused" || bad "nosuch: $(cat "$A")"
WT="$HANDLOOM_HOME/work/job-$J1/alpha"
grep -q "hello" "$(ls "$WT"/.handloom/results/echo/*/note.txt 2>/dev/null | head -1)" 2>/dev/null && ok "the result file is in the call's results folder" || bad "no result file under $WT/.handloom/results"
[ -f "$WT/docs/note.txt" ] && grep -q "promote exit 0" "$A" && ok "promote copied the file into the project tree" || bad "promote: $(cat "$A")"
grep -q "denied-budget" "$A" && ok "the call limit (3) is enforced with denied-budget" || bad "no budget refusal: $(cat "$A")"

say "the audit log on the hub: what was called, never the text"
waitfor 30 sh -c "'$BIN' --version >/dev/null; HANDLOOM_HUB=$H HANDLOOM_TOKEN=$OT '$BIN' audit | grep -q 'addon.echo.denied'"
owner audit > "$OUT/audit.txt" 2>&1
grep -q "addon.echo.call" "$OUT/audit.txt" && ok "a call entry" || bad "no addon.echo.call in the audit"
grep -q "addon.echo.result" "$OUT/audit.txt" && ok "a result entry" || bad "no addon.echo.result"
grep -q "addon.echo.denied" "$OUT/audit.txt" && ok "a denied entry" || bad "no addon.echo.denied"
grep -q "addon.grant" "$OUT/audit.txt" && ok "the grant is in the audit" || bad "no addon.grant"
grep -q "hello" "$OUT/audit.txt" && bad "the text of a call is in the audit" || ok "the call's text is not in the audit"
grep -aq "hello" "$OUT/data/"*.db* 2>/dev/null && bad "the call's text is in the hub's database" || ok "the call's text is not in the hub's database"
grep -c "addon.echo.call" "$OUT/audit.txt" | grep -q '^[1-9]' && ok "calls counted: $(grep -c 'addon.echo.call' "$OUT/audit.txt") call lines" || true

say "a confidential job is refused a tool that leaves the computer"
owner job new "Private work" --repo "$REPO" --verify true --device $DEV --confidential > "$OUT/job2.txt" 2>&1 || { bad "confidential job new: $(cat "$OUT/job2.txt")"; }
J2="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/job2.txt" | head -1)"
run_helper bravo "$J2" vault '
handloom tool list; echo "list exit $?"
handloom tool call echo.say --arg text=secret; echo "say exit $?"
'
B="$OUT/agents/bravo.log"
grep -q "denied-confidential" "$B" && grep -q "say exit 1" "$B" && ok "refused with denied-confidential" || bad "confidential: $(cat "$B")"
grep -q "do not guess" "$B" && ok "the helper is told not to guess the answer" || bad "no do-not-guess line: $(cat "$B")"
waitfor 30 sh -c "HANDLOOM_HUB=$H HANDLOOM_TOKEN=$OT '$BIN' audit | grep -c 'addon.echo.denied' | grep -q '[2-9]'"
owner audit | grep -q "denied-confidential" && ok "the refusal is in the audit with its code" || bad "no denied-confidential in the audit"

say "an add-on that changed stops working until it is approved again"
cp "$HANDLOOM_HOME/addons/echo/service.py" "$OUT/service.py.orig"
printf '\n# edited after it was approved\n' >> "$HANDLOOM_HOME/addons/echo/service.py"
"$BIN" addon list | grep -q "echo.*Changed since approved" && ok "the computer lists it as Changed since approved" || bad "list: $("$BIN" addon list)"
waitfor 30 sh -c "HANDLOOM_HUB=$H HANDLOOM_TOKEN=$OT '$BIN' addon status | grep -q 'Changed since approved'"
owner addon status | grep -q "Changed since approved" && ok "the hub shows it as Changed since approved" || bad "hub status: $(owner addon status)"
owner job new "After the change" --repo "$REPO" --verify true --device $DEV > "$OUT/job3.txt" 2>&1
J3="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/job3.txt" | head -1)"
run_helper charlie "$J3" coder '
handloom tool list; echo "list exit $?"
handloom tool call echo.say --arg text=late; echo "say exit $?"
'
C="$OUT/agents/charlie.log"
grep -q "changed" "$C" && grep -q "say exit 1" "$C" && ok "a helper started after the change gets no tool, with the code changed" || bad "changed: $(cat "$C")"
cp "$OUT/service.py.orig" "$HANDLOOM_HOME/addons/echo/service.py"
"$BIN" addon list | grep -q "echo.*Ready" && ok "put back as approved, it is Ready again" || bad "after restoring: $("$BIN" addon list)"

say "version skew: an add-on written for an add-on API this program does not speak"
cp -r "$ECHO" "$OUT/skew/future"
sed -i 's/^name: echo/name: future/; s/^api: 1/api: 2/' "$OUT/skew/future/addon.yaml"
"$BIN" addon add "$OUT/skew/future" --yes > "$OUT/skew-add.txt" 2>&1 && bad "an add-on for API 2 was installed" || { grep -q "add-on API 2" "$OUT/skew-add.txt" && grep -q "speaks API 1" "$OUT/skew-add.txt" && ok "add refuses it in plain words: $(grep -o 'written for add-on API 2[^.]*' "$OUT/skew-add.txt" | head -1)" || bad "skew add: $(cat "$OUT/skew-add.txt")"; }
# One that was approved while it spoke API 1 and whose folder is then replaced by a newer one (as after a downgrade).
cp -r "$ECHO" "$OUT/skew/later"
sed -i 's/^name: echo/name: later/' "$OUT/skew/later/addon.yaml"
"$BIN" addon add "$OUT/skew/later" --yes >/dev/null 2>&1 && ok "installed a second add-on, later" || bad "install later"
waitfor 30 sh -c "HANDLOOM_HUB=$H HANDLOOM_TOKEN=$OT '$BIN' addon status | grep -q 'later.*Ready'"
owner addon grant coder later.say --leaves no >/dev/null && ok "later.say granted" || bad "grant later"
sed -i 's/^api: 1/api: 2/' "$HANDLOOM_HOME/addons/later/addon.yaml"
"$BIN" addon list | grep -q "later.*Not running" && ok "the computer lists it as Not running" || bad "list: $("$BIN" addon list)"
DOC="$("$BIN" addon doctor 2>&1)"
grep -q "later.*cannot run on this computer.*add-on API 2" <<<"$DOC" && ok "addon doctor gives the reason" || bad "addon doctor: $DOC"
DOC="$("$BIN" doctor 2>&1)"
grep -q "later.*cannot run on this computer" <<<"$DOC" && ok "handloom doctor warns in words" || bad "handloom doctor: $DOC"
waitfor 30 sh -c "HANDLOOM_HUB=$H HANDLOOM_TOKEN=$OT '$BIN' addon status | grep -q 'later.*Not running'"
owner addon status | grep -q "later.*Not running" && ok "the hub shows it as Not running" || bad "hub status: $(owner addon status)"
owner job new "After the skew" --repo "$REPO" --verify true --device $DEV > "$OUT/job4.txt" 2>&1
J4="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/job4.txt" | head -1)"
run_helper delta "$J4" coder '
handloom tool list; echo "list exit $?"
handloom tool call later.say --arg text=hi; echo "say exit $?"
handloom tool call echo.say --arg text=still; echo "echo exit $?"
'
D="$OUT/agents/delta.log"
grep -q "plugin-down" "$D" && grep -q "say exit 1" "$D" && ok "a call to it is refused with plugin-down" || bad "skew call: $(cat "$D")"
grep -q "echo exit 0" "$D" && ok "the other add-on still works" || bad "echo after the skew: $(cat "$D")"

if [ "$fails" = 0 ]; then printf '\nADDONS PASS\n'; else printf '\nADDONS FAIL (%s)\n' "$fails"; exit 1; fi
