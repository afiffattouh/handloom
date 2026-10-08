#!/bin/sh
# Installs the handloom binary (and the short name, hl) from a GitHub release.
#   curl -fsSL https://raw.githubusercontent.com/afiffattouh/handloom/main/install.sh | sh
#   HANDLOOM_VERSION=v0.1.0 sh install.sh        a specific release (default: the latest)
#   HANDLOOM_INSTALL_DIR=/opt/bin sh install.sh  where to put it (default: /usr/local/bin if you can write there, else ~/.local/bin)
# It checks the download against the release's checksums.txt and installs nothing if they differ.
# It never uses sudo: if you want /usr/local/bin, run it as a user who can write there, or set HANDLOOM_INSTALL_DIR.
set -eu

REPO="afiffattouh/handloom"
say() { printf '%s\n' "$*"; }
die() { printf 'handloom install: %s\n' "$*" >&2; exit 1; }

os="$(uname -s | tr 'A-Z' 'a-z')"
case "$os" in linux|darwin) ;; *) die "this install script supports Linux and macOS (found: $os). Windows is not supported yet." ;; esac
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
    case "$version" in v*) ;; *) die "could not find the latest release (is there one yet?). Try HANDLOOM_VERSION=v0.1.0, or build from source." ;; esac
  fi
  base="https://github.com/$REPO/releases/download/$version"
elif [ "$version" = latest ]; then
  die "with HANDLOOM_RELEASE_URL, also set HANDLOOM_VERSION"
fi

asset="handloom_${version}_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
say "Downloading handloom $version for $os/$arch ..."
fetch "$base/$asset" "$tmp/$asset" || die "could not download $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download the checksums"

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
cat <<NEXT

Next:
  handloom link join <hub-url> <join-token>   join this machine to your hub (token: web UI, Machines, Add a machine)
  handloom login <hub-url>                    sign in for the terminal console, then: handloom tui
  handloom doctor                             check git, tmux and your agent CLIs
Docs: https://github.com/$REPO#quick-start
NEXT
