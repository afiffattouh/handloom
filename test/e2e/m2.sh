#!/usr/bin/env bash
# M2 check (DESIGN.md section 16): the M1 scenario with a mixed team, and with
# one worker that has no terminal.
#
#   lead      Claude Code, this machine, in tmux             woken by tmux nudge
#   codex     Codex, $REMOTE, in tmux, handloom through MCP      woken by tmux nudge / Stop hook
#   headless  Claude Code, $REMOTE, no terminal at all       woken by a headless turn on its session
#   pi        Pi, this machine, in tmux (only if PI_MODEL is set)
#
# The lead creates one task per worker; the last task depends on the first.
# As in m1.sh the script types one thing into an agent, the brief into the
# lead, and afterwards only reads. Setup before the brief: it answers startup
# dialogs, and it creates the headless worker's session with one `claude -p`
# run, because a session must exist before it can be resumed.
#
# Run: test/e2e/m2.sh
#   PI_MODEL=provider/model   add a Pi worker using that model
#   CLAUDE_MODEL=sonnet  TIMEOUT=900  KEEP=1

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

MODEL="${CLAUDE_MODEL:-sonnet}"
TIMEOUT="${TIMEOUT:-900}"
LEAD=hltest-lead
CODEX=hltest-codex
HEADLESS=hltest-headless
PI=hltest-pi
REMOTE_CLAUDE="${REMOTE_CLAUDE:-/root/.local/bin/claude}"
REMOTE_CODEX="${REMOTE_CODEX:-/usr/local/bin/codex}"
LOCAL_CLAUDE="$(command -v claude)"
LOCAL_PI="${LOCAL_PI:-$(command -v pi || true)}"

[ -n "${KEEP:-}" ] || trap e2e_cleanup EXIT
e2e_setup m2
R_HOME="$REMOTE_DIR/home" R_HL="$REMOTE_DIR/bin/handloom"
rtmux() { rsh "$(printf '%q ' tmux -L hltest "$@")"; }
ltmux() { $TMUX_L "$@"; }

# The remote link learns how to run a headless Claude Code turn: absolute
# path, and the allowlist on the command line (see the headless worker below).
# It reads headless.json at start, so it is restarted.
ALLOWED='Read,Write,Edit,Glob,Grep,Bash(handloom *),Bash(uname *),Bash(uname),Bash(hostname),Bash(cat *),Bash(ls *),Bash(echo *)'
rsh "cat > $R_HOME/headless.json" <<EOF
{"claude": ["$REMOTE_CLAUDE", "-p", "--model", "$MODEL", "--permission-mode", "dontAsk", "--allowedTools", "$ALLOWED", "--resume", "{session_id}", "{prompt}"]}
EOF
rtmux kill-session -t hltest-link
sleep 1
rsh "tmux -L hltest new-session -d -s hltest-link 'PATH=$REMOTE_DIR/bin:\$PATH HANDLOOM_HOME=$R_HOME $R_HL link run >>$REMOTE_DIR/link.log 2>&1'"
for _ in $(seq 50); do rsh "test -S $R_HOME/link.sock" && break; sleep 0.2; done

say "the CLIs"
echo "lead:     $("$LOCAL_CLAUDE" --version)"
echo "codex:    $(rsh "$REMOTE_CODEX --version; $REMOTE_CODEX login status 2>&1 | head -1" | tr '\n' ' ')"
echo "headless: $(rsh "$REMOTE_CLAUDE --version")"
rsh "$REMOTE_CLAUDE auth status 2>&1 | grep -q '\"loggedIn\": true'" || fail "Claude Code on $REMOTE is not logged in"
WORKERS=("$CODEX" "$HEADLESS")
if [ -n "${PI_MODEL:-}" ]; then
  [ -n "$LOCAL_PI" ] || fail "PI_MODEL is set but pi is not on PATH"
  echo "pi:       $("$LOCAL_PI" --version) with model $PI_MODEL"
  WORKERS+=("$PI")
else
  echo "pi:       not in this run (set PI_MODEL=provider/model to add a Pi worker)"
fi

