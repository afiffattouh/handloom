#!/usr/bin/env bash
# install.sh against a local fake release server, in a temporary HOME, with systemd switched off
# (HANDLOOM_NO_SYSTEMD=1: the case of WSL and of plain containers). It checks: --help; a tampered
# download installs nothing; `install.sh join` installs, joins a real local hub, starts the link in the
# background and writes ONE login line; running it again changes nothing; `link stop` / `link start`;
# `link uninstall` stops the link and removes the line. Needs: go, curl, python3, tar, sha256sum.
#   test/e2e/install_nosystemd.sh
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/install_nosystemd"; PORT="${PORT:-17840}"; RPORT="${RPORT:-17841}"; H="http://127.0.0.1:$PORT"
fails=0; ok() { echo "   ok: $*"; }; bad() { echo "   FAIL: $*"; fails=$((fails+1)); }; say() { printf '\n== %s\n' "$*"; }
rm -rf "$OUT"; mkdir -p "$OUT/rel" "$OUT/bin" "$OUT/data" "$OUT/home" "$OUT/hubhome"
case "$(uname -m)" in x86_64) A=amd64;; *) A=arm64;; esac
V=v0.0.0-e2e; ASSET="handloom_${V}_linux_${A}.tar.gz"

say "build a fake release and serve it"
(cd "$ROOT" && "$GO" build -o "$OUT/handloom" ./cmd/handloom) || exit 1
tar -czf "$OUT/rel/$ASSET" -C "$OUT" handloom
(cd "$OUT/rel" && sha256sum "$ASSET" > checksums.txt)
(cd "$OUT/rel" && exec python3 -m http.server "$RPORT" --bind 127.0.0.1 >/dev/null 2>&1) & WEB=$!
HUB=""; cleanup() { kill "$HUB" "$WEB" 2>/dev/null; HOME="$OUT/home" HANDLOOM_HOME="$OUT/home/.config/handloom" "$OUT/bin/handloom" link stop >/dev/null 2>&1; true; }; trap cleanup EXIT
for i in $(seq 1 40); do curl -sf "http://127.0.0.1:$RPORT/checksums.txt" >/dev/null && break; sleep 0.25; done
HANDLOOM_ADDR="127.0.0.1:$PORT" HANDLOOM_HOME="$OUT/hubhome" "$OUT/handloom" hub serve --auto-init --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB=$!
for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
ADMIN="$(grep -o 'hva_[A-Za-z0-9_-]*' "$OUT/hub.log" | head -1)"
newtoken() { HANDLOOM_HUB="$H" HANDLOOM_TOKEN="$ADMIN" HANDLOOM_HOME="$OUT/hubhome" "$OUT/handloom" device add "$1" --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])'; }

export HOME="$OUT/home" HANDLOOM_HOME="$OUT/home/.config/handloom" HANDLOOM_NO_SYSTEMD=1 HANDLOOM_NO_TTY=1 SHELL=/bin/bash
export HANDLOOM_RELEASE_URL="http://127.0.0.1:$RPORT" HANDLOOM_VERSION="$V" HANDLOOM_INSTALL_DIR="$OUT/bin"
printf '# my own line\nexport KEEP=1\n' > "$HOME/.profile"
profile_lines() { grep -c 'link start --quiet' "$HOME/.profile"; }

say "install.sh --help"
sh "$ROOT/install.sh" --help | grep -q "install.sh join <hub-url> <token>" && ok "--help prints the usage" || bad "--help"
U="$(sh "$ROOT/install.sh" join 2>&1)"; echo "$U" | grep -q "usage:" && ok "join with no arguments says how to use it" || bad "join usage"

say "a download that does not match its checksum installs nothing"
cp "$OUT/rel/checksums.txt" "$OUT/checksums.good"
echo "0000000000000000000000000000000000000000000000000000000000000000  $ASSET" > "$OUT/rel/checksums.txt"
HANDLOOM_INSTALL_DIR="$OUT/bin/none" sh "$ROOT/install.sh" > "$OUT/bad.log" 2>&1
[ ! -e "$OUT/bin/none/handloom" ] && grep -q "checksum does not match" "$OUT/bad.log" && ok "tampered download refused, nothing installed" || bad "tampered download was not refused"
cp "$OUT/checksums.good" "$OUT/rel/checksums.txt"

say "install.sh join, with no systemd"
TOK="$(newtoken e2e-box)"
sh "$ROOT/install.sh" join "$H" "$TOK" > "$OUT/join1.log" 2>&1; echo "   (join exit code $?: 1 if this box has no signed-in agent CLI, which is fine here)"
grep -q "Checksum ok" "$OUT/join1.log" && ok "checksum verified" || bad "no checksum line"
grep -q "joined as e2e-box" "$OUT/join1.log" && ok "joined" || bad "not joined"
grep -q "running in the background" "$OUT/join1.log" && ok "the link was started in the background" || bad "link not started"
"$OUT/bin/handloom" link status >/dev/null 2>&1 && ok "the link answers on its socket" || bad "the link is not running"
[ -s "$HANDLOOM_HOME/link.pid" ] && ok "pid file written" || bad "no pid file"
[ "$(profile_lines)" = 1 ] && grep -q "KEEP=1" "$HOME/.profile" && ok "one login line added, the person's own lines kept" || bad "profile: $(cat "$HOME/.profile")"
grep -q "^Ready: this machine can run agents\|^Not ready yet: " "$OUT/join1.log" && ok "the check answered in plain words (first line ready or not)" || bad "no plain check output"
PID1="$(cat "$HANDLOOM_HOME/link.pid")"

say "again: nothing changes"
TOK2="$(newtoken e2e-box2)"
sh "$ROOT/install.sh" join "$H" "$TOK2" > "$OUT/join2.log" 2>&1
grep -q "already joined as e2e-box" "$OUT/join2.log" && ok "second run keeps the credential" || bad "second run"
grep -q "already running" "$OUT/join2.log" && ok "the link is not started twice" || bad "link restarted"
[ "$(profile_lines)" = 1 ] && ok "still one login line" || bad "login line duplicated: $(profile_lines)"
[ "$(cat "$HANDLOOM_HOME/link.pid")" = "$PID1" ] && ok "same process" || bad "process changed"

say "link stop, then link start (what the login line runs)"
"$OUT/bin/handloom" link stop | grep -q "Stopped the link" && ok "stopped" || bad "stop"
sleep 1; "$OUT/bin/handloom" link status >/dev/null 2>&1 && bad "still running after stop" || ok "no longer answering"
sh -c '. "$HOME/.profile"'; sleep 1
"$OUT/bin/handloom" link status >/dev/null 2>&1 && ok "sourcing the login file started it again" || bad "the login line did not start the link"
sh -c '. "$HOME/.profile"'; [ "$(grep -c . "$HANDLOOM_HOME/link.pid")" = 1 ] && ok "sourcing it twice is harmless" || bad "pid file"

say "link uninstall"
"$OUT/bin/handloom" link uninstall > "$OUT/un.log" 2>&1; cat "$OUT/un.log" | sed 's/^/    /'
sleep 1
"$OUT/bin/handloom" link status >/dev/null 2>&1 && bad "link still running" || ok "link stopped"
[ "$(profile_lines)" = 0 ] && [ "$(cat "$HOME/.profile")" = "$(printf '# my own line\nexport KEEP=1')" ] && ok "login line removed, the rest of the file is as it was" || bad "profile after uninstall: $(cat "$HOME/.profile")"

echo; [ "$fails" = 0 ] && echo "ALL OK" || { echo "$fails FAILED"; exit 1; }
