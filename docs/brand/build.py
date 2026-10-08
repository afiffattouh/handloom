import os, json
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen

GREEN="#1f5f4a"; TEAL="#4fc3b5"; INK="#14201e"; PAPER="#f7f8f7"; WHITE="#ffffff"
FONT="/home/user/projects/handloom/internal/hub/web/static/fonts/Geist-Variable.woff2"

def wordmark(text, size, wght=620, track=-0.015):
    f=TTFont(FONT); f=instantiateVariableFont(f,{"wght":wght}); gs=f.getGlyphSet(); cmap=f.getBestCmap(); upm=f["head"].unitsPerEm
    sc=size/upm; x=0; d=""
    for ch in text:
        g=cmap[ord(ch)]; pen=SVGPathPen(gs)
        gs[g].draw(TransformPen(pen,(sc,0,0,-sc,x,0)))
        d+=pen.getCommands(); x+=gs[g].width*sc+track*size
    return d, x-track*size

def mark(uid, stem=WHITE, bar=TEAL, k=1.0, sw=8, gap=2.6):
    """The weave H on a 64 grid: the bar passes over the left stem and under the right one."""
    lx, rx, top, bot, by = 21, 43, 11, 53, 32
    return f'''<defs><mask id="{uid}a"><rect width="64" height="64" fill="#fff"/><rect x="{7-gap}" y="{by-sw/2-gap}" width="{50+2*gap}" height="{sw+2*gap}" rx="{sw/2+gap}" fill="#000"/></mask>
<mask id="{uid}b"><rect width="64" height="64" fill="#fff"/><rect x="{rx-sw/2-gap}" y="{top-gap}" width="{sw+2*gap}" height="{bot-top+2*gap}" rx="{sw/2+gap}" fill="#000"/></mask></defs>
<rect x="{lx-sw/2}" y="{top}" width="{sw}" height="{bot-top}" rx="{sw/2}" fill="{stem}" mask="url(#{uid}a)"/>
<rect x="7" y="{by-sw/2}" width="50" height="{sw}" rx="{sw/2}" fill="{bar}" mask="url(#{uid}b)"/>
<rect x="{rx-sw/2}" y="{top}" width="{sw}" height="{bot-top}" rx="{sw/2}" fill="{stem}"/>'''

def icon(uid="i", bg=GREEN, r=14, **kw):
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="Handloom"><rect width="64" height="64" rx="{r}" fill="{bg}"/>{mark(uid, **kw)}</svg>'

def glyph(color="currentColor"):
    # the mark alone, one colour: the bar is the same colour, the weave shows in the gaps
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="Handloom">{mark("g", stem=color, bar=color)}</svg>'

def lockup(dark=False):
    word, w = wordmark("Handloom", 40)
    fg = WHITE if dark else INK
    H=64; total=64+18+w
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {total:.1f} {H}" role="img" aria-label="Handloom">'
            f'<g>{mark("l")}</g><rect width="64" height="64" rx="14" fill="{GREEN}" style="mix-blend-mode:normal" opacity="0"/>'
            f'<path transform="translate(82 45)" d="{word}" fill="{fg}"/></svg>'), total

def favicon():
    # small sizes: heavier strokes, narrower gaps, no fine detail
    return icon("f", sw=10, gap=2.2, r=14)

out="out"; os.makedirs(out, exist_ok=True)
def put(name, s): open(f"{out}/{name}","w").write(s)

# the icon needs the green tile behind the mark: lockup draws it
def lockup_full(dark=False):
    word, w = wordmark("Handloom", 40); fg = WHITE if dark else INK
    total=64+18+w
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {total:.1f} 64" role="img" aria-label="Handloom">'
            f'<rect width="64" height="64" rx="14" fill="{GREEN}"/>{mark("l")}<path transform="translate(82 45)" d="{word}" fill="{fg}"/></svg>')
put("mark.svg", icon("m"))
put("favicon.svg", favicon())
put("glyph.svg", glyph())
put("logo.svg", lockup_full(False))
put("logo-dark.svg", lockup_full(True))
word,w=wordmark("Handloom",40)
put("wordmark.svg", f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w:.1f} 52" role="img" aria-label="Handloom"><path transform="translate(0 40)" d="{word}" fill="{INK}"/></svg>')
print("built", round(w,1))
