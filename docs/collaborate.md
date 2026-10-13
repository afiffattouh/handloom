# Working on one project together

This is how a small team works on one code project with Handloom: what is shared, what is not, and where the limits are.

## The shape

- **One hub.** Everyone signs in to the same web UI with their own account (the owner invites people; see [setup](setup.md)). Everyone sees the same jobs, inbox and activity.
- **Machines belong to the hub.** Each person's laptop, a shared build machine or a local-model box joins the hub with `handloom join`. Which machine runs a job is chosen when the job starts.
- **The code is shared through git.** Each machine that runs jobs for the project has its own clone of the repository, and the team's shared remote (GitHub, a company server, a bare repository on a shared disk, or the hub itself, see below) is how the clones meet. Handloom never pulls or pushes on its own, except when a person asks it to publish a job.
- **A project is the name Handloom gives to that shared remote.** The hub keeps the project's name, the remote's address and, for each machine, where its clone is and whether it is ready. For a remote you attach or have created on GitHub it never holds the code or anyone's git login. For a project you let the hub hold, it keeps the code (next section).

## No GitHub: let the hub hold the code

If your team has no GitHub, no ssh keys and no wish to set any up, the hub can keep the repository itself. Nothing to configure on the machines: each one already has the credential it got when it joined, and Handloom sets git up to use it.

1. **Make the project.** `handloom project new billing` (no address), or **Let Handloom hold the code** on the Projects page. Add `--device laptop-ana` to have that machine fetch its copy straight away. The project starts with one commit (a README) on `main`.
2. **Every machine gets its copy** the usual way (`handloom project clone billing --device laptop-ben`, or just start a job with `--project billing`). Plain `git fetch` and `git push` in that copy then work with no password, and no credential is written into the repository's address or files.
3. **Work and publish as usual.** `handloom job publish 7` pushes the job's branch to the hub as `handloom/job-7-...` and says so; there are no pull requests here.
4. **Merge.** The owner or a member merges a published job into `main`: `handloom job merge 7`, or the **Merge** button on the job's page. The hub does the merge itself (a fast-forward when it can, otherwise a merge commit "Merge job 7: ..."). If the work conflicts with what is in `main`, nothing changes and the answer names the files; start a new job from the current `main` and publish again. Other machines get the new `main` at the start of their next job, as for any project.

**The limits are on purpose.** Machines can only add branches named `handloom/...` and move them forward: the hub refuses pushes to `main`, tags, deletes and rewrites, in a sentence that says what to do instead. Only a person merges; an agent or an AI app cannot. **Confidential jobs are refused**: the code would sit on the hub, so publishing one to a hosted project says so, and the answer is to keep that work on the machine or use a repository you control. The code is in the hub's data folder (`git/`), not in its database: **include that folder in your backups** ([deploy](deploy.md)). Hosting needs `git` on the hub (the container image and the `.deb`/`.rpm` packages have it) and a public address for the hub: set `HANDLOOM_BASE_URL` to the address your machines joined with. Not done: copying a hosted project to GitHub from the hub, deleting a hosted project, and access that differs between people of one hub.

## Projects: getting the repository onto a machine

You no longer clone by hand. In the web UI, **Projects** (in the menu after Jobs), or on the command line:

- **A repository that exists:** `handloom project new billing --remote https://github.com/acme/billing`, or the **Attach a repository** form. The address is `https://host/owner/name`, `ssh://git@host/owner/name` or `git@host:owner/name`. An address with a password or token in it is refused, so is a path on a disk.
- **A repository that does not exist yet:** `handloom project new billing --create --owner acme --device laptop-ana` (add `--public` for a public one; it is private otherwise), or the **Create a repository** form. The chosen machine runs `gh repo create` under its own GitHub login, makes a first commit (a README) and pushes it so that the main branch exists, and reports the address back. Without `--owner` it is made under the account that machine's `gh` is logged in as. The machine needs the GitHub command line tool (`gh`) installed and `gh auth login` done; if not, the result says so in words.
- **A teammate's machine:** `handloom project clone billing --device laptop-ben`, or **Clone to a machine** on the project. That machine fetches its own copy into `<link folder>/projects/billing` (the link folder is `~/.config/handloom` by default), using `gh repo clone` for GitHub addresses when `gh` is there and `git clone` otherwise, and reports where it is. Asking again when the copy is already there changes nothing.
- **Starting a job:** `handloom job new "Fix the importer" --project billing --device laptop-ben`. If that machine has no copy yet, the command fetches it first (and waits, up to five minutes), then starts the job from the copy, from the project's main branch unless you give `--base`. The web form has a **Project** choice that lists the machines where the copy is ready. Publishing works as before, because the copy is an ordinary clone with an `origin`.

