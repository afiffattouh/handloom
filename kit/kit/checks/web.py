#!/usr/bin/env python3
"""Web checks for a site (factory-kit web preset). Vendored as .factory/checks/web.py.

    python3 .factory/checks/web.py links     every internal link, image and script resolves; #anchors exist
    python3 .factory/checks/web.py images    every <img> has an alt attribute (alt="" marks a decoration)
    python3 .factory/checks/web.py meta      every page has a <title>, a meta description and exactly one <h1>
    python3 .factory/checks/web.py budget    no image or page over its size limit

They read the built site: [web] dir in factory.toml, else the first of dist, build, out, _site,
public that exists, else the project folder. Standard library only. Exit 0 pass, 1 problems found
(each printed as `file: what is wrong`), 2 the site folder cannot be found.

[web] in factory.toml (all optional):  dir, max_image_kb (500), max_html_kb (200), ignore (a list of
URL prefixes the links check skips, for example ["/api/"]).
"""
from __future__ import annotations

import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import _toml  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent.parent
SITE_DIRS = ("dist", "build", "out", "_site", "public")
IMAGE_SUFFIXES = {".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".svg"}
SKIP_PARTS = {"node_modules", ".git", ".factory", "runs", "venv", ".venv", "tests", "specs"}


def config() -> dict:
    path = ROOT / "factory.toml"
    return (_toml.loads(path.read_text()).get("web") or {}) if path.exists() else {}


def site_dir(cfg: dict) -> Path | None:
    if cfg.get("dir"):
        d = ROOT / cfg["dir"]
        return d if d.is_dir() else None
    for name in SITE_DIRS:
        if (ROOT / name).is_dir():
            return ROOT / name
    return ROOT


class Page(HTMLParser):
    """What the checks need from one HTML file."""

    def __init__(self):
        super().__init__()
        self.refs, self.imgs, self.ids = [], [], set()
        self.title = self.description = ""
        self.h1 = 0
        self._in_title = False

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if a.get("id"):
            self.ids.add(a["id"])
        if a.get("name") and tag == "a":
            self.ids.add(a["name"])
        if tag == "a" and a.get("href"):
            self.refs.append(("link", a["href"]))
        elif tag == "img":
            self.imgs.append(a)
            if a.get("src"):
                self.refs.append(("image", a["src"]))
        elif tag == "script" and a.get("src"):
            self.refs.append(("script", a["src"]))
        elif tag == "link" and a.get("href") and a.get("rel", "") in ("stylesheet", "icon", "preload"):
            self.refs.append(("stylesheet", a["href"]))
        elif tag == "meta" and (a.get("name") or "").lower() == "description":
            self.description = (a.get("content") or "").strip()
        elif tag == "h1":
            self.h1 += 1
        elif tag == "title":
            self._in_title = True

    def handle_endtag(self, tag):
        if tag == "title":
            self._in_title = False

    def handle_data(self, data):
        if self._in_title:
            self.title += data.strip()


def pages(base: Path) -> dict:
    out = {}
    for p in sorted(base.rglob("*.html")):
        if SKIP_PARTS.intersection(p.relative_to(base).parts):
            continue
        page = Page()
        page.feed(p.read_text(errors="replace"))
        out[p] = page
    return out


def resolve(base: Path, page_path: Path, href: str) -> Path | None:
    """The local file an internal URL points to, or None when it is not internal."""
    parts = urlsplit(href)
    if parts.scheme or parts.netloc or href.startswith(("mailto:", "tel:", "javascript:", "data:", "#")):
        return None
    target = unquote(parts.path)
    path = (base / target.lstrip("/")) if target.startswith("/") else (page_path.parent / target)
    for candidate in (path, path / "index.html", path.with_suffix(".html") if path.suffix == "" else path):
        if candidate.is_file():
            return candidate
    return path  # does not exist; the caller reports it


def check_links(base: Path, cfg: dict, found: dict) -> list:
    problems = []
    ignore = tuple(cfg.get("ignore") or [])
    for path, page in found.items():
        rel = path.relative_to(base).as_posix()
        for kind, href in page.refs:
            if ignore and href.startswith(ignore):
                continue
            target = resolve(base, path, href)
            if target is None:
                continue
            if not target.is_file():
                problems.append(f"{rel}: broken {kind} {href}")
                continue
            frag = urlsplit(href).fragment
            if frag and kind == "link" and target in found and frag not in found[target].ids:
                problems.append(f"{rel}: {href} points to a missing anchor #{frag}")
        for frag in {urlsplit(h).fragment for k, h in page.refs if h.startswith("#") and len(h) > 1}:
            if frag not in page.ids:
                problems.append(f"{rel}: #{frag} anchor does not exist on this page")
    return problems


def check_images(base: Path, cfg: dict, found: dict) -> list:
    problems = []
    for path, page in found.items():
        rel = path.relative_to(base).as_posix()
        for img in page.imgs:
            if "alt" not in img:
                problems.append(f"{rel}: image {img.get('src', '(no src)')} has no alt text")
    return problems


def check_meta(base: Path, cfg: dict, found: dict) -> list:
    problems = []
    for path, page in found.items():
        rel = path.relative_to(base).as_posix()
        if not page.title:
            problems.append(f"{rel}: no <title>")
        if not page.description:
            problems.append(f"{rel}: no meta description")
        if page.h1 != 1:
            problems.append(f"{rel}: {page.h1} <h1> headings (want exactly 1)")
    return problems


def check_budget(base: Path, cfg: dict, found: dict) -> list:
    problems = []
    img_kb, html_kb = int(cfg.get("max_image_kb", 500)), int(cfg.get("max_html_kb", 200))
    for p in sorted(base.rglob("*")):
        if not p.is_file() or SKIP_PARTS.intersection(p.relative_to(base).parts):
            continue
        kb = p.stat().st_size / 1024
        rel = p.relative_to(base).as_posix()
        if p.suffix.lower() in IMAGE_SUFFIXES and kb > img_kb:
            problems.append(f"{rel}: image is {kb:.0f} KB (limit {img_kb} KB). Resize or compress it.")
        elif p.suffix == ".html" and kb > html_kb:
            problems.append(f"{rel}: page is {kb:.0f} KB (limit {html_kb} KB)")
    return problems


CHECKS = {"links": check_links, "images": check_images, "meta": check_meta, "budget": check_budget}


def main(argv=None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) != 1 or argv[0] not in CHECKS:
        print(__doc__.split("\n\n")[0], file=sys.stderr)
        return 2
    cfg = config()
    base = site_dir(cfg)
    if base is None:
        print(f"web: the site folder {cfg.get('dir')!r} does not exist. Build the site first, or fix [web] dir.", file=sys.stderr)
        return 2
    found = pages(base)
    if not found and argv[0] != "budget":
        print(f"web: no HTML pages under {base.relative_to(ROOT).as_posix() or '.'}. Build the site first, or set [web] dir.", file=sys.stderr)
        return 2
    problems = CHECKS[argv[0]](base, cfg, found)
    for line in problems[:50]:
        print(line)
    if len(problems) > 50:
        print(f"... and {len(problems) - 50} more")
    if problems:
        print(f"web {argv[0]}: {len(problems)} problem(s)")
        return 1
    print(f"web {argv[0]}: ok ({len(found)} page(s))")
    return 0


if __name__ == "__main__":
    sys.exit(main())
