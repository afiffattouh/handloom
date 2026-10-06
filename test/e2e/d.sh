#!/usr/bin/env bash
# Milestone D check, on one machine with real Claude Code agents:
#
#   one command starts a job on a toy repository: the job starts its own lead;
#   the lead (reading only the brief) plans two tasks, starts a worker from a
#   profile, assigns the tasks (the second depends on the first); the worker
#   edits its own worktree, commits and submits; the device checks each
#   submission with the job's check command; the lead accepts; the job asks the
#   human to close it. Nobody types anything after `job new`.
#   Then, with no model involved, one deliberate out-of-scope edit is refused.
#
# Needs: go, tmux, git, claude (logged in), python3, curl. Costs a few cents.
#   RUNS=3   how many times to run the whole thing (the kill criterion counts failures)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
RUNS="${RUNS:-1}"
TIMEOUT="${TIMEOUT:-600}"
BASEPORT="${PORT:-17470}"
SOCK=hltest-d
T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"

one_run() {
  local n="$1" OUT="$ROOT/test/e2e/out/d-$1" PORT=$((BASEPORT + $1)) fails=0
  local H="http://127.0.0.1:$PORT"
  say()  { printf '\n== [run %s] %s\n' "$n" "$*"; }
  ok()   { printf '   ok: %s\n' "$*"; }
  bad()  { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }
  rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/prof/boss" "$OUT/prof/coder"
  trap '' RETURN
  local BIN="$OUT/bin/handloom"
  (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || { bad "build"; return 1; }
  export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"

  # The toy repository: one commit, a check script, two folders.
  local REPO="$OUT/repo"; mkdir -p "$REPO/src" "$REPO/docs"
  git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
  echo "toy" > "$REPO/README.md"; touch "$REPO/src/.keep" "$REPO/docs/.keep"
  cat > "$REPO/check.sh" <<'EOF2'
#!/bin/sh
# The job's check: what the worker has built so far must be right.
[ -f src/hello.txt ] || { echo "src/hello.txt is missing"; exit 1; }
[ "$(cat src/hello.txt)" = "hello" ] || { echo "src/hello.txt must contain exactly: hello"; exit 1; }
if [ -f docs/notes.md ]; then [ "$(cat docs/notes.md)" = "done" ] || { echo "docs/notes.md must contain exactly: done"; exit 1; }; fi
echo "check passed"
EOF2
  chmod +x "$REPO/check.sh"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"

  HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s > "$OUT/hub.log" 2>&1 & local HUB_PID=$!
  for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
  local ADMIN; ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
  admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
  local JT HT
  JT="$(admin device add hltest-d-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
  HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
  human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
  $CLEAN "$BIN" link join "$H" "$JT" >/dev/null
  $CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & local LINK_PID=$!
  for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done

  finish() {
    $T kill-server 2>/dev/null; kill $HUB_PID $LINK_PID 2>/dev/null
    python3 - "$OUT" <<'PY' 2>/dev/null
import json, os, sys
p = os.path.expanduser("~/.claude.json")
d = json.load(open(p)); out = sys.argv[1]
for k in [k for k in d.get("projects", {}) if k.startswith(out)]:
    del d["projects"][k]
json.dump(d, open(p + ".tmp", "w")); os.replace(p + ".tmp", p)
PY
  }

  say "two profiles: a lead that plans and reads, a coder that may change only src/ and docs/"
  cat > "$OUT/prof/boss/profile.yaml" <<'EOF2'
name: boss
description: Plans a job, starts workers, reviews what they submit
kind: claude
tools:
  allow: [read]
EOF2
  cat > "$OUT/prof/boss/PROMPT.md" <<'EOF2'
You lead jobs. You never write code yourself. You plan tasks, start workers, wait for them, and accept
only work whose device check passed.
EOF2
  cat > "$OUT/prof/coder/profile.yaml" <<'EOF2'
name: coder
description: Makes small changes in src/ and docs/ and commits them
kind: claude
tools:
  allow: [read, edit, shell]
  deny_commands: [rm]
write: ["src/**", "docs/**"]
EOF2
  cat > "$OUT/prof/coder/PROMPT.md" <<'EOF2'
You work in your own git worktree on your own branch. Change only files under src/ and docs/.
When a task is done: run `git add -A && git commit -q -m "<what you did>"`, then submit the task with evidence
`commit:<output of git rev-parse --short HEAD>` using `handloom task submit <id> --evidence "commit:<sha>"`.
Then end your turn; handloom wakes you when there is more.
EOF2
  admin profile new "$OUT/prof/boss" >/dev/null && admin profile new "$OUT/prof/coder" >/dev/null || { bad "profiles"; finish; return 1; }

  say "one command: job new"
  local BRIEF="Do exactly this, nothing more.
Plan exactly two tasks and start exactly one worker named coder-a from the profile coder.
Task 1, assigned to coder-a: create the file src/hello.txt containing exactly one line: hello
Task 2, assigned to coder-a and depending on task 1: create the file docs/notes.md containing exactly one line: done
Create the tasks only after the worker is running. Review each submission with handloom task show; accept it only if the device check passed, otherwise reject it with the reason.
When both tasks are accepted, stop."
  human job new "Add hello and notes files" --repo "$REPO" --verify ./check.sh --device hltest-d-dev --lead-profile boss --lead-name boss-1 --body "$BRIEF" | tee "$OUT/jobnew.txt"
  grep -q "Started job #1" "$OUT/jobnew.txt" || { bad "job new"; finish; return 1; }
  local JOBSEQ; JOBSEQ="$(admin audit | grep -c .)"

  say "wait: the lead plans, the worker works, the device checks, the lead accepts (up to ${TIMEOUT}s)"
  local done_at=0 i
  for i in $(seq 1 "$TIMEOUT"); do
    if human digest --json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if any(i["kind"]=="job-done" for i in d["to_review"]) else 1)'; then done_at=$i; break; fi
    sleep 1
  done
  if [ "$done_at" = 0 ]; then
    bad "the job did not reach 'every task done' in ${TIMEOUT}s"
    human job show 1 | sed 's/^/    /'; human spawns | sed 's/^/    /'
    echo "--- lead pane:"; $T capture-pane -p -t handloom:boss-1 2>&1 | grep -v '^$' | tail -25
    echo "--- worker pane:"; $T capture-pane -p -t handloom:coder-a 2>&1 | grep -v '^$' | tail -25
    finish; return 1
  fi
  ok "the job reached 'every task done' after ${done_at}s with no human input"
  human job show 1 | sed 's/^/    /'

  say "what happened"
  local SP; SP="$(human spawns --json)"
  echo "$SP" | python3 -c '
import json,sys
s=json.load(sys.stdin)
names={x["name"]:x for x in s}
assert "boss-1" in names and names["boss-1"]["role"]=="lead", s
workers=[x for x in s if x["role"]=="worker"]
assert len(workers)==1 and workers[0]["name"]=="coder-a" and workers[0]["profile"]=="coder@1", workers
print("   ok: spawns: the job started boss-1 (lead) and the lead started coder-a (worker, coder@1)")' || bad "spawns are not as expected: $SP"
  local TASKS; TASKS="$(human task list --job 1 --json)"
  echo "$TASKS" | python3 -c '
import json,sys
t=json.load(sys.stdin)
assert len(t)==2, t
assert all(x["status"]=="done" for x in t), t
assert all(x.get("check") and x["check"]["exit_code"]==0 and x["check"]["device"] for x in t), "a task was accepted without a passing device check"
assert any(1 for x in t if x["depends_on"]), "no task depends on another"
print("   ok: two tasks, both done, both with a passing check made by the device")' || bad "tasks are not as expected: $TASKS"
  admin audit > "$OUT/audit.txt"
  python3 - "$OUT/audit.txt" <<'PY' || bad "the order of events"
import re,sys
rows=[l.split(None,4) for l in open(sys.argv[1]).read().splitlines() if l.strip() and l.split()[0].isdigit()]
def first(action, target=None, after=0):
    for i,r in enumerate(rows):
        if i>after and len(r)>3 and r[3]==action and (target is None or r[4].split()[0]==target): return i
    return None
claim2=first("task.claim","task:3"); acc1=first("task.accept","task:2")
assert acc1 is not None and claim2 is not None, "claim or accept missing"
assert claim2>acc1, "task 2 was claimed before task 1 was accepted"
print("   ok: task 2 was claimed only after task 1 was accepted")
PY
  # Nobody but agents, devices and the hub acted after the job was started.
  tail -n +$((JOBSEQ+1)) "$OUT/audit.txt" | awk '{print $3}' | grep -E '^(human:|admin)' | sort | uniq -c > "$OUT/human-actions.txt"
  if [ -s "$OUT/human-actions.txt" ]; then bad "human or admin actions after job new: $(cat "$OUT/human-actions.txt")"; else ok "no human or admin action after job new"; fi
  (git -C "$REPO" branch --list 'job/*' | sed 's/^/    branch: /')
  git -C "$REPO" log --oneline --all | sed 's/^/    /' | head -6
  git -C "$REPO" show job/1/coder-a:src/hello.txt >/dev/null 2>&1 && ok "the worker's commit is on its own branch job/1/coder-a" || bad "no commit on job/1/coder-a"

  say "a deliberate out-of-scope edit, no model: the same worker identity submits a task after touching README.md"
  local WORK="$OUT/home/work/job-1/coder-a" TID
  TID="$(human task create "Scope probe" --job 1 --assign coder-a --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')"
  as_worker() { (cd "$WORK" && HANDLOOM_AGENT=coder-a $CLEAN "$BIN" "$@"); }
  as_worker task claim "$TID" >/dev/null 2>&1 || bad "the probe could not claim its task"
  echo "changed outside the scope" >> "$WORK/README.md"
  local out; out="$(as_worker task submit "$TID" --evidence "file:README.md" 2>&1)"; local code=$?
  echo "    $out" | cut -c1-260
  if [ "$code" != 0 ] && echo "$out" | grep -q "README.md"; then ok "the submit was refused and names README.md"; else bad "the out-of-scope submit was not refused"; fi
  [ "$(human task show "$TID" --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')" = claimed ] && ok "the hub never saw the refused submit" || bad "the refused submit reached the hub"
  git -C "$WORK" checkout -q -- README.md
  as_worker task submit "$TID" --evidence "file:src/hello.txt" >/dev/null 2>&1 && ok "after reverting it, the submit goes through" || bad "the submit after the revert was refused"
  admin audit | grep -q "task.scope_refused" && ok "the refusal is in the audit log" || bad "no task.scope_refused in the audit log"

  finish
  [ "$fails" = 0 ] && { echo "RUN $n: PASS"; return 0; } || { echo "RUN $n: FAIL ($fails)"; return 1; }
}

pass=0
for n in $(seq 1 "$RUNS"); do one_run "$n" && pass=$((pass+1)); done
printf '\nmilestone D check: %s of %s runs passed\n' "$pass" "$RUNS"
[ "$pass" = "$RUNS" ]
