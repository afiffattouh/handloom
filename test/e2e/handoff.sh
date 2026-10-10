#!/usr/bin/env bash
# A task changes hands across machines using only the handoff note, git and the evidence.
# Agent A (scripted, on machine A) does half of a task, commits it to a branch, leaves a handoff note and goes silent;
# the lease runs out; agent B (a real Claude Code, on machine B, another link and another folder) is assigned the task
# and has to finish it from what A left. Needs: go, git, claude (logged in), python3, curl. Costs a few cents.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/handoff"; PORT="${PORT:-17840}"; H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
fails=0; ok() { echo "   ok: $*"; }; bad() { echo "   FAIL: $*"; fails=$((fails+1)); }; say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT"/{homeA,homeB,data,bin}
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export PATH="$OUT/bin:$PATH"

say "a project with a shared remote, and a hub with a short lease"
REMOTE="$OUT/remote.git"; git init -q --bare -b main "$REMOTE"
SEED="$OUT/seed"; git init -q -b main "$SEED" && cd "$SEED" && git config user.email t@t && git config user.name t
cat > stats.py <<'PY'
def mean(xs):
    raise NotImplementedError

def median(xs):
    raise NotImplementedError
PY
cat > check.sh <<'SH'
#!/bin/sh
python3 -m unittest discover -s . -p 'test_*.py' 2>&1 | tail -3
python3 -m unittest discover -s . -p 'test_*.py' >/dev/null 2>&1
SH
chmod +x check.sh; git add -A && git commit -q -m start && git remote add origin "$REMOTE" && git push -q origin main
cd "$ROOT"
HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --lease 15s --sweep 2s > "$OUT/hub.log" 2>&1 & HUB=$!
trap 'kill $HUB $LA $LB 2>/dev/null; tmux -L hltest-hoA kill-server 2>/dev/null; tmux -L hltest-hoB kill-server 2>/dev/null' EXIT
for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
tok() { python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])'; }
HT="$(admin human add tester --json | tok)"; human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
for m in A B; do
  JT="$(admin device add "machine-$m" --json | tok)"
  HANDLOOM_HOME="$OUT/home$m" $CLEAN "$BIN" link join "$H" "$JT" >/dev/null
  HANDLOOM_HOME="$OUT/home$m" $CLEAN "$BIN" link run --tmux-socket "hltest-ho$m" --tick 2s --live-every 3s > "$OUT/link$m.log" 2>&1 &
  eval "L$m=$!"
done
for i in $(seq 1 40); do HANDLOOM_HOME="$OUT/homeA" $CLEAN "$BIN" link status >/dev/null 2>&1 && HANDLOOM_HOME="$OUT/homeB" $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.25; done
as() { local m="$1" a="$2"; shift 2; $CLEAN env HANDLOOM_HOME="$OUT/home$m" HANDLOOM_AGENT="$a" "$BIN" "$@"; }
as A agent-a register agent-a --kind shell >/dev/null; as B agent-b register agent-b --kind shell >/dev/null
as A lead register lead --kind shell >/dev/null; human agent role lead lead >/dev/null
human job new "Statistics module" --lead lead --body "stats.py needs mean() and median(), each with unit tests in test_stats.py. ./check.sh must pass." >/dev/null
human task create "Implement mean() and median() with tests" --job 1 --assign agent-a --body "Work on branch work/stats of the shared remote (origin). Both functions, with tests. ./check.sh must pass." >/dev/null

say "machine A: agent A does half, leaves a handoff note, and goes silent"
A_DIR="$OUT/work-a"; git clone -q "$REMOTE" "$A_DIR" && cd "$A_DIR" && git config user.email a@a && git config user.name agent-a && git checkout -q -b work/stats
as A agent-a task claim 2 >/dev/null
python3 - <<'PY'
import re
s=open('stats.py').read()
s=s.replace("def mean(xs):\n    raise NotImplementedError","def mean(xs):\n    return sum(xs) / len(xs)")
open('stats.py','w').write(s)
PY
cat > test_stats.py <<'PY'
import unittest
import stats


class StatsTest(unittest.TestCase):
    def test_mean(self):
        self.assertEqual(stats.mean([1, 2, 3, 4]), 2.5)

    def test_mean_single(self):
        self.assertEqual(stats.mean([7]), 7)
