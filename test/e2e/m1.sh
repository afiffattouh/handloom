#!/usr/bin/env bash
# M1 check (DESIGN.md section 16): a lead Claude Code on this machine and a
# worker Claude Code on $REMOTE finish a three-task job (task 3 depends on
# task 1) with no human input after the first brief. The audit log must show
# every wake, claim, submit and accept.
#
# What this script does and does not do:
#   - It starts the hub, two links and two Claude Code sessions (each in tmux).
#   - It types exactly one thing into an agent: the brief, into the lead.
#     Before that it answers Claude Code's "trust this folder" dialog (Down,
#     Enter), which appears once per new directory.
#   - After the brief it only reads: the board, the audit log, the panes.
#     The worker gets no human input at all; handloom wakes it.
#   - It passes only if the audit log shows the whole job. It does not
#     create, claim, submit or accept anything itself.
#
# Run: test/e2e/m1.sh
#   WORKER_HOST=local   rehearsal with both agents on this machine (two devices)
#   CLAUDE_MODEL=...    model for both agents (default sonnet)
#   TIMEOUT=900         seconds to wait for the job
#   KEEP=1              leave the sessions running afterwards

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

WORKER_HOST="${WORKER_HOST:-remote}"
MODEL="${CLAUDE_MODEL:-sonnet}"
TIMEOUT="${TIMEOUT:-900}"
LEAD=hltest-lead
WORKER=hltest-worker
REMOTE_CLAUDE="${REMOTE_CLAUDE:-/root/.local/bin/claude}"
LOCAL_CLAUDE="$(command -v claude)"

[ -n "${KEEP:-}" ] || trap e2e_cleanup EXIT
e2e_setup m1

# ---- where the worker lives ----
if [ "$WORKER_HOST" = local ]; then
  say "rehearsal: the worker runs on this machine as a second device"
  W_HOME="$OUT/home2" W_DIR="$OUT/worker-project" W_HL="$BIN" W_CLAUDE="$LOCAL_CLAUDE" W_WHERE="$(hostname -s) (second device)"
  mkdir -p "$W_HOME"
  join="$(admin device add hltest-local2 --json | sed -n 's/.*"token": "\(.*\)".*/\1/p')"
  HANDLOOM_HOME="$W_HOME" "$BIN" link join "$HUB_URL" "$join" | head -1
  $TMUX_L new-session -d -s hltest-link2 "HANDLOOM_HOME='$W_HOME' '$BIN' link run >'$OUT/link2.log' 2>&1"
  for _ in $(seq 50); do [ -S "$W_HOME/link.sock" ] && break; sleep 0.2; done
  wsh() { bash -c "$1"; }
else
  W_HOME="$REMOTE_DIR/home" W_DIR="$REMOTE_DIR/worker-project" W_HL="$REMOTE_DIR/bin/handloom" W_CLAUDE="$REMOTE_CLAUDE" W_WHERE="$REMOTE"
  wsh() { rsh "$1"; }
  scp -q -o ControlPath=/tmp/hltest-ssh-%C "$ROOT/test/e2e/agent.sh" "$REMOTE:$REMOTE_DIR/agent.sh"
  say "Claude Code on $REMOTE"
  rsh "$REMOTE_CLAUDE --version; $REMOTE_CLAUDE auth status 2>&1 | grep -E 'loggedIn|authMethod'"
  rsh "$REMOTE_CLAUDE auth status 2>&1 | grep -q '\"loggedIn\": true'" ||
    fail "Claude Code on $REMOTE is not logged in. Log in once (ssh -t $REMOTE $REMOTE_CLAUDE, then /login) and rerun."
fi
wtmux() { wsh "$(printf '%q ' tmux -L hltest "$@")"; }
L_DIR="$OUT/lead-project"
mkdir -p "$L_DIR"
wsh "mkdir -p '$W_DIR'"

