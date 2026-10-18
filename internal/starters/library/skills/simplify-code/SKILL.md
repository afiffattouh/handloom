---
name: simplify-code
description: Make code easier to read without changing what it does. Use when asked to clean up, or when your own change came out convoluted.
---

1. Behaviour must stay exactly the same: same results, same errors, same side effects. Run the tests before and after.
2. Find out why the code is the way it is before changing it (read the history or the callers). Do not remove what you do not understand.
3. Follow the project's own conventions, not yours.
4. Prefer clear to clever: early returns for nested conditions, names that say what a thing is, no dense one-liners.
5. Do not inline a helper that names a concept, merge unrelated logic, or shorten code just to have fewer lines.
6. Only simplify code you changed or were asked to. Change one thing at a time and run the tests after each.

Source: condensed from addyosmani/agent-skills (MIT), code-simplification.
