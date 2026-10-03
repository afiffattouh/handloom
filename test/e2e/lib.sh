# Shared setup for the two-machine end-to-end scenarios. Source it.
#
# Everything it starts lives in tmux servers on a dedicated socket
# (`tmux -L hltest`) and under one directory per machine, so it never
# touches other sessions or files. Names are prefixed "hltest-".
#
# Settings (environment):
#   REMOTE      ssh name of the second machine            (default mantis)
#   HUB_ADDR    address the hub listens on; the remote     (default: this machine's
#               machine must reach it                       Tailscale IP, port 7420)
#   REMOTE_DIR  work directory on the remote machine       (default /root/hltest)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REMOTE="${REMOTE:-mantis}"
REMOTE_DIR="${REMOTE_DIR:-/root/hltest}"
if [ -z "${HUB_ADDR:-}" ]; then
  HUB_ADDR="$(tailscale ip -4 2>/dev/null | head -1):7420"
fi
HUB_URL="http://$HUB_ADDR"
LOCAL_DEVICE="hltest-$(hostname -s)"
REMOTE_DEVICE="hltest-$REMOTE"
TMUX_L="tmux -L hltest"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o ControlMaster=auto -o ControlPersist=120 -o ControlPath=/tmp/hltest-ssh-%C"

say()  { printf '\n== %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

# handloom as admin or human, straight to the hub.
admin() { HANDLOOM_HUB="$HUB_URL" HANDLOOM_TOKEN="$ADMIN_TOKEN" "$BIN" "$@"; }
human() { HANDLOOM_HUB="$HUB_URL" HANDLOOM_TOKEN="$HUMAN_TOKEN" "$BIN" "$@"; }

# handloom as an agent on this machine / on the remote machine.
# usage: lhl <agent> <args...>     rhl <agent> <args...>
lhl() {
  local a="$1"; shift
  env -u HERDR_ENV -u HERDR_PANE_ID -u TMUX -u TMUX_PANE HANDLOOM_HOME="$OUT/home" HANDLOOM_AGENT="$a" "$BIN" "$@"
}
rhl() {
  local a="$1"; shift
  $SSH "$REMOTE" "$(printf '%q ' env HANDLOOM_HOME="$REMOTE_DIR/home" HANDLOOM_AGENT="$a" "$REMOTE_DIR/bin/handloom" "$@")"
}
rsh() { $SSH "$REMOTE" "$@"; }

# show runs a command, printing it first, so the transcript is the evidence.
# usage: show <label> lhl|rhl <agent> <args...>   or   show <label> admin|human <args...>
show() {
  local who="$1"; shift
  printf '\n[%s] $ handloom %s\n' "$who" "$(handloom_args "$@")"
  "$@" 2>&1 | sed 's/^/    /'
  return "${PIPESTATUS[0]}"
}

# handloom_args prints the handloom arguments of a helper call, for the transcript.
handloom_args() {
  case "$1" in
    admin | human) shift ;;
    *) shift 2 ;;
  esac
  echo "$*"
}

# rejected runs a command that must fail, and shows why it did.
rejected() {
  local who="$1"; shift
  printf '\n[%s] $ handloom %s\n' "$who" "$(handloom_args "$@")"
  local out
  if out="$("$@" 2>&1)"; then
    printf '%s\n' "$out" | sed 's/^/    /'
    fail "expected the hub to reject: handloom $(handloom_args "$@")"
  fi
  printf '%s\n    (rejected, as it must be)\n' "$out" | sed 's/^/    /'
}

e2e_cleanup() {
  $TMUX_L kill-server 2>/dev/null || true
  rsh "tmux -L hltest kill-server 2>/dev/null; true" 2>/dev/null || true
}

