#!/usr/bin/env bash
# Set up handloom on a server: copy the binary, then make the server the hub or
# join it to a hub as a device, running as a systemd service.
#
# Run it from this repo, on a machine that can ssh to the server.
#
#   scripts/setup.sh hub    <host> [--addr IP:PORT] [--data DIR]
#   scripts/setup.sh device <host> --hub URL [--name NAME] [--join-token TOKEN] [--rejoin]
#   scripts/setup.sh status <host>
#   scripts/setup.sh remove <host> [--purge]
#
# <host> is an ssh name, or "local" for this machine.
#
# hub      Installs handloom, creates the database if there is none (the admin
#          token is printed once: store it), and starts the hub service.
#          The default address is the server's Tailscale IP, port 7420.
# device   Installs handloom, joins the hub and starts the link service. It needs
#          a join token: pass --join-token, or set HANDLOOM_TOKEN to the admin
#          token and the script creates the device for you.
# status   Shows what is installed and running.
# remove   Stops and removes the services and the binary. The hub's data is
#          never deleted. --purge also deletes the device credential.
#
# Options for all: --binary FILE (use this handloom binary instead of building),
#                  --bin-dir DIR (where to install; default /usr/local/bin for
#                  root, ~/.local/bin otherwise).
#
# handloom speaks plain HTTP. Keep the hub on a private network (Tailscale,
# WireGuard): anyone who can reach it can in effect run code on every
# connected machine.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
usage() { sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit "${1:-2}"; }
say()  { printf '\n== %s\n' "$*"; }
fail() { printf '\nsetup: %s\n' "$*" >&2; exit 1; }

[ $# -ge 1 ] || usage
case "$1" in -h | --help | help) usage 0 ;; esac
[ $# -ge 2 ] || usage
MODE="$1" HOST="$2"
shift 2
ADDR="" DATA="" HUB="${HANDLOOM_HUB:-}" NAME="" JOIN="" REJOIN="" PURGE="" BINARY="" BIN_DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --addr) ADDR="$2"; shift 2 ;;
    --data) DATA="$2"; shift 2 ;;
    --hub) HUB="${2%/}"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --join-token) JOIN="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --rejoin) REJOIN=1; shift ;;
    --purge) PURGE=1; shift ;;
    *) fail "unknown option $1 (see --help)" ;;
  esac
done

# ---- running things on the server ----
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=10)
if [ "$HOST" = local ]; then
  on() { bash -c "$1"; }                  # run a shell line on the server
  put() { install -m 755 "$1" "$2"; }     # copy a file there, executable
else
  on() { "${SSH[@]}" "$HOST" "$1"; }
  put() {
    scp -q -o BatchMode=yes "$1" "$HOST:$2.new"
    on "$(printf 'chmod 755 %q && mv -f %q %q' "$2.new" "$2.new" "$2")"
  }
fi
q() { printf '%q ' "$@"; } # quote words for the server's shell

on true 2>/dev/null || fail "cannot reach $HOST over ssh (ssh $HOST must work without a password prompt)"
read -r OS ARCH <<<"$(on 'uname -s -m')"
[ "$OS" = Linux ] || fail "$HOST runs $OS; this script supports Linux servers only"
case "$ARCH" in
  x86_64) GOARCH=amd64 ;;
  aarch64 | arm64) GOARCH=arm64 ;;
  *) fail "unknown CPU $ARCH on $HOST" ;;
esac
REMOTE_UID="$(on 'id -u')"
REMOTE_HOME="$(on 'printf %s "$HOME"')"
if [ -z "$BIN_DIR" ]; then
  if [ "$REMOTE_UID" = 0 ]; then BIN_DIR=/usr/local/bin; else BIN_DIR="$REMOTE_HOME/.local/bin"; fi
fi
HANDLOOM_BIN="$BIN_DIR/handloom"
SERVER="$HOST ($(on 'hostname -s'), linux/$GOARCH, $(on 'id -un'))"

