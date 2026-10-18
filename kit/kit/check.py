#!/usr/bin/env python3
"""Project check runner and gate (factory-kit).

Vendored into each project as .factory/check.py and run through ./check. Standard library
only, so a fresh worktree or another machine can run it with nothing installed.

    ./check                  run every stage in factory.toml, stop at the first failure
    ./check --fast           the quick variant (stages overridden or skipped under [fast])
    ./check --gate           also: BLOCKED.md exit, protected files, scope of the change
    ./check --gate --spec specs/x.md --base main

Exit codes: 0 pass, 1 fail, 2 configuration error, 3 blocked (an honest "cannot be done").

An orchestrator (Handloom, CI) can drive it with environment variables instead of arguments, so
one command works for every task: HANDLOOM_BASE, HANDLOOM_SPEC, HANDLOOM_MODE (FACTORY_* names
are accepted too; an argument beats HANDLOOM_*, which beats FACTORY_*). The mode says what to run:
    submit    a worker has finished: the gate (with the spec when there is one)
    merge     work was merged into an integration branch: every stage, no scope check
    maintain  a small change to a live project: the gate, no spec needed, capped by [maintain]

Every run writes runs/<time>-<mode>/ with one log per stage and summary.json. The terminal
gets one line per stage plus the tail of the first failure, so an agent reads little.
"""
from __future__ import annotations

import argparse
import fnmatch
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

KIT_VERSION = "0.2.0"
SUMMARY_SCHEMA = 1
MODES = ("submit", "merge", "maintain")
sys.path.insert(0, str(Path(__file__).resolve().parent))
import _toml  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
EXIT_PASS, EXIT_FAIL, EXIT_CONFIG, EXIT_BLOCKED = 0, 1, 2, 3
# Files a person owns. An agent changing one on its branch fails the gate, because each of
# them can make the gate pass without the work being done (empty a stage, edit a test).
PROTECTED = ("factory.toml", ".factory/", "check", ".claude/settings.json")
ALWAYS_IN_SCOPE = ("BLOCKED.md", "LESSONS.md", "specs/")
TAIL_LINES = 20
KEEP_RUNS = 50


class ConfigError(Exception):
    pass


def load_config(root: Path = ROOT) -> dict:
    path = root / "factory.toml"
    if not path.exists():
        raise ConfigError("factory.toml not found. Run `factory init` in this project.")
    try:
        cfg = _toml.loads(path.read_text())
    except _toml.TOMLError as e:
        raise ConfigError(f"factory.toml is not valid TOML: {e}")
    for table in ("stages", "fast", "timeouts", "gate"):
        if not isinstance(cfg.get(table, {}), dict):
            raise ConfigError(f"factory.toml: [{table}] must be a table")
    for name, cmd in (cfg.get("stages") or {}).items():
        if not isinstance(cmd, str):
            raise ConfigError(f"factory.toml: stage {name!r} must be a command string (empty to skip)")
    return cfg


def env_first(*names: str) -> str:
    """The first of these environment variables that is set and not empty."""
    for name in names:
        if os.environ.get(name, "").strip():
            return os.environ[name].strip()
    return ""


def acceptance_dir(cfg: dict) -> str:
    return (cfg.get("gate", {}).get("acceptance_dir") or "tests/acceptance").strip("/")


