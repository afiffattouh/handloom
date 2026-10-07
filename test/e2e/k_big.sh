#!/usr/bin/env bash
# A LARGER MULTI-PART JOB, measured. One brief (a small todo command-line app: storage, five commands, tags,
# export, tests, docs), a lead that must decompose it, and a black-box acceptance script the agents never see.
#
#   LEAD=claude test/e2e/k_big.sh      a Claude lead (lead-engineering starter) and Claude workers (coder starter)
#   LEAD=omp    test/e2e/k_big.sh      an OMP lead on the local model; workers are Claude (one local-model agent at a time)
#   LEAD=omp WORKER=omp ...            local lead and local worker (they take turns; the lead waits while the worker works)
#
# Prints one RESULT line: quality (acceptance passed of 15), speed (seconds from job new to every task done),
# and the shape of the plan and of the work (tasks, workers, sent back, failed checks, merge conflicts).
set -uo pipefail
LEAD="${LEAD:?set LEAD to claude or omp}"; WORKER="${WORKER:-claude}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
RUN="${RUN:-1}"; TIMEOUT="${TIMEOUT:-1500}"; MODEL="${MODEL:-gb10/qwen3.8-27b}"
PORT=$((${PORT:-17630} + RUN + $([ "$LEAD" = omp ] && echo 10 || echo 0))); SOCK="hltest-k-$LEAD-$WORKER"; T="tmux -L $SOCK"
OUT="$ROOT/test/e2e/out/k-$LEAD-$WORKER-$RUN"; H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"

REPO="$OUT/repo"; mkdir -p "$REPO"
git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
printf '# todo\n\nA small command-line todo app. Not built yet.\n' > "$REPO/README.md"
cat > "$REPO/check.sh" <<'EOF2'
#!/bin/sh
# Runs the unit tests in tests/ (if any) and checks that every Python file compiles.
if ls tests/test_*.py >/dev/null 2>&1; then
  python3 -m unittest discover -s tests -p 'test_*.py' || exit 1
fi
for f in *.py; do
  [ -e "$f" ] || continue
  python3 -m py_compile "$f" || exit 1
done
echo "check passed"
EOF2
chmod +x "$REPO/check.sh"; git -C "$REPO" add -A && git -C "$REPO" commit -q -m "start"

HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add k-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
cleanup() { $T kill-server 2>/dev/null; kill $HUB_PID $LINK_PID 2>/dev/null; python3 - "$OUT" <<'PY' 2>/dev/null
import json, os, sys
p = os.path.expanduser("~/.claude.json"); d = json.load(open(p))
for k in [k for k in d.get("projects", {}) if k.startswith(sys.argv[1])]: del d["projects"][k]
json.dump(d, open(p + ".tmp", "w")); os.replace(p + ".tmp", p)
PY
}
trap cleanup EXIT

say "profiles: lead = $LEAD, worker = $WORKER"
if [ "$LEAD" = omp ]; then admin profile add lead-engineering --kind omp --runtime local --model "$MODEL" --name boss --adapt | sed 's/^/    /'
else admin profile add lead-engineering --kind claude --runtime cloud --name boss | sed 's/^/    /'; fi
if [ "$WORKER" = omp ]; then admin profile add coder --kind omp --runtime local --model "$MODEL" --adapt | sed 's/^/    /'
else admin profile add coder --kind claude --runtime cloud | sed 's/^/    /'; fi

BRIEF='Build a small command-line todo app in Python in this repository, using only the standard library.

A task has an id, a text, a done flag and tags. The commands, all run as `python3 todo.py ...`:
- add TEXT [--tag T] ...   adds a task and prints only its new id (1, then 2, ...). --tag can be repeated.
- list [--tag T] [--all]   prints one line per task: `<id> [ ] <text>` followed by ` #tag` for each tag; a done task shows `[x]`. Done tasks are hidden unless --all. --tag shows only tasks with that tag.
- done ID                  marks a task done.
- remove ID                deletes a task.
- export --format csv|json csv: a header line `id,text,done,tags` then one row per task, done as true/false and tags joined with `;`. json: an array of objects with id, text, done, tags.
An unknown id for done or remove prints a message to stderr and exits with a non-zero status.

