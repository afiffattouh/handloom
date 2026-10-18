#!/bin/sh
# Install factory-kit without Handloom.
#   sh install.sh               copy this kit to ${FACTORY_HOME:-$HOME/.local/share/factory-kit}
#                               and put a `factory` command in ${BIN_DIR:-$HOME/.local/bin}
#   KIT_DIR=/path sh install.sh install from another checkout or unpacked tarball
#   sh install.sh --uninstall   remove both
# Safe to run again. Needs Python 3.8 or newer when you run `factory`, not to install.
set -eu

home_dir="${FACTORY_HOME:-${HOME:?HOME is not set}/.local/share/factory-kit}"
bin_dir="${BIN_DIR:-${HOME:?HOME is not set}/.local/bin}"
launcher="$bin_dir/factory"

if [ "${1:-}" = "--uninstall" ]; then
  if [ -f "$launcher" ] && grep -q "factory-kit launcher" "$launcher" 2>/dev/null; then
    rm -f "$launcher"
    echo "removed $launcher"
  fi
  if [ -f "$home_dir/.factory-kit-install" ]; then
    rm -rf "$home_dir"
    echo "removed $home_dir"
  else
    echo "nothing to remove at $home_dir"
  fi
  exit 0
fi
if [ $# -gt 0 ]; then
  echo "usage: sh install.sh [--uninstall]" >&2
  exit 2
fi

src="${KIT_DIR:-$(cd "$(dirname "$0")" && pwd)}"
if [ ! -f "$src/bin/factory" ] || [ ! -d "$src/kit" ]; then
  echo "install: $src is not a factory-kit folder (no bin/factory). Run this from the kit folder or set KIT_DIR." >&2
  exit 2
fi
src="$(cd "$src" && pwd)"
if [ -e "$home_dir" ] && [ ! -f "$home_dir/.factory-kit-install" ]; then
  echo "install: $home_dir exists and was not made by this installer. Set FACTORY_HOME to another folder." >&2
  exit 2
fi
if [ -e "$launcher" ] && ! grep -q "factory-kit launcher" "$launcher" 2>/dev/null; then
  echo "install: $launcher exists and is not ours. Set BIN_DIR to another folder." >&2
  exit 2
fi

if [ "$src" != "$(cd "$home_dir" 2>/dev/null && pwd || true)" ]; then
  rm -rf "$home_dir"
  mkdir -p "$home_dir"
  cp -R "$src/bin" "$src/kit" "$home_dir/"
  for f in README.md docs; do
    [ -e "$src/$f" ] && cp -R "$src/$f" "$home_dir/"
  done
  find "$home_dir" -name __pycache__ -type d -prune -exec rm -rf {} + 2>/dev/null || true
  echo "installed factory-kit files in $home_dir"
fi
: > "$home_dir/.factory-kit-install"

mkdir -p "$bin_dir"
cat > "$launcher" <<LAUNCHER
#!/bin/sh
# factory-kit launcher (written by install.sh; uninstall with: sh install.sh --uninstall)
for py in python3 python; do
  if command -v "\$py" >/dev/null 2>&1 && "\$py" -c 'import sys; sys.exit(sys.version_info < (3, 8))' 2>/dev/null; then
    exec "\$py" "$home_dir/bin/factory" "\$@"
  fi
done
echo "factory: needs Python 3.8 or newer, and none was found." >&2
echo "         Install Python from https://www.python.org/downloads/ (or with your package manager) and try again." >&2
exit 2
LAUNCHER
chmod 755 "$launcher"
echo "installed $launcher"

case ":${PATH:-}:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH. Add this line to your shell profile:"
     echo "      export PATH=\"$bin_dir:\$PATH\"" ;;
esac
echo "try: factory --help"
