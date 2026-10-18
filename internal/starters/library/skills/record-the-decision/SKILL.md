---
name: record-the-decision
description: Write down why a hard-to-reverse choice was made. Use when you pick a framework, data shape, interface or trade-off others will live with.
---

1. Write a short decision note: the situation, the choice, the options you rejected and why, and what it costs later.
2. Put it where the project keeps such notes (look for docs/adr or docs/decisions and match the format and numbering). If there is none, add docs/decisions/NNNN-title.md.
3. Never edit or delete an old note. If a decision changes, write a new note that replaces it and link the two.
4. Comment code only to say why, never what. Write down known traps next to the code they affect.
5. Update the README or changelog in the same change when behaviour users see has changed.

Source: condensed from addyosmani/agent-skills (MIT), documentation-and-adrs.