# e2e_setup <name> [hub flags...]: build, start the hub here, a link on both
# machines, and create one human. Leaves ADMIN_TOKEN and HUMAN_TOKEN set.
e2e_setup() {
  local name="$1"; shift
  OUT="$ROOT/test/e2e/out/$name"
  BIN="$ROOT/bin/handloom"

  say "build"
  local rarch
  case "$(rsh uname -m)" in
    x86_64) rarch=amd64 ;;
    aarch64 | arm64) rarch=arm64 ;;
    *) fail "unknown remote architecture" ;;
  esac
  (cd "$ROOT" && CGO_ENABLED=0 "$GO" build -o bin/handloom ./cmd/handloom &&
    CGO_ENABLED=0 GOOS=linux GOARCH="$rarch" "$GO" build -o "dist/handloom-linux-$rarch" ./cmd/handloom)
  echo "local:  $BIN ($("$BIN" version))"
  echo "remote: dist/handloom-linux-$rarch -> $REMOTE:$REMOTE_DIR/bin/handloom"

  say "clean start"
  e2e_cleanup
  rm -rf "$OUT"
  mkdir -p "$OUT/home"
  case "$(basename "$REMOTE_DIR")" in
    hltest*) ;;
    *) fail "REMOTE_DIR must be a directory named hltest*: it is deleted and recreated" ;;
  esac
  rsh "rm -rf $REMOTE_DIR && mkdir -p $REMOTE_DIR/bin $REMOTE_DIR/home"
  scp -q -o ControlPath=/tmp/hltest-ssh-%C "$ROOT/dist/handloom-linux-$rarch" "$REMOTE:$REMOTE_DIR/bin/handloom"
  if ss -ltn | grep -q ":${HUB_ADDR##*:}\b"; then
    fail "port ${HUB_ADDR##*:} is in use on this machine"
  fi

  say "hub on $HUB_ADDR"
  "$BIN" hub init --data "$OUT/hub" >"$OUT/hub-init.txt"
  ADMIN_TOKEN="$(grep '^hva_' "$OUT/hub-init.txt")"
  $TMUX_L new-session -d -s hltest-hub \
    "$(printf '%q ' "$BIN" hub serve --data "$OUT/hub" --addr "$HUB_ADDR" "$@") >$(printf '%q' "$OUT/hub.log") 2>&1"
  local i
  for i in $(seq 50); do
    curl -fsS "$HUB_URL/healthz" >/dev/null 2>&1 && break
    sleep 0.2
  done
  echo "this machine: $(curl -fsS "$HUB_URL/healthz")"
  echo "$REMOTE:      $(rsh "curl -fsS $HUB_URL/healthz")"

  say "devices and links"
  local join
  join="$(admin device add "$LOCAL_DEVICE" --json | sed -n 's/.*"token": "\(.*\)".*/\1/p')"
  HANDLOOM_HOME="$OUT/home" "$BIN" link join "$HUB_URL" "$join" | head -1
  $TMUX_L new-session -d -s hltest-link \
    "HANDLOOM_HOME=$(printf '%q' "$OUT/home") $(printf '%q' "$BIN") link run ${LINK_FLAGS:-} >$(printf '%q' "$OUT/link.log") 2>&1"

  join="$(admin device add "$REMOTE_DEVICE" --json | sed -n 's/.*"token": "\(.*\)".*/\1/p')"
  rsh "HANDLOOM_HOME=$REMOTE_DIR/home $REMOTE_DIR/bin/handloom link join $HUB_URL $join" | head -1
  rsh "tmux -L hltest new-session -d -s hltest-link 'HANDLOOM_HOME=$REMOTE_DIR/home $REMOTE_DIR/bin/handloom link run ${LINK_FLAGS:-} >$REMOTE_DIR/link.log 2>&1'"

  for i in $(seq 50); do
    [ -S "$OUT/home/link.sock" ] && rsh "test -S $REMOTE_DIR/home/link.sock" && break
    sleep 0.2
  done
  HANDLOOM_HOME="$OUT/home" "$BIN" link status
  rsh "HANDLOOM_HOME=$REMOTE_DIR/home $REMOTE_DIR/bin/handloom link status"

  HUMAN_TOKEN="$(admin human add hltest-human --json | sed -n 's/.*"token": "\(.*\)".*/\1/p')"
  admin device list
}

# task_status <id>
task_status() { human task show "$1" --json | sed -n 's/.*"status": "\(.*\)".*/\1/p'; }

expect_status() {
  local got
  got="$(task_status "$1")"
  [ "$got" = "$2" ] || fail "task $1 is $got, want $2"
  echo "    task #$1 is $got"
}