# ---- register; the admin makes the lead ----
say "register agents; the admin makes $LEAD the lead"
L_DIR="$OUT/lead-project" C_DIR="$REMOTE_DIR/codex-project" H_DIR="$REMOTE_DIR/headless-project" P_DIR="$OUT/pi-project"
mkdir -p "$L_DIR" "$P_DIR"
rsh "mkdir -p $C_DIR $H_DIR"
lhl "$LEAD" register "$LEAD" --kind claude | head -1
rhl "$CODEX" register "$CODEX" --kind codex --dir "$C_DIR" | head -1
rhl "$HEADLESS" register "$HEADLESS" --kind claude --dir "$H_DIR" --wake-target headless | head -1
[ -z "${PI_MODEL:-}" ] || lhl "$PI" register "$PI" --kind pi --dir "$P_DIR" | head -1
admin agent role "$LEAD" lead

# ---- lead: Claude Code in tmux (as in M1) ----
say "lead: Claude Code on $(hostname -s)"
"$ROOT/test/e2e/agent.sh" "$LEAD" "$OUT/home" "$L_DIR" "$BIN" "$LOCAL_CLAUDE" "$MODEL" | tail -1

# ---- codex worker: Codex in tmux on the remote machine ----
# Sandbox stays on (workspace-write, no network). The handloom verbs come as MCP
# tools, which Codex runs outside the sandbox; they are pre-approved because
# nobody is there to approve them. The hook trust flag skips the review of
# the hooks `handloom adapter install` just wrote.
say "codex worker: Codex on $REMOTE"
rhl "$CODEX" adapter install codex --name "$CODEX" --dir "$C_DIR" | head -1
rsh "cat > $REMOTE_DIR/start-codex.sh" <<START
#!/bin/sh
# Starts the Codex test worker. Written by test/e2e/m2.sh.
export HANDLOOM_HOME=$R_HOME
exec $R_HL run $CODEX -- $REMOTE_CODEX --enable hooks --dangerously-bypass-hook-trust -a never -s workspace-write \\
  -c 'model_reasoning_effort="low"' \\
  -c 'mcp_servers.handloom.command="$R_HL"' -c 'mcp_servers.handloom.args=["mcp"]' \\
  -c 'mcp_servers.handloom.default_tools_approval_mode="approve"' \\
  -c 'mcp_servers.handloom.env={HANDLOOM_HOME="$R_HOME",HANDLOOM_AGENT="$CODEX"}'
START
rsh "tail -n +4 $REMOTE_DIR/start-codex.sh" | sed 's/^/    /'
rtmux new-session -d -s "$CODEX" -x 160 -y 50 -c "$C_DIR" "sh $REMOTE_DIR/start-codex.sh"

# ---- headless worker: a Claude Code session with no terminal ----
# Its project is not "trusted" in Claude Code's sense (nobody ever opened it
# interactively), so the allowlist is passed on the command line, both for
# the first run and, through headless.json, for every turn the link starts.
say "headless worker: Claude Code on $REMOTE, no terminal"
rhl "$HEADLESS" adapter install claude --name "$HEADLESS" --dir "$H_DIR" | head -1
SESSION_ID="$(cat /proc/sys/kernel/random/uuid)"
echo "    creating its session ($SESSION_ID) with one headless run: setup, before the brief"
rsh "cd $H_DIR && PATH=$REMOTE_DIR/bin:\$PATH HANDLOOM_HOME=$R_HOME HANDLOOM_AGENT=$HEADLESS HANDLOOM_HEADLESS=1 timeout 120 $REMOTE_CLAUDE -p --model $MODEL --permission-mode dontAsk --allowedTools '$ALLOWED' --session-id $SESSION_ID 'You are a handloom worker with no terminal. handloom will wake you when there is work. For now reply with the single word READY.' </dev/null" | tail -2 | sed 's/^/    /'

# ---- pi worker (optional) ----
if [ -n "${PI_MODEL:-}" ]; then
  say "pi worker: Pi on $(hostname -s), model $PI_MODEL"
  lhl "$PI" adapter install pi --name "$PI" --dir "$P_DIR" | head -1
  $TMUX_L new-session -d -s "$PI" -x 160 -y 50 -c "$P_DIR" \
    "env -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH HANDLOOM_HOME='$OUT/home' PATH='$ROOT/bin':\"\$PATH\" '$LOCAL_PI' --approve --model '$PI_MODEL'"
