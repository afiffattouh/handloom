---
name: safe-migrations
description: Change a database without losing data or locking it. Use for any schema change.
---

1. Make changes additive first: add the new column or table, write to both, backfill, switch reads, then remove the old one in a later release.
2. Every migration has a rollback plan written in the task note. If there is none, say so.
3. Test it on a copy of realistic data, including empty tables and the largest table.
4. Avoid long locks: add indexes concurrently where the database allows it; batch backfills.
5. Never edit a migration that has already run anywhere.