PY
./check.sh >/dev/null && git add -A && git commit -q -m "mean() with tests" && git push -q origin work/stats || { echo "agent A could not do its half: the test setup is broken"; exit 1; }
as A agent-a task handoff 2 --done "mean() and its tests are committed and pushed on branch work/stats of origin; median() is not started and has no tests" --tried "nothing failed" --next "check out origin/work/stats, implement median() (for an even number of values, the average of the two middle values; empty list raises ValueError), add tests to test_stats.py, run ./check.sh, push the branch, then submit" --verify "git log origin/work/stats shows 'mean() with tests'; ./check.sh passes" >/dev/null && ok "A left a handoff note"
cd "$ROOT"
sleep 1
say "A is gone; the lease runs out; the task is open again; agent B is assigned it"
for i in $(seq 1 30); do [ "$(human task show 2 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')" = open ] && break; sleep 1; done
[ "$(human task show 2 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')" = open ] && ok "the task came back" || bad "the task did not come back"
as A lead inbox 2>/dev/null | grep -q "done: mean() and its tests\|mean() and its tests are committed" && ok "the lead's message carried A's note" || { as A lead inbox --all 2>/dev/null | grep -q "mean() and its tests" && ok "the lead's message carried A's note" || bad "the lead was not shown the note"; }
human task assign 2 agent-b >/dev/null

say "machine B: a real Claude Code finishes it from the note, in a fresh clone"
B_DIR="$OUT/work-b"; git clone -q "$REMOTE" "$B_DIR" && cd "$B_DIR" && git config user.email b@b && git config user.name agent-b && mkdir -p .handloom
PROMPT='You are agent-b, a worker, on a team coordinated with the handloom command (already on your PATH and configured). You work alone in this folder, a git clone of the project; nobody can answer questions, so never stop to ask. Task #2 is assigned to you. Do these in order: 1) run `handloom inbox`; 2) run `handloom task claim 2` and read everything it prints; 3) do exactly what the task and any handoff note say, using git to see what already exists; do not redo work that is already done; 4) run ./check.sh until it passes; 5) push your work to the same branch of origin; 6) submit with `handloom task submit 2 --evidence "commit:<sha>" --evidence "test:./check.sh -> ok" --note "what you did"`. Then stop.'
( cd "$B_DIR" && CLAUDECODE= env -u TMUX -u TMUX_PANE HANDLOOM_HOME="$OUT/homeB" HANDLOOM_AGENT=agent-b timeout 600 claude -p --model haiku --allowedTools "Bash" "Read" "Edit" "Write" --permission-mode acceptEdits "$PROMPT" > "$OUT/agent-b.txt" 2>&1 )
tail -8 "$OUT/agent-b.txt" | sed 's/^/    /'
cd "$ROOT"

say "what B did"
[ "$(human task show 2 --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')" = submitted ] && ok "B submitted the task" || bad "the task is not submitted"
VER="$OUT/verify"; rm -rf "$VER"; git clone -q "$REMOTE" "$VER" && cd "$VER" && git checkout -q work/stats 2>/dev/null
./check.sh >/dev/null 2>&1 && ok "./check.sh passes on the pushed branch" || bad "./check.sh fails on the pushed branch"
grep -q "def median" stats.py && python3 -c "import stats; assert stats.median([1,3,2])==2 and stats.median([1,2,3,4])==2.5" 2>/dev/null && ok "median() is right (odd and even)" || bad "median() is missing or wrong"
python3 -c "import stats; stats.median([])" 2>/dev/null && bad "median([]) did not raise" || ok "median([]) raises"
git log --format=%s origin/work/stats | grep -q "mean() with tests" && ok "A's commit is still in the history (nothing was redone from scratch)" || bad "A's commit is gone"
[ "$(git log --format=%s origin/work/stats | wc -l)" -ge 3 ] && ok "B's work sits on top of A's" || bad "no commit from B"
[ "$(grep -c "def test_mean" test_stats.py)" = 2 ] && ok "A's tests were kept, not rewritten" || bad "A's tests changed"
grep -qi "handoff" "$OUT/agent-b.txt" && ok "B mentions the handoff note" || echo "   (B did not mention the note in its summary)"
cd "$ROOT"; human task show 2 | sed 's/^/    /' | head -14
[ "$fails" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL ($fails)"; exit 1; }