**Whose login is used.** Cloning and creating run on the machine, with the git and `gh` login of the user the link runs as, so each person's own GitHub permissions apply: if Ben cannot see the repository, his machine's clone fails with git's message and nobody else is affected. The hub only stores the name, the address and each machine's status and path. Nothing is ever deleted: if a folder with that name is already there and is not a clone of the same repository, the request fails and says so.

**Who may do it.** Attaching, creating and cloning are for people (owner and member). Creating a repository is outward-facing, so agents and AI apps cannot do it; an AI app can read the list of projects (`handloom_projects`). Each step is in the audit log (`project.attach`, `project.create`, `project.clone`, and `project.clone_done`, `project.create_done`, `project.clone_failed`, `project.create_failed`).

## A fresh start for each job (stage two)

When a job of a project starts on a machine, that machine first looks at the project's remote (`git fetch origin`) and, if it can do so safely, brings its own copy of the job's base branch up to date before the agents' worktrees are made. "Safely" means a plain fast-forward: nothing is merged, rebased or reset, and the machine's own history is never rewritten. A job never waits for this to succeed. The outcome is a short line on the job: on the job page under the title, in `handloom job show` (`Base: ...`), and in the audit log (`job.base`).

- **fresh**: the copy is at the remote's latest commit. The line says how far it moved, for example "main moved forward 3 commits to 4f2a9c1", or "main is already current at 4f2a9c1".
- **stale**: Handloom could not bring the copy up to date, and the job starts from the copy as it is. The line says why in words: the fetch failed (usually the machine is offline or has no access), the remote has no such branch, or the working folder has uncommitted changes on that branch (so nothing was touched).
- **diverged**: "this machine's copy of main has commits that are not on origin; the job starts from it as it is". Somebody committed straight onto main in this copy and did not push. Handloom leaves those commits alone and does not merge the remote's commits in, so the job's work may later conflict with the remote's main. To fix it, look at the copy yourself: push the commits if they belong on the remote (`git -C <copy> push`), or move them to a branch and reset main, then start the next job. Handloom will not do this for you.

Only the first agent of a job triggers this; later agents of the same job start from the same base, and the base does not move in the middle of a job. A job that is not from a project (it names a repository path) is not touched at all.

## Following the pull request (stage two)

After a publish that opened a pull request, the machine that published asks `gh pr view` about it, at most once every two minutes, while the pull request is open and for at most 14 days after the publish. The job shows where it stands: "Pull request: open, approved, can merge" (or "waiting for a review", "changes requested", "has conflicts"), "merged", or "closed without being merged". It appears in the Publish card on the job page, in `handloom job show`, and as a small "pull request merged" marker in the Jobs list and `handloom job list`. When it is merged or closed, the activity feed says so and the audit log has `publish.pr_merged` or `publish.pr_closed`, once. Nothing else happens by itself: the job is not closed, nothing is deleted, no notes are collected. If `gh` is missing on that machine, or it fails (offline, not logged in), the pull request is simply not looked at and the publish still shows its link. For trials the interval can be shortened with the environment variable `HANDLOOM_PR_POLL` on the link (for example `5s`).

**Not built yet (stage three, see the backlog):** a warning while a job runs that the base branch moved on the remote (across machines), and warnings when two jobs on different machines change the same files.

## Which machine runs which job

A job runs on **one machine**, in a repository path there (for a project, that machine's copy). The lead and every worker of that job run on that machine, each in its own git worktree, and the accepted work is merged into the branch `job/<id>/integration` of that machine's clone. Starting two jobs on the same project from two people's laptops means two clones, two integration branches, and git to reconcile them.

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
