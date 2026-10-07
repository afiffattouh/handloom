---
name: debug-systematically
description: Find a bug's cause before changing code. Use when something fails and the cause is unknown.
---

1. Reproduce it with the smallest input. Write the exact command and output.
2. State what you expected and what happened. The gap is the bug.
3. Form one hypothesis and the cheapest test that would disprove it. Run it. Do not change code to "see what happens".
4. Bisect: halve the code path or the history until the cause is one change or one line.
5. Fix the cause, not the symptom. Add a test that failed before your fix.
6. If two hypotheses fail in a row, stop and say what you have ruled out.