# ---- the binary ----
find_go() {
  if [ -n "${GO:-}" ]; then echo "$GO"; return; fi
  command -v go 2>/dev/null || { [ -x "$HOME/.local/go/bin/go" ] && echo "$HOME/.local/go/bin/go"; } ||
    fail "Go is not installed here. Install Go, or pass --binary with a handloom binary built for linux/$GOARCH"
}
build() { # build <goos> <goarch> <output>
  local go version
  go="$(find_go)"
  version="$(cd "$ROOT" && git describe --always --dirty 2>/dev/null || echo dev)"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" "$go" build -ldflags "-X handloom/internal/cli.Version=$version" -o "$3" ./cmd/handloom)
}
install_binary() {
  say "install handloom on $SERVER"
  local file="$BINARY"
  if [ -z "$file" ]; then
    file="$ROOT/dist/handloom-linux-$GOARCH"
    build linux "$GOARCH" "$file"
  fi
  on "mkdir -p $(q "$BIN_DIR")"
  put "$file" "$HANDLOOM_BIN"
  echo "$HANDLOOM_BIN: $(on "$(q "$HANDLOOM_BIN" version)")"
  on "command -v handloom >/dev/null 2>&1" ||
    echo "Note: $BIN_DIR is not on the PATH of a login shell on $HOST. Agents call \`handloom\`, so add it: export PATH=$BIN_DIR:\$PATH"
}
# A handloom binary for this machine, to talk to the hub as admin.
local_handloom() {
  if [ "$HOST" = local ]; then echo "$HANDLOOM_BIN"; return; fi
  [ -x "$ROOT/bin/handloom" ] || build "$(uname -s | tr A-Z a-z)" "$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')" "$ROOT/bin/handloom"
  echo "$ROOT/bin/handloom"
}

case "$MODE" in
hub)
  install_binary
  if [ -z "$DATA" ]; then
    if [ "$REMOTE_UID" = 0 ]; then DATA=/var/lib/handloom; else DATA="$REMOTE_HOME/.local/share/handloom"; fi
  fi
  if [ -z "$ADDR" ]; then
    ip="$(on 'tailscale ip -4 2>/dev/null | head -1' || true)"
    if [ -n "$ip" ]; then
      ADDR="$ip:7420"
    else
      ADDR=127.0.0.1:7420
      echo "Note: no Tailscale address found on $HOST; the hub will listen on $ADDR only. Pass --addr for a private address."
    fi
  fi
  say "hub database in $DATA"
  if on "test -f $(q "$DATA/handloom.db")"; then
    echo "A database is already there; keeping it. The admin token was printed when it was created."
  else
    on "$(q "$HANDLOOM_BIN" hub init --data "$DATA")"
    echo
    echo ">>> Store the admin token above now. It is not saved anywhere and cannot be shown again."
  fi
  say "hub service on $ADDR"
  on "$(q "$HANDLOOM_BIN" hub install --data "$DATA" --addr "$ADDR")"
  for _ in $(seq 25); do
    health="$(on "curl -fsS http://$ADDR/healthz 2>/dev/null" || true)"
    [ -n "$health" ] && break
    sleep 0.4
  done
  [ -n "${health:-}" ] || fail "the hub does not answer on http://$ADDR/healthz; see: journalctl -u handloom-hub"
  echo "http://$ADDR/healthz -> $health"
  cat <<EOF

The hub is running. Next:
  export HANDLOOM_HUB=http://$ADDR HANDLOOM_TOKEN=<the admin token>
  handloom human add <your name>                         # a token for you
  scripts/setup.sh device <host> --hub http://$ADDR   # add each machine, this one included
EOF
  ;;

