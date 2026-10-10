#!/usr/bin/env bash
# A finished job teaches (issue #18), with real Claude agents. A small job runs; its lead is told, in the brief, to look back and
# propose ONE lesson for the coder profile's skill small-commits (the brief names the mechanism: a natural lesson cannot be forced
# in a toy job, so this proves the loop, not the lead's judgement). Then a person accepts it, the profile gets a new version, and
# a worker started afterwards is pinned to that version, which has the lesson in its skill.
# Needs: go, tmux, git, claude (logged in), python3, curl. Costs a few cents.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
RUNS="${RUNS:-1}"
TIMEOUT="${TIMEOUT:-600}"
BASEPORT="${PORT:-17910}"
SOCK=hltest-ls
T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"

one_run() {
  local n="$1" OUT="$ROOT/test/e2e/out/ls-$1" PORT=$((BASEPORT + $1)) fails=0
  local H="http://127.0.0.1:$PORT"
  say()  { printf '\n== [run %s] %s\n' "$n" "$*"; }
  ok()   { printf '   ok: %s\n' "$*"; }
  bad()  { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }
  rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/prof/boss" "$OUT/prof/coder"
  trap '' RETURN
  local BIN="$OUT/bin/handloom"
  (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || { bad "build"; return 1; }
  export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"

  # The toy repository, and the client's knowledge repository next to it.
  local REPO="$OUT/repo"; mkdir -p "$REPO/src"
  git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
  printf '#!/bin/sh\necho "TODO"\n' > "$REPO/src/credit.sh"; chmod +x "$REPO/src/credit.sh"
  printf '# credit notes\n' > "$REPO/README.md"
  printf '#!/bin/sh\n[ "$(./src/credit.sh)" = "ACM-CN-0001" ] || { echo "credit.sh must print ACM-CN-0001"; exit 1; }\necho "check passed"\n' > "$REPO/check.sh"
  chmod +x "$REPO/check.sh"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"

  HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s > "$OUT/hub.log" 2>&1 & local HUB_PID=$!
  for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
  local ADMIN; ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
  admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
  local JT HT
  JT="$(admin device add hltest-ls-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
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


  say "two profiles: a lead that plans and reads, a coder that may change only src/"
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
description: Makes small changes in src/ and commits them
kind: claude
tools:
  allow: [read, edit, shell]
  deny_commands: [rm]
write: ["src/**"]
EOF2
  cat > "$OUT/prof/coder/PROMPT.md" <<'EOF2'
You work in your own git worktree on your own branch. Change only files under src/.
When a task is done: run `git add -A && git commit -q -m "<what you did>"`, then submit the task with evidence
`commit:<output of git rev-parse --short HEAD>` using `handloom task submit <id> --evidence "commit:<sha>"`.
Then end your turn; handloom wakes you when there is more.
EOF2
  mkdir -p "$OUT/prof/coder/skills/small-commits"; printf -- "---\nname: small-commits\ndescription: commit small and often\n---\nCommit each logical change on its own.\n" > "$OUT/prof/coder/skills/small-commits/SKILL.md"
  admin profile new "$OUT/prof/boss" >/dev/null && admin profile new "$OUT/prof/coder" >/dev/null || { bad "profiles"; finish; return 1; }

  say "one command: job new"
  local BRIEF="Make src/credit.sh print ACM-CN-0001. ./check.sh must pass. When every task is accepted, look back and, before you report the job done, propose exactly one lesson with: handloom lesson propose --profile coder --skill small-commits --why \"<what happened in this job>\" \"<one or two sentences that would help the next worker>\". It must not name the client."
  human job new "Credit note numbers" --repo "$REPO" --verify ./check.sh --device hltest-ls-dev --lead-profile boss --lead-name boss-1 --body "$BRIEF" | tee "$OUT/jobnew.txt"
  JOB="$(grep -o "Started job #[0-9]*" "$OUT/jobnew.txt" | grep -o "[0-9]*$")"; [ -n "$JOB" ] || { bad "job new"; finish; return 1; }
  local JOBSEQ; JOBSEQ="$(admin audit | grep -c .)"

  say "wait: the lead plans, the worker works, the device checks, the lead accepts (up to ${TIMEOUT}s)"
  local done_at=0 i
  for i in $(seq 1 "$TIMEOUT"); do
    if human digest --json 2>/dev/null | JOB="$JOB" python3 -c 'import json,os,sys; d=json.load(sys.stdin); sys.exit(0 if any(i["kind"]=="job-done" and str(i.get("id"))==os.environ["JOB"] for i in d["to_review"]) else 1)'; then done_at=$i; break; fi
    sleep 1
  done
  if [ "$done_at" = 0 ]; then
    bad "the job did not reach 'every task done' in ${TIMEOUT}s"
    human job show "$JOB" | sed 's/^/    /'; human spawns | sed 's/^/    /'
    echo "--- lead pane:"; $T capture-pane -p -t handloom:boss-1 2>&1 | grep -v '^$' | tail -25
    echo "--- worker pane:"; $T capture-pane -p -t handloom:coder-a 2>&1 | grep -v '^$' | tail -25
    finish; return 1
  fi
  ok "the job reached 'every task done' after ${done_at}s with no human input"
  human job show "$JOB" | sed 's/^/    /'

  say "what happened"
  local TASKS NT; TASKS="$(human task list --job "$JOB" --json)"; NT="$(echo "$TASKS" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')"
  echo "PLAN run $n: tasks=$NT"
  echo "$TASKS" | python3 -c '
import json,sys
t=json.load(sys.stdin)
assert t and all(x["status"]=="done" for x in t), "not every task is done"
assert all(x.get("check") and x["check"]["exit_code"]==0 for x in t), "a task was accepted without a passing device check"
print("   ok: every task done with a passing device check")' || bad "tasks: $TASKS"
  local k; for k in $(seq 1 40); do [ "$(human task list --job "$JOB" --json | python3 -c 'import json,sys;t=json.load(sys.stdin);print(sum(1 for x in t if (x.get("merge") or {}).get("status")=="merged"))')" = "$NT" ] && break; sleep 1; done
  local FINAL="$OUT/home/work/job-$JOB/_integration"
  say "the lead looked back and proposed a lesson (nothing has changed yet)"
  local L=0; for k in $(seq 1 180); do L="$(human lesson list --status proposed --json 2>/dev/null | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"; [ "$L" -ge 1 ] && break; sleep 1; done
  [ "$L" -ge 1 ] && ok "$L lesson proposed" || bad "the lead proposed no lesson"
  human lesson list | sed 's/^/    /'
  [ "$(curl -s -H "Authorization: Bearer $ADMIN" "$H/v1/profiles/coder" | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])' 2>/dev/null)" = 1 ] && ok "the profile is still version 1" || bad "the profile changed before anyone accepted"
  say "a person (the owner) accepts it: a new version of the profile"
  python3 - "$(ls "$OUT"/data/*.db | head -1)" <<'PY'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1]); db.execute("update human set role='owner' where name='tester'"); db.commit()
