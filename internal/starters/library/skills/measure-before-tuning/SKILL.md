---
name: measure-before-tuning
description: Make something faster only after measuring it. Use for any speed, size or load-time task.
---

1. Measure first with a tool, and write down the number and how you measured it. No number means no change.
2. Find the one place the time goes (profile, query plan, network trace). Do not guess.
3. Fix that one thing, then measure again the same way. If it did not improve, revert it.
4. Common culprits: a query per item in a loop, unbounded lists, missing indexes, images without sizes or modern formats, and large bundles loaded up front.
5. Keep a note of every attempt, including the ones you reverted.
6. Leave a guard behind where one is cheap: a test or a size budget that fails if it gets worse.

Source: condensed from addyosmani/agent-skills (MIT), performance-optimization.
