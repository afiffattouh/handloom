# Spec: <short title>

## Goal

<What should be true when this is done, in one or two sentences.>

## Files

<Only these may change. One per line, globs allowed (src/parser/*.py, docs/).>
- path/to/file

## Out of scope

- <What not to touch, even if it looks related.>

## Risks

<Ways this could go wrong that the acceptance tests must cover: hostile input (`../x`, empty, huge), data shared with a caller, boundaries (zero, one, the largest). Write "none" only if the change takes no outside input and stores nothing.>
- <risk, and the acceptance test that covers it>

## Checks

<Commands that prove the goal. `./check --gate --spec specs/<this file>` runs them after the stages.>
- `command that fails today and passes when done`

## Notes

<Context, links, decisions.>
