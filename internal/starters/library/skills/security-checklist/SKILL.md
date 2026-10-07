---
name: security-checklist
description: Look for the common ways code gets attacked. Use when reviewing or writing code that handles outside input.
---

Check, and report what you found with file and line:
- Injection: SQL, shell, template, path. Is outside input ever concatenated into a command or query?
- Authentication and authorisation: is every route checked, and checked for this user and this object?
- Secrets: any key, token or password in code, logs, errors or tests?
- Input limits: size, type, rate. Can one request exhaust memory or disk?
- Output: is untrusted text escaped where it is shown?
- Dependencies: anything unmaintained or pinned to a known-bad version?
Rank findings by how easily they can be exploited and what is lost. Do not run exploits against systems that are not yours.
