#!/bin/sh
# Installs the handloom binary (and the short name, hl) from a GitHub release.
#   curl -fsSL https://raw.githubusercontent.com/afiffattouh/handloom/main/install.sh | sh
#   HANDLOOM_VERSION=v0.1.0 sh install.sh        a specific release (default: the latest)
#   HANDLOOM_INSTALL_DIR=/opt/bin sh install.sh  where to put it (default: /usr/local/bin if you can write there, else ~/.local/bin)
#   curl -fsSL .../install.sh | sh -s -- join <hub-url> <join-token>   install, then join this machine to a hub in one go
#   sh install.sh --help
# It checks the download against the release's checksums.txt and installs nothing if they differ.
# It never uses sudo: if you want /usr/local/bin, run it as a user who can write there, or set HANDLOOM_INSTALL_DIR.
# Linux and macOS only, in plain POSIX sh (no GNU-only options). Windows: use install.ps1 (it runs this inside WSL).
set -eu

REPO="afiffattouh/handloom"
usage() {
  cat <<'HELP'
Installs Handloom (the handloom program and its short name, hl) from a GitHub release.

  sh install.sh                          install the latest release
  sh install.sh join <hub-url> <token>   install, then join this machine to your hub and start its link
                                         (the web page Machines, Add a machine, shows this line ready to paste)
  sh install.sh --help                   this text

  join also takes:  --no-service  keep the link running in the background instead of as a service
                    --no-start    only join; do not start the link or check the machine

Settings (environment):
  HANDLOOM_VERSION=v0.1.0       install a specific release (default: the latest)
  HANDLOOM_INSTALL_DIR=/opt/bin where to put it (default: /usr/local/bin if you can write there, else ~/.local/bin)

It checks the download against the release's checksums.txt (and its Sigstore signature if cosign is
installed) and installs nothing if they differ. It never uses sudo. On Windows, use install.ps1.
HELP
}
case "${1:-}" in -h|--help|help) usage; exit 0 ;; esac
mode=install; JOIN_HUB=""; JOIN_TOKEN=""
if [ "${1:-}" = join ]; then
  [ $# -ge 3 ] || { printf 'usage: sh install.sh join <hub-url> <join-token> [--no-service] [--no-start]\n' >&2; exit 1; }
  mode=join; JOIN_HUB="$2"; JOIN_TOKEN="$3"; shift 3   # what is left (flags) goes to handloom join
fi
say() { printf '%s\n' "$*"; }
die() { printf 'handloom install: %s\n' "$*" >&2; exit 1; }

os="$(uname -s | tr 'A-Z' 'a-z')"
case "$os" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*) die "this is Windows. Use install.ps1 in PowerShell: it sets up Linux (WSL) and runs this inside it. See https://github.com/$REPO#windows" ;;
  *) die "this install script supports Linux and macOS (found: $os)." ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "no build for the processor $(uname -m); build from source: go build ./cmd/handloom" ;;
esac

if command -v curl >/dev/null 2>&1; then fetch() { curl -fsSL "$1" -o "$2"; }; final_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then fetch() { wget -q "$1" -O "$2"; }; final_url() { wget -q --server-response --spider "$1" 2>&1 | awk '/Location:/ {u=$2} END {print u}'; }
else die "need curl or wget"; fi

version="${HANDLOOM_VERSION:-latest}"
base="${HANDLOOM_RELEASE_URL:-}"   # a mirror, or a test server; default is GitHub
if [ -z "$base" ]; then
  if [ "$version" = latest ]; then
    version="$(final_url "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')"
    case "$version" in v*) ;; *) die "there is no full release yet (a pre-release does not count as the latest). Pick one from https://github.com/$REPO/releases and run again with HANDLOOM_VERSION=<tag>, or build from source." ;; esac
  fi
  base="https://github.com/$REPO/releases/download/$version"
elif [ "$version" = latest ]; then
  die "with HANDLOOM_RELEASE_URL, also set HANDLOOM_VERSION"
fi

asset="handloom_${version}_${os}_${arch}.tar.gz"
tmp="$(mktemp -d 2>/dev/null || mktemp -d -t handloom)" || die "cannot make a temporary folder"; trap 'rm -rf "$tmp"' EXIT
say "Downloading handloom $version for $os/$arch ..."
fetch "$base/$asset" "$tmp/$asset" || die "could not download $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download the checksums"

# If cosign is installed, the checksums themselves are checked against their Sigstore signature first.
if command -v cosign >/dev/null 2>&1 && [ -z "${HANDLOOM_SKIP_SIGNATURE:-}" ] && [ -z "${HANDLOOM_RELEASE_URL:-}" ]; then
  if fetch "$base/checksums.txt.sigstore.json" "$tmp/checksums.txt.sigstore.json" 2>/dev/null; then
    cosign verify-blob --bundle "$tmp/checksums.txt.sigstore.json" \
      --certificate-identity-regexp "^https://github.com/$REPO/.github/workflows/release.yml@refs/" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com "$tmp/checksums.txt" >/dev/null 2>&1 \
      || die "the signature on checksums.txt does not verify. Nothing was installed."
    say "Signature ok (Sigstore)."
  else
    say "Note: this release has no signature file; checking the checksum only."
  fi
fi

want="$(awk -v f="$asset" '$2 == f || $2 == "*" f {print $1}' "$tmp/checksums.txt")"
[ -n "$want" ] || die "$asset is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
else die "need sha256sum or shasum to check the download"; fi
[ "$got" = "$want" ] || die "the checksum does not match (expected $want, got $got). Nothing was installed."
say "Checksum ok."

tar -xzf "$tmp/$asset" -C "$tmp" handloom
dir="${HANDLOOM_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir" || die "cannot create $dir"
install -m 0755 "$tmp/handloom" "$dir/handloom" || die "cannot write to $dir (set HANDLOOM_INSTALL_DIR to a folder you own)"
# hl is the short name, unless something else already owns it
if [ -e "$dir/hl" ] && [ "$(readlink "$dir/hl" 2>/dev/null)" != "handloom" ]; then
  say "Note: $dir/hl already exists; not replacing it."
else
  ln -sf handloom "$dir/hl"
fi

say "Installed: $("$dir/handloom" version)"
case ":$PATH:" in *":$dir:"*) ;; *) say "Note: $dir is not on your PATH. Add it: export PATH=\"$dir:\$PATH\"" ;; esac
if [ "$mode" = join ]; then
  # Piped from curl, this script is the shell's input, so the program would have no one to ask (it may
  # want to install git or tmux, and asks first). If there is a terminal, hand it over; if not, it
  # only prints the commands.
  if [ ! -t 0 ] && [ -z "${HANDLOOM_NO_TTY:-}" ] && (: </dev/tty) 2>/dev/null; then
    # the program is not on PATH yet in this shell, and the service it installs copies PATH: put its folder first
    PATH="$dir:$PATH" exec "$dir/handloom" join "$JOIN_HUB" "$JOIN_TOKEN" "$@" </dev/tty
  fi
  PATH="$dir:$PATH" exec "$dir/handloom" join "$JOIN_HUB" "$JOIN_TOKEN" "$@"
fi
cat <<NEXT

Next:
  handloom join <hub-url> <join-token>        join this machine to your hub and start its link (token: web UI, Machines, Add a machine)
  handloom login <hub-url>                    sign in for the terminal console, then: handloom tui
  handloom doctor [--fix]                     check git, tmux and your agent CLIs, and install git and tmux if they are missing
Docs: https://github.com/$REPO#quick-start
NEXT
