"""factory-kit tests. Each test builds a throwaway git repo, runs `factory init` in it and
drives the vendored ./check and hooks as subprocesses, the way agents and Handloom will."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

KIT = Path(__file__).resolve().parents[1]
FACTORY = [sys.executable, str(KIT / "bin" / "factory")]


def sh(cmd, cwd, env=None, stdin=None):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, input=stdin,
                          env={**os.environ, **(env or {})}, shell=isinstance(cmd, str))


class Project:
    """A git repo with one passing unit test, set up with factory init and committed on main."""

    def __init__(self, stages=None):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        sh("git init -q -b main && git config user.email t@t && git config user.name t", self.root)
        (self.root / "src").mkdir()
        (self.root / "src" / "app.py").write_text("def add(a, b):\n    return a + b\n")
        (self.root / "tests").mkdir()
        (self.root / "tests" / "test_app.py").write_text(
            "import sys, unittest\nsys.path.insert(0, 'src')\nfrom app import add\n"
            "class T(unittest.TestCase):\n    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n")
        r = sh(FACTORY + ["init", str(self.root)], self.root)
        assert r.returncode == 0, r.stdout + r.stderr
        if stages is not None:
            self.set_stages(stages)
        acc = self.root / "tests" / "acceptance"
        (acc / "test_accept.py").write_text(
            "import sys, unittest\nsys.path.insert(0, 'src')\nfrom app import add\n"
            "class A(unittest.TestCase):\n    def test_add_negative(self):\n        self.assertEqual(add(-1, 1), 0)\n")
        sh(FACTORY + ["accept-tests", str(self.root)], self.root)
        sh("git add -A && git commit -qm base", self.root)
        sh("git checkout -qb task/1", self.root)

    def set_stages(self, stages: dict):
        text = (self.root / "factory.toml").read_text()
        head, _, rest = text.partition("\n[stages]\n")
        head += "\n"
        rest = rest[rest.index("\n["):] if "\n[" in rest else ""
        body = "".join(f'{k} = "{v}"\n' for k, v in stages.items())
        (self.root / "factory.toml").write_text(head + "[stages]\n" + body + rest)

    def check(self, *args, env=None):
        return sh(["./check", *args], self.root, env=env)

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def summary(self):
        runs = sorted(d for d in (self.root / "runs").iterdir() if d.is_dir())
        return json.loads((runs[-1] / "summary.json").read_text())

    def close(self):
        self.tmp.cleanup()


class InitTests(unittest.TestCase):
    def test_init_creates_the_contract_and_detects_unittest(self):
        p = Project()
        try:
            for rel in ("factory.toml", "AGENTS.md", "CLAUDE.md", "specs/TEMPLATE.md", "LESSONS.md",
                        "tests/acceptance/README.md", "tests/acceptance/.manifest", ".factory/check.py",
                        ".factory/hooks/protect.py", ".factory/hooks/stop_check.py", "check", ".claude/settings.json"):
                self.assertTrue((p.root / rel).exists(), rel)
            self.assertIn("unittest discover", (p.root / "factory.toml").read_text())
            self.assertIn("runs/", (p.root / ".gitignore").read_text())
            settings = json.loads((p.root / ".claude" / "settings.json").read_text())
            self.assertIn("PreToolUse", settings["hooks"])
            self.assertIn("Stop", settings["hooks"])
        finally:
            p.close()

    def test_init_never_overwrites_and_merges_existing_settings(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            sh("git init -q -b main", root)
            (root / "AGENTS.md").write_text("mine\n")
            (root / ".claude").mkdir()
            (root / ".claude" / "settings.json").write_text(json.dumps(
                {"permissions": {"allow": ["Bash(ls)"]}, "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo hi"}]}]}}))
            self.assertEqual(sh(FACTORY + ["init", str(root)], root).returncode, 0)
            self.assertEqual((root / "AGENTS.md").read_text(), "mine\n")
            s = json.loads((root / ".claude" / "settings.json").read_text())
            self.assertEqual(s["permissions"], {"allow": ["Bash(ls)"]})
            commands = [h["command"] for g in s["hooks"]["Stop"] for h in g["hooks"]]
            self.assertIn("echo hi", commands)
            self.assertEqual(len([c for c in commands if "stop_check" in c]), 1)
            sh(FACTORY + ["init", str(root)], root)   # twice: no duplicate hooks
            s = json.loads((root / ".claude" / "settings.json").read_text())
            self.assertEqual(len([h for g in s["hooks"]["Stop"] for h in g["hooks"] if "stop_check" in h["command"]]), 1)

    def test_existing_check_script_is_kept_as_a_stage(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            sh("git init -q -b main", root)
            (root / "check.sh").write_text("#!/bin/sh\necho legacy ok\n")
            (root / "check.sh").chmod(0o755)
            sh(FACTORY + ["init", str(root)], root)
            self.assertIn('suite = "./check.sh"', (root / "factory.toml").read_text())


class CheckTests(unittest.TestCase):
    def setUp(self):
        self.p = Project()

    def tearDown(self):
        self.p.close()

    def test_passing_project(self):
        r = self.p.check()
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("PASS    unit", r.stdout)
        s = self.p.summary()
        self.assertEqual(s["result"], "pass")
        self.assertTrue(any(st["name"] == "unit" and st["status"] == "pass" for st in s["stages"]))
        self.assertTrue((self.p.root / "runs" / "history.csv").exists())

    def test_failing_stage_exits_1_with_tail_and_log(self):
        self.p.write("src/app.py", "def add(a, b):\n    return a - b\n")
        r = self.p.check()
        self.assertEqual(r.returncode, 1)
        self.assertIn("FAIL    unit", r.stdout)
        self.assertIn("AssertionError", r.stdout, "the tail of the failure is shown")
        self.assertEqual(self.p.summary()["result"], "fail")

    def test_stops_at_first_failure_unless_keep_going(self):
        self.p.set_stages({"lint": "false", "unit": "true"})
        self.assertNotIn("unit", self.p.check().stdout.split("FAIL")[1])
        self.assertIn("PASS    unit", self.p.check("--keep-going").stdout)

    def test_fast_overrides_and_skips(self):
        text = (self.p.root / "factory.toml").read_text().replace("[fast]\n", '[fast]\nunit = ""\n')
        self.p.write("factory.toml", text)
        r = self.p.check("--fast")
        self.assertEqual(r.returncode, 0)
        self.assertIn("SKIP    unit", r.stdout)

    def test_timeout_fails_the_stage(self):
        self.p.set_stages({"unit": "sleep 5"})
        text = (self.p.root / "factory.toml").read_text().replace("default = 900", "default = 1")
        self.p.write("factory.toml", text)
        r = self.p.check()
        self.assertEqual(r.returncode, 1)
        self.assertIn("timed out", r.stdout)

    def test_bad_config_exits_2(self):
        self.p.write("factory.toml", "[stages\n")
        self.assertEqual(self.p.check().returncode, 2)

    def test_agents_md_over_limit_fails(self):
        self.p.write("AGENTS.md", "x\n" * 150)
        r = self.p.check()
        self.assertEqual(r.returncode, 1)
        self.assertIn("AGENTS.md has 150 lines", r.stdout)

    def test_edited_acceptance_test_fails_even_without_the_gate(self):
        self.p.write("tests/acceptance/test_accept.py", "import unittest\n")
        r = self.p.check()
        self.assertEqual(r.returncode, 1)
        self.assertIn("differ from tests/acceptance/.manifest", r.stdout)


class GateTests(unittest.TestCase):
    def setUp(self):
        self.p = Project()
        self.p.write("specs/add.md", "# Spec: add\n\n## Goal\n\nx\n\n## Files\n\n- src/app.py\n- tests/test_app.py\n\n"
                                     "## Checks\n\n- `python3 -c \"import sys; sys.path.insert(0,'src'); from app import add; assert add(1,1)==2\"`\n")

    def tearDown(self):
        self.p.close()

    def gate(self, *extra):
        return self.p.check("--gate", "--spec", "specs/add.md", *extra)

    def test_in_scope_change_passes_and_runs_spec_checks(self):
        self.p.write("src/app.py", "def add(a, b):\n    return a + b  # same\n")
        r = self.gate()
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("PASS    scope", r.stdout)
        self.assertIn("PASS    spec-1", r.stdout)
        s = self.p.summary()
        self.assertEqual(s["changed_files"], ["specs/add.md", "src/app.py"])

    def test_generated_cache_files_do_not_count_as_changes(self):
        self.p.write("src/app.py", "def add(a, b):\n    return b + a\n")
        self.p.write("src/__pycache__/app.cpython-312.pyc", "x")
        self.p.write("tests/__pycache__/test_app.cpython-312.pyc", "x")
        r = self.gate()
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertEqual(self.p.summary()["changed_files"], ["specs/add.md", "src/app.py"])

    def test_out_of_scope_change_fails(self):
        self.p.write("src/other.py", "x = 1\n")
        r = self.gate()
        self.assertEqual(r.returncode, 1)
        self.assertIn("outside the spec's ## Files: src/other.py", r.stdout)

    def test_changed_acceptance_test_fails_even_with_regenerated_manifest(self):
        self.p.write("tests/acceptance/test_accept.py", "import unittest\n")
        sh(FACTORY + ["accept-tests", str(self.p.root)], self.p.root)   # the agent "fixes" the manifest too
        self.assertEqual(self.p.check().returncode, 0, "the manifest now matches, so plain ./check is fooled")
        r = self.gate()
        self.assertEqual(r.returncode, 1, "the gate is not: it compares against the base branch")
        self.assertIn("changed on this branch: tests/acceptance/", r.stdout)

    def test_committed_tamper_is_caught_too(self):
        self.p.write("tests/acceptance/test_accept.py", "import unittest\n")
        sh(FACTORY + ["accept-tests", str(self.p.root)], self.p.root)
        sh("git add -A && git commit -qm 'tests'", self.p.root)
        self.assertEqual(self.gate().returncode, 1)

    def test_emptying_a_stage_in_factory_toml_fails(self):
        self.p.set_stages({"unit": ""})
        r = self.gate()
        self.assertEqual(r.returncode, 1)
        self.assertIn("factory.toml", r.stdout)

    def test_blocked_md_is_an_honest_exit_3(self):
        self.p.write("BLOCKED.md", "# Cannot do this: the API key is missing\n\nTried X.\n")
        r = self.gate()
        self.assertEqual(r.returncode, 3)
        self.assertIn("BLOCKED", r.stdout)
        self.assertIn("API key is missing", r.stdout)
        self.assertEqual(self.p.summary()["result"], "blocked")

    def test_too_many_files_fails(self):
        text = (self.p.root / "factory.toml").read_text().replace("max_changed_files = 30", "max_changed_files = 1")
        sh("git checkout -q main", self.p.root)
        self.p.write("factory.toml", text)
        sh("git commit -qam limit && git checkout -q task/1 && git merge -q main", self.p.root)
        self.p.write("src/app.py", "def add(a, b):\n    return b + a\n")
        self.p.write("tests/test_app.py", (self.p.root / "tests/test_app.py").read_text() + "\n")
        r = self.gate()
        self.assertEqual(r.returncode, 1)
        self.assertIn("Split the task", r.stdout)

    def test_spec_without_files_is_a_config_error(self):
        self.p.write("specs/bad.md", "# Spec\n\n## Goal\n\nx\n")
        self.assertEqual(self.p.check("--gate", "--spec", "specs/bad.md").returncode, 2)

    def test_gate_without_spec_still_protects(self):
        self.p.write("check", "#!/bin/sh\nexit 0\n")
        r = sh([sys.executable, ".factory/check.py", "--gate"], self.p.root)
        self.assertEqual(r.returncode, 1)
        self.assertIn("changed on this branch: check", r.stdout)


class ContractTests(unittest.TestCase):
    """Contract A: what Handloom (or CI) relies on. Variables, modes, exit codes, the reason, the evidence."""

    def setUp(self):
        self.p = Project()
        self.p.write("specs/add.md", "# Spec: add\n\n## Goal\n\nx\n\n## Files\n\n- src/app.py\n\n"
                                     "## Checks\n\n- `python3 -c \"import sys; sys.path.insert(0,'src'); from app import add; assert add(1,1)==2\"`\n")
        self.env = {"HANDLOOM_MODE": "submit", "HANDLOOM_SPEC": "specs/add.md", "HANDLOOM_BASE": "main"}

    def tearDown(self):
        self.p.close()

    def test_variables_alone_run_the_gate_with_the_spec(self):
        self.p.write("src/app.py", "def add(a, b):\n    return a + b  # same\n")
        r = self.p.check(env=self.env)
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("PASS    spec-1", r.stdout)
        s = self.p.summary()
        self.assertEqual((s["mode"], s["requested_mode"], s["schema"]), ("gate", "submit", 1))

    def test_out_of_scope_work_is_refused_and_listed(self):
        self.p.write("src/other.py", "x = 1\n")
        r = self.p.check(env=self.env)
        self.assertEqual(r.returncode, 1)
        s = self.p.summary()
        self.assertEqual(s["refused"], [{"file": "src/other.py", "why": "outside the spec's ## Files"}])
        self.assertIn("outside the spec's ## Files: src/other.py", s["reason"])

    def test_names_win_in_order_argument_then_handloom_then_factory(self):
        self.p.write("src/other.py", "x = 1\n")
        both = {**self.env, "FACTORY_SPEC": "specs/nothing.md"}
        self.assertEqual(self.p.check(env=both).returncode, 1, "HANDLOOM_SPEC beats FACTORY_SPEC")
        only_factory = {"FACTORY_MODE": "submit", "FACTORY_SPEC": "specs/add.md", "FACTORY_BASE": "main"}
        self.assertEqual(self.p.check(env=only_factory).returncode, 1, "FACTORY_* still works on its own")
        self.p.write("specs/wide.md", "# Spec\n\n## Files\n\n- src/\n\n## Checks\n\n")
        self.assertEqual(self.p.check("--spec", "specs/wide.md", env=self.env).returncode, 0, "an argument beats the variable")

    def test_merge_mode_runs_everything_but_not_the_scope_check(self):
        self.p.write("src/other.py", "x = 1\n")
        r = self.p.check(env={**self.env, "HANDLOOM_MODE": "merge"})
        self.assertEqual(r.returncode, 0, r.stdout)
        s = self.p.summary()
        self.assertEqual(s["mode"], "full")
        self.assertNotIn("scope", [st["name"] for st in s["stages"]])

    def test_maintain_mode_needs_no_spec_and_still_protects(self):
        env = {"HANDLOOM_MODE": "maintain", "HANDLOOM_BASE": "main"}
        self.p.write("src/app.py", "def add(a, b):\n    return a + b  # same\n")
        self.assertEqual(self.p.check(env=env).returncode, 0)
        self.p.write("check", "#!/bin/sh\nexit 0\n")
        r = sh([sys.executable, ".factory/check.py"], self.p.root, env=env)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(self.p.summary()["refused"][0]["file"], "check")

    def test_maintain_mode_has_its_own_cap_on_changed_files(self):
        sh("git checkout -q main", self.p.root)
        text = (self.p.root / "factory.toml").read_text().replace("[maintain]\nmax_changed_files = 10", "[maintain]\nmax_changed_files = 1")
        self.p.write("factory.toml", text)
        sh("git commit -qam cap && git checkout -q task/1 && git merge -q main", self.p.root)
        self.p.write("a.txt", "a\n")
        self.p.write("b.txt", "b\n")
        env = {"HANDLOOM_MODE": "maintain", "HANDLOOM_BASE": "main"}
        self.assertEqual(self.p.check(env=env).returncode, 1)
        self.assertEqual(self.p.check(env={"HANDLOOM_MODE": "submit", "HANDLOOM_BASE": "main"}).returncode, 0,
                         "the cap applies to maintain only")

    def test_unknown_mode_is_a_config_error(self):
        r = self.p.check(env={"HANDLOOM_MODE": "yolo"})
        self.assertEqual(r.returncode, 2)
        self.assertIn("unknown mode", r.stderr)

    def test_the_last_lines_say_why_in_words(self):
        self.p.set_stages({"unit": "echo boom; exit 1"})
        r = self.p.check()
        self.assertEqual(r.returncode, 1)
        last = r.stdout.strip().splitlines()
        self.assertTrue(last[-2].startswith("FAIL: unit: exit 1"), last)
        self.assertTrue(last[-1].startswith("evidence: runs/"))
        self.assertEqual(self.p.summary()["reason"].split(" ->")[0], "unit: exit 1")

    def test_blocked_note_travels_in_the_summary(self):
        self.p.write("BLOCKED.md", "# Need a database password\n\nTried the env.\n")
        r = self.p.check(env=self.env)
        self.assertEqual(r.returncode, 3)
        s = self.p.summary()
        self.assertIn("Need a database password", s["blocked"])
        self.assertIn("Need a database password", s["reason"])

    def test_a_timeout_is_marked_and_the_slowest_stage_named(self):
        self.p.set_stages({"fast_one": "true", "slow": "sleep 5"})
        text = (self.p.root / "factory.toml").read_text().replace("default = 900", "default = 1")
        self.p.write("factory.toml", text)
        self.assertEqual(self.p.check().returncode, 1)
        s = self.p.summary()
        slow = next(st for st in s["stages"] if st["name"] == "slow")
        self.assertTrue(slow["timed_out"])
        self.assertEqual(s["slowest_stage"], "slow")


class WebPresetTests(unittest.TestCase):
    PAGE = ('<!doctype html><title>{t}</title><meta name="description" content="d"><h1>{t}</h1>{body}')

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        sh("git init -q -b main && git config user.email t@t && git config user.name t", self.root)
        self.write("index.html", self.PAGE.format(t="Home", body='<a href="/about.html">About</a> <a href="about.html#team">Team</a>'
                                                 '<img src="logo.png" alt="Logo"><a href="https://example.com/x">out</a>'))
        self.write("about.html", self.PAGE.format(t="About", body='<h2 id="team">Team</h2><a href="/">Home</a>'))
        (self.root / "logo.png").write_bytes(b"\x89PNG" + b"0" * 100)
        r = sh(FACTORY + ["init", "--preset", "web", str(self.root)], self.root)
        assert r.returncode == 0, r.stdout + r.stderr
        sh("git add -A && git commit -qm base", self.root)

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, rel, text):
        (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
        (self.root / rel).write_text(text)

    def web(self, what):
        return sh([sys.executable, ".factory/checks/web.py", what], self.root)

    def test_preset_adds_the_four_stages_and_vendors_the_script(self):
        toml = (self.root / "factory.toml").read_text()
        for stage in ("web-links", "web-images", "web-meta", "web-budget"):
            self.assertIn(stage, toml)
        self.assertIn('preset = "web"', toml)
        self.assertTrue((self.root / ".factory/checks/web.py").exists())
        r = sh(["./check"], self.root)
        self.assertEqual(r.returncode, 0, r.stdout)

    def test_a_clean_site_passes_every_check(self):
        for what in ("links", "images", "meta", "budget"):
            self.assertEqual(self.web(what).returncode, 0, what)

    def test_broken_link_image_and_anchor_are_named(self):
        self.write("index.html", self.PAGE.format(t="Home", body='<a href="/gone.html">x</a><a href="about.html#nope">y</a>'
                                                                '<img src="missing.png" alt="m"><a href="#top">z</a>'))
        r = self.web("links")
        self.assertEqual(r.returncode, 1)
        for want in ("broken link /gone.html", "missing anchor #nope", "broken image missing.png", "#top anchor does not exist"):
            self.assertIn(want, r.stdout)

    def test_ignore_skips_listed_prefixes(self):
        self.write("index.html", self.PAGE.format(t="Home", body='<a href="/api/x">x</a>'))
        self.assertEqual(self.web("links").returncode, 1)
        toml = (self.root / "factory.toml").read_text().replace('# ignore = ["/api/"]', 'ignore = ["/api/"]')
        self.write("factory.toml", toml)
        self.assertEqual(self.web("links").returncode, 0)

    def test_image_without_alt_fails_but_empty_alt_is_a_decoration(self):
        self.write("index.html", self.PAGE.format(t="Home", body='<img src="logo.png"><img src="logo.png" alt="">'))
        r = self.web("images")
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.stdout.count("has no alt text"), 1)

    def test_page_needs_title_description_and_one_h1(self):
        self.write("bad.html", "<p>hello</p><h1>a</h1><h1>b</h1>")
        r = self.web("meta")
        self.assertEqual(r.returncode, 1)
        for want in ("bad.html: no <title>", "no meta description", "2 <h1> headings"):
            self.assertIn(want, r.stdout)

    def test_oversized_image_fails_the_budget(self):
        (self.root / "big.jpg").write_bytes(b"0" * 600 * 1024)
        r = self.web("budget")
        self.assertEqual(r.returncode, 1)
        self.assertIn("big.jpg: image is 600 KB (limit 500 KB)", r.stdout)

    def test_built_site_folder_is_found_and_a_missing_dir_is_a_config_problem(self):
        self.write("dist/index.html", self.PAGE.format(t="Dist", body='<a href="/nope">x</a>'))
        self.assertEqual(self.web("links").returncode, 1, "reads dist/ when it exists")
        self.write("factory.toml", (self.root / "factory.toml").read_text().replace('# dir = "dist"', 'dir = "elsewhere"'))
        self.assertEqual(self.web("links").returncode, 2)

    def test_upgrade_refreshes_the_web_script_too(self):
        (self.root / ".factory/checks/web.py").write_text("# old\n")
        sh(FACTORY + ["upgrade", str(self.root)], self.root)
        self.assertIn("Web checks for a site", (self.root / ".factory/checks/web.py").read_text())


class HandoverTests(unittest.TestCase):
    def test_handover_adds_ci_guide_owners_and_marks_the_stage(self):
        p = Project()
        try:
            sh("git checkout -q main", p.root)
            r = sh(FACTORY + ["handover", "--approver", "@owner", str(p.root)], p.root)
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            ci = (p.root / ".github/workflows/check.yml").read_text()
            self.assertIn("HANDLOOM_MODE: maintain", ci)
            self.assertIn("branches: [main]", ci)
            guide = (p.root / "MAINTAIN.md").read_text()
            self.assertIn("@owner", guide)
            self.assertNotIn("{", guide, "no placeholder left in the guide")
            self.assertIn("/factory.toml @owner", (p.root / ".github/CODEOWNERS").read_text())
            self.assertIn('stage = "maintained"', (p.root / "factory.toml").read_text())
            self.assertIn("[x] green", r.stdout)
            self.assertIn("branches/main/protection", r.stdout)
            # the handed-over project still passes its own gate, and a second run changes nothing
            self.assertEqual(sh(["./check"], p.root).returncode, 0)
            again = sh(FACTORY + ["handover", "--approver", "owner", str(p.root)], p.root)
            self.assertIn("kept   MAINTAIN.md", again.stdout)
            self.assertEqual((p.root / "factory.toml").read_text().count("stage ="), 1)
        finally:
            p.close()

    def test_handover_refuses_a_project_that_is_not_green(self):
        p = Project(stages={"unit": "exit 1"})
        try:
            r = sh(FACTORY + ["handover", str(p.root)], p.root)
            self.assertEqual(r.returncode, 1)
            self.assertIn("handed over green", r.stdout)
            self.assertFalse((p.root / "MAINTAIN.md").exists())
        finally:
            p.close()

    def test_without_an_approver_the_checklist_says_so_and_no_codeowners_is_written(self):
        p = Project()
        try:
            r = sh(FACTORY + ["handover", str(p.root)], p.root)
            self.assertIn("[ ] approver named", r.stdout)
            self.assertFalse((p.root / ".github/CODEOWNERS").exists())
        finally:
            p.close()


class DescribeTests(unittest.TestCase):
    def test_describe_json_and_words_for_a_web_project(self):
        p = type("P", (), {})()
        p.tmp = tempfile.TemporaryDirectory()
        p.root = Path(p.tmp.name)
        p.close = p.tmp.cleanup
        sh("git init -q -b main", p.root)
        try:
            sh(FACTORY + ["init", "--preset", "web", str(p.root)], p.root)
            text = (p.root / "factory.toml").read_text()
            r = sh(FACTORY + ["describe", "--json", str(p.root)], p.root)
            self.assertEqual(r.returncode, 0, r.stderr)
            d = json.loads(r.stdout)
            self.assertEqual(d["schema"], 1)
            self.assertEqual(d["contract_version"], 1)
            self.assertIn("factory.toml", d["protected"])
            self.assertIn("tests/acceptance/", d["protected"])
            self.assertFalse(d["gate"]["on"])
            self.assertEqual(d["maintain_cap"], 10)
            (p.root / "factory.toml").write_text(text.replace("[stages]\n", '[stages]\nweird-stage = "true"\n'))
            words = sh(FACTORY + ["describe", str(p.root)], p.root)
            self.assertEqual(words.returncode, 0)
            for phrase in ("every link on the site works", "images have descriptions and are not too big",
                           "pages have titles", "pages stay small", "weird-stage", "contract version 1"):
                self.assertIn(phrase, words.stdout)
        finally:
            p.close()

    def test_gate_on_after_handover_and_broken_config_exits_2(self):
        p = Project()
        try:
            sh("git checkout -q main", p.root)
            sh(FACTORY + ["handover", "--preview", "netlify", str(p.root)], p.root)
            d = json.loads(sh(FACTORY + ["describe", "--json", str(p.root)], p.root).stdout)
            self.assertTrue(d["gate"]["on"])
            self.assertEqual(d["preview"], "netlify")
            (p.root / "factory.toml").write_text("not [valid")
            self.assertEqual(sh(FACTORY + ["describe", str(p.root)], p.root).returncode, 2)
        finally:
            p.close()


class PreviewAndOperatorTests(unittest.TestCase):
    def handover(self, *args):
        p = Project()
        sh("git checkout -q main", p.root)
        return p, sh(FACTORY + ["handover", *args, str(p.root)], p.root)

    def test_no_preview_by_default_and_guide_does_not_promise_one(self):
        p, r = self.handover()
        try:
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertIn("connect preview hosting so every pull request gets a link", r.stdout)
            for host in ("Cloudflare", "Netlify", "Vercel"):
                self.assertIn(host, r.stdout)
            guide = (p.root / "MAINTAIN.md").read_text()
            self.assertIn("ask whoever set the project up", guide)
            self.assertNotIn("Open the preview link", guide)
            self.assertNotIn("preview =", (p.root / "factory.toml").read_text())
        finally:
            p.close()

    def test_chosen_preview_is_recorded_and_guide_mentions_it(self):
        p, r = self.handover("--preview", "vercel")
        try:
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertIn('preview = "vercel"', (p.root / "factory.toml").read_text())
            self.assertIn("Open the preview link", (p.root / "MAINTAIN.md").read_text())
            self.assertNotIn("connect preview hosting so every", r.stdout)
            self.assertEqual(sh(["./check"], p.root).returncode, 0)
            bad = sh(FACTORY + ["handover", "--preview", "nope", str(p.root)], p.root)
            self.assertNotEqual(bad.returncode, 0)
        finally:
            p.close()

    def test_operator_edition(self):
        p, r = self.handover("--for", "operator", "--approver", "boss")
        try:
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertFalse((p.root / "MAINTAIN.md").exists())
            g = (p.root / "MAINTAIN-operator.md").read_text()
            self.assertNotIn("{", g)
            for phrase in ("./check", "summary.json", "BLOCKED.md", "broken checks", "@boss", "Revert", "blocked"):
                self.assertIn(phrase, g)
        finally:
            p.close()


class InstallerTests(unittest.TestCase):
    INSTALL = str(KIT / "install.sh")

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = Path(self.tmp.name)
        self.env = {"HOME": str(self.home)}
        self.env.pop("FACTORY_HOME", None)

    def tearDown(self):
        self.tmp.cleanup()

    def run_install(self, *args, env=None):
        e = {**os.environ, "HOME": str(self.home), **(env or {})}
        for k in ("FACTORY_HOME", "BIN_DIR", "KIT_DIR"):
            if k not in (env or {}):
                e.pop(k, None)
        return subprocess.run(["sh", self.INSTALL, *args], capture_output=True, text=True, env=e)

    def test_install_run_idempotent_and_uninstall(self):
        r = self.run_install()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        launcher = self.home / ".local" / "bin" / "factory"
        self.assertTrue(launcher.exists())
        self.assertIn("not on your PATH", r.stdout)
        self.assertTrue((self.home / ".local/share/factory-kit/bin/factory").exists())
        proj = self.home / "proj"
        proj.mkdir()
        sh("git init -q -b main", proj)
        run = subprocess.run([str(launcher), "init", str(proj)], capture_output=True, text=True, cwd=proj)
        self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
        self.assertTrue((proj / "factory.toml").exists())
        again = self.run_install()
        self.assertEqual(again.returncode, 0, again.stdout + again.stderr)
        gone = self.run_install("--uninstall")
        self.assertEqual(gone.returncode, 0)
        self.assertFalse(launcher.exists())
        self.assertFalse((self.home / ".local/share/factory-kit").exists())
        self.assertEqual(self.run_install("--uninstall").returncode, 0)

    def test_custom_dirs_on_path_and_kit_dir(self):
        bindir = self.home / "b"
        r = self.run_install(env={"FACTORY_HOME": str(self.home / "fh"), "BIN_DIR": str(bindir),
                                  "KIT_DIR": str(KIT), "PATH": f"{bindir}:{os.environ['PATH']}"})
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("PATH", r.stdout.replace("try:", ""))
        self.assertTrue((self.home / "fh" / "bin" / "factory").exists())
        h = subprocess.run([str(bindir / "factory"), "--help"], capture_output=True, text=True)
        self.assertEqual(h.returncode, 0)

    def test_refuses_a_folder_that_is_not_ours_and_a_bad_kit_dir(self):
        (self.home / "fh").mkdir()
        (self.home / "fh" / "mine.txt").write_text("x")
        r = self.run_install(env={"FACTORY_HOME": str(self.home / "fh")})
        self.assertEqual(r.returncode, 2)
        self.assertTrue((self.home / "fh" / "mine.txt").exists())
        self.assertEqual(self.run_install(env={"KIT_DIR": str(self.home)}).returncode, 2)


class OldPythonTests(unittest.TestCase):
    """The checks run on Python 3.8+, so a stock Mac works. Older Pythons read factory.toml with the kit's own reader."""

    def setUp(self):
        sys.path.insert(0, str(KIT / "kit"))
        import _toml
        self.toml = _toml

    def test_the_small_reader_agrees_with_tomllib_on_every_generated_file(self):
        try:
            import tomllib
        except ImportError:
            self.skipTest("this Python has no tomllib to compare with")
        for preset in ("generic", "web"):
            p = Project() if preset == "generic" else None
            if p is None:
                tmp = tempfile.TemporaryDirectory()
                root = Path(tmp.name)
                sh("git init -q -b main", root)
                sh(FACTORY + ["init", "--preset", "web", str(root)], root)
                text = (root / "factory.toml").read_text()
                tmp.cleanup()
            else:
                text = (p.root / "factory.toml").read_text()
                p.close()
            self.assertEqual(self.toml._loads(text), tomllib.loads(text), preset)

    def test_the_small_reader_handles_arrays_comments_and_escapes(self):
        got = self.toml._loads('# c\n[a]\nx = "q\\"uote\\n"  # trailing\ny = [\n  "one", # first\n  \'two\',\n]\nn = 1_000\nflag = true\n[a.b]\nk = ""\n')
        self.assertEqual(got["a"]["x"], 'q"uote\n')
        self.assertEqual(got["a"]["y"], ["one", "two"])
        self.assertEqual((got["a"]["n"], got["a"]["flag"], got["a"]["b"]), (1000, True, {"k": ""}))

    def test_the_small_reader_refuses_what_it_cannot_read(self):
        for bad in ("[a]\n[a]\n", "x = 1\nx = 2\n", 'x = "open\n', "x = {a = 1}\n", "just words\n", "x = 1 2\n", "a = 1\n[a]\n"):
            with self.assertRaises(ValueError, msg=bad):
                self.toml._loads(bad)

    def test_without_python_the_launcher_says_what_to_install(self):
        p = Project()
        try:
            r = subprocess.run(["/bin/sh", "./check"], cwd=p.root, capture_output=True, text=True, env={"PATH": "/nonexistent"})
            self.assertEqual(r.returncode, 2)
            self.assertIn("need Python 3.8 or newer", r.stderr)
        finally:
            p.close()


