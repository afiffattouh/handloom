---
name: plan-a-job
description: Break a job into tasks a worker can finish and a check can judge. Use when you lead a job and have just read the brief.
---

1. Restate the goal in one sentence and write what "done" looks like as things you can check (a file, a passing test, a number, a decision).
2. Split the work along who touches what. Two tasks that edit the same file are one task, or the second depends on the first.
3. Give each task: a title that names the outcome, the files or folders it may touch, and its acceptance test in one line.
4. Make dependencies explicit (`--depends`). A worker never sees another worker's files until they are merged.
5. Run independent work at the same time. Tasks that touch different files and do not need each other's result go to different workers and run in parallel. Do not chain tasks only because they will all end up registered in one shared file (a dispatcher, a README, a config): let each worker do its own files, and give the edits to the shared files to one last task that depends on all of them. Use one worker when the work is small or tightly connected; use two or three when independent parts can proceed together.
6. Create tasks only after the worker you assign them to is running.
7. If a fact you need is not in the brief or the client knowledge, ask the human once, with options, before you plan around a guess.
