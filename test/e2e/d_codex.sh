#!/usr/bin/env bash
# Milestone D on two machines: the hub and the human here; Mantis is the device.
# A Claude lead on Mantis plans a repo job and starts a CODEX worker (Mantis has
# both logged in); the worker edits its worktree (it cannot run git there: the
# link commits for it), submits through MCP; the device checks and the lead
# accepts; the device merges the branch into job/1/integration.
#
# Run: test/e2e/d_codex.sh        (costs a few cents of Claude and Codex usage)
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TIMEOUT="${TIMEOUT:-700}"
SOCK=hltest-dcodex
LINK_FLAGS="--tmux-socket $SOCK --tick 2s --live-every 3s"
cleanup() {
  rsh "tmux -L $SOCK kill-server 2>/dev/null; sed -i '/^\\[projects\\.\"\\/root\\/hltest\\/home\\/work/,+1d' ~/.codex/config.toml; python3 - <<'PY'
import json, os
p = os.path.expanduser('~/.claude.json')
try:
    d = json.load(open(p))
    for k in [k for k in d.get('projects', {}) if k.startswith('/root/hltest')]: del d['projects'][k]
    json.dump(d, open(p + '.tmp', 'w')); os.replace(p + '.tmp', p)
except Exception: pass
PY
true" 2>/dev/null || true
  e2e_cleanup
}
trap cleanup EXIT
e2e_setup dcodex

REPO="$REMOTE_DIR/repo"
die() {
  { printf '\nFAIL: %s\n' "$*"; human job show 1; human spawns
    echo "--- lead pane:"; rsh "tmux -L $SOCK capture-pane -p -t handloom:boss-1 2>&1 | grep -v '^$' | tail -20"
    echo "--- worker pane:"; rsh "tmux -L $SOCK capture-pane -p -t handloom:coder-a 2>&1 | grep -v '^$' | tail -25"
    echo "--- remote link log:"; rsh "tail -15 $REMOTE_DIR/link.log"; } >&2
  exit 1
}

say "a toy repository on $REMOTE"
rsh "mkdir -p $REPO/src $REPO/docs && cd $REPO && git init -q && git config user.email t@t && git config user.name t &&
  echo toy > README.md && touch src/.keep docs/.keep &&
  printf '#!/bin/sh\n[ -f src/hello.txt ] || { echo src/hello.txt is missing; exit 1; }\n[ \"\$(cat src/hello.txt)\" = hello ] || { echo src/hello.txt must contain exactly: hello; exit 1; }\necho check passed\n' > check.sh && chmod +x check.sh &&
  git add -A && git commit -q -m toy"

say "profiles: a Claude lead, a Codex coder that may change only src/"
mkdir -p "$OUT/prof/boss" "$OUT/prof/coder"
cat > "$OUT/prof/boss/profile.yaml" <<'EOF2'
name: boss
description: Plans a job, starts workers, reviews what they submit
kind: claude
tools:
  allow: [read]
EOF2
echo "You lead jobs. You never write code yourself. You plan tasks, start workers, and accept only work whose device check passed." > "$OUT/prof/boss/PROMPT.md"
cat > "$OUT/prof/coder/profile.yaml" <<'EOF2'
name: coder
description: Makes small changes in src/
kind: codex
tools:
  allow: [read, edit, shell]
write: ["src/**"]
EOF2
cat > "$OUT/prof/coder/PROMPT.md" <<'EOF2'
You work in your own git worktree. Change only files under src/. Do not run git: the device commits your work for you.
When the task is done, submit it with the handloom task submit tool, with evidence "file:<path you changed>".
Then stop; handloom wakes you when there is more.
EOF2
admin profile new "$OUT/prof/boss" >/dev/null && admin profile new "$OUT/prof/coder" >/dev/null || die "profiles"

say "one command: job new, on $REMOTE"
BRIEF="Do exactly this, nothing more.
Start exactly one worker named coder-a from the profile coder, and wait until it is running.
Then create one task assigned to coder-a: create the file src/hello.txt containing exactly one line: hello
Review the submission with handloom task show; accept it only if the device check passed, otherwise reject it with the reason.
When it is accepted, stop."
human job new "Add hello file" --repo "$REPO" --verify ./check.sh --device "$REMOTE_DEVICE" --lead-profile boss --lead-name boss-1 --body "$BRIEF" | tee "$OUT/jobnew.txt"
grep -q "Started job #1" "$OUT/jobnew.txt" || die "job new"

say "wait for: lead plans, Codex works, the device commits and checks, the lead accepts, the device merges (up to ${TIMEOUT}s)"
merged=0
for i in $(seq 1 "$TIMEOUT"); do
  if human task list --job 1 --json 2>/dev/null | python3 -c 'import json,sys; t=json.load(sys.stdin); sys.exit(0 if t and all((x.get("merge") or {}).get("status")=="merged" for x in t) else 1)'; then merged=$i; break; fi
  sleep 1
done
[ "$merged" != 0 ] || die "the job did not get its work accepted and merged in ${TIMEOUT}s"
echo "   ok: accepted and merged after ${merged}s with no human input"
human job show 1 | sed 's/^/    /'
human task list --job 1 --json | python3 -c '
import json,sys
t=json.load(sys.stdin)
assert len(t)==1 and t[0]["status"]=="done", t
c=t[0]["check"]; assert c["exit_code"]==0 and c["device"], c
print("   ok: one task, done, with a passing check made by the device")' || die "task state"
rsh "git -C $REPO show job/1/integration:src/hello.txt" | grep -qx hello && echo "   ok: job/1/integration on $REMOTE holds src/hello.txt" || die "the integration branch lacks the file"
rsh "git -C $REPO log --format='%an: %s' job/1/integration | head -5" | sed 's/^/    /'
admin spawns --json 2>/dev/null | python3 -c '
import json,sys
s=json.load(sys.stdin); w=[x for x in s if x["role"]=="worker"]
assert len(w)==1 and w[0]["kind"]=="codex", s
print("   ok: the worker was Codex")' || die "the worker was not codex"
printf '\nPASS: milestone D with a Codex worker on %s\n' "$REMOTE"