class WorktreeTests(unittest.TestCase):
    def test_fresh_worktree_gets_the_env_linked_and_the_gate_ignores_it(self):
        p = Project()
        try:
            (p.root / "venv" / "bin").mkdir(parents=True)
            text = (p.root / "factory.toml").read_text().replace("link = []", 'link = ["venv"]')
            sh("git checkout -q main", p.root)
            p.write("factory.toml", text)
            p.write("specs/s.md", "# Spec\n\n## Files\n\n- src/app.py\n")
            sh("git add -A && git commit -qm cfg && git branch task/wt", p.root)
            wt = p.root.parent / (p.root.name + "-wt")
            sh(f"git worktree add -q {wt} task/wt", p.root)
            try:
                r = sh(["./check", "--gate", "--spec", "specs/s.md"], wt)
                self.assertEqual(r.returncode, 0, r.stdout)
                self.assertTrue((wt / "venv").is_symlink(), "venv linked from the main checkout")
                self.assertEqual(json.loads(sorted((wt / "runs").glob("*/summary.json"))[-1].read_text())["changed_files"], [])
                self.assertNotIn("venv", sh("git status --porcelain", wt).stdout, "the link is excluded from git")
            finally:
                sh(f"git worktree remove --force {wt}", p.root)
        finally:
            p.close()