device)
  [ -n "$HUB" ] || fail "device needs --hub URL (or HANDLOOM_HUB)"
  install_binary
  say "hub $HUB"
  health="$(on "curl -fsS $(q "$HUB/healthz") 2>/dev/null" || true)"
  [ -n "$health" ] || fail "$HOST cannot reach $HUB/healthz. Check the address and that both machines are on the same private network."
  echo "reachable from $HOST: $health"

  say "join"
  if [ -z "$REJOIN" ] && on 'test -f "${HANDLOOM_HOME:-$HOME/.config/handloom}/link.json"'; then
    echo "$HOST has already joined a hub; keeping its credential (--rejoin to join again)."
  else
    if [ -z "$JOIN" ]; then
      [ -n "${HANDLOOM_TOKEN:-}" ] || fail "no join token: pass --join-token, or set HANDLOOM_TOKEN to the admin token so the device can be created"
      [ -n "$NAME" ] || NAME="$(on 'hostname -s')"
      lh="$(local_handloom)"
      if ! out="$(HANDLOOM_HUB="$HUB" HANDLOOM_TOKEN="$HANDLOOM_TOKEN" "$lh" device add "$NAME" --json 2>&1)"; then
        fail "could not create device $NAME on the hub: $out
If a device with that name exists, pick another with --name, or revoke it first: handloom device revoke $NAME"
      fi
      JOIN="$(sed -n 's/.*"token": "\(.*\)".*/\1/p' <<<"$out")"
      echo "created device $NAME on the hub"
    fi
    on "$(q "$HANDLOOM_BIN" link join "$HUB" "$JOIN")" | sed '/^Start the link with/d'
  fi

  say "link service"
  on "$(q "$HANDLOOM_BIN" link install)"
  ok=""
  for _ in $(seq 25); do
    if status="$(on "$(q "$HANDLOOM_BIN" link status)" 2>/dev/null)"; then ok=1; break; fi
    sleep 0.4
  done
  [ -n "$ok" ] || fail "the link did not start; see: journalctl -u handloom-link (or --user -u handloom-link)"
  echo "$status"
  cat <<EOF

$HOST is connected. To add an agent there, in the project it will work in:
  handloom adapter install claude --name <agent> --dir .     # or codex, pi, omp, opencode
  tmux new -s <agent> claude                              # the install prints the exact start line per kind
Give one agent the lead role (admin or human token): handloom agent role <agent> lead
EOF
  ;;

status)
  say "$SERVER"
  on "
    if [ -x $(q "$HANDLOOM_BIN") ]; then echo \"binary:  $HANDLOOM_BIN (\$($(q "$HANDLOOM_BIN") version))\"; else echo 'binary:  not installed'; fi
    user=''; [ \"\$(id -u)\" = 0 ] || user='--user'
    for s in handloom-hub handloom-link; do
      en=\$(systemctl \$user is-enabled \$s 2>/dev/null) || en=''
      if [ -n \"\$en\" ] && [ \"\$en\" != not-found ]; then echo \"\$s: \$(systemctl \$user is-active \$s 2>/dev/null || true), \$en at boot\"; else echo \"\$s: not installed\"; fi
    done
    [ -x $(q "$HANDLOOM_BIN") ] && $(q "$HANDLOOM_BIN") link status 2>&1 || true
  "
  ;;

remove)
  say "remove handloom from $SERVER"
  on "
    if [ -x $(q "$HANDLOOM_BIN") ]; then $(q "$HANDLOOM_BIN") link uninstall; $(q "$HANDLOOM_BIN") hub uninstall; rm -f $(q "$HANDLOOM_BIN"); echo 'Removed $HANDLOOM_BIN.'; else echo 'handloom is not installed in $BIN_DIR.'; fi
  "
  if [ -n "$PURGE" ]; then
    on 'd="${HANDLOOM_HOME:-$HOME/.config/handloom}"; if [ -d "$d" ]; then rm -rf "$d"; echo "Deleted the device credential in $d."; fi'
    echo "Revoke the device on the hub as well: handloom device revoke <name>"
  else
    echo "The device credential (~/.config/handloom) is kept; --purge deletes it."
  fi
  echo "Hub data, if this server was the hub, is not deleted."
  ;;

*) usage ;;
esac
