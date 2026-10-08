#!/usr/bin/env bash
# Builds the release files: one static binary per platform in a tar.gz, and checksums.txt.
#   scripts/release-build.sh v0.1.0 dist            all platforms
#   TARGETS="linux/amd64" scripts/release-build.sh v0.1.0 dist
# The same script runs in CI (.github/workflows/release.yml), so a release is what you can build locally.
set -euo pipefail
VERSION="${1:?usage: release-build.sh <version, such as v0.1.0> <output dir>}"; OUT="${2:?output dir}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
rm -rf "$OUT"; mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
cd "$ROOT"
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  work="$(mktemp -d)"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$GO" build -trimpath \
    -ldflags "-s -w -X handloom/internal/cli.Version=$VERSION" -o "$work/handloom" ./cmd/handloom
  cp LICENSE README.md "$work/"
  tar -C "$work" -czf "$OUT/handloom_${VERSION}_${os}_${arch}.tar.gz" handloom LICENSE README.md
  rm -rf "$work"
  echo "built handloom_${VERSION}_${os}_${arch}.tar.gz"
done
cp "$ROOT/install.sh" "$OUT/install.sh"
(cd "$OUT" && { command -v sha256sum >/dev/null && sha256sum *.tar.gz install.sh || shasum -a 256 *.tar.gz install.sh; } > checksums.txt)
echo "wrote $OUT/checksums.txt"
