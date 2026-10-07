# Design principles for the Handloom UI

These are rules, not taste. Anything built for the web UI follows them, and a change that breaks one needs a reason written down. They come from reading how shadcn/ui builds its components (card, badge, button, table, tabs, alert, sidebar, item, empty) and from what looked generic in the first redesign mockup.

## The look in one sentence

A calm, dense, neutral console: hairlines and whitespace do the structure, one accent colour means "act here", and colour otherwise appears only to say something is wrong, fine or waiting.

## Rules

1. **Semantic tokens only.** Components use `--background`, `--card`, `--muted`, `--border`, `--primary`, `--destructive` and their `-foreground` pairs, never a raw colour. Light and dark are the same tokens with different values.
2. **One border language.** A container has a full 1px hairline on all four sides, or no border at all. **Never a border on one side only** (no left-stripe cards, no coloured top bars). Severity is shown by an icon and text colour, or by the badge, never by a stripe.
3. **At most three surfaces:** the page, a card, and an inset (a muted block inside a card, such as a diff or a terminal). No card inside a card inside a card. Groups inside a card are rows divided by hairlines.
4. **Cards are for objects** (a job, a question, a chart), not for every heading. A page is mostly tables and rows.
5. **Colour is rare.** Neutrals with a slight green cast carry 95% of the screen. The primary colour marks the one action per view. Success, warning and destructive colours appear only as small text, icons or badges. No gradients, no glow, no coloured tiles.
6. **Type.** One sans family for the interface, one mono family for identifiers and numbers in tables. Five sizes: 12, 13, 14, 16, 24. Labels are sentence case and muted. No spaced-out uppercase eyebrow labels. Numbers use tabular figures.
7. **Spacing on a 4px grid.** Gaps of 8, 12, 16, 24. Controls are 28, 32 or 36px high; everything of one kind is the same height.
8. **A small set of components, used everywhere:**
   - Button: default (primary), outline, ghost, destructive; sizes sm and default.
   - Badge: a pill; secondary, outline, success, warning, destructive; optional status dot.
   - Card: header (title, description, action on the right), content, footer.
   - Table: hairline rows, muted sentence-case header, hover tint, no outer box of its own.
   - Tabs: a muted track with a raised active tab.
   - Alert and Insight: full hairline, an icon, a title and a description.
   - Progress, Separator, Kbd, Empty state, Sidebar item.
9. **Navigation shows the active item with a filled row**, not a side bar.
10. **Icons are one stroke family** (Lucide, 1.5px stroke look), 16px in controls. No emoji and no text glyphs standing in for icons.
11. **Charts:** one hue per series, faint dashed gridlines, a few ticks, direct values where they fit. No rainbow, no 3D, no decoration.
12. **Motion** only to show a change (a state turning over, a toast). Nothing pulses by default. `prefers-reduced-motion` turns it all off.
13. **Every state is designed:** empty (says what will appear and how to add the first), loading, error (says what to do), disabled.
14. **Accessibility:** text contrast at least 4.5:1, a 3px focus ring at half opacity on every interactive element, everything reachable by keyboard, state never shown by colour alone.
15. **Words:** short sentences, the user's vocabulary ("question", "work to review", "machine"), buttons say what happens ("Accept", "Send back"). Nothing is "powered by AI".

## Things that read as generic and are banned

- Cards with one coloured edge. Gradient or tinted KPI tiles. A row of six equal boxes of big numbers as the page's only structure.
- Pulsing dots and glows. Purple or blue-violet as the accent. Emoji as icons.
- Everything in a card, everything rounded the same, everything with a shadow.
- Uppercase mono micro-labels over every block.
- Centered hero text on an app screen.

## How to check a screen

Squint test: with the colour taken out, the hierarchy must still read. Border test: search the CSS for `border-left`, `border-top`, `border-inline-start`; none may be used as an accent. Colour test: count the saturated colours on screen; more than two besides state colours is too many.
