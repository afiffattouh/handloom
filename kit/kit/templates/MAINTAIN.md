# Looking after {name}

This guide is for the person who looks after {name} after launch. You do not need to be a
developer. You ask a coding agent (Claude Code, or another one your team uses) for a change, look at
what it did, and approve it. A check runs on every change and stops the ones that are not safe.

## Asking for a change

Open this folder in your agent and ask in plain words. Good requests are small and say what you
want to see:

- "Change the opening hours on the contact page to 9 to 5, Monday to Friday."
- "Add this photo to the gallery with the caption 'Opening day'." (attach the photo)
- "Add a new page called Careers with the text below, and link it from the menu."

The agent works on its own copy of the project (a branch), never on the live site directly.

## Before you approve

Look at the preview or the list of changes and ask yourself:

1. Is this what I asked for, and nothing more?
2. Did the check pass? The pull request shows a green tick when it did. A red cross means do not approve.
3. Did the agent say anything was **blocked** or **not checked**? Read that part first.

Then approve. {approver_line}

## If something goes wrong

- **Undo a change that is already live:** open the change in GitHub and press **Revert**, then approve the revert.
- **The agent says it is blocked** (it creates a file named `BLOCKED.md`): it is telling you honestly that it
  cannot do the request as asked. Read why, then change the request or ask {contact}.
- **The check fails:** the message says which check and why, for example a broken link or a picture with
  no description. Give the message to the agent and ask it to fix it.

## What you cannot change, on purpose

These files decide what counts as correct, so only the owner changes them. The check, and the
owner's approval on GitHub, stop any change that touches them:

{protected}

If a request really needs one of them, ask {contact}.

## Commands (for the curious)

- `./check` runs every check on your computer. Done means it prints PASS.
{commands}
