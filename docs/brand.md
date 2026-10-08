# Handloom brand

## The idea

A handloom is a loom worked by a person. **Warp** threads are held in tension; the **weft** thread is carried across by hand, over some, under others, until separate threads are cloth. Handloom does the same for coding agents: the agents and machines are the threads, a person holds the tension and carries the work across, and what comes out is one piece, not a heap.

The words we use follow from this and stay plain: *job*, *agent*, *machine*, *review*. We do not say "weave" in the product; the metaphor lives in the mark.

**Tagline:** Your agents, on your machines.
**One line:** Open-source, self-hosted coordination of coding agents across devices.

## The mark

An **H** made of two upright threads and one thread carried across. The crossing thread passes **over** the left stem and **under** the right one, so the letter is also a weave. Rounded ends, no outline, one gap where a thread goes under.

| File | Use |
| --- | --- |
| `docs/brand/mark.svg` | The app icon: the mark on the green tile. Use wherever a square logo fits. |
| `docs/brand/logo.svg`, `logo-dark.svg` | Mark and wordmark side by side, for light and dark backgrounds. |
| `docs/brand/wordmark.svg` | The name alone, for tight places next to an existing mark. |
| `docs/brand/glyph.svg` | The mark alone in one colour (it takes `currentColor` when inlined): menus, stamps, a single-ink print. |
| `docs/brand/favicon.svg` | The small-size cut: heavier threads and narrower gaps so it reads at 16 px. Also in the web UI as `favicon-32.png` and `apple-touch-icon.png`. |
| `docs/brand/banner.png` | The README header. |
| `docs/brand/build.py` | Draws all of the above (Python with `fonttools`). Edit the numbers there, never the SVGs. |

**Clear space:** half the height of the mark on every side. **Smallest size:** 16 px for the favicon cut, 24 px for the full mark.

**Do not:** recolour the threads, outline the mark, add a gradient or shadow, tilt it, put it on a busy photograph, redraw the weave so both threads go over, or stretch it. Do not set the name in another typeface.

## Colour

| Name | Value | Use |
| --- | --- | --- |
| Loom green | `#1f5f4a` | The tile behind the mark. The brand's ground colour. |
| Thread teal | `#4fc3b5` | The crossing thread. Also the interface's accent in dark mode. |
| Warp white | `#ffffff` | The two stems. |
| Ink | `#14201e` | Text on light. |
| Paper | `#f7f8f7` | The page. |

The interface's own tokens (`internal/hub/web/static/app.css`, `docs/design-principles.md`) are the working palette: primary teal `#0f766e` on light, `#4fc3b5` on dark, neutrals with a slight green cast. Colour in the product means something (act here, wrong, fine, waiting); the brand colours are for the mark, not for decoration.

## Type

**Geist** for text and the wordmark (semi-bold, a little tight), **Geist Mono** for identifiers, commands and numbers. Both are SIL OFL and ship inside the hub, so a self-hosted hub never calls a font service. The wordmark in the SVGs is converted to outlines, so it needs no font installed.

## Voice

Calm, plain, specific. Short sentences. The person's words ("question", "work to review", "machine"), buttons that say what happens ("Accept", "Send back"). Say what Handloom did and what it did not do; never claim more than was checked. Nothing is "powered by AI", nothing is "seamless". The product is a tool someone trusts with their repositories, so it sounds like a careful colleague, not a campaign.

## Where it appears

The web UI (favicon, sidebar, sign-in), the README banner, the published overview page, release notes, and the terminal console's header text. In the terminal the mark is not drawn; the name is.
