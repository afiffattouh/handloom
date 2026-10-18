# Acceptance tests

Tests in this folder define "done" and are owned by a person. Agents may read and run them,
never change them. The gate fails any branch that changes a file here, and `./check` fails
when a file no longer matches `.manifest`.

To change them: edit on the base branch, review, then run `factory accept-tests` and commit
the new `.manifest` with the change.
