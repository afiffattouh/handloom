# Working on one project together

This is how a small team works on one code project with Handloom: what is shared, what is not, and where the limits are.

## The shape

- **One hub.** Everyone signs in to the same web UI with their own account (the owner invites people; see [setup](setup.md)). Everyone sees the same jobs, inbox and activity.
- **Machines belong to the hub.** Each person's laptop, a shared build machine or a local-model box joins the hub with `handloom join`. Which machine runs a job is chosen when the job starts.
- **The code is shared through git, not through Handloom.** Each machine that runs jobs for the project has its own clone of the repository, and the team's shared remote (GitHub, a company server, a bare repository on a shared disk) is how the clones meet. Handloom never pulls or pushes on its own.

## Which machine runs which job

A job names a repository path **on one machine**. The lead and every worker of that job run on that machine, each in its own git worktree, and the accepted work is merged into the branch `job/<id>/integration` of that machine's clone. Starting two jobs on the same project from two people's laptops means two clones, two integration branches, and git to reconcile them.

## Splitting the work

Split by job. One person starts a job for the parser and another for the importer, each on their own machine or on the same one. Inside a job the lead splits the work into tasks for its workers. Tasks of one job are merged into one branch by the machine, with the check command run after each merge.

## Publishing a finished job

When a job has accepted, merged work, a person (owner or member, not a viewer, not an AI app, not an agent) can publish it:

- Web: the **Publish** card on the job page, with **Push branch** and **Push and open pull request**.
- Command line: `handloom job publish <id> [--remote R] [--pr]`.

The machine that holds the clone pushes `job/<id>/integration` to the remote (default `origin`) as `handloom/job-<id>-<title slug>`. It is **never forced**: if that branch already exists on the remote and is not an ancestor of yours, the publish fails with git's message and the remote is left as it was. Before pushing it fetches the remote and tells you if the job's base branch has moved on there ("the base moved: N new commits on origin/main; the pull request may conflict").

With `--pr` and `gh` installed on that machine, it also opens the pull request, titled with the job's title, with the job's brief and the list of tasks with who did them. The agents' evidence and handoff notes are not included. Without `gh` the branch is still pushed and the result says to open the pull request from the branch yourself.

Publishing is a person's decision and is in the audit log (`job.publish`, `job.published`, `job.publish_failed`). It is not offered to AI apps or agents.

## Overlap warnings

When you start a repository job, Handloom lists the other **open** jobs on the same repository path and the same machine: "Also open on this repository: #3 Title (lead x). Jobs that change the same files will conflict when their branches are merged." You see it in the command's output, in the notice after the web form, on the job page and in `handloom job show`. It is a warning only; nothing is blocked.

## What is not shared

- **Work between machines.** Jobs on different machines do not see each other's branches until someone publishes and the other side pulls. The overlap warning only knows about jobs on the same machine and path.
- **One job's agents stay on one machine.** A job cannot spread its workers over several machines.
- **Conflicts are git's.** Handloom reports a merge conflict inside a job and asks the lead to resolve it; conflicts between two jobs, or between a job and the remote, are resolved in the pull request as usual.
- **Client notes** stay on the machine that holds the knowledge repository.
- There is no per-person isolation inside a hub (see [Teams](../README.md#teams)).
