---
name: build-in-slices
description: Build one small working slice at a time. Use for any change that touches more than one file or step.
---

1. Pick the smallest slice that works end to end (one path through the code), not one layer at a time.
2. Implement it, run the tests, check it does what the task says, then commit it before starting the next slice.
3. Keep the code compiling and the tests green between slices. If a slice is not finished, put it behind a flag or leave it out.
4. Touch only what the task needs. Note unrelated problems in your handoff; do not fix them.
5. Take the simplest design that works. Add no abstraction for a second use that does not exist yet.
6. Make each slice easy to undo: one commit, no mixed concerns.
7. Stop and say so if a slice grows past what you can test in one go; split it.

Source: condensed from addyosmani/agent-skills (MIT), incremental-implementation.
