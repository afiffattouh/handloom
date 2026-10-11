#!/usr/bin/env bash
# Projects (D104, D105), with NO model: a project is a name and a shared git remote that a machine gets on demand.
# One machine: a hub (file:// remotes allowed, for this test only), a link, a local bare repository as the
# shared remote, and a fake `gh` that makes bare repositories instead of calling GitHub. A fake `claude`
# only sleeps; this script drives the agent through the link socket.
# Needs: go, tmux, git, python3, curl. Nothing touches the network or GitHub; HOME is a temporary directory.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17941}"
SOCK=hltest-proj
T="tmux -L $SOCK"
OUT="$ROOT/test/e2e/out/projects"
H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
DEV=hltest-proj-dev
fails=0
say() { printf '\n== %s\n' "$*"; }
ok()  { printf '   ok: %s\n' "$*"; }
bad() { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/fakebin" "$OUT/fakehome" "$OUT/ghremotes"
BIN="$OUT/bin/handloom"
(cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || { echo "build failed"; exit 1; }

printf '#!/bin/sh\nexec sleep 3600\n' > "$OUT/fakebin/claude"
# fake gh: "repo create [owner/]name --private|--public --clone" makes a bare repository and clones it into ./name;
# every call's arguments are appended to gh-args.txt, one call per line.
cat > "$OUT/fakebin/gh" <<EOF2
#!/bin/sh
printf '%s ' "\$@" >> "$OUT/gh-args.txt"; echo >> "$OUT/gh-args.txt"
# "pr create" prints a pull request address; "pr view" answers with the file the script switches (open, then merged).
if [ "\$1 \$2" = "pr create" ]; then echo "https://github.com/acme/shared/pull/1"; exit 0; fi
if [ "\$1 \$2" = "pr view" ]; then cat "$OUT/pr-answer.json"; exit 0; fi
[ "\$1 \$2" = "repo create" ] || exit 2
name="\${3##*/}"
git init -q --bare "$OUT/ghremotes/\$name.git" || exit 1
git clone -q "file://$OUT/ghremotes/\$name.git" "\$name" 2>/dev/null
echo "created \$3"
EOF2
chmod +x "$OUT/fakebin/claude" "$OUT/fakebin/gh"

# the shared remote: a bare repository with one commit on main
BARE="$OUT/shared.git"; SEED="$OUT/seed"
git init -q --bare "$BARE"
git init -q -b main "$SEED" && git -C "$SEED" config user.email t@t && git -C "$SEED" config user.name t
printf 'hello\n' > "$SEED/README.md"; git -C "$SEED" add -A && git -C "$SEED" commit -q -m "toy"
git -C "$SEED" push -q "$BARE" main && git -C "$BARE" symbolic-ref HEAD refs/heads/main
REMOTE="file://$BARE"

# the pull request is looked at every 2 seconds here (2 minutes by default)
export HANDLOOM_PR_POLL=2s
export HANDLOOM_HOME="$OUT/home" HOME="$OUT/fakehome" PATH="$OUT/fakebin:$OUT/bin:$PATH"
# git needs an identity for the first commit of a created project (the fake home has none)
finish() {
  $T kill-server 2>/dev/null
  kill ${HUB_PID:-} ${LINK_PID:-} 2>/dev/null
}
trap finish EXIT

HANDLOOM_ALLOW_FILE_REMOTES=1 HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add $DEV --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
agent() { local a="$1"; shift; HANDLOOM_AGENT="$a" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done

say "attach a repository by its address; credentials in the address are refused"
if human project new bad --remote "https://user:secret@github.com/acme/toy" > "$OUT/cred.txt" 2>&1; then bad "an address with a password was accepted"; else grep -q "password or token" "$OUT/cred.txt" && ok "refused in words" || bad "no reason: $(cat "$OUT/cred.txt")"; fi
human project new shared --remote "$REMOTE" | tee "$OUT/attach.txt"
grep -q "Attached project shared" "$OUT/attach.txt" && ok "attached" || bad "attach output"
human project list | grep -q "shared.*on no machine yet" && ok "no machine has it yet" || bad "list before the clone"
[ -d "$HANDLOOM_HOME/projects/shared" ] && bad "a copy exists before anyone asked" || ok "nothing was cloned by attaching"

say "job new --project fetches the copy on its own, then starts from it"
human job new "Add greeting" --project shared --device $DEV --verify "true" --body "Add a greeting file." > "$OUT/jobnew.txt" 2> "$OUT/jobnew.err"; cat "$OUT/jobnew.txt" "$OUT/jobnew.err"
CLONE="$HANDLOOM_HOME/projects/shared"
grep -q "Getting project shared onto $DEV" "$OUT/jobnew.err" && ok "it said it was fetching the copy" || bad "no fetch message"
[ "$(git -C "$CLONE" remote get-url origin 2>/dev/null)" = "$REMOTE" ] && ok "the copy is a normal clone with origin = the shared remote" || bad "no clone at $CLONE"
[ -f "$CLONE/README.md" ] && ok "the copy has the code" || bad "the copy lacks the code"
JREPO="$(human job show 1 --json | python3 -c 'import json,sys;j=json.load(sys.stdin)["job"];print(j.get("repo",""),j.get("codebase",""),j.get("base",""),sep="|")')"
[ "$JREPO" = "$CLONE|shared|main" ] && ok "the job's repository is the copy, with the project's name and base branch" || bad "job repo|project|base = $JREPO"
human project show shared | grep -q "$DEV: ready at $CLONE" && ok "project show: ready on this machine" || bad "project show"

say "an agent changes the code and the work is merged"
human spawn alpha --job 1 --device $DEV --wait 60s | tee "$OUT/spawn.txt"
grep -q "is running" "$OUT/spawn.txt" || { bad "the agent did not start"; tail -5 "$OUT/link.log"; exit 1; }
human task create "write the greeting" --job 1 --assign alpha >/dev/null
TID="$(human task list --job 1 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["id"])')"
agent alpha task claim "$TID" >/dev/null || bad "claim"
WT="$HANDLOOM_HOME/work/job-1/alpha"
printf 'hi from the job\n' > "$WT/greeting.txt"
git -C "$WT" add -A && git -C "$WT" -c user.name=alpha -c user.email=alpha@handloom.local commit -q -m "add greeting"
agent alpha task submit "$TID" --evidence "file:greeting.txt" --note "done" >/dev/null || bad "submit"
human task accept "$TID" >/dev/null || bad "accept"
for i in $(seq 1 40); do human task show "$TID" 2>/dev/null | grep -qi "merged" && break; sleep 1; done
git -C "$CLONE" rev-parse --verify -q job/1/integration >/dev/null && ok "job/1/integration exists in the copy" || bad "no integration branch"

say "publish pushes back to the shared remote"
human job publish 1 | tee "$OUT/publish.txt"
BRANCH="handloom/job-1-add-greeting"
[ "$(git -C "$BARE" show "$BRANCH:greeting.txt" 2>/dev/null)" = "hi from the job" ] && ok "the shared remote has the job's branch with the commit" || bad "the remote lacks the job's work"
[ "$(git -C "$BARE" rev-parse main)" = "$(git -C "$SEED" rev-parse main)" ] && ok "main on the remote was not touched" || bad "main moved"

say "a new job starts from the latest base: the remote moved, the copy is fast-forwarded"
MATE="$OUT/mate"
git clone -q "$REMOTE" "$MATE" && git -C "$MATE" config user.email m@m && git -C "$MATE" config user.name mate
printf 'from a teammate\n' > "$MATE/teammate.txt"; git -C "$MATE" add -A && git -C "$MATE" commit -q -m "teammate change" && git -C "$MATE" push -q origin main
[ "$(git -C "$CLONE" rev-parse main)" != "$(git -C "$BARE" rev-parse main)" ] && ok "the copy is behind the remote before the job starts" || bad "the copy was not behind"
human job new "Second job" --project shared --device $DEV > "$OUT/jobnew-fresh.txt" 2>&1 || { bad "job new: $(cat "$OUT/jobnew-fresh.txt")"; }
J2="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/jobnew-fresh.txt" | head -1)"
human spawn beta --job "$J2" --device $DEV --wait 60s > "$OUT/spawn-beta.txt" 2>&1
grep -q "is running" "$OUT/spawn-beta.txt" && ok "the job's agent started" || { bad "beta did not start: $(cat "$OUT/spawn-beta.txt")"; }
[ "$(git -C "$CLONE" rev-parse main)" = "$(git -C "$BARE" rev-parse main)" ] && ok "the copy of main was fast-forwarded to the remote" || bad "the copy is still behind"
[ -f "$CLONE/teammate.txt" ] && [ -f "$HANDLOOM_HOME/work/job-$J2/beta/teammate.txt" ] && ok "the job's worktree has the teammate's commit" || bad "the worktree lacks the teammate's commit"
human job show "$J2" | tee "$OUT/show2.txt" | grep -q "^Base: fresh: main moved forward 1 commit to " && ok "job show says fresh" || bad "job show 2: $(cat "$OUT/show2.txt")"
human job list | grep -q "job #$J2 .*Second job" && ok "listed" || bad "job list"

say "a copy with commits of its own is not touched"
printf 'my local work\n' > "$CLONE/local.txt"; git -C "$CLONE" add -A && git -C "$CLONE" -c user.name=me -c user.email=me@m commit -q -m "local only"
LOCAL="$(git -C "$CLONE" rev-parse main)"
printf 'more from a teammate\n' > "$MATE/teammate2.txt"; git -C "$MATE" add -A && git -C "$MATE" commit -q -m "teammate change 2" && git -C "$MATE" push -q origin main
human job new "Third job" --project shared --device $DEV > "$OUT/jobnew-div.txt" 2>&1 || bad "job new: $(cat "$OUT/jobnew-div.txt")"
J3="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/jobnew-div.txt" | head -1)"
human spawn gamma --job "$J3" --device $DEV --wait 60s > "$OUT/spawn-gamma.txt" 2>&1
grep -q "is running" "$OUT/spawn-gamma.txt" && ok "the job still started" || bad "gamma did not start: $(cat "$OUT/spawn-gamma.txt")"
human job show "$J3" | tee "$OUT/show3.txt" | grep -q "^Base: diverged: this machine's copy of main has commits that are not on origin; the job starts from it as it is" && ok "job show says diverged" || bad "job show 3: $(cat "$OUT/show3.txt")"
[ "$(git -C "$CLONE" rev-parse main)" = "$LOCAL" ] && [ "$(cat "$CLONE/local.txt")" = "my local work" ] && [ ! -e "$CLONE/teammate2.txt" ] && ok "nothing was overwritten or merged" || bad "the diverged copy was changed"

say "the pull request is followed: open, then merged"
printf '{"state":"OPEN","mergedAt":null,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","title":"Add greeting"}\n' > "$OUT/pr-answer.json"
human job publish 1 --pr > "$OUT/publish-pr.txt" 2>&1; cat "$OUT/publish-pr.txt"
grep -q "https://github.com/acme/shared/pull/1" "$OUT/publish-pr.txt" && ok "the pull request was opened through gh" || bad "no pull request link"
for i in $(seq 1 20); do human job show 1 2>/dev/null | grep -q "^Pull request: open, approved, can merge" && break; sleep 1; done
human job show 1 | grep -q "^Pull request: open, approved, can merge" && ok "job show: open, approved, can merge" || bad "job show 1 after publish: $(human job show 1)"
human job list | grep -q "job #1 .*pull request merged" && bad "marked merged too early" || ok "not marked merged while open"
printf '{"state":"MERGED","mergedAt":"2026-10-11T10:00:00Z","mergeable":"UNKNOWN","reviewDecision":"APPROVED","title":"Add greeting"}\n' > "$OUT/pr-answer.json"
for i in $(seq 1 20); do human job show 1 2>/dev/null | grep -q "^Pull request: merged" && break; sleep 1; done
human job show 1 | grep -q "^Pull request: merged" && ok "job show: merged" || bad "job show 1 after merge: $(human job show 1)"
human job list | grep -q "job #1 .*\[pull request merged\]" && ok "the jobs list marks it" || bad "job list lacks the marker"
human job show 1 --json | python3 -c 'import json,sys;j=json.load(sys.stdin)["job"];sys.exit(0 if j["status"]=="open" else 1)' && ok "nothing closed the job by itself" || bad "the job was closed"
curl -s -H "Authorization: Bearer $HT" "$H/v1/digest" | grep -q "pull request merged for job 1" && ok "the activity feed says so" || bad "no activity line"

say "the hub never held the code or a login"
grep -aq "hi from the job" "$OUT/data/"*.db* 2>/dev/null && bad "the job's code is in the hub's database" || ok "the code is not in the hub's database"
grep -aq "secret" "$OUT/data/"*.db* 2>/dev/null && bad "the refused password is in the hub's database" || ok "the refused password is not in the hub's database"

say "create a repository with gh on the machine"
human project new fresh --create --owner acme --device $DEV | tee "$OUT/create.txt"
grep -q "Created project fresh: file://$OUT/ghremotes/fresh.git" "$OUT/create.txt" && ok "the output gives the new address" || bad "create output"
grep -qx "repo create acme/fresh --private --clone " "$OUT/gh-args.txt" && ok "gh was asked for a private repository under the owner" || bad "gh arguments: $(cat "$OUT/gh-args.txt" 2>/dev/null)"
NEWBARE="$OUT/ghremotes/fresh.git"
[ "$(git -C "$NEWBARE" show main:README.md 2>/dev/null)" = "# fresh" ] || [ "$(git -C "$NEWBARE" show master:README.md 2>/dev/null)" = "# fresh" ] && ok "the first commit (README) was pushed, so a base branch exists" || bad "no first commit on the new remote"
human project show fresh | grep -q "$DEV: ready at $HANDLOOM_HOME/projects/fresh" && ok "the copy is ready for this machine" || bad "project show fresh"
human job new "Second project job" --project fresh --device $DEV | tee "$OUT/jobnew2.txt"
grep -q "Started job #[0-9]*.*\[project fresh, " "$OUT/jobnew2.txt" && ok "a job starts from the new project" || bad "job on the new project"

say "an agent or an AI app cannot create or fetch"
OT="$(human token new --app --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
if HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$OT" $CLEAN "$BIN" project new nope --create --device $DEV > "$OUT/app.txt" 2>&1; then bad "an AI app created a repository"; else ok "AI app refused: $(head -c 100 "$OUT/app.txt")"; fi
HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$OT" $CLEAN "$BIN" project list | grep -q "shared" && ok "an AI app can read the projects" || bad "an AI app cannot read projects"
if agent alpha project new nope --create --device $DEV > "$OUT/agent.txt" 2>&1; then bad "an agent created a repository"; else ok "agent refused"; fi

if [ "$fails" = 0 ]; then printf '\nPROJECTS PASS\n'; else printf '\nPROJECTS FAIL (%s)\n' "$fails"; exit 1; fi
