#!/usr/bin/env python3
"""Turn tmux's `capture-pane -e` output into a small HTML page that looks like a terminal window (for screenshots)."""
import html, os, re, sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))

src, dst, title = sys.argv[1], sys.argv[2], sys.argv[3]
text = open(src, encoding="utf-8").read().rstrip("\n")
BASE = ["#1b2321", "#e06c75", "#7fd6a8", "#e3b341", "#61afef", "#c678dd", "#56b6c2", "#d7dedb",
        "#5c6a66", "#ff7b72", "#9ae6b4", "#f0d77a", "#8cc4ff", "#d8a6ff", "#7fe0e0", "#ffffff"]

def c256(n):
    if n < 16: return BASE[n]
    if n < 232:
        n -= 16; r, g, b = n // 36, (n // 6) % 6, n % 6
        v = lambda x: 0 if x == 0 else 55 + 40 * x
        return "#%02x%02x%02x" % (v(r), v(g), v(b))
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)

out, fg, bg, bold, dim = [], None, None, False, False
def span(t):
    if not t: return ""
    st = []
    if fg: st.append("color:" + fg)
    if bg: st.append("background:" + bg)
    if bold: st.append("font-weight:700")
    if dim: st.append("opacity:.6")
    t = html.escape(t)
    return '<span style="%s">%s</span>' % (";".join(st), t) if st else t

for line in text.split("\n"):
    pos, buf, row = 0, "", ""
    for m in re.finditer(r"\x1b\[([0-9;]*)m", line):
        row += span(line[pos:m.start()]); pos = m.end()
        codes = [int(x) if x else 0 for x in m.group(1).split(";")]
        i = 0
        while i < len(codes):
            c = codes[i]
            if c == 0: fg = bg = None; bold = dim = False
            elif c == 1: bold = True
            elif c == 2: dim = True
            elif c == 22: bold = dim = False
            elif 30 <= c <= 37: fg = BASE[c - 30]
            elif 90 <= c <= 97: fg = BASE[c - 90 + 8]
            elif 40 <= c <= 47: bg = BASE[c - 40]
            elif 100 <= c <= 107: bg = BASE[c - 100 + 8]
            elif c == 39: fg = None
            elif c == 49: bg = None
            elif c in (38, 48) and i + 1 < len(codes):
                if codes[i + 1] == 2 and i + 4 < len(codes):
                    col = "#%02x%02x%02x" % tuple(codes[i + 2:i + 5]); i += 4
                elif codes[i + 1] == 5 and i + 2 < len(codes):
                    col = c256(codes[i + 2]); i += 2
                else: col = None
                if c == 38: fg = col
                else: bg = col
            i += 1
    row += span(line[pos:])
    out.append(row)
page = f"""<!doctype html><meta charset=utf-8><style>
@font-face {{ font-family: GeistMono; src: url("file://{ROOT}/internal/hub/web/static/fonts/GeistMono-Variable.woff2"); font-weight: 100 900; }}
body {{ margin: 0; background: #0d1312; padding: 28px; display: inline-block; }}
.win {{ background: #131b1a; border: 1px solid #25312f; border-radius: 12px; overflow: hidden; box-shadow: 0 20px 50px rgba(0,0,0,.45); }}
.bar {{ height: 36px; display: flex; align-items: center; gap: 8px; padding: 0 14px; background: #1a2423; border-bottom: 1px solid #25312f; color: #93a3a6; font: 500 13px/1 system-ui, sans-serif; }}
.dot {{ width: 11px; height: 11px; border-radius: 50%; background: #3a4a47; }}
pre {{ margin: 0; padding: 16px 18px 18px; color: #d7dedb; font: 14.5px/1.32 GeistMono, ui-monospace, monospace; font-variant-ligatures: none; }}
</style><div class=win><div class=bar><i class=dot></i><i class=dot></i><i class=dot></i><span style="margin-left:10px">{html.escape(title)}</span></div><pre>{chr(10).join(out)}</pre></div>"""
open(dst, "w", encoding="utf-8").write(page)
