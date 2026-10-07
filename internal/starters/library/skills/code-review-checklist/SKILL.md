---
name: code-review-checklist
description: Review a diff for correctness, risk and clarity. Use when asked to review.
---

Go through the diff in this order and write only findings that matter:
1. Does it do what the task says, and nothing else?
2. Correctness: edge cases, error paths, off-by-one, concurrency, data loss.
3. Safety: input from outside, permissions, secrets, injection.
4. Tests: do they fail without the change? Do they check behaviour or only call the code?
5. Clarity: names, size of functions, comments that explain why.
Each finding: file and line, what is wrong, a concrete fix, and how sure you are. Separate "must fix" from "consider". Say what you did not check.