class HookTests(unittest.TestCase):
    def setUp(self):
        self.p = Project()

    def tearDown(self):
        self.p.close()

    def protect(self, path):
        event = {"tool_name": "Edit", "cwd": str(self.p.root), "tool_input": {"file_path": path}}
        r = sh([sys.executable, ".factory/hooks/protect.py"], self.p.root, stdin=json.dumps(event))
        return json.loads(r.stdout)["hookSpecificOutput"]["permissionDecision"] if r.stdout.strip() else "allow"

    def test_protect_denies_owned_files_and_allows_the_rest(self):
        self.assertEqual(self.protect("tests/acceptance/test_accept.py"), "deny")
        self.assertEqual(self.protect(str(self.p.root / "factory.toml")), "deny")
        self.assertEqual(self.protect(".factory/check.py"), "deny")
        self.assertEqual(self.protect(".claude/settings.json"), "deny")
        self.assertEqual(self.protect("src/app.py"), "allow")
        self.assertEqual(self.protect("tests/test_app.py"), "allow")
        self.assertEqual(self.protect("/etc/hosts"), "allow", "outside the project is not this hook's business")

    def stop(self, event=None, env=None):
        return sh([sys.executable, ".factory/hooks/stop_check.py"], self.p.root,
                  stdin=json.dumps(event or {}), env=env)

    def test_stop_allows_a_clean_tree(self):
        self.assertEqual(self.stop().returncode, 0)

    def test_stop_blocks_while_checks_fail(self):
        self.p.write("src/app.py", "def add(a, b):\n    return a - b\n")
        r = self.stop()
        self.assertEqual(r.returncode, 2)
        self.assertIn("BLOCKED.md", r.stderr)
        self.assertIn("FAIL", r.stderr)

    def test_stop_never_traps_the_agent(self):
        self.p.write("src/app.py", "def add(a, b):\n    return a - b\n")
        self.assertEqual(self.stop({"stop_hook_active": True}).returncode, 0)
        self.assertEqual(self.stop(env={"FACTORY_STOP_CHECK": "0"}).returncode, 0)
        self.p.write("BLOCKED.md", "cannot\n")
        self.assertEqual(self.stop().returncode, 0)

    def test_stop_allows_when_checks_pass(self):
        self.p.write("src/app.py", "def add(a, b):\n    return b + a\n")
        self.assertEqual(self.stop().returncode, 0)


