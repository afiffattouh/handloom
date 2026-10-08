# Contributing

Handloom is a Go project (one static binary) with a server-rendered web UI and no front-end build step.

```
go vet ./... && go test ./...
```

Before a change that touches the UI, read `docs/design-principles.md`; before one that changes behaviour, add a line to `DECISIONS.md` saying what you chose and why (and what you did not test). Tests that start real agents live in `test/e2e/` and need the agent CLIs installed; the unit tests do not.

Keep changes small and plain: sentence-case words a person would use, no new dependency without a reason, comments that explain why.
