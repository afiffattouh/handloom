#!/usr/bin/env bash
# Publishing a job's work to a shared remote (D103) and the overlap warning, with NO model.
# One machine: a hub, a link, a toy repository with a local bare repository as its remote "origin".
# The agent is a shell script that stands in for Claude (a fake `claude` that only sleeps) and is
# driven by this script through the link socket; `gh` is a fake that records its arguments.
# Needs: go, tmux, git, python3, curl. Nothing touches the network or GitHub; HOME is a temporary directory.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17940}"
SOCK=hltest-pub
T="tmux -L $SOCK"
OUT="$ROOT/test/e2e/out/publish"
H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
fails=0
say() { printf '\n== %s\n' "$*"; }
ok()  { printf '   ok: %s\n' "$*"; }
bad() { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/fakebin" "$OUT/fakehome"
BIN="$OUT/bin/handloom"
(cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || { echo "build failed"; exit 1; }

# fake claude: the spawned pane runs it after handloom has registered the agent
printf '#!/bin/sh\nexec sleep 3600\n' > "$OUT/fakebin/claude"
# fake gh: records its arguments, one per line, and prints a pull request link (the link also looks at the pull
# request with "pr view" afterwards, D105; that call is not recorded, so the file keeps the "pr create" arguments)
cat > "$OUT/fakebin/gh" <<EOF2
#!/bin/sh
[ "\$2" = view ] && exit 1
for a in "\$@"; do printf '%s\n' "\$a"; done > "$OUT/gh-args.txt"
echo "https://github.com/acme/toy/pull/7"
EOF2
chmod +x "$OUT/fakebin/claude" "$OUT/fakebin/gh"

# the toy repository, and a bare repository as its remote
REPO="$OUT/repo"; BARE="$OUT/remote.git"
git init -q -b main "$REPO" && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
printf 'hello\n' > "$REPO/README.md"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"
git init -q --bare "$BARE"
git -C "$REPO" remote add origin "$BARE" && git -C "$REPO" push -q origin main

# all state under temporary directories; the link and its tmux see the fakes first and a HOME of their own
export HANDLOOM_HOME="$OUT/home" HOME="$OUT/fakehome" PATH="$OUT/fakebin:$OUT/bin:$PATH"

finish() {
  $T kill-server 2>/dev/null
  kill ${HUB_PID:-} ${LINK_PID:-} 2>/dev/null
}
trap finish EXIT

HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add hltest-pub-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
admin device assign hltest-pub-dev --to tester >/dev/null || { echo "could not hand the machine to tester"; exit 1; }   # machines belong to people (D108): the admin token made it, so it is handed over
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
agent() { local a="$1"; shift; HANDLOOM_AGENT="$a" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done

say "a job runs on the toy repository; an agent changes it and the work is merged"
human job new "Add greeting" --repo "$REPO" --device hltest-pub-dev --verify "true" --body "Add a greeting file to the toy project." | tee "$OUT/jobnew1.txt"
grep -q "Also open" "$OUT/jobnew1.txt" && bad "the first job warned about overlap" || ok "no overlap warning for the first job"
human spawn alpha --job 1 --device hltest-pub-dev --wait 60s | tee "$OUT/spawn.txt"
grep -q "is running" "$OUT/spawn.txt" || { bad "the agent did not start"; tail -5 "$OUT/link.log"; exit 1; }
human task create "write the greeting" --job 1 --assign alpha >/dev/null
TID="$(human task list --job 1 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["id"])')"
agent alpha task claim "$TID" >/dev/null || bad "claim"
WT="$HANDLOOM_HOME/work/job-1/alpha"
printf 'hi from the job\n' > "$WT/greeting.txt"
git -C "$WT" add -A && git -C "$WT" -c user.name=alpha -c user.email=alpha@handloom.local commit -q -m "add greeting"
agent alpha task submit "$TID" --evidence "file:greeting.txt" --note "SECRET-NOTE-FOR-HANDOFF" >/dev/null || bad "submit"
human task accept "$TID" >/dev/null || bad "accept"
for i in $(seq 1 40); do human task show "$TID" 2>/dev/null | grep -qi "merged" && break; sleep 1; done
git -C "$REPO" rev-parse --verify -q job/1/integration >/dev/null && ok "job/1/integration exists in the clone" || bad "no integration branch"

say "publish with a pull request"
human job publish 1 --pr | tee "$OUT/publish.txt"
BRANCH="handloom/job-1-add-greeting"
grep -q "$BRANCH" "$OUT/publish.txt" && ok "the output names the branch" || bad "branch not in output"
grep -q "https://github.com/acme/toy/pull/7" "$OUT/publish.txt" && ok "the pull request link is printed" || bad "no pull request link"
[ "$(git -C "$BARE" show "$BRANCH:greeting.txt" 2>/dev/null)" = "hi from the job" ] && ok "the remote has the branch with the job's commit" || bad "the remote lacks the job's work"
[ "$(git -C "$BARE" rev-parse "$BRANCH" 2>/dev/null)" = "$(git -C "$REPO" rev-parse job/1/integration)" ] && ok "the remote branch is the integration branch" || bad "remote branch differs"
[ -f "$OUT/gh-args.txt" ] && ok "gh was called" || bad "gh was not called"
grep -qx -- "--head" "$OUT/gh-args.txt" && grep -qx "$BRANCH" "$OUT/gh-args.txt" && grep -qx "main" "$OUT/gh-args.txt" && grep -qx "Add greeting" "$OUT/gh-args.txt" && ok "gh got head, base and title" || bad "gh arguments: $(cat "$OUT/gh-args.txt" 2>/dev/null)"
grep -q "Add a greeting file to the toy project." "$OUT/gh-args.txt" && grep -q "write the greeting (alpha)" "$OUT/gh-args.txt" && ok "the body has the brief and the task list" || bad "body lacks brief or tasks"
grep -q "SECRET-NOTE-FOR-HANDOFF\|file:greeting.txt" "$OUT/gh-args.txt" && bad "evidence or notes in the pull request" || ok "no evidence or notes in the pull request"
human job show 1 | grep -q "Latest publish: done" && ok "job show lists the publish" || bad "job show lacks the publish line"

say "never forced: a teammate's different branch of that name is kept"
MATE="$OUT/mate"; git clone -q "$BARE" "$MATE"
git -C "$MATE" checkout -q -b other origin/main
printf 'theirs\n' > "$MATE/theirs.txt"; git -C "$MATE" add -A
git -C "$MATE" -c user.name=m -c user.email=m@m commit -q -m theirs
git -C "$MATE" push -q --force origin "other:refs/heads/$BRANCH"
THEIRS="$(git -C "$BARE" rev-parse "$BRANCH")"
if human job publish 1 > "$OUT/publish2.txt" 2>&1; then bad "a diverged publish succeeded"; else grep -qi "rejected" "$OUT/publish2.txt" && ok "refused: $(head -c 120 "$OUT/publish2.txt")" || bad "no reason given"; fi
[ "$(git -C "$BARE" rev-parse "$BRANCH")" = "$THEIRS" ] && ok "the remote branch was not overwritten" || bad "the remote branch changed"

say "a second job on the same repository shows the overlap warning"
human job new "Second change" --repo "$REPO" --device hltest-pub-dev | tee "$OUT/jobnew2.txt"
JOB2="$(grep -o "Started job #[0-9]*" "$OUT/jobnew2.txt" | grep -o "[0-9]*$")"
grep -q "Also open on this repository: #1 Add greeting" "$OUT/jobnew2.txt" && ok "the warning names job #1" || bad "no overlap warning"
human job show "$JOB2" | grep -q "Also open on this repository" && ok "job show repeats it" || bad "job show lacks it"

if [ "$fails" = 0 ]; then printf '\nPUBLISH PASS\n'; else printf '\nPUBLISH FAIL (%s)\n' "$fails"; exit 1; fi