fi

agent_field() { human agents --json | python3 -c "
import json, sys
for a in json.load(sys.stdin):
    if a['name'] == '$1': print(a.get('$2', ''))"; }

KEYS=0
# wait_ready <agent> <tmux-fn> <state>: answer startup dialogs, then wait
# until the agent's hooks have reported it in the given state.
wait_ready() {
  local agent="$1" tm="$2" want="$3" i pane
  for i in $(seq 90); do
    pane="$($tm capture-pane -p -t "$agent" 2>/dev/null || true)"
    if grep -q 'Yes, I trust this folder' <<<"$pane"; then # Claude Code
      if grep -qE '❯ *Yes, I trust this folder' <<<"$pane"; then $tm send-keys -t "$agent" Enter; sleep 2; else $tm send-keys -t "$agent" Down; sleep 1; fi
      KEYS=$((KEYS + 1)); echo "    $agent: answering Claude Code's trust-this-folder dialog"; continue
    fi
    if grep -q 'Hooks need review' <<<"$pane"; then # Codex
      if grep -qE '› *2\. Trust all and continue' <<<"$pane"; then $tm send-keys -t "$agent" Enter; sleep 2; else $tm send-keys -t "$agent" Down; sleep 1; fi
      KEYS=$((KEYS + 1)); echo "    $agent: answering Codex's hooks-need-review dialog"; continue
    fi
    if [ "$(agent_field "$agent" state)" = "$want" ]; then
      echo "    $agent: $want, kind $(agent_field "$agent" kind), wake target $(agent_field "$agent" wake_target), session $(agent_field "$agent" session_id)"
      return 0
    fi
    sleep 1
  done
  $tm capture-pane -p -t "$agent" 2>/dev/null | grep -v '^\s*$' | tail -25
  fail "$agent did not become $want"
}
say "waiting for the agents"
wait_ready "$LEAD" ltmux idle
wait_ready "$CODEX" rtmux idle
none() { true; }
wait_ready "$HEADLESS" none offline
[ "$(agent_field "$HEADLESS" wake_target)" = headless ] || fail "$HEADLESS has wake target '$(agent_field "$HEADLESS" wake_target)', want headless"
[ -z "${PI_MODEL:-}" ] || wait_ready "$PI" ltmux idle
sleep 3
SETUP_KEYS=$KEYS

# ---- the one human input ----
THIRD="$CODEX"
[ -z "${PI_MODEL:-}" ] || THIRD="$PI"
BRIEF="You are the lead of a handloom team. I will not be available again, so do not ask me anything. Use the handloom command for all coordination; your CLAUDE.md explains it. Your workers are other agents on other machines: ${WORKERS[*]}. The job: collect three facts. Create exactly three handloom tasks. First task, assigned to $CODEX: run uname -r and write the output to kernel.txt. Second task, assigned to $HEADLESS: run hostname and write the output to hostname.txt. Third task, assigned to $THIRD, which must depend on the first task: run uname -m and write the output to arch.txt. Every task must ask for evidence of the form test:cat <file> -> <content>. After creating the tasks, end your turn; handloom wakes you when work is submitted. When woken, read your inbox and check each submitted task with handloom task show. The files are on the workers' machines and you cannot read them, so judge the evidence text: accept the task if the evidence shows the file content, reject it with a reason if not. When all three tasks are done, say DONE and stop."

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
save_evidence() {
  human audit >"$OUT/audit.txt" || true
  human audit --json >"$OUT/audit.json" || true
  $TMUX_L capture-pane -p -S -2000 -t "$LEAD" >"$OUT/lead-pane.txt" 2>/dev/null || true
  rtmux capture-pane -p -S -2000 -t "$CODEX" >"$OUT/codex-pane.txt" 2>/dev/null || true
  rsh "cat $R_HOME/headless-$HEADLESS.log 2>/dev/null" >"$OUT/headless.log" || true
  [ -z "${PI_MODEL:-}" ] || $TMUX_L capture-pane -p -S -2000 -t "$PI" >"$OUT/pi-pane.txt" 2>/dev/null || true
}
while :; do
  line="$(human task list --json | python3 -c '
import json, sys
tasks = json.load(sys.stdin)
print(" ".join("#%d:%s" % (t["id"], t["status"]) for t in tasks) or "no tasks yet")')"
  states=""
  for a in "$LEAD" "${WORKERS[@]}"; do states+="${a#hltest-}=$(agent_field "$a" state) "; done
  if [ "$line | $states" != "$last" ]; then
    printf '    +%4ds  %-40s %s\n' "$((SECONDS - start))" "$line" "$states"
    last="$line | $states"
  fi
  if [ "$(grep -o ':done' <<<"$line" | wc -l)" -ge 3 ]; then break; fi
  if [ $((SECONDS - start)) -gt "$TIMEOUT" ]; then
    save_evidence
    say "TIMEOUT: lead pane"; grep -v '^\s*$' "$OUT/lead-pane.txt" | tail -30
    say "TIMEOUT: codex pane"; grep -v '^\s*$' "$OUT/codex-pane.txt" | tail -30
    say "TIMEOUT: headless log"; tail -30 "$OUT/headless.log"
    fail "the job did not finish in ${TIMEOUT}s (audit log in $OUT/audit.txt)"
  fi
  sleep 5
done
sleep 10 # let the last turns end and their hooks report
save_evidence

say "board"
human task list
for id in 1 2 3; do human task show "$id" | sed -n '/Evidence:/,$p' | sed "s/^/    #$id /"; done

say "audit log after the brief: every wake, create, claim, submit, accept, reject"
awk -v after="$BRIEF_AT" '$1 > after' "$OUT/audit.txt" |
  grep -E ' (wake|wake\.failed|task\.create|task\.claim|task\.submit|task\.accept|task\.reject|task\.lease_expired|denied) ' || true

say "checks"
python3 - "$OUT/audit.json" "$BRIEF_AT" "$SETUP_KEYS" "$LEAD" "$CODEX" "$HEADLESS" "${PI_MODEL:+$PI}" <<'PY'
import json, sys
rows = [r for r in json.load(open(sys.argv[1])) if r["seq"] > int(sys.argv[2])]
setup_keys = sys.argv[3]
lead, codex, headless = ["agent:" + n for n in sys.argv[4:7]]
pi = "agent:" + sys.argv[7] if len(sys.argv) > 7 and sys.argv[7] else None
workers = [w for w in (codex, headless, pi) if w]
def of(action, actor=None, target=None):
    return [r for r in rows if r["action"] == action and (actor is None or r["actor"] == actor)
            and (target is None or r["target"] == target)]
ok = True
def check(label, cond):
    global ok
    ok = ok and bool(cond)
    print("    %-4s %s" % ("ok" if cond else "FAIL", label))

creates = of("task.create", lead)
check("the lead created three tasks", len(creates) == 3)
assignees = {c["target"]: "agent:" + c["payload"]["assigned_to"] for c in creates}
check("the tasks are spread over the workers %s" % sorted(set(assignees.values())), set(assignees.values()) == set(workers))
for t, who in sorted(assignees.items()):
    check("%s: claimed and submitted by %s, accepted by the lead" % (t, who),
          of("task.claim", who, t) and of("task.submit", who, t) and of("task.accept", lead, t))
dependents = [c for c in creates if c["payload"]["depends_on"]]
check("one task depends on another", len(dependents) >= 1)
for d in dependents:
    for dep in d["payload"]["depends_on"]:
        accepted = of("task.accept", lead, "task:%d" % dep)
        claimed = of("task.claim", None, d["target"])
        check("%s was claimed only after task:%d was accepted" % (d["target"], dep),
              accepted and claimed and accepted[0]["seq"] < claimed[0]["seq"])
def methods(agent):
    return [w["payload"]["method"] for w in of("wake") if w["target"] == agent]
check("the lead (Claude Code) was woken by handloom: %s" % methods(lead), methods(lead))
check("the Codex worker was woken by handloom: %s" % methods(codex), methods(codex))
check("the Codex worker's first wake was a typed terminal nudge", methods(codex)[:1] in (["tmux"], ["herdr"]))
check("the headless worker was woken by headless turns only: %s" % methods(headless),
      methods(headless) and set(methods(headless)) <= {"headless", "hook"} and methods(headless)[0] == "headless")
if pi:
    check("the Pi worker was woken by handloom: %s" % methods(pi), methods(pi))
check("no human or admin action after the brief", not [r for r in rows if r["actor"].startswith("human:") or r["actor"] == "admin"])
actors = sorted({r["actor"] for r in rows if r["action"].startswith("task.")})
check("task actions came only from the team %s" % actors, set(actors) <= set([lead] + workers))
print("    keys typed by this script: %s key(s) on startup dialogs during setup, then the brief; nothing after" % setup_keys)
if not pi:
    print("    NOTE no Pi worker in this run: the M2 check asks for one")
sys.exit(0 if ok else 1)
PY

if [ -n "${PI_MODEL:-}" ]; then
  printf '\nM2 PASS: Claude Code lead, Codex worker, Pi worker and a headless Claude Code worker finished the job with no human input after the brief.\n'
else
  printf '\nM2 PARTIAL PASS: Claude Code lead, Codex worker and a headless Claude Code worker finished the job with no human input after the brief. No Pi worker ran.\n'
fi
echo "Evidence: $OUT/audit.txt, $OUT/lead-pane.txt, $OUT/codex-pane.txt, $OUT/headless.log"

# ---- extra: Codex's end-of-turn hook, live ----
# As in m1.sh: mail that arrives while the Codex worker is mid-turn must be
# handed over by its Stop hook, with nothing typed. Runs after the verdict
# above and does type one prompt, into the Codex worker.
[ -z "${NO_EXTRA:-}" ] || exit 0
say "extra, after the job: mail for a working Codex agent is delivered by its Stop hook"
EXTRA_AT="$(human audit --json | python3 -c 'import json,sys; print(json.load(sys.stdin)[-1]["seq"])')"
rtmux send-keys -t "$CODEX" -l "Write a note of about 200 words on why tests matter to note.txt, then print it with cat note.txt, then end your turn."
sleep 1
rtmux send-keys -t "$CODEX" Enter
for _ in $(seq 40); do [ "$(agent_field "$CODEX" state)" = working ] && break; sleep 0.5; done
[ "$(agent_field "$CODEX" state)" = working ] || fail "extra: the Codex worker did not start working"
show human human send "$CODEX" "Extra check: please reply with a handloom message to role:lead saying hook-delivery-ok"
verdict=""
for _ in $(seq 40); do
  verdict="$(human audit --json | python3 -c '
import json, sys
rows = [r for r in json.load(sys.stdin) if r["seq"] > int(sys.argv[1])]
agent = "agent:" + sys.argv[2]
wakes = [r for r in rows if r["action"] == "wake" and r["target"] == agent]
read = [r for r in rows if r["action"] == "inbox.read" and r["actor"] == agent]
idle = [r for r in rows if r["action"] == "agent.state" and r["target"] == agent and r["payload"]["state"] == "idle"]
reply = [r for r in rows if r["action"] == "message.send" and r["actor"] == agent]
if wakes and read and idle and wakes[0]["seq"] < read[0]["seq"] < idle[-1]["seq"]:
    print(wakes[0]["payload"]["method"], "replied" if reply else "no-reply")' "$EXTRA_AT" "$CODEX")"
  [ -n "$verdict" ] && break
  sleep 3
done
human audit | awk -v after="$EXTRA_AT" '$1 > after' | grep -E ' (wake|wake\.failed|message\.send|inbox\.read|agent\.state|denied) ' || true
rtmux capture-pane -p -S -2000 -t "$CODEX" >"$OUT/codex-pane.txt" 2>/dev/null || true
[ "${verdict%% *}" = hook ] || fail "extra: expected the Codex worker to be woken by its Stop hook and to read its inbox, got '${verdict:-nothing}'"
echo "    ok   the Codex worker was working, nothing was typed, its Stop hook delivered the mail and it read it"
case "$verdict" in
  *no-reply) echo "    note the worker read the message but did not send the reply it asked for (see $OUT/codex-pane.txt)" ;;
  *) echo "    ok   the worker acted on the message and replied to the lead" ;;
esac
