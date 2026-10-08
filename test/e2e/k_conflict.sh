#!/usr/bin/env bash
# A FORCED MERGE CONFLICT, with real agents. Two workers, in parallel, change the same lines of greet.py in incompatible ways
# (on purpose: the brief tells the lead to run both at once). The hub must report the merge problem, the lead must
# resolve it, and the merged branch must end up consistent and passing. Prints one RESULT line.
#   LEAD=claude test/e2e/k_conflict.sh
set -uo pipefail
LEAD="${LEAD:?set LEAD to claude or omp}"; WORKER="${WORKER:-claude}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
RUN="${RUN:-1}"; TIMEOUT="${TIMEOUT:-1500}"; MODEL="${MODEL:-gb10/qwen3.8-27b}"
PORT=$((${PORT:-17700} + RUN + $([ "$LEAD" = omp ] && echo 10 || echo 0))); SOCK="hltest-c-$LEAD-$WORKER"; T="tmux -L $SOCK"
OUT="$ROOT/test/e2e/out/c-$LEAD-$WORKER-$RUN"; H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin"
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"

REPO="$OUT/repo"; mkdir -p "$REPO/tests"
git -C "$REPO" init -q && git -C "$REPO" config user.email t@t && git -C "$REPO" config user.name t
printf '# textool\n\nA small text tool.\n\n## Commands\n\n- `greet NAME`: prints a greeting. Example: `python3 tool.py greet World` prints `hello, World`.\n' > "$REPO/README.md"
cat > "$REPO/greet.py" <<'EOF2'
def run(args):
    print("hello, " + " ".join(args))
    return 0
EOF2
cat > "$REPO/tool.py" <<'EOF2'
import sys

from greet import run as greet_run

# command name -> function taking the arguments after the name
COMMANDS = {
    "greet": greet_run,
}


def main(argv):
    if not argv or argv[0] == "help":
        print("commands: " + " ".join(sorted(COMMANDS)))
        return 0
    cmd = COMMANDS.get(argv[0])
    if cmd is None:
        print("unknown command: " + argv[0], file=sys.stderr)
        return 2
    return cmd(argv[1:]) or 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
EOF2
cat > "$REPO/tests/test_greet.py" <<'EOF2'
import io, unittest
from contextlib import redirect_stdout
import greet


class GreetTest(unittest.TestCase):
    def test_greets(self):
        out = io.StringIO()
        with redirect_stdout(out):
            greet.run(["World"])
        self.assertEqual(out.getvalue().strip(), "hello, World")
EOF2
cat > "$REPO/check.sh" <<'EOF2'
#!/bin/sh
# Runs the unit tests in tests/ and checks that every Python file compiles.
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
(cd "$REPO" && ./check.sh >/dev/null) || { echo "the starting repository does not pass its own check"; exit 1; }

HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" --agent-lease 60s ${HUB_ARGS:-} > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add c-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
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

BRIEF='The greeting of this tool is being changed, and two people want different things. Do both as two separate tasks that run AT THE SAME TIME, with no dependency between them, each on its own branch:

- Task A: greet.py must print `hi, NAME` instead of `hello, NAME`. Update the README example and the unit test to match.
- Task B: greet.py must print `greetings, NAME` instead of `hello, NAME`. Update the README example and the unit test to match.

They edit the same lines on purpose, so the second one to be merged will conflict with the first. When Handloom reports that a branch did not merge, resolve it: the final greeting must be `greetings, NAME` (task B wins), and README, test and greet.py must all agree. Keep ./check.sh passing.'
START=$(date +%s)
human job new "Change the greeting twice" --repo "$REPO" --verify ./check.sh --device c-dev --lead-profile boss --lead-name boss-1 --body "$BRIEF" | tee "$OUT/jobnew.txt"
grep -q "Started job #1" "$OUT/jobnew.txt" || { echo "RESULT lead=$LEAD run=$RUN FAILED job new"; exit 1; }

say "wait for the conflict to be resolved and the job done (up to ${TIMEOUT}s)"
FINAL="$OUT/home/work/job-1/_integration"
done_at=0
for i in $(seq 1 "$TIMEOUT"); do
  if human digest --json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if any(i["kind"]=="job-done" for i in d["to_review"]) else 1)' \
     && [ "$(cd "$FINAL" 2>/dev/null && python3 tool.py greet World 2>&1)" = "greetings, World" ]; then done_at=$(( $(date +%s) - START )); break; fi
  sleep 1
done
sleep 5
human job show 1 | sed 's/^/    /'
CONF="$(python3 - "$OUT" <<'PY'
import glob, sqlite3, sys
c = sqlite3.connect("file:" + glob.glob(sys.argv[1] + "/data/*.db")[0] + "?mode=ro", uri=True)
print(c.execute("select count(*) from merge where status in ('conflict','failed')").fetchone()[0])
PY
)"
say "the merged branch"
OUTG="$(cd "$FINAL" && python3 tool.py greet World 2>&1)"
echo "   greet World -> $OUTG"
GOT=0; [ "$OUTG" = "greetings, World" ] && GOT=1
CHK=0; (cd "$FINAL" && ./check.sh >/dev/null 2>&1) && CHK=1
README=0; grep -q "greetings, World" "$FINAL/README.md" && ! grep -q "hello, World" "$FINAL/README.md" && README=1
MARK=0; ! grep -rq '^<<<<<<<\|^>>>>>>>' "$FINAL" --include='*.py' --include='*.md' && MARK=1
printf '\nRESULT lead=%s run=%s seconds=%s conflict_reported=%s final_greeting=%s check=%s readme=%s no_markers=%s\n' "$LEAD" "$RUN" "$done_at" "$CONF" "$GOT" "$CHK" "$README" "$MARK" | tee "$OUT/result.txt"
