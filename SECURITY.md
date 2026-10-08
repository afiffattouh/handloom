# Security

Handloom stores credentials hashed, keeps the hub free of git, shells and models, and lets only people approve work (see `docs/protocol.md` and the guard rails in `docs/overview.html`).

**Reporting a problem.** Please do not open a public issue for a vulnerability. Use GitHub's private vulnerability reporting on this repository (Security, then "Report a vulnerability"), with what you saw, what you expected and how to reproduce it. You will get an answer, not silence.

**What is in scope:** the hub, the link, the CLI and the web UI in this repository: authentication and sessions, the scope of tokens (admin, device, person, app, run), the per-job scope and merge checks, and anything that lets an agent do what only a person may.

**Self-hosting.** Run the hub behind TLS (the included Caddy file does this), keep the admin token and the data volume private, and back up with `handloom hub backup`.