# ---------------------------------------------------------------------------
# git helpers
# ---------------------------------------------------------------------------
def git(*args, root: Path = ROOT, check=True) -> str:
    r = subprocess.run(["git", *args], cwd=root, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise ConfigError(f"git {' '.join(args)} failed: {r.stderr.strip()}")
    return r.stdout


def base_commit(base: str, root: Path = ROOT) -> str:
    for ref in [base] + ([] if base != "main" else ["master"]):
        r = subprocess.run(["git", "merge-base", "HEAD", ref], cwd=root, capture_output=True, text=True)
        if r.returncode == 0 and r.stdout.strip():
            return r.stdout.strip()
    raise ConfigError(f"cannot find the base branch {base!r} (set --base, FACTORY_BASE or [gate] base)")


def changed_files(base_sha: str, root: Path = ROOT, links=()) -> list:
    """Files that differ from the base: committed on the branch, staged, unstaged or new."""
    files = set(git("diff", "--name-only", base_sha, root=root).split())
    files |= set(git("ls-files", "--others", "--exclude-standard", root=root).split())
    links = {l.strip("/") for l in links}
    return sorted(f for f in files if not f.startswith("runs/") and not _generated(f)
                  and f.split("/")[0] not in links)


def prepare_worktree(cfg: dict, root: Path = ROOT) -> list:
    """In a linked git worktree, link what [worktree] link names (a venv, node_modules) from the
    main checkout, and keep the links out of git. A fresh worktree from any orchestrator can then
    run ./check without installing anything. Returns the paths it linked."""
    links = (cfg.get("worktree") or {}).get("link") or []
    if not links:
        return []
    r = subprocess.run(["git", "rev-parse", "--git-dir", "--git-common-dir"], cwd=root, capture_output=True, text=True)
    if r.returncode != 0:
        return []
    git_dir, common = [Path(root, p).resolve() for p in r.stdout.split()[:2]]
    if git_dir == common:
        return []  # the main checkout itself
    main_root = common.parent
    exclude = common / "info" / "exclude"
    exclude.parent.mkdir(parents=True, exist_ok=True)
    known = exclude.read_text().splitlines() if exclude.exists() else []
    done = []
    for link in links:
        link = link.strip("/")
        target, source = root / link, main_root / link
        if not target.exists() and not target.is_symlink() and source.exists():
            target.symlink_to(source)
            done.append(link)
        if f"/{link}" not in known:
            with exclude.open("a") as f:
                f.write(f"/{link}\n")
            known.append(f"/{link}")
    return done


# Build and test output that tools write on their own. It is never part of the work, so it
# must not fail the scope check of a project whose .gitignore forgot it.
GENERATED_PARTS = {"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", "node_modules", ".next", ".turbo"}
GENERATED_SUFFIXES = (".pyc", ".pyo")


def _generated(path: str) -> bool:
    parts = path.split("/")
    return bool(GENERATED_PARTS.intersection(parts[:-1])) or path.endswith(GENERATED_SUFFIXES)


# ---------------------------------------------------------------------------
# acceptance manifest
# ---------------------------------------------------------------------------
def hash_tree(directory: Path) -> dict:
    out = {}
    if directory.is_dir():
        for p in sorted(directory.rglob("*")):
            if p.is_file() and p.name != ".manifest" and "__pycache__" not in p.parts:
                out[p.relative_to(directory).as_posix()] = hashlib.sha256(p.read_bytes()).hexdigest()
    return out


def read_manifest(text: str) -> dict:
    out = {}
    for line in text.splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            digest, _, name = line.partition("  ")
            out[name] = digest
    return out


def manifest_text(hashes: dict) -> str:
    head = "# sha256 of every acceptance test. Regenerate with `factory accept-tests` (a person, on the base branch).\n"
    return head + "".join(f"{d}  {n}\n" for n, d in sorted(hashes.items()))


# ---------------------------------------------------------------------------
# spec parsing
# ---------------------------------------------------------------------------
def spec_sections(text: str) -> dict:
    sections, current = {}, None
    for line in text.splitlines():
        if line.startswith("## "):
            current = line[3:].strip().lower()
            sections[current] = []
        elif current is not None:
            sections[current].append(line)
    return sections


def bullet_items(lines: list) -> list:
    items = []
    for line in lines:
        s = line.strip()
        if s.startswith(("- ", "* ")):
            item = s[2:].strip()
            if item.startswith("`") and "`" in item[1:]:
                item = item[1:item.index("`", 1)]
            else:
                item = item.split("  ")[0].split(" (")[0].strip()
            if item and not item.startswith("("):
                items.append(item)
    return items


def in_scope(path: str, patterns: list) -> bool:
    for pat in list(patterns) + list(ALWAYS_IN_SCOPE):
        if pat.endswith("/") and path.startswith(pat):
            return True
        if path == pat or fnmatch.fnmatch(path, pat):
            return True
    return False


# ---------------------------------------------------------------------------
# runner
# ---------------------------------------------------------------------------
class Run:
    def __init__(self, mode: str, quiet: bool, root: Path = ROOT):
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        self.root = root
        self.dir = root / "runs" / f"{stamp}-{mode}"
        n = 1
        while self.dir.exists():
            n += 1
            self.dir = root / "runs" / f"{stamp}-{mode}-{n}"
        self.dir.mkdir(parents=True)
        self.quiet = quiet
        self.started = time.time()
        self.stages = []
        self.notes = []
        self.refused = []
        self.summary = {"schema": SUMMARY_SCHEMA, "kit_version": KIT_VERSION, "mode": mode, "started": stamp,
                        "stages": self.stages, "notes": self.notes, "refused": self.refused}

    def say(self, text: str):
        if not self.quiet:
            print(text, flush=True)

    def record(self, name: str, status: str, seconds: float = 0.0, detail: str = "", log: str = "", **extra):
        self.stages.append({"name": name, "status": status, "seconds": round(seconds, 2), "detail": detail, "log": log, **extra})
        mark = {"pass": "PASS", "fail": "FAIL", "skip": "SKIP", "blocked": "BLOCKED"}[status]
        line = f"{mark:7} {name:14} {seconds:6.1f}s"
        if detail:
            line += f"  {detail}"
        self.say(line)

    def reason(self, exit_code: int, config_error: str = "") -> str:
        """The result in one sentence, for the last lines of output and for summary.json."""
        if exit_code == EXIT_PASS:
            ran = sum(1 for s in self.stages if s["status"] == "pass")
            return f"{ran} check(s) passed"
        if exit_code == EXIT_CONFIG:
            return config_error or "the project's check is misconfigured"
        bad = next((s for s in self.stages if s["status"] in ("fail", "blocked")), None)
        if bad is None:
            return "a stage failed"
        return f"{bad['name']}: {bad['detail'] or 'failed'}"

    def finish(self, exit_code: int, config_error: str = "") -> int:
        result = {EXIT_PASS: "pass", EXIT_FAIL: "fail", EXIT_CONFIG: "config-error", EXIT_BLOCKED: "blocked"}[exit_code]
        reason = self.reason(exit_code, config_error)
        timed = [s for s in self.stages if s["seconds"] > 0]
        slowest = max(timed, key=lambda s: s["seconds"], default=None)
        self.summary.update(result=result, exit_code=exit_code, reason=reason,
                            slowest_stage=slowest["name"] if slowest else None,
                            seconds=round(time.time() - self.started, 2))
        r = subprocess.run(["git", "rev-parse", "HEAD"], cwd=self.root, capture_output=True, text=True)
        self.summary["git_sha"] = r.stdout.strip() or None
        (self.dir / "summary.json").write_text(json.dumps(self.summary, indent=2) + "\n")
        hist = self.root / "runs" / "history.csv"
        new = not hist.exists()
        with hist.open("a") as f:
            if new:
                f.write("started,mode,result,seconds,git_sha,failed_stage\n")
            failed = next((s["name"] for s in self.stages if s["status"] in ("fail", "blocked")), "")
            f.write(f"{self.summary['started']},{self.summary['mode']},{result},{self.summary['seconds']},{self.summary['git_sha'] or ''},{failed}\n")
        prune(self.root / "runs")
        self.say(f"{result.upper()}: {reason}")
        self.say(f"evidence: {self.dir.relative_to(self.root)}/summary.json")
        return exit_code


def prune(runs_dir: Path):
    dirs = sorted(d for d in runs_dir.iterdir() if d.is_dir())
    for d in dirs[:-KEEP_RUNS]:
        shutil.rmtree(d, ignore_errors=True)


def tail(path: Path, n: int = TAIL_LINES) -> str:
    try:
        lines = path.read_text(errors="replace").splitlines()
    except OSError:
        return ""
    return "\n".join(lines[-n:])


def run_stage(run: Run, name: str, cmd: str, timeout: int) -> bool:
    log = run.dir / f"{name}.log"
    t0 = time.time()
    with log.open("w") as f:
        f.write(f"$ {cmd}\n")
        f.flush()
        try:
            r = subprocess.run(cmd, shell=True, cwd=run.root, stdout=f, stderr=subprocess.STDOUT,
                               timeout=timeout, env={**os.environ, "FACTORY_RUN_DIR": str(run.dir)})
            code = r.returncode
        except subprocess.TimeoutExpired:
            f.write(f"\n[factory] timed out after {timeout}s\n")
            code = None
    seconds = time.time() - t0
    rel = log.relative_to(run.root).as_posix()
    if code == 0:
        run.record(name, "pass", seconds, log=rel)
        return True
    why = f"timed out after {timeout}s" if code is None else f"exit {code}"
    run.record(name, "fail", seconds, f"{why} -> {rel}", log=rel, **({"timed_out": True} if code is None else {}))
    run.say(tail(log))
    return False


def contract_stage(run: Run, cfg: dict) -> bool:
    limit = int(cfg.get("gate", {}).get("max_agents_md_lines", 100))
    agents = run.root / "AGENTS.md"
    if agents.exists():
        n = len(agents.read_text().splitlines())
        if n > limit:
            run.record("contract", "fail", 0, f"AGENTS.md has {n} lines (limit {limit}). Move detail into docs/ and link it.")
            return False
    manifest = run.root / acceptance_dir(cfg) / ".manifest"
    if manifest.exists():
        want = read_manifest(manifest.read_text())
        have = hash_tree(run.root / acceptance_dir(cfg))
        if want != have:
            drift = sorted(set(want) ^ set(have) | {k for k in want if k in have and want[k] != have[k]})
            run.record("contract", "fail", 0, f"acceptance tests differ from {acceptance_dir(cfg)}/.manifest: {', '.join(drift)}. "
                                               "A person runs `factory accept-tests` after reviewing them.")
            return False
    run.record("contract", "pass", 0)
    return True


def gate_checks(run: Run, cfg: dict, spec: str, base: str, limit_key: str = "gate") -> int | None:
    """Gate-only checks. Returns an exit code to stop with, or None to go on to the stages."""
    blocked = run.root / "BLOCKED.md"
    if blocked.exists():
        first = next((l.strip("# ").strip() for l in blocked.read_text().splitlines() if l.strip()), "no reason given")
        run.record("blocked", "blocked", 0, first)
        run.notes.append("BLOCKED.md present: the agent reports the task cannot be done as specified. A person decides.")
        run.summary["blocked"] = blocked.read_text()[:4000]
        return EXIT_BLOCKED
    sha = base_commit(base, run.root)
    files = changed_files(sha, run.root, (cfg.get("worktree") or {}).get("link") or [])
    run.summary["base"] = {"ref": base, "sha": sha}
    run.summary["changed_files"] = files
    acc = acceptance_dir(cfg) + "/"
    owned = [f for f in files if f.startswith(acc) or any(f == p or (p.endswith("/") and f.startswith(p)) for p in PROTECTED)]
    if owned:
        run.refused += [{"file": f, "why": "protected: a person owns it"} for f in owned]
        run.record("protected", "fail", 0, f"changed on this branch: {', '.join(owned)}. A person changes these on {base}.")
        return EXIT_FAIL
    run.record("protected", "pass", 0)
    limit = int(cfg.get(limit_key, {}).get("max_changed_files") or cfg.get("gate", {}).get("max_changed_files", 30))
    if len(files) > limit:
        run.record("scope", "fail", 0, f"{len(files)} files changed (limit {limit}). Split the task.")
        return EXIT_FAIL
    if spec:
        path = run.root / spec
        if not path.exists():
            raise ConfigError(f"spec {spec} not found")
        sections = spec_sections(path.read_text())
        patterns = bullet_items(sections.get("files", []))
        if not patterns:
            raise ConfigError(f"spec {spec} has no `## Files` list, so the scope cannot be checked")
        outside = [f for f in files if f != spec and not in_scope(f, patterns)]
        if outside:
            run.refused += [{"file": f, "why": "outside the spec's ## Files"} for f in outside]
            run.record("scope", "fail", 0, f"outside the spec's ## Files: {', '.join(outside)}")
            return EXIT_FAIL
        run.record("scope", "pass", 0, f"{len(files)} files, all in scope")
        run.summary["spec_checks"] = bullet_items(sections.get("checks", []))
    else:
        run.record("scope", "skip", 0, "no --spec given")
    return None


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(prog="check", description=__doc__.split("\n\n")[0])
    ap.add_argument("--fast", action="store_true", help="quick variant: apply [fast] overrides")
    ap.add_argument("--gate", action="store_true", help="also check BLOCKED.md, protected files and scope")
    ap.add_argument("--mode", default=env_first("HANDLOOM_MODE", "FACTORY_MODE"), metavar="MODE",
                    help="submit | merge | maintain: what to run, so the caller needs one command (HANDLOOM_MODE)")
    ap.add_argument("--spec", default=env_first("HANDLOOM_SPEC", "FACTORY_SPEC"),
                    help="task spec; enables the scope check and its ## Checks (HANDLOOM_SPEC)")
    ap.add_argument("--base", default=env_first("HANDLOOM_BASE", "FACTORY_BASE"),
                    help="base branch for the gate (default [gate] base, else main) (HANDLOOM_BASE)")
    ap.add_argument("--stage", action="append", default=[], help="run only these stages")
    ap.add_argument("--keep-going", action="store_true", help="run every stage even after a failure")
    ap.add_argument("--quiet", action="store_true", help="print only the result line")
    args = ap.parse_args(argv)

    try:
        cfg = load_config()
        if args.mode and args.mode not in MODES:
            raise ConfigError(f"unknown mode {args.mode!r} (HANDLOOM_MODE / --mode): use {', '.join(MODES)}")
    except ConfigError as e:
        print(f"CONFIG  {e}", file=sys.stderr)
        return EXIT_CONFIG
    # The mode decides what to run unless the caller named it with flags.
    gate = args.gate or (args.mode in ("submit", "maintain") and not args.fast)
    spec = "" if args.mode == "merge" else args.spec
    limit_key = "maintain" if args.mode == "maintain" else "gate"
    mode = "gate" if gate else ("fast" if args.fast else "full")
    linked = prepare_worktree(cfg)
    run = Run(mode, args.quiet)
    if args.mode:
        run.summary["requested_mode"] = args.mode
    if linked:
        run.notes.append(f"linked from the main checkout: {', '.join(linked)}")
    try:
        if not contract_stage(run, cfg) and not args.keep_going:
            return run.finish(EXIT_FAIL)
        spec_cmds = []
        if gate or spec:
            stop = gate_checks(run, cfg, spec, args.base or cfg.get("gate", {}).get("base", "main"), limit_key) if gate else None
            if stop is not None:
                return run.finish(stop)
            if spec and not gate:
                spec_cmds = bullet_items(spec_sections((ROOT / spec).read_text()).get("checks", []))
            else:
                spec_cmds = run.summary.get("spec_checks", [])
    except ConfigError as e:
        run.say(f"CONFIG  {e}")
        return run.finish(EXIT_CONFIG, str(e))

    stages = dict(cfg.get("stages") or {})
    if args.fast:
        for name, cmd in (cfg.get("fast") or {}).items():
            if name in stages:
                stages[name] = cmd
    for i, cmd in enumerate(spec_cmds, 1):
        stages[f"spec-{i}"] = cmd
    timeouts = cfg.get("timeouts") or {}
    default_timeout = int(timeouts.get("default", 900))
    ok = all(s["status"] != "fail" for s in run.stages)
    for name, cmd in stages.items():
        if args.stage and name not in args.stage:
            continue
        if not cmd.strip():
            run.record(name, "skip", 0, "not configured" if not args.fast else "skipped in --fast")
            continue
        if not run_stage(run, name, cmd, int(timeouts.get(name, default_timeout))):
            ok = False
            if not args.keep_going:
                break
    return run.finish(EXIT_PASS if ok else EXIT_FAIL)


if __name__ == "__main__":
    sys.exit(main())
