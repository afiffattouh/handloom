#!/usr/bin/env python3
"""Claude Code Stop hook (factory-kit): do not let an agent finish while checks fail.

Runs `./check --fast` when the session changed files. If it fails, the hook blocks the stop
(exit 2) and tells the agent to fix it or write BLOCKED.md. An agent is never trapped:
- a second stop in a row (stop_hook_active) is allowed,
- BLOCKED.md counts as an honest finish,
- no file changes means nothing to check,
- FACTORY_STOP_CHECK=0 turns the hook off for a session.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
TIMEOUT = int(os.environ.get("FACTORY_STOP_TIMEOUT", "300"))


def main() -> int:
    try:
        event = json.load(sys.stdin)
    except Exception:
        event = {}
    if event.get("stop_hook_active") or os.environ.get("FACTORY_STOP_CHECK") == "0":
        return 0
    if (ROOT / "BLOCKED.md").exists():
        return 0
    status = subprocess.run(["git", "status", "--porcelain", "--untracked-files=normal"],
                            cwd=ROOT, capture_output=True, text=True)
    changes = [l for l in status.stdout.splitlines() if l[3:].strip() and not l[3:].startswith("runs/")]
    if status.returncode != 0 or not changes:
        return 0
    try:
        r = subprocess.run([sys.executable, str(ROOT / ".factory" / "check.py"), "--fast"],
                           cwd=ROOT, capture_output=True, text=True, timeout=TIMEOUT)
    except subprocess.TimeoutExpired:
        return 0  # a slow check should not hold the session hostage; the gate still runs later
    if r.returncode in (0, 3):
        return 0
    out = (r.stdout + r.stderr).strip().splitlines()
    print("The project's checks are failing, so the task is not done yet:\n"
          + "\n".join(out[-25:])
          + "\n\nFix the failures and stop again. If the task cannot be done as specified, "
            "write BLOCKED.md (what you tried, what blocks you, what you need) instead of forcing a pass.",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
