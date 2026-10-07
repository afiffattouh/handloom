#!/usr/bin/env bash
# A LEAD THAT IS OMP ON THE LOCAL MODEL (lead-engineering starter, adapted), a Claude worker (coder starter),
# and the multi-part vague brief. Only one local-model agent exists at a time. Counts how often the lead plans well.
# (Derived from the multi-part script.) Milestone D with a VAGUE, MULTI-PART brief (two features in one file, so merges can conflict) (the lead decides the plan), real Claude agents.
# Counts how often the lead plans and finishes a small job well: the kill
# criterion for the lead-orchestrated design is 3 mis-plans in 5 runs.
# (Original header of d.sh follows.)
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
BASEPORT="${PORT:-17610}"
SOCK=hltest-dl
T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"

one_run() {
  local n="$1" OUT="$ROOT/test/e2e/out/dl-$1" PORT=$((BASEPORT + $1)) fails=0
  local H="http://127.0.0.1:$PORT"
  say()  { printf '\n== [run %s] %s\n' "$n" "$*"; }
  ok()   { printf '   ok: %s\n' "$*"; }
  bad()  { printf '   FAIL: %s\n' "$*"; fails=$((fails+1)); }
  rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/prof/boss" "$OUT/prof/coder"
  trap '' RETURN
  local BIN="$OUT/bin/handloom"
  (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || { bad "build"; return 1; }
  export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"

  # The toy repository: a greeting script, a README, a check.
  local REPO="$OUT/repo"; mkdir -p "$REPO/src"
  git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
  printf '# greet\n\nA tiny greeting script.\n' > "$REPO/README.md"
  printf '#!/bin/sh\necho "hello, world"\n' > "$REPO/src/greet.sh"; chmod +x "$REPO/src/greet.sh"
  cat > "$REPO/check.sh" <<'EOF2'
#!/bin/sh
[ "$(./src/greet.sh)" = "hello, world" ] || { echo "greet.sh with no arguments must print: hello, world"; exit 1; }
if grep -q -- "--shout" src/greet.sh; then
  [ "$(./src/greet.sh --shout)" = "HELLO, WORLD" ] || { echo "greet.sh --shout must print: HELLO, WORLD"; exit 1; }
fi
if grep -q -- "--times" src/greet.sh; then
  [ "$(./src/greet.sh --times 2 | wc -l)" = 2 ] || { echo "greet.sh --times 2 must print two lines"; exit 1; }
fi
echo "check passed"
EOF2
  chmod +x "$REPO/check.sh"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"

  HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s > "$OUT/hub.log" 2>&1 & local HUB_PID=$!
  for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
  local ADMIN; ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
  admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
  local JT HT
  JT="$(admin device add hltest-dl-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
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

  say "profiles: an OMP lead on the local model, a Claude worker"
  admin profile add lead-engineering --kind omp --runtime local --model gb10/qwen3.8-27b --name boss --adapt | sed 's/^/    /'
  admin profile add coder --kind claude --runtime cloud | sed 's/^/    /'

  say "one command: job new"
  local BRIEF="Give src/greet.sh two options: --shout prints the greeting in capitals, and --times N prints it N times (one per line). They must work together. Document both in README.md. ./check.sh must pass, and you must keep the options from breaking each other."
  human job new "Add hello and notes files" --repo "$REPO" --verify ./check.sh --device hltest-dl-dev --lead-profile boss --lead-name boss-1 --body "$BRIEF" | tee "$OUT/jobnew.txt"
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
  local TASKS WORKERS NT
  TASKS="$(human task list --job 1 --json)"; WORKERS="$(human spawns --json | python3 -c 'import json,sys;print(sum(1 for x in json.load(sys.stdin) if x["role"]=="worker"))')"
  NT="$(echo "$TASKS" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')"
  echo "PLAN run $n: tasks=$NT workers=$WORKERS"
  echo "$TASKS" | python3 -c '
import json,sys
t=json.load(sys.stdin)
assert t and all(x["status"]=="done" for x in t), "not every task is done"
assert all(x.get("check") and x["check"]["exit_code"]==0 for x in t), "a task was accepted without a passing device check"
print("   ok: every task done with a passing device check")' || bad "tasks: $TASKS"
  [ "$WORKERS" -ge 1 ] && ok "the lead started $WORKERS worker(s)" || bad "the lead did the work itself (no worker)"
  local k; for k in $(seq 1 40); do [ "$(human task list --job 1 --json | python3 -c 'import json,sys;t=json.load(sys.stdin);print(sum(1 for x in t if (x.get("merge") or {}).get("status")=="merged"))')" = "$NT" ] && break; sleep 1; done
  local FINAL="$OUT/home/work/job-1/_integration"  # the device's integration worktree
  if (cd "$FINAL" && ./check.sh >/dev/null 2>&1 && [ "$(./src/greet.sh --shout --times 2)" = "$(printf 'HELLO, WORLD\nHELLO, WORLD')" ] && grep -q -- "--shout" README.md && grep -q -- "--times" README.md); then ok "job/1/integration passes check.sh, supports --shout --times together and documents both"; else bad "job/1/integration is not right: $(cd "$FINAL" 2>/dev/null && ./check.sh 2>&1 | tail -2)"; fi
  admin audit > "$OUT/audit.txt"
  JOBSEQ="${JOBSEQ:-0}"
  tail -n +$((JOBSEQ+1)) "$OUT/audit.txt" | awk '{print $3}' | grep -E '^(human:|admin)' | sort | uniq -c > "$OUT/human-actions.txt"
  if [ -s "$OUT/human-actions.txt" ]; then bad "human or admin actions after job new: $(cat "$OUT/human-actions.txt")"; else ok "no human or admin action after job new"; fi

  finish
  [ "$fails" = 0 ] && { echo "RUN $n: PASS"; return 0; } || { echo "RUN $n: FAIL ($fails)"; return 1; }
}

pass=0
for n in $(seq 1 "$RUNS"); do one_run "$n" && pass=$((pass+1)); done
printf '\nmilestone D check: %s of %s runs passed\n' "$pass" "$RUNS"
[ "$pass" = "$RUNS" ]
