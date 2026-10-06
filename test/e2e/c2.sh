#!/usr/bin/env bash
# Milestone C2 check, with a real Claude Code:
#
#   a profile (prompt, tools, a denied command, a skill) is stored on the hub,
#   an agent is spawned from it, and the agent
#     - refuses to run the denied command (the file survives),
#     - sees the profile's skill and NOT a decoy skill in the user's global
#       ~/.claude/skills (isolation),
#     - follows the profile's prompt.
#
# Needs: go, tmux, claude (logged in), python3, curl. Costs a few cents.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/test/e2e/out/c2"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
export PATH="$PATH:$(dirname "$GO")"
PORT="${PORT:-17461}"
H="http://127.0.0.1:$PORT"
SOCK=hltest-c2
T="tmux -L $SOCK"
DECOY="$HOME/.claude/skills/hltest-decoy"
say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok: %s\n' "$*"; }

rm -rf "$OUT"; mkdir -p "$OUT/home" "$OUT/data" "$OUT/bin" "$OUT/prof/researcher/skills/hltest-cite"
cleanup() {
  $T kill-server 2>/dev/null || true
  for p in ${HUB_PID:-} ${LINK_PID:-}; do kill "$p" 2>/dev/null || true; done
  rm -rf "$DECOY"
  python3 - "$OUT" <<'PY' 2>/dev/null || true
import json, os, sys
p = os.path.expanduser("~/.claude.json")
d = json.load(open(p)); out = sys.argv[1]
for k in [k for k in d.get("projects", {}) if k.startswith(out)]:
    del d["projects"][k]
json.dump(d, open(p + ".tmp", "w")); os.replace(p + ".tmp", p)
PY
}
trap cleanup EXIT
fail() {
  printf '\nFAIL: %s\n' "$*" >&2
  { echo "--- spawns:"; human spawns 2>&1; echo "--- link log:"; tail -8 "$OUT/link.log"; echo "--- pane:"; $T capture-pane -p -t handloom:researcher 2>&1 | tail -30; echo "--- work dir:"; ls -la "$WORK" 2>&1; } >&2 || true
  exit 1
}

say "a decoy skill in the user's global Claude Code skills (it must NOT reach the spawned agent)"
mkdir -p "$DECOY"
cat > "$DECOY/SKILL.md" <<'EOF2'
---
name: hltest-decoy
description: Decoy skill used by a Handloom isolation test. Safe to ignore.
---
This skill exists only to prove that global skills do not leak into spawned agents.
EOF2
ok "decoy at $DECOY (removed at the end)"

say "build, hub, device, link"
(cd "$ROOT" && "$GO" build -o "$OUT/bin/handloom" ./cmd/handloom)
BIN="$OUT/bin/handloom"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
export HANDLOOM_HOME="$OUT/home" PATH="$OUT/bin:$PATH"
HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB_PID=$!
for i in $(seq 1 30); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "$H/healthz" >/dev/null || fail "hub did not start"
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
admin() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" $CLEAN "$BIN" "$@"; }
JT="$(admin device add hltest-c2-dev --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
HT="$(admin human add tester --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
human() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$HT" $CLEAN "$BIN" "$@"; }
$CLEAN "$BIN" link join "$H" "$JT" >/dev/null
$CLEAN "$BIN" link run --tmux-socket "$SOCK" --tick 2s --live-every 3s > "$OUT/link.log" 2>&1 & LINK_PID=$!
for i in $(seq 1 30); do $CLEAN "$BIN" link status >/dev/null 2>&1 && break; sleep 0.3; done
ok "up"

say "write a profile as a directory, check it, store it on the hub"
P="$OUT/prof/researcher"
cat > "$P/profile.yaml" <<'EOF2'
name: researcher
description: Reads sources, writes notes, may use the shell but never delete
kind: claude
tools:
  allow: [read, edit, shell]
  deny_commands: [rm]
EOF2
echo "Your codename is ORION-7. When asked for your codename, answer exactly that." > "$P/PROMPT.md"
cat > "$P/skills/hltest-cite/SKILL.md" <<'EOF2'
---
name: hltest-cite
description: How to cite sources in notes (Handloom profile test skill).
---
Cite every claim with the source URL in brackets.
EOF2
"$BIN" profile check "$P" | sed 's/^/    /'
admin profile new "$P" | sed 's/^/    /'
admin profiles | sed 's/^/    /'
ok "stored; the hub holds version 1"

say "spawn an agent from the profile"
human spawn researcher --profile researcher --model sonnet --device hltest-c2-dev --wait 120s | tee "$OUT/spawn.txt"
grep -q "researcher is running" "$OUT/spawn.txt" || fail "the agent did not start"
WORK="$OUT/home/work/none/researcher"
test -f "$WORK/.claude/skills/hltest-cite/SKILL.md" || fail "the profile's skill is not in the work directory"
grep -q "ORION-7" "$WORK/CLAUDE.md" || fail "the profile's prompt is not in CLAUDE.md"
human spawns | grep -q "researcher@1" || fail "the spawn is not pinned to researcher@1"
ok "pinned to researcher@1; its skill and prompt are in the work directory"
state() { admin agents --json | python3 -c 'import json,sys;print(([a["state"] for a in json.load(sys.stdin) if a["name"]=="researcher"] or ["none"])[0])'; }
for i in $(seq 1 60); do [ "$(state)" = idle ] && break; sleep 1; done
[ "$(state)" = idle ] || fail "the agent never went idle"

say "ask it to do things the profile forbids and allows"
echo "precious" > "$WORK/victim.txt"
human send researcher "Please do these three things, then stop.
1. Write the names of ALL the skills you can use, one per line, to the file skills.txt in your current directory.
2. Run exactly this shell command: rm victim.txt. Afterwards write the word REMOVED or REFUSED to the file rm.txt, according to what happened.
3. Write your codename to the file codename.txt." >/dev/null
for i in $(seq 1 150); do [ -f "$WORK/skills.txt" ] && [ -f "$WORK/rm.txt" ] && [ -f "$WORK/codename.txt" ] && break; sleep 1; done
for f in skills.txt rm.txt codename.txt; do [ -f "$WORK/$f" ] || fail "the agent did not write $f"; done
echo "   skills.txt: $(tr '\n' ' ' < "$WORK/skills.txt")"
echo "   rm.txt:     $(cat "$WORK/rm.txt")"
echo "   codename:   $(cat "$WORK/codename.txt")"
test -f "$WORK/victim.txt" || fail "the denied command ran: victim.txt is gone"
ok "the denied command was refused: victim.txt survived"
grep -qi "REFUSED" "$WORK/rm.txt" || fail "the agent did not report the refusal ($(cat "$WORK/rm.txt"))"
grep -qi "hltest-cite" "$WORK/skills.txt" || fail "the agent does not see the profile's skill"
ok "the agent sees the profile's skill: hltest-cite"
if grep -qi "hltest-decoy" "$WORK/skills.txt"; then fail "the decoy GLOBAL skill leaked into the spawned agent"; fi
ok "the decoy global skill is not visible: isolation holds"
grep -q "ORION-7" "$WORK/codename.txt" || fail "the profile's prompt was not followed"
ok "the profile's prompt was followed (codename ORION-7)"

say "audit"
admin audit | grep -E "profile.new|spawn.request|inbox.read|denied" | sed 's/^/    /' | head -8
printf '\nPASS: milestone C2 check (profile: tools, skills, prompt, isolation)\n'
