---
name: ci-hygiene
description: Keep build and deploy pipelines safe and boring. Use when changing CI or deploy files.
---

- Pin versions of actions and base images. Never use `latest` in anything that deploys.
- Secrets come from the platform's secret store, never from the repo or the log; mask them.
- A change to a pipeline is tested on a branch before it touches the main one.
- Destructive steps (delete, destroy, force-push) need an explicit human approval in the task; do not add one on your own.
- Every deploy has a way back: say what it is in the task note.
