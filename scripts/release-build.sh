#!/usr/bin/env bash
# Builds the release files: one static binary per platform in a tar.gz, and checksums.txt.
#   scripts/release-build.sh v0.1.0 dist            all platforms
#   TARGETS="linux/amd64" scripts/release-build.sh v0.1.0 dist
# The same script runs in CI (.github/workflows/release.yml), so a release is what you can build locally.
set -euo pipefail
VERSION="${1:?usage: release-build.sh <version, such as v0.1.0> <output dir>}"; OUT="${2:?output dir}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
rm -rf "$OUT"; mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
cd "$ROOT"
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  work="$(mktemp -d)"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$GO" build -trimpath \
    -ldflags "-s -w -X handloom/internal/cli.Version=$VERSION" -o "$work/handloom" ./cmd/handloom
  cp LICENSE README.md "$work/"
  if [ "$os" = windows ]; then
    mv "$work/handloom" "$work/handloom.exe"
    (cd "$work" && python3 -c "import sys, zipfile; z = zipfile.ZipFile(sys.argv[1], 'w', zipfile.ZIP_DEFLATED); [z.write(f) for f in ('handloom.exe', 'LICENSE', 'README.md')]; z.close()" "$OUT/handloom_${VERSION}_${os}_${arch}.zip")
    name="handloom_${VERSION}_${os}_${arch}.zip"
  else
    tar -C "$work" -czf "$OUT/handloom_${VERSION}_${os}_${arch}.tar.gz" handloom LICENSE README.md
    name="handloom_${VERSION}_${os}_${arch}.tar.gz"
  fi
  rm -rf "$work"
  echo "built $name"
done
cp "$ROOT/install.sh" "$OUT/install.sh"
# .deb and .rpm packages too, when nfpm is installed (CI installs it)
if [ "${PACKAGES:-}" = 1 ]; then "$ROOT/scripts/package-linux.sh" "$VERSION" "$OUT"; fi
(cd "$OUT" && files="$(ls *.tar.gz *.zip *.deb *.rpm install.sh 2>/dev/null || true)" && { command -v sha256sum >/dev/null && sha256sum $files || shasum -a 256 $files; } > checksums.txt)
echo "wrote $OUT/checksums.txt"
