# Working on one project together

This is how a small team works on one code project with Handloom: what is shared, what is not, and where the limits are.

## The shape

- **One hub.** Everyone signs in to the same web UI with their own account (the owner invites people; see [setup](setup.md)). Everyone sees the same jobs, inbox and activity.
- **Machines belong to the hub.** Each person's laptop, a shared build machine or a local-model box joins the hub with `handloom join`. Which machine runs a job is chosen when the job starts.
- **The code is shared through git, not through Handloom.** Each machine that runs jobs for the project has its own clone of the repository, and the team's shared remote (GitHub, a company server, a bare repository on a shared disk) is how the clones meet. Handloom never pulls or pushes on its own, except when a person asks it to publish a job.
- **A project is the name Handloom gives to that shared remote.** The hub keeps the project's name, the remote's address and, for each machine, where its clone is and whether it is ready. It never holds the code or anyone's git login.

## Projects: getting the repository onto a machine

You no longer clone by hand. In the web UI, **Projects** (in the menu after Jobs), or on the command line:

- **A repository that exists:** `handloom project new billing --remote https://github.com/acme/billing`, or the **Attach a repository** form. The address is `https://host/owner/name`, `ssh://git@host/owner/name` or `git@host:owner/name`. An address with a password or token in it is refused, so is a path on a disk.
- **A repository that does not exist yet:** `handloom project new billing --create --owner acme --device laptop-ana` (add `--public` for a public one; it is private otherwise), or the **Create a repository** form. The chosen machine runs `gh repo create` under its own GitHub login, makes a first commit (a README) and pushes it so that the main branch exists, and reports the address back. Without `--owner` it is made under the account that machine's `gh` is logged in as. The machine needs the GitHub command line tool (`gh`) installed and `gh auth login` done; if not, the result says so in words.
- **A teammate's machine:** `handloom project clone billing --device laptop-ben`, or **Clone to a machine** on the project. That machine fetches its own copy into `<link folder>/projects/billing` (the link folder is `~/.config/handloom` by default), using `gh repo clone` for GitHub addresses when `gh` is there and `git clone` otherwise, and reports where it is. Asking again when the copy is already there changes nothing.
- **Starting a job:** `handloom job new "Fix the importer" --project billing --device laptop-ben`. If that machine has no copy yet, the command fetches it first (and waits, up to five minutes), then starts the job from the copy, from the project's main branch unless you give `--base`. The web form has a **Project** choice that lists the machines where the copy is ready. Publishing works as before, because the copy is an ordinary clone with an `origin`.

**Whose login is used.** Cloning and creating run on the machine, with the git and `gh` login of the user the link runs as, so each person's own GitHub permissions apply: if Ben cannot see the repository, his machine's clone fails with git's message and nobody else is affected. The hub only stores the name, the address and each machine's status and path. Nothing is ever deleted: if a folder with that name is already there and is not a clone of the same repository, the request fails and says so.

**Who may do it.** Attaching, creating and cloning are for people (owner and member). Creating a repository is outward-facing, so agents and AI apps cannot do it; an AI app can read the list of projects (`handloom_projects`). Each step is in the audit log (`project.attach`, `project.create`, `project.clone`, and `project.clone_done`, `project.create_done`, `project.clone_failed`, `project.create_failed`).

**Not built yet (planned, see the backlog):** starting a job from a freshly fetched base branch (today the copy is as the last fetch left it); showing the pull request's status in Handloom; a warning when the base branch moved on the remote, or when two jobs on different machines change the same files.

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
