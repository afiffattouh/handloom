#!/usr/bin/env bash
# Hosted projects (D106), with NO model and NO gh: the hub itself holds a project's code, and a machine
# works on it with plain git and nothing else set up by hand. One machine: a hub on 127.0.0.1, a link, a
# fake `claude` that only sleeps (this script drives the agent through the link socket).
# The script builds the binary with the normal PATH, then runs everything with a MINIMAL PATH that has
# git, tmux, python3, curl and sh tools but no gh at all, so nothing here can quietly use GitHub.
# Needs: go (to build), tmux, git, python3, curl. HOME is a temporary directory.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
PORT="${PORT:-17943}"
SOCK=hltest-hosted
OUT="$ROOT/test/e2e/out/hosted"
H="http://127.0.0.1:$PORT"
DEV=hltest-hosted-dev
fails=0
say() { printf '\n== %s\n' "$*"; }
ok()  { printf '   ok: %s\n' "$*"; }
bad() { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/fakebin" "$OUT/fakehome" "$OUT/minpath"
BIN="$OUT/bin/handloom"
(cd "$ROOT" && PATH="$PATH:$(dirname "$GO")" "$GO" build -o "$BIN" ./cmd/handloom) || { echo "build failed"; exit 1; }

# the minimal PATH: only these tools, found with the normal PATH, linked into one folder
for tool in git tmux python3 curl sh bash env cat grep sed tr head tail wc sort seq sleep mkdir rm cp ln ls dirname basename tee date uname touch test true false cut mktemp readlink; do
  p="$(command -v "$tool" 2>/dev/null)" && [ -n "$p" ] && [ "${p#/}" != "$p" ] && ln -sf "$p" "$OUT/minpath/$tool"
done
printf '#!/bin/sh\nexec sleep 3600\n' > "$OUT/fakebin/claude"; chmod +x "$OUT/fakebin/claude"
export PATH="$OUT/fakebin:$OUT/bin:$OUT/minpath"
export HANDLOOM_HOME="$OUT/home" HOME="$OUT/fakehome"
export GIT_TERMINAL_PROMPT=0 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
T="tmux -L $SOCK"
finish() { $T kill-server 2>/dev/null; kill ${HUB_PID:-} ${LINK_PID:-} 2>/dev/null; }
trap finish EXIT

say "the PATH has git and no gh"
command -v gh >/dev/null 2>&1 && bad "gh is reachable: $(command -v gh)" || ok "gh is not reachable"
command -v git >/dev/null 2>&1 && command -v tmux >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 && ok "git, tmux, python3 and curl are" || { bad "a needed tool is missing from the minimal PATH"; exit 1; }

HANDLOOM_ALLOW_CONFIDENTIAL=1 HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add $DEV --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
agent() { local a="$1"; shift; HANDLOOM_AGENT="$a" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
CRED="$(python3 -c 'import json,os;print(json.load(open(os.environ["HANDLOOM_HOME"]+"/link.json"))["credential"])')"
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done

say "project new demo (no flags): the hub holds the code, and this machine gets its copy by itself"
human project new demo --device $DEV | tee "$OUT/new.txt"
grep -q "Created project demo on the hub" "$OUT/new.txt" && ok "created on the hub" || bad "new output"
CLONE="$HANDLOOM_HOME/projects/demo"; BARE="$OUT/data/git/demo.git"
[ -d "$BARE" ] && ok "the repository is on the hub's disk, not in the database" || bad "no repository at $BARE"
[ "$(git -C "$BARE" show main:README.md 2>/dev/null)" = "# demo" ] && ok "main has the first commit (README)" || bad "no first commit"
[ "$(git -C "$CLONE" remote get-url origin 2>/dev/null)" = "$H/git/demo.git" ] && ok "the copy's origin is the hub's address, with no login in it" || bad "origin = $(git -C "$CLONE" remote get-url origin 2>&1)"
{ git -C "$CLONE" remote -v; cat "$CLONE/.git/config"; git -C "$CLONE" config --list --show-origin; } > "$OUT/clone-text.txt" 2>&1
grep -q "hvd_" "$OUT/clone-text.txt" && bad "a credential is in git remote -v / .git/config" || ok "no token in git remote -v or .git/config"
grep -q "git-credential" "$CLONE/.git/config" && ok "the copy asks handloom git-credential for its login" || bad "no helper in .git/config"
git -C "$CLONE" fetch -q origin && ok "plain git fetch works, no prompt" || bad "git fetch needed a login"
CLEANGIT="env GIT_TERMINAL_PROMPT=0 git"
$CLEANGIT clone -q "$H/git/demo.git" "$OUT/anon" >/dev/null 2>&1 && bad "an anonymous clone worked" || ok "an anonymous clone is refused"

say "the credential helper answers for the hub only"
printf 'protocol=http\nhost=127.0.0.1:%s\n\n' "$PORT" | "$BIN" git-credential get | grep -q "^username=handloom$" && ok "answers for the hub" || bad "no answer for the hub"
[ -z "$(printf 'protocol=https\nhost=github.com\n\n' | "$BIN" git-credential get)" ] && ok "says nothing for github.com" || bad "answered for another host"
[ -z "$(printf 'protocol=http\nhost=127.0.0.1:1\n\n' | "$BIN" git-credential get)" ] && ok "says nothing for another port" || bad "answered for another port"

say "a job on the project; an agent changes the code; the work is merged on the machine"
human job new "Add greeting" --project demo --device $DEV --verify "true" --body "Add a greeting file." > "$OUT/jobnew.txt" 2>&1; cat "$OUT/jobnew.txt"
grep -q "Started job #1" "$OUT/jobnew.txt" || { bad "job new failed"; exit 1; }
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

say "a confidential job cannot be published to the hub"
human job new "Client secrets" --project demo --device $DEV --confidential > "$OUT/conf.txt" 2>&1; cat "$OUT/conf.txt"
CJ="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/conf.txt" | head -1)"
if human job publish "$CJ" > "$OUT/conf-pub.txt" 2>&1; then bad "a confidential job was published to the hub"; else grep -q "must not sit on the hub" "$OUT/conf-pub.txt" && ok "refused: $(head -c 120 "$OUT/conf-pub.txt")" || bad "refused, but not in words: $(cat "$OUT/conf-pub.txt")"; fi

say "publish pushes a branch under handloom/ to the hub, and says who merges"
human job publish 1 | tee "$OUT/publish.txt"
BRANCH="handloom/job-1-add-greeting"
[ "$(git -C "$BARE" show "$BRANCH:greeting.txt" 2>/dev/null)" = "hi from the job" ] && ok "the hub has the job's branch with the commit" || bad "the hub lacks the job's work"
grep -q "ask the owner to merge it with: handloom job merge 1" "$OUT/publish.txt" && ok "the message says how to merge (not a failure, no gh talk)" || bad "publish message: $(cat "$OUT/publish.txt")"
grep -qi "no gh\|gh is not installed\|pull request" "$OUT/publish.txt" && bad "the message talks about gh or pull requests" || ok "no gh or pull request wording"
[ "$(git -C "$BARE" rev-parse main)" = "$(git -C "$CLONE" rev-parse main)" ] && ok "main on the hub was not touched by the publish" || bad "main moved"

say "the machine cannot touch main, tags, deletes or rewrites"
git -C "$CLONE" checkout -q -b scratch && printf 'sneaky\n' > "$CLONE/sneaky.txt" && git -C "$CLONE" add -A && git -C "$CLONE" -c user.name=x -c user.email=x@x commit -q -m sneaky
git -C "$CLONE" push origin HEAD:main > "$OUT/push-main.txt" 2>&1 && bad "a direct push to main was accepted" || { grep -q "cannot be pushed to" "$OUT/push-main.txt" && ok "git push origin HEAD:main is refused, in words" || bad "refused but not in words: $(cat "$OUT/push-main.txt")"; }
git -C "$CLONE" tag v1 && git -C "$CLONE" push origin v1 > "$OUT/push-tag.txt" 2>&1 && bad "a tag was accepted" || ok "a tag is refused"
git -C "$CLONE" push origin --delete "$BRANCH" > "$OUT/push-del.txt" 2>&1 && bad "a delete was accepted" || ok "a delete is refused"
git -C "$CLONE" push --force origin "HEAD:refs/heads/$BRANCH" > "$OUT/push-force.txt" 2>&1 && bad "a forced rewrite was accepted" || ok "a rewrite of a published branch is refused"
git -C "$CLONE" checkout -q main
[ "$(git -C "$BARE" rev-parse main)" = "$(git -C "$CLONE" rev-parse main)" ] && ok "main on the hub is unchanged" || bad "main moved"

say "an agent and an AI app cannot merge; the person can"
OT="$(human token new --app --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
if HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$OT" $CLEAN "$BIN" job merge 1 > "$OUT/app-merge.txt" 2>&1; then bad "an AI app merged"; else ok "AI app refused: $(head -c 100 "$OUT/app-merge.txt")"; fi
if agent alpha job merge 1 > "$OUT/agent-merge.txt" 2>&1; then bad "an agent merged"; else ok "agent refused: $(head -c 100 "$OUT/agent-merge.txt")"; fi
[ "$(git -C "$BARE" rev-parse main)" = "$(git -C "$CLONE" rev-parse main)" ] && ok "nothing moved" || bad "main moved after refused merges"
human job merge 1 | tee "$OUT/merge.txt"
grep -q "Merged job #1 (handloom/job-1-add-greeting) into main of demo: fast-forward" "$OUT/merge.txt" && ok "merged (fast-forward)" || bad "merge output"
[ "$(git -C "$BARE" show main:greeting.txt 2>/dev/null)" = "hi from the job" ] && ok "main on the hub now has the work" || bad "main lacks the work"
human job show 1 | grep -q "merged into main" && ok "job show says merged into main" || bad "job show: $(human job show 1)"
curl -s -H "Authorization: Bearer $HT" "$H/v1/digest" | grep -q "job 1 merged into demo" && ok "the activity feed says so" || bad "no activity line"

say "a new job on another machine's view of the project starts from the merged main"
human job new "Second job" --project demo --device $DEV > "$OUT/jobnew2.txt" 2>&1 || bad "job new: $(cat "$OUT/jobnew2.txt")"
J2="$(sed -n 's/^Started job #\([0-9]*\).*/\1/p' "$OUT/jobnew2.txt" | head -1)"
human spawn beta --job "$J2" --device $DEV --wait 60s > "$OUT/spawn-beta.txt" 2>&1
grep -q "is running" "$OUT/spawn-beta.txt" && ok "the second job's agent started" || bad "beta did not start: $(cat "$OUT/spawn-beta.txt")"
human job show "$J2" | tee "$OUT/show2.txt" | grep -Eq "^Base: fresh: main moved forward [0-9]+ commits? to " && ok "the second job's base shows: fresh, main moved forward" || bad "job show $J2: $(cat "$OUT/show2.txt")"
[ -f "$HANDLOOM_HOME/work/job-$J2/beta/greeting.txt" ] && ok "the second job's worktree has the merged work" || bad "the worktree lacks the merged work"

say "the hub's database holds no code and no machine credential; the logs hold no credential"
grep -aq "hi from the job" "$OUT/data/"handloom.db* 2>/dev/null && bad "the job's code is in the hub's database" || ok "the code is not in the hub's database"
grep -aq "$CRED" "$OUT/data/"handloom.db* 2>/dev/null && bad "the plain credential is in the database" || ok "the plain credential is not in the database (only its hash)"
for f in "$OUT/hub.log" "$OUT/link.log" "$OUT/new.txt" "$OUT/publish.txt" "$OUT/push-main.txt"; do grep -aq "$CRED" "$f" && bad "the credential is in $(basename "$f")"; done; ok "no credential in the hub log, the link log or any output"
grep -rq "$CRED" "$CLONE/.git" 2>/dev/null && bad "the credential is in the copy's .git" || ok "the credential is not in the copy's .git folder"

if [ "$fails" = 0 ]; then printf '\nHOSTED PASS\n'; else printf '\nHOSTED FAIL (%s)\n' "$fails"; exit 1; fi
