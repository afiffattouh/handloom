#!/usr/bin/env bash
# Milestone C3 check, on two machines with a real Codex: the hub and a human
# here, Mantis as the device that starts Codex. Needs Codex logged in on
# $REMOTE (it is, on Mantis) and the other things lib.sh needs.
#
#   a profile for a Codex agent is stored on the hub; the human asks for an
#   agent on Mantis; Mantis' link starts Codex in a tmux window of its own,
#   with the handloom verbs as MCP tools and a workspace-write sandbox; a
#   message wakes it; it reads its inbox, follows the profile's prompt and
#   writes a file in its work directory; its attempt to write outside the
#   work directory is refused by the sandbox.
#
# Run: test/e2e/c3.sh        (costs a few cents of Codex usage)
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TIMEOUT="${TIMEOUT:-240}"
SOCK=hltest-c3
LINK_FLAGS="--tmux-socket $SOCK --tick 2s --live-every 3s"
cleanup() {
  kill ${PANE_LOG:-} 2>/dev/null || true
  rsh "tmux -L $SOCK kill-server 2>/dev/null; rm -f /root/hltest-outside.txt; sed -i '/^\\[projects\\.\"\\/root\\/hltest\\/home\\/work/,+1d' ~/.codex/config.toml; true" 2>/dev/null || true
  e2e_cleanup
}
trap cleanup EXIT
e2e_setup c3

WORK="$REMOTE_DIR/home/work/none/coder1"
dump() {
  echo "--- spawns:"; human spawns 2>&1; echo "--- agents:"; admin agents 2>&1
  echo "--- remote link log:"; rsh "tail -8 $REMOTE_DIR/link.log" 2>&1
  echo "--- pane:"; rsh "tmux -L $SOCK capture-pane -p -t handloom:coder1 2>&1 | tail -30"
  echo "--- work dir:"; rsh "ls -la $WORK 2>&1"
}
die() { { printf '\nFAIL: %s\n' "$*"; dump; } >&2; exit 1; }

say "a profile for a Codex agent"
P="$OUT/prof/coder"; mkdir -p "$P"
cat > "$P/profile.yaml" <<'EOF2'
name: coder
description: Writes small files in its own work directory
kind: codex
tools:
  allow: [read, edit, shell]
EOF2
echo "Your codename is ORION-9. When asked for your codename, answer exactly that." > "$P/PROMPT.md"
"$BIN" profile check "$P" | sed 's/^/    /'
admin profile new "$P" | sed 's/^/    /'

say "ask for an agent on $REMOTE"
human spawn coder1 --profile coder --device "$REMOTE_DEVICE" --wait 150s | tee "$OUT/spawn.txt"
grep -q "coder1 is running" "$OUT/spawn.txt" || die "Codex did not start on $REMOTE"
rsh "tmux -L $SOCK list-windows -t handloom -F '#{window_name}'" | grep -qx coder1 || die "no window coder1 in the link's tmux server on $REMOTE"
echo "   ok: started in the link's own tmux server on $REMOTE"
admin agents --json | python3 -c '
import json,sys
a=[a for a in json.load(sys.stdin) if a["name"]=="coder1"][0]
assert a["kind"]=="codex" and a["wake_target"].startswith("tmux:"), a
print("   ok: registered as codex with wake target", a["wake_target"], "state", a["state"])' || die "registration is wrong"
for i in $(seq 1 40); do rsh "test -f $WORK/AGENTS.md && test -f $WORK/.codex/hooks.json && test -f $WORK/.handloom/tokens/coder1" && break; sleep 1; done
rsh "grep -q ORION-9 $WORK/AGENTS.md" || die "the profile's prompt is not in AGENTS.md"
echo "   ok: adapter files, run token and the profile's prompt are in its work directory"

# Keep a record of what Codex's screen showed, for the report and for debugging.
( for i in $(seq 1 120); do rsh "tmux -L $SOCK capture-pane -p -t handloom:coder1 2>/dev/null | grep -v '^$' | grep -v '[⣀-⣿]' | tail -14" > "$OUT/pane-$(printf %03d $i).txt" 2>/dev/null; sleep 3; done ) &
PANE_LOG=$!
say "wake it: it must read its inbox (through MCP), follow the prompt, write in its directory, and be refused outside it"
sleep 8   # let Codex finish drawing its first screen
human send coder1 "Please do two things, then stop.
1. Write your codename to the file codename.txt in your current directory.
2. Try to create the file /root/hltest-outside.txt containing the word hi. If that does not work, say so in the file outside.txt in your current directory." >/dev/null
for i in $(seq 1 "$TIMEOUT"); do
  rsh "test -f $WORK/codename.txt && (test -f $WORK/outside.txt || test -f /root/hltest-outside.txt)" && break; sleep 1
done
rsh "test -f $WORK/codename.txt" || die "it did not write codename.txt"
echo "   codename.txt: $(rsh "cat $WORK/codename.txt")"
rsh "grep -q ORION-9 $WORK/codename.txt" || die "the profile's prompt was not followed"
echo "   ok: it followed the profile's prompt"
if rsh "test -f /root/hltest-outside.txt"; then die "the sandbox let it write outside its work directory"; fi
echo "   ok: nothing was written outside the work directory (outside.txt says: $(rsh "cat $WORK/outside.txt 2>/dev/null | head -c 200"))"
admin audit | grep "agent:coder1 *inbox.read" | head -2 | sed 's/^/    /'
admin audit | grep -q "agent:coder1 *inbox.read.*\[run-token\]" || die "it did not read its inbox with its run token"
echo "   ok: it read its inbox through MCP with its run token"

say "audit"
admin audit | grep -E "profile.new|spawn\.|inbox.read" | sed 's/^/    /' | head -10
printf '\nPASS: milestone C3 check (a real Codex started by spawn on %s)\n' "$REMOTE"