PY
  human lesson accept 1 | tee "$OUT/accept.txt"
  grep -q "version 2" "$OUT/accept.txt" && ok "the profile is now version 2" || bad "accepting did not make version 2"
  curl -s -H "Authorization: Bearer $ADMIN" "$H/v1/profiles/coder" | python3 -c 'import json,sys;d=json.load(sys.stdin);t="".join(f["content"] for f in d["spec"]["skills"]);sys.exit(0 if "Lessons learned" in t else 1)' && ok "the skill text has the lesson" || bad "the skill text lacks the lesson"
  say "a worker started afterwards is pinned to the new version"
  human spawn coder-late --profile coder --device hltest-ls-dev --job "$JOB" >/dev/null 2>&1 || admin spawns >/dev/null
  human spawns --json 2>/dev/null | python3 -c 'import json,sys;s=[x for x in json.load(sys.stdin) if x["name"]=="coder-late"];print("   ",[x.get("profile") for x in s]);sys.exit(0 if s and s[0]["profile"]=="coder@2" else 1)' && ok "coder-late runs coder@2" || bad "the later worker is not on coder@2"
  human lesson list | sed 's/^/    /'
  human lesson list --json | python3 -c 'import json,sys;l=json.load(sys.stdin);sys.exit(0 if l[0]["used"]>=1 else 1)' && ok "the lesson shows it was used by a later agent" || bad "used is 0"
  admin audit > "$OUT/audit.txt"
  JOBSEQ="${JOBSEQ:-0}"
  tail -n +$((JOBSEQ+1)) "$OUT/audit.txt" | awk '{print $3}' | grep -E '^(human:|admin)' | grep -v "human:tester" | sort | uniq -c > "$OUT/human-actions.txt"
  true

  finish
  [ "$fails" = 0 ] && { echo "RUN $n: PASS"; return 0; } || { echo "RUN $n: FAIL ($fails)"; return 1; }
}

pass=0
for n in $(seq 1 "$RUNS"); do one_run "$n" && pass=$((pass+1)); done
printf '\nmilestone D check: %s of %s runs passed\n' "$pass" "$RUNS"
[ "$pass" = "$RUNS" ]
