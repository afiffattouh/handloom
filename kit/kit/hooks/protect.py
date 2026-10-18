#!/usr/bin/env python3
"""Claude Code PreToolUse hook (factory-kit): refuse edits to files a person owns.

Owned: the acceptance tests, factory.toml, .factory/, ./check and .claude/settings.json.
An agent that edits any of them can make the gate pass without doing the work.

This hook only sees Claude's file tools (Edit, Write, MultiEdit, NotebookEdit). A shell
command can still write these files, so the gate's diff check is the real protection; this
hook makes the honest path the easy one and explains why.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import _toml  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent.parent
OWNED_FILES = ("factory.toml", "check", ".claude/settings.json")
OWNED_DIRS = (".factory",)


def acceptance_dir() -> str:
    try:
        cfg = _toml.loads((ROOT / "factory.toml").read_text())
        return (cfg.get("gate", {}).get("acceptance_dir") or "tests/acceptance").strip("/")
    except Exception:
        return "tests/acceptance"


def owned(target: Path) -> str | None:
    try:
        rel = target.resolve().relative_to(ROOT).as_posix()
    except ValueError:
        return None  # outside this project
    if rel in OWNED_FILES:
        return rel
    for d in OWNED_DIRS + (acceptance_dir(),):
        if rel == d or rel.startswith(d + "/"):
            return rel
    return None


def main() -> int:
    try:
        event = json.load(sys.stdin)
    except Exception:
        return 0
    tool_input = event.get("tool_input") or {}
    raw = tool_input.get("file_path") or tool_input.get("notebook_path") or tool_input.get("path")
    if not raw:
        return 0
    target = Path(raw)
    if not target.is_absolute():
        target = Path(event.get("cwd") or ROOT) / target
    rel = owned(target)
    if rel is None:
        return 0
    reason = (f"{rel} is owned by a person (acceptance tests and the check setup). Do not change it. "
              "If the task cannot be done without changing it, stop and write BLOCKED.md explaining why.")
    print(json.dumps({"hookSpecificOutput": {"hookEventName": "PreToolUse",
                                             "permissionDecision": "deny",
                                             "permissionDecisionReason": reason}}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
