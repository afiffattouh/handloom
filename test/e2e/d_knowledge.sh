#!/usr/bin/env bash
# Milestone F: client knowledge. The invoice prefix exists only in the knowledge repository;
# a note must come back as a proposal on a branch, and the confidential folder must not reach a cloud agent.
# (Derived from the vague-brief script.) Milestone D with a VAGUE brief (the lead decides the plan), real Claude agents.
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
BASEPORT="${PORT:-17530}"
SOCK=hltest-dk
T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"

one_run() {
  local n="$1" OUT="$ROOT/test/e2e/out/dk-$1" PORT=$((BASEPORT + $1)) fails=0
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
  local REPO="$OUT/repo" KNOW="$OUT/acme-knowledge"; mkdir -p "$REPO/src" "$KNOW/clients" "$KNOW/confidential"
  git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
  printf '#!/bin/sh\necho "INV-0001"\n' > "$REPO/src/invoice.sh"; chmod +x "$REPO/src/invoice.sh"
  printf '# invoice\n' > "$REPO/README.md"
  cat > "$REPO/check.sh" <<'EOF2'
#!/bin/sh
[ "$(./src/invoice.sh)" = "ACM-0001" ] || { echo "invoice.sh must print the client's prefix, then -0001: got $(./src/invoice.sh)"; exit 1; }
echo "check passed"
EOF2
  chmod +x "$REPO/check.sh"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"
  git -C "$KNOW" init -q && git -C "$KNOW" config user.email t@t && git -C "$KNOW" config user.name t
  printf '# Acme Ltd\n\n- Invoice prefix: ACM (their accounting system rejects anything else).\n- Contact for billing questions: Dana.\n' > "$KNOW/clients/acme.md"
  printf 'SECRET-TERMS-7731: payment net 90, penalty 4 percent\n' > "$KNOW/confidential/contract.md"
  git -C "$KNOW" add -A && git -C "$KNOW" commit -q -m "acme"
  local KNOW_HEAD; KNOW_HEAD="$(git -C "$KNOW" rev-parse HEAD)"

  HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s > "$OUT/hub.log" 2>&1 & local HUB_PID=$!
  for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
  local ADMIN; ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
  admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
  local JT HT
  JT="$(admin device add hltest-dk-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
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
  admin profile new "$OUT/prof/boss" >/dev/null && admin profile new "$OUT/prof/coder" >/dev/null || { bad "profiles"; finish; return 1; }

  say "one command: job new"
  local BRIEF="Make src/invoice.sh print the client's invoice number: the client's invoice prefix, a dash, then 0001 (for example XXX-0001). The prefix is a client fact: find it in the client knowledge, do not guess. ./check.sh must pass. Also record, as a short note in the client knowledge, which file now builds invoice numbers and that the prefix comes from the client's own rule."
  human job new "Add hello and notes files" --repo "$REPO" --verify ./check.sh --device hltest-dk-dev --lead-profile boss --lead-name boss-1 --knowledge "$KNOW" --body "$BRIEF" | tee "$OUT/jobnew.txt"
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
  local TASKS NT; TASKS="$(human task list --job 1 --json)"; NT="$(echo "$TASKS" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')"
  echo "PLAN run $n: tasks=$NT"
  echo "$TASKS" | python3 -c '
import json,sys
t=json.load(sys.stdin)
assert t and all(x["status"]=="done" for x in t), "not every task is done"
assert all(x.get("check") and x["check"]["exit_code"]==0 for x in t), "a task was accepted without a passing device check"
print("   ok: every task done with a passing device check")' || bad "tasks: $TASKS"
  local k; for k in $(seq 1 40); do [ "$(human task list --job 1 --json | python3 -c 'import json,sys;t=json.load(sys.stdin);print(sum(1 for x in t if (x.get("merge") or {}).get("status")=="merged"))')" = "$NT" ] && break; sleep 1; done
  local FINAL="$OUT/home/work/job-1/_integration"
  if (cd "$FINAL" && [ "$(./src/invoice.sh)" = "ACM-0001" ]); then ok "the integration branch prints ACM-0001: the prefix came from the client knowledge"; else bad "invoice.sh does not print ACM-0001 on the integration branch"; fi
  # Ask the device to gather the notes: closing the job queues it. Wait for the link.
  human job close 1 >/dev/null 2>&1 || human job close 1 --cancel >/dev/null 2>&1
  for k in $(seq 1 40); do git -C "$KNOW" rev-parse --verify -q job/1 >/dev/null && break; sleep 1; done
  [ "$(git -C "$KNOW" rev-parse HEAD)" = "$KNOW_HEAD" ] && ok "the knowledge repository's own branch is untouched" || bad "something was committed to the knowledge repository's own branch"
  local DIFF; DIFF="$(git -C "$KNOW" diff --name-only "$KNOW_HEAD" job/1 2>/dev/null)"
  echo "    proposed on job/1: ${DIFF:-nothing}"
  [ -n "$DIFF" ] && ok "a note was proposed on the branch job/1 of the knowledge repository" || bad "no proposed note on job/1"
  human job show 1 --json | python3 -c 'import json,sys; j=json.load(sys.stdin)["job"]; assert j["notes"]>=1 and j["knowledge"], j; print("   ok: the hub knows only a count (%d) and the path" % j["notes"])' || bad "the hub's view of the notes"
  if grep -rq "SECRET-TERMS-7731" "$OUT/home/work/job-1/boss-1" "$OUT/home/work/job-1/"coder-* 2>/dev/null; then bad "the confidential contract reached a cloud agent's directory"; else ok "the confidential folder never reached the agents' directories"; fi
  if git -C "$KNOW" log -p "$KNOW_HEAD"..job/1 2>/dev/null | grep -q "SECRET-TERMS-7731"; then bad "a proposal contains the confidential text"; else ok "no proposal contains the confidential text"; fi
  if (cd "$FINAL" && grep -rq "SECRET-TERMS-7731" . 2>/dev/null); then bad "the confidential text reached the code"; else ok "the confidential text is not in the code"; fi
  admin audit > "$OUT/audit.txt"
  JOBSEQ="${JOBSEQ:-0}"
  tail -n +$((JOBSEQ+1)) "$OUT/audit.txt" | awk '{print $3}' | grep -E '^(human:|admin)' | grep -v "human:tester" | sort | uniq -c > "$OUT/human-actions.txt"
  if [ -s "$OUT/human-actions.txt" ]; then bad "unexpected human or admin actions: $(cat "$OUT/human-actions.txt")"; else ok "no human action except closing the job"; fi

  finish
  [ "$fails" = 0 ] && { echo "RUN $n: PASS"; return 0; } || { echo "RUN $n: FAIL ($fails)"; return 1; }
}

pass=0
for n in $(seq 1 "$RUNS"); do one_run "$n" && pass=$((pass+1)); done
printf '\nmilestone D check: %s of %s runs passed\n' "$pass" "$RUNS"
[ "$pass" = "$RUNS" ]