class TaskTests(unittest.TestCase):
    def test_new_then_seal_makes_the_owner_commit_the_gate_base(self):
        p = Project()
        try:
            sh("git checkout -q main", p.root)
            r = sh(FACTORY + ["task", "new", "neg", str(p.root)], p.root)
            self.assertEqual(r.returncode, 0, r.stdout)
            self.assertEqual(sh("git branch --show-current", p.root).stdout.strip(), "task/neg")
            spec = (p.root / "specs/neg.md").read_text()
            p.write("specs/neg.md", spec)
            self.assertEqual(sh(FACTORY + ["task", "seal", "neg", str(p.root)], p.root).returncode, 2,
                             "seal refuses a spec that is still the template")
            spec = (spec.replace("- path/to/file", "- src/app.py").replace("- <risk, and the acceptance test that covers it>", "- none: takes a number, stores nothing").replace("<What should be true", "neg(x) returns -x. <")
                    .replace("- `command that fails today and passes when done`", "- `python3 -c \"import sys; sys.path.insert(0, 'src'); from app import neg; assert neg(3) == -3\"`"))
            p.write("specs/neg.md", spec)
            p.write("tests/acceptance/test_neg.py", "import sys, unittest\nsys.path.insert(0, 'src')\nfrom app import neg\n"
                    "class N(unittest.TestCase):\n    def test_neg(self):\n        self.assertEqual(neg(2), -2)\n")
            text = (p.root / "factory.toml").read_text()
            p.write("factory.toml", text.replace('acceptance = ""', 'acceptance = "python3 -m unittest discover -s tests/acceptance -q"', 1))
            r = sh(FACTORY + ["task", "seal", "neg", str(p.root)], p.root)
            self.assertEqual(r.returncode, 0, r.stdout)
            self.assertIn("--base task/neg-base", r.stdout)
            gate = ["./check", "--gate", "--spec", "specs/neg.md", "--base", "task/neg-base"]
            self.assertEqual(sh(gate, p.root).returncode, 1, "fails on the new acceptance test before the work")
            p.write("src/app.py", "def add(a, b):\n    return a + b\n\ndef neg(x):\n    return -x\n")
            r = sh(gate, p.root)
            self.assertEqual(r.returncode, 0, r.stdout)
            p.write("tests/acceptance/test_neg.py", "import unittest\n")
            self.assertEqual(sh(gate, p.root).returncode, 1, "an agent editing the sealed test is caught")
        finally:
            p.close()

    def test_new_refuses_a_dirty_tree(self):
        p = Project()
        try:
            p.write("src/app.py", "x = 1\n")
            self.assertEqual(sh(FACTORY + ["task", "new", "x", str(p.root)], p.root).returncode, 2)
        finally:
            p.close()


class LessonsTests(unittest.TestCase):
    def test_lists_only_repeats_without_a_check(self):
        with tempfile.TemporaryDirectory() as d:
            Path(d, "LESSONS.md").write_text(
                "# Lessons\n\n## once\n- seen: 2026-10-01\n- check:\n\n"
                "## twice\n- seen: 2026-10-01, 2026-10-09\n- check:\n\n"
                "## handled\n- seen: 2026-10-01, 2026-10-02\n- check: lint rule no-print\n")
            out = sh(FACTORY + ["lessons", d], d).stdout
            self.assertIn("DUE  twice", out)
            self.assertNotIn("once", out)
            self.assertNotIn("handled", out)


if __name__ == "__main__":
    unittest.main()