# ---- agents ----
say "register agents; the admin makes $LEAD the lead"
lhl "$LEAD" register "$LEAD" --kind claude | head -1
wsh "env -u HERDR_ENV -u HERDR_PANE_ID -u TMUX -u TMUX_PANE HANDLOOM_HOME='$W_HOME' '$W_HL' register '$WORKER' --kind claude" | head -1
admin agent role "$LEAD" lead

say "start Claude Code: lead on $(hostname -s), worker on $W_WHERE (model $MODEL)"
"$ROOT/test/e2e/agent.sh" "$LEAD" "$OUT/home" "$L_DIR" "$BIN" "$LOCAL_CLAUDE" "$MODEL"
if [ "$WORKER_HOST" = local ]; then
  "$ROOT/test/e2e/agent.sh" "$WORKER" "$W_HOME" "$W_DIR" "$W_HL" "$W_CLAUDE" "$MODEL"
else
  rsh "bash $REMOTE_DIR/agent.sh '$WORKER' '$W_HOME' '$W_DIR' '$W_HL' '$W_CLAUDE' '$MODEL'"
fi

agent_field() { human agents --json | python3 -c "
import json, sys
for a in json.load(sys.stdin):
    if a['name'] == '$1': print(a.get('$2', ''))"; }

KEYS=0 # everything this script types into an agent's terminal is counted
# wait_ready <agent> <tmux-fn>: accept the trust dialog if it shows, then wait
# until the SessionStart hook has reported the agent idle.
wait_ready() {
  local agent="$1" tm="$2" i pane
  for i in $(seq 90); do
    pane="$($tm capture-pane -p -t "$agent" 2>/dev/null || true)"
    if grep -q 'Yes, I trust this folder' <<<"$pane"; then
      # The dialog's default is "No, exit" when the folder pre-approves
      # permissions, so move the cursor to "Yes" before confirming.
      if grep -qE '❯ *Yes, I trust this folder' <<<"$pane"; then
        echo "    $agent: confirming Claude Code's trust-this-folder dialog (Enter)"
        $tm send-keys -t "$agent" Enter
        sleep 2
      else
        echo "    $agent: trust-this-folder dialog: moving to \"Yes, I trust this folder\" (Down)"
        $tm send-keys -t "$agent" Down
        sleep 1
      fi
      KEYS=$((KEYS + 1))
      continue
    fi
    if [ "$(agent_field "$agent" state)" = idle ]; then
      echo "    $agent: idle, session $(agent_field "$agent" session_id), wake target $(agent_field "$agent" wake_target)"
      return 0
    fi
    sleep 1
  done
  $tm capture-pane -p -t "$agent" | tail -30
  fail "$agent did not become ready"
}
ltmux() { $TMUX_L "$@"; }
wait_ready "$LEAD" ltmux
wait_ready "$WORKER" wtmux
sleep 3
SETUP_KEYS=$KEYS

# ---- the one human input ----
BRIEF="You are the lead of a handloom team. I will not be available again, so do not ask me anything. Use the handloom command for all coordination; your CLAUDE.md explains it. The job: collect three facts from the worker agent $WORKER, which runs on another machine. Create exactly three handloom tasks, all assigned to $WORKER. First task: run uname -r and write the output to kernel.txt. Second task: run hostname and write the output to hostname.txt. Third task, which must depend on the first task: write summary.txt with two lines, the kernel version and then the hostname. Every task must ask for evidence of the form test:cat <file> -> <content>. After creating the tasks, end your turn; handloom wakes you when work is submitted. When woken, read your inbox and check each submitted task with handloom task show. The files are on the worker's machine and you cannot read them, so judge the evidence text: accept the task if the evidence shows the file content, reject it with a reason if not. When all three tasks are done, say DONE and stop."

say "the first brief (the only thing typed into an agent after setup)"
echo "$BRIEF" | fold -s -w 110 | sed 's/^/    /'
BRIEF_AT="$(human audit --json | python3 -c 'import json,sys; print(json.load(sys.stdin)[-1]["seq"])')"
$TMUX_L send-keys -t "$LEAD" -l "$BRIEF"
sleep 1
$TMUX_L send-keys -t "$LEAD" Enter
echo "    typed into $LEAD at $(date -u +%H:%M:%SZ), after audit row $BRIEF_AT. From here the script only reads."

# ---- watch, read-only ----
say "waiting for the job (up to ${TIMEOUT}s)"
start=$SECONDS last=""
while :; do
  line="$(human task list --json | python3 -c '
import json, sys
tasks = json.load(sys.stdin)
print(" ".join("#%d:%s" % (t["id"], t["status"]) for t in tasks) or "no tasks yet")')"
  states="$LEAD=$(agent_field "$LEAD" state) $WORKER=$(agent_field "$WORKER" state)"
  if [ "$line | $states" != "$last" ]; then
    printf '    +%4ds  %-50s %s\n' "$((SECONDS - start))" "$line" "$states"
    last="$line | $states"
  fi
  if [ "$(grep -o ':done' <<<"$line" | wc -l)" -ge 3 ]; then break; fi
  if [ $((SECONDS - start)) -gt "$TIMEOUT" ]; then
    say "TIMEOUT: lead pane"; $TMUX_L capture-pane -p -t "$LEAD" | tail -40
    say "TIMEOUT: worker pane"; wtmux capture-pane -p -t "$WORKER" | tail -40
    human audit >"$OUT/audit.txt" || true
    fail "the job did not finish in ${TIMEOUT}s (audit log in $OUT/audit.txt)"
  fi
  sleep 5
done
sleep 8 # let the last turn end and its hook report

# ---- evidence ----
human audit >"$OUT/audit.txt"
human audit --json >"$OUT/audit.json"
$TMUX_L capture-pane -p -S -2000 -t "$LEAD" >"$OUT/lead-pane.txt"
wtmux capture-pane -p -S -2000 -t "$WORKER" >"$OUT/worker-pane.txt"

say "board"
human task list
for id in 1 2 3; do human task show "$id" | sed -n '/Evidence:/,$p' | sed "s/^/    #$id /"; done

say "audit log after the brief: every wake, create, claim, submit, accept, reject"
awk -v after="$BRIEF_AT" '$1 > after' "$OUT/audit.txt" |
  grep -E ' (wake|wake\.failed|task\.create|task\.claim|task\.submit|task\.accept|task\.reject|task\.lease_expired|denied) ' || true

say "checks"
python3 - "$OUT/audit.json" "$BRIEF_AT" "$LEAD" "$WORKER" "$SETUP_KEYS" <<'PY'
import json, sys
rows = [r for r in json.load(open(sys.argv[1])) if r["seq"] > int(sys.argv[2])]
lead, worker, setup_keys = "agent:" + sys.argv[3], "agent:" + sys.argv[4], sys.argv[5]
def of(action, actor=None, target=None):
    return [r for r in rows if r["action"] == action and (actor is None or r["actor"] == actor)
            and (target is None or r["target"] == target)]
ok = True
def check(label, cond):
    global ok
    ok = ok and bool(cond)
    print("    %-4s %s" % ("ok" if cond else "FAIL", label))

creates = of("task.create", lead)
check("the lead created three tasks, all assigned to the worker",
      len(creates) == 3 and all(c["payload"]["assigned_to"] == sys.argv[4] for c in creates))
dependents = [c for c in creates if c["payload"]["depends_on"]]
check("one task depends on another", len(dependents) >= 1)
tasks = [c["target"] for c in creates]
for t in tasks:
    check("%s: claimed and submitted by the worker, accepted by the lead" % t,
          of("task.claim", worker, t) and of("task.submit", worker, t) and of("task.accept", lead, t))
for d in dependents:
    for dep in d["payload"]["depends_on"]:
        accepted = of("task.accept", lead, "task:%d" % dep)
        claimed = of("task.claim", worker, d["target"])
        check("%s was claimed only after task:%d was accepted" % (d["target"], dep),
              accepted and claimed and accepted[0]["seq"] < claimed[0]["seq"])
wakes = of("wake")
w_wakes = [w for w in wakes if w["target"] == worker]
l_wakes = [w for w in wakes if w["target"] == lead]
check("the worker was woken by handloom (%s)" % ", ".join(w["payload"]["method"] for w in w_wakes), w_wakes)
check("the worker's first wake was a typed terminal nudge", w_wakes and w_wakes[0]["payload"]["method"] in ("tmux", "herdr"))
check("the lead was woken by handloom (%s)" % ", ".join(w["payload"]["method"] for w in l_wakes), l_wakes)
humans = [r for r in rows if r["actor"].startswith("human:") or r["actor"] == "admin"]
check("no human or admin action after the brief", not humans)
actors = sorted({r["actor"] for r in rows if r["action"].startswith("task.")})
check("task actions came only from the two agents %s" % actors, set(actors) <= {lead, worker})
print("    keys typed by this script: %s key(s) on the trust dialogs during setup, then the brief; nothing after" % setup_keys)
sys.exit(0 if ok else 1)
PY

printf '\nM1 PASS: lead on %s and worker on %s finished the three-task job with no human input after the brief.\n' "$(hostname -s)" "$W_WHERE"
echo "Evidence: $OUT/audit.txt, $OUT/lead-pane.txt, $OUT/worker-pane.txt"

# ---- extra: the end-of-turn hook, live ----
# The job above may or may not exercise wake ladder step 1, depending on
# timing. This phase forces it: mail arrives while the worker is mid-turn, so
# nothing may be typed, and the Stop hook must hand the mail over. It runs
# after the M1 verdict and does type one prompt, into the worker.
[ -z "${NO_EXTRA:-}" ] || exit 0
say "extra, after the job: mail for a working agent is delivered by the end-of-turn hook"
EXTRA_AT="$(human audit --json | python3 -c 'import json,sys; print(json.load(sys.stdin)[-1]["seq"])')"
wtmux send-keys -t "$WORKER" -l "Write a note of about 200 words on why tests matter to note.txt, then print it with cat note.txt, then end your turn."
sleep 1
wtmux send-keys -t "$WORKER" Enter
for _ in $(seq 40); do [ "$(agent_field "$WORKER" state)" = working ] && break; sleep 0.5; done
[ "$(agent_field "$WORKER" state)" = working ] || fail "extra: the worker did not start working"
show human human send "$WORKER" "Extra check. Please run: handloom send role:lead hook-delivery-ok"
verdict=""
for _ in $(seq 60); do
  verdict="$(human audit --json | python3 -c '
import json, sys
rows = [r for r in json.load(sys.stdin) if r["seq"] > int(sys.argv[1])]
worker = "agent:" + sys.argv[2]
wakes = [r for r in rows if r["action"] == "wake" and r["target"] == worker]
reply = [r for r in rows if r["action"] == "message.send" and r["actor"] == worker]
if wakes and reply:
    print(wakes[0]["payload"]["method"])' "$EXTRA_AT" "$WORKER")"
  [ -n "$verdict" ] && break
  sleep 3
done
human audit | awk -v after="$EXTRA_AT" '$1 > after' | grep -E ' (wake|wake\.failed|message\.send|inbox\.read|agent\.state) ' || true
[ "$verdict" = hook ] || fail "extra: expected the worker to be woken by the hook, got '${verdict:-nothing}'"
echo "    ok   the worker was working, nothing was typed, the Stop hook delivered the mail and the worker acted on it"
wtmux capture-pane -p -S -2000 -t "$WORKER" >"$OUT/worker-pane.txt"
