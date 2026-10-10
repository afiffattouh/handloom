#!/usr/bin/env bash
# Reusing what the team already knows (issue #17), with real Claude agents. Acme's invoice prefix is only in the client's
# notes; the document number format and the credit-note code were only ever said in an EARLIER job (an answered question
# and a handoff note). A second job asks for credit-note numbers without saying any of that: the agents have to search.
# Checks: the check passes (it cannot without those facts), the agents ran `handloom search`, they cited what they used,
# and no note text reached the hub's database.
# Needs: go, tmux, git, claude (logged in), python3, curl. Costs a few cents.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
RUNS="${RUNS:-1}"
TIMEOUT="${TIMEOUT:-600}"
BASEPORT="${PORT:-17870}"
SOCK=hltest-ru
T="tmux -L $SOCK"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"

one_run() {
  local n="$1" OUT="$ROOT/test/e2e/out/ru-$1" PORT=$((BASEPORT + $1)) fails=0
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
  local REPO="$OUT/repo" KNOW="$OUT/acme-knowledge"; mkdir -p "$REPO/src" "$KNOW/clients"
  git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
  printf '#!/bin/sh\necho "TODO"\n' > "$REPO/src/credit.sh"; chmod +x "$REPO/src/credit.sh"
  printf '# credit notes\n' > "$REPO/README.md"
  cat > "$REPO/check.sh" <<'EOF2'
#!/bin/sh
want="ACM-CN-$(date +%Y%m)-0001"
[ "$(./src/credit.sh)" = "$want" ] || { echo "credit.sh must print the number of the next Acme credit note, following the team's conventions for Acme documents: got $(./src/credit.sh)"; exit 1; }
echo "check passed"
EOF2
  chmod +x "$REPO/check.sh"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "toy"
  git -C "$KNOW" init -q && git -C "$KNOW" config user.email t@t && git -C "$KNOW" config user.name t
  printf '# Acme Ltd\n\n- Document number prefix: ACM (their accounting system rejects anything else). Reference KNOW-8841.\n- Contact for billing questions: Dana.\n' > "$KNOW/clients/acme.md"
  git -C "$KNOW" add -A && git -C "$KNOW" commit -q -m "acme"
  local KNOW_HEAD; KNOW_HEAD="$(git -C "$KNOW" rev-parse HEAD)"

  HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s > "$OUT/hub.log" 2>&1 & local HUB_PID=$!
  for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
  local ADMIN; ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
  admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
  local JT HT
  JT="$(admin device add hltest-ru-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
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

  say "an earlier job for the same client, recorded without any model: an answered question and a handoff note"
  local ag; ag() { local a="$1"; shift; $CLEAN env HANDLOOM_AGENT="$a" "$BIN" "$@"; }
  ag lead0 register lead0 --kind shell >/dev/null; ag w0 register w0 --kind shell >/dev/null
  human agent role lead0 lead >/dev/null
  human job new "Invoice numbers for Acme" --lead lead0 --knowledge "$KNOW" --repo "$REPO" --device hltest-ru-dev --body "Number Acme's invoices the way their accounting system wants." >/dev/null
  human task create "Number the invoices" --job 1 --assign w0 >/dev/null
  ag w0 task claim 2 >/dev/null
  ag lead0 ask "Which period format do Acme document numbers use?" --option YYYYMM --option YYMMDD --option none >/dev/null
  human answer 1 YYYYMM >/dev/null
  ag w0 task handoff 2 --done "Acme documents are numbered PREFIX-KIND-PERIOD-SEQ, for example ACM-INV-202509-0007. Kind codes: INV for invoices, CN for credit notes. The period is the current year and month." --next "none: this is the convention" >/dev/null
  ag w0 task submit 2 --evidence "file:src/invoice.sh" --note "Invoices are numbered ACM-INV-<YYYYMM>-<SEQ>. Credit notes use the kind code CN instead of INV." >/dev/null
  human task accept 2 >/dev/null
  human job close 1 >/dev/null 2>&1
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
  local BRIEF="Make src/credit.sh print the number of the next Acme credit note, with sequence 0001, following the conventions the team already uses for Acme documents. Do not guess any convention: the team has worked out how Acme documents are numbered before, so look it up first. ./check.sh must pass."
  human job new "Credit note numbers for Acme" --repo "$REPO" --verify ./check.sh --device hltest-ru-dev --lead-profile boss --lead-name boss-1 --knowledge "$KNOW" --body "$BRIEF" | tee "$OUT/jobnew.txt"
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
  if (cd "$FINAL" && [ "$(./src/credit.sh)" = "ACM-CN-$(date +%Y%m)-0001" ]); then ok "credit.sh prints ACM-CN-$(date +%Y%m)-0001: the prefix came from the notes, the kind code and period from the earlier job"; else bad "credit.sh is wrong on the integration branch: $(cd "$FINAL" && ./src/credit.sh)"; fi
  # did the agents search, and say what they used?
  local LOGS; LOGS="$(ls -d "$HOME"/.claude/projects/*"$(basename "$OUT")"* 2>/dev/null)"
  if [ -n "$LOGS" ] && grep -rqs "handloom search" $LOGS; then ok "an agent ran handloom search"; else bad "no agent ran handloom search"; fi
  echo "$TASKS" | python3 -c '
import json,sys
t=json.load(sys.stdin)
used=[e for x in t for e in x.get("evidence",[]) if e.startswith("used:")]
print("   evidence that cites what was used:", used or "none")
sys.exit(0 if used else 1)' && ok "the evidence says what was used" || echo "   (no used: evidence: the convention is advice, not a gate)"
  # the notes never went to the hub
  if python3 - "$OUT/data" <<'PY'
import glob, sqlite3, sys
db = sqlite3.connect("file:" + glob.glob(sys.argv[1] + "/*.db")[0] + "?mode=ro", uri=True)
hit = 0
for (t,) in db.execute("select name from sqlite_master where type='table'").fetchall():
    cols = [r[1] for r in db.execute(f"pragma table_info({t})") if r[2].upper().startswith(("TEXT", "CHAR"))]
    for c in cols:
        hit += db.execute(f"select count(*) from {t} where {c} like '%KNOW-8841%' or {c} like '%Dana%'").fetchone()[0]
print(hit)
sys.exit(1 if hit else 0)
PY
  then ok "no note text is in the hub's database"; else bad "text from a note is in the hub's database"; fi
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
