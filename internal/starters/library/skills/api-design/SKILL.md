---
name: api-design
description: Design an API that is hard to misuse. Use when adding or changing an interface.
---

- Name things after what callers want, not how you implement them.
- Make the common call short; make dangerous options explicit.
- Errors say what to do next and carry a stable code.
- Never break an existing caller silently: add, deprecate, then remove.
- Write the example call first; if it reads badly, the design is wrong.
- Document each endpoint with a request, a response and one failure.
