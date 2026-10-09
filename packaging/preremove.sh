#!/bin/sh
# Runs before the package is removed (not on an upgrade): stop and delete the systemd services that
# `handloom link install` and `handloom hub install` wrote, so removing the package leaves nothing running.
# Data, credentials and repositories are not touched; `handloom uninstall --purge` (before removing) handles the link's folder.
case "$1" in remove|purge|0) ;; *) exit 0 ;; esac
for s in handloom-link handloom-hub; do
  if [ -f "/etc/systemd/system/$s.service" ]; then
    command -v systemctl >/dev/null 2>&1 && systemctl disable --now "$s" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/$s.service"
  fi
done
command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload >/dev/null 2>&1 || true
exit 0
