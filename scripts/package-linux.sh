#!/usr/bin/env bash
# Builds .deb and .rpm packages from the linux tarballs in a release directory (needs nfpm on the PATH).
#   scripts/package-linux.sh v0.1.0 dist
set -euo pipefail
VERSION="${1:?usage: package-linux.sh <version> <release dir>}"; DIR="${2:?release dir}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; DIR="$(cd "$DIR" && pwd)"
command -v nfpm >/dev/null || { echo "nfpm is needed: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest" >&2; exit 1; }
for arch in amd64 arm64; do
  tgz="$DIR/handloom_${VERSION}_linux_${arch}.tar.gz"; [ -f "$tgz" ] || continue
  work="$(mktemp -d)"; tar -xzf "$tgz" -C "$work" handloom
  for fmt in deb rpm; do
    # fill the config's ${...} placeholders ourselves: nfpm does not expand them in every field
    sed -e "s|\${VERSION}|${VERSION#v}|g" -e "s|\${ARCH}|$arch|g" -e "s|\${BINARY}|$work/handloom|g" -e "s|\${ROOT}|$ROOT|g" \
      "$ROOT/packaging/nfpm.yaml" > "$work/nfpm.yaml"
    nfpm package --config "$work/nfpm.yaml" --packager "$fmt" --target "$DIR/handloom_${VERSION}_linux_${arch}.$fmt" >/dev/null
    echo "built handloom_${VERSION}_linux_${arch}.$fmt"
  done
  rm -rf "$work"
done