Tasks are stored as JSON in the file named by the TODO_FILE environment variable (default ./todo.json). Keep the storage in its own module, separate from the command-line module todo.py. Write unit tests for the storage and for the commands in tests/ (at least six), make ./check.sh pass, and document every command with an example, and TODO_FILE, in README.md.'
START=$(date +%s)
human job new "Build the todo command-line app" --repo "$REPO" --verify ./check.sh --device k-dev --lead-profile boss --lead-name boss-1 --body "$BRIEF" | tee "$OUT/jobnew.txt"
grep -q "Started job #1" "$OUT/jobnew.txt" || { echo "RESULT lead=$LEAD worker=$WORKER run=$RUN FAILED job new"; exit 1; }

say "wait for every task to be done (up to ${TIMEOUT}s)"
done_at=0
for i in $(seq 1 "$TIMEOUT"); do
  if human digest --json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if any(i["kind"]=="job-done" for i in d["to_review"]) else 1)'; then done_at=$(( $(date +%s) - START )); break; fi
  sleep 1
done
END=$(date +%s)
if [ "$done_at" = 0 ]; then echo "   the job did not finish in ${TIMEOUT}s"; human job show 1 | sed 's/^/    /'; fi
for i in $(seq 1 40); do
  n=$(human task list --job 1 --json 2>/dev/null | python3 -c 'import json,sys;t=json.load(sys.stdin);print(sum(1 for x in t if (x.get("merge") or {}).get("status")=="merged"), len(t))' 2>/dev/null)
  set -- $n; [ -n "${2:-}" ] && [ "$1" = "$2" ] && break; sleep 1
done

say "what the plan and the work looked like"
human job show 1 | sed 's/^/    /'
python3 - "$OUT" <<'PY'
import glob, json, sqlite3, sys
db = glob.glob(sys.argv[1] + "/data/*.db")[0]
c = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
q = lambda s: c.execute(s).fetchone()[0]
tasks = q("select count(*) from task where kind='task'")
workers = q("select count(*) from spawn where role='worker'")
rej = q("select count(*) from audit where action='task.reject'")
failed = q("select count(*) from audit where action='task.verified' and payload like '%\"exit_code\":%' and payload not like '%\"exit_code\":0%'")
checks = q("select count(*) from audit where action='task.verified'")
conf = q("select count(*) from merge where status in ('conflict','failed')")
msgs = q("select count(*) from message")
dep = q("select count(*) from task where kind='task' and depends_on != '[]'")
print(f"PLAN tasks={tasks} workers={workers} with_dependencies={dep} sent_back={rej} checks={checks} failed_checks={failed} merge_problems={conf} messages={msgs}")
open(sys.argv[1] + "/plan.txt", "w").write(f"{tasks} {workers} {dep} {rej} {checks} {failed} {conf} {msgs}")
PY
FINAL="$OUT/home/work/job-1/_integration"
say "acceptance (black box, on the merged integration branch)"
"$ROOT/test/e2e/acceptance/todo_acceptance.sh" "$FINAL" | tee "$OUT/accept.txt"
SCORE="$(grep '^ACCEPT' "$OUT/accept.txt" | awk '{print $2}')"
(cd "$FINAL" && ./check.sh >/dev/null 2>&1 && echo "   check.sh passes on the merged branch" || echo "   check.sh FAILS on the merged branch"; echo "   files: $(ls | tr '\n' ' ')"; echo "   tests: $(grep -rh 'def test_' tests 2>/dev/null | wc -l)")
admin audit > "$OUT/audit.txt"
HUMAN=$(grep -E '^\s*[0-9]+\s+\S+\s+(human:|admin)' "$OUT/audit.txt" | grep -vc "human:tester" || true)
read -r TASKS WORKERS DEPS REJ CHECKS FAILED CONF MSGS < "$OUT/plan.txt"
printf '\nRESULT lead=%s worker=%s run=%s accept=%s seconds=%s tasks=%s workers=%s deps=%s sent_back=%s failed_checks=%s/%s merge_problems=%s\n' "$LEAD" "$WORKER" "$RUN" "${SCORE:-0/15}" "$done_at" "$TASKS" "$WORKERS" "$DEPS" "$REJ" "$FAILED" "$CHECKS" "$CONF" | tee "$OUT/result.txt"
