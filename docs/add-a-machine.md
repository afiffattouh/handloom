# Add a machine

One pasted line turns a computer into a machine that can run agents for your hub. This page is for the person pasting it. Handloom does the configuration underneath: it installs itself, joins the hub, keeps the link running and checks what the machine needs.

**Whose it is.** A machine belongs to the person who adds it. Owners and members can add machines; viewers cannot. You see only your own machines (the owner sees all of them, and whose each is), and agents you start run only on them, with the agent-CLI logins and files of that computer. Your teammates add their own computers the same way: do not paste your line on someone else's machine, and do not share one machine between two people. A machine the owner shares (one added with the admin token, or before this rule) is for the owner until the owner hands it to someone: `handloom device assign NAME --to PERSON`, or the Hand over control on the Machines page.

**Where:** in the web UI, Machines, Add a machine (or `handloom device add NAME`, which prints the one-time token). Give the machine a name, confirm with your password, and the page shows the line for each system. The token inside it works once and for a short time (the page says how long). Make a new one if it expires.

## Linux

Open a terminal and paste the line:

```
curl -fsSL https://raw.githubusercontent.com/afiffattouh/handloom/main/install.sh | sh -s -- join https://handloom.example.com hvj_...
```

What happens, in order: the program is downloaded and checked against the release's checksums (and its signature, if `cosign` is installed), then joined to the hub, then the link is started, then the machine is checked.

- **Where the link runs.** With systemd, as a user service that starts at boot. Without it (WSL with no systemd, a container), the link is started in the background so it survives closing the terminal, and one guarded line is added to your login file (`~/.profile`, or `~/.zprofile` for zsh, or `~/.bash_profile` if you have one) that starts it again at login. Without a service manager nothing can start it at boot, only at login. `handloom link uninstall` stops it and removes that line.
- **If `git` or `tmux` is missing.** You are shown the command for your package manager and asked `[y/N]`. `sudo` is used only if you are not root, and only after you say yes. If the line was piped from `curl`, the question is asked on your terminal (`/dev/tty`); with no terminal, the commands are printed and nothing is run. You can ask again any time: `handloom doctor --fix`.
- **If you have no agent program.** Handloom does not install Claude Code, Codex or the others. `handloom doctor` prints the official install line and how to sign in, and checks again when you run it again.

## macOS

The same line, in Terminal. The link runs as a LaunchAgent (`~/Library/LaunchAgents/ai.handloom.link.plist`): it starts when you log in and is restarted if it stops. Its log is `~/Library/Logs/handloom-link.log`. Homebrew is how `tmux` gets installed (`brew install tmux`); if you have no Homebrew, the check says where to get it. The service is given a PATH that includes `/opt/homebrew/bin` and `/usr/local/bin`, because launchd starts programs with almost none.

## Windows

Open PowerShell (a normal window) and paste the line the page shows for Windows:

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; & ([scriptblock]::Create((irm https://raw.githubusercontent.com/afiffattouh/handloom/main/install.ps1))) -Hub https://handloom.example.com -Token hvj_...
```

Handloom runs its agents in Linux. Windows 10 (version 2004 or newer) and Windows 11 have Linux built in as WSL2, so the script:

1. checks the Windows version;
2. checks for WSL. If it is missing, it explains the one-time `wsl --install` (it needs administrator permission, and a restart afterwards), offers to run it, and tells you to paste **the same line again** after the restart;
3. picks your Ubuntu (or installs one, which opens a window asking for a Linux user name and password; then paste the line again);
4. runs the Linux line above **inside** Linux, so the installing, joining and checking all happen there;
5. creates a Scheduled Task, "Handloom link", that runs at your sign-in, hidden, and keeps the link up; and starts it now.

The agent programs (Claude Code and the others) must be installed **in the Linux side**, not in Windows. Open it with `wsl -d Ubuntu`, run `handloom doctor`, and do what it says. Native Windows without WSL is not supported.

To remove: `Unregister-ScheduledTask -TaskName 'Handloom link' -Confirm:$false`, then `handloom uninstall --purge` in Linux, then revoke the machine on the Machines page (`handloom device revoke NAME`). You can revoke your own; the owner can revoke any.

## Updating a computer

When a newer release is out, the computer's row on the Computers page says so and shows **Update to vX.Y.Z** (the CLI form is `handloom device update NAME`). The computer does the work itself, in this order:

1. waits until none of its helpers is in the middle of a turn (the row says "waiting for N helper(s)");
2. downloads the release from GitHub and checks it against that release's `checksums.txt`, then runs the new program once to see that it starts and reports the version it should;
3. swaps its own program file and starts again in place. Helpers in their tmux windows keep running; a tool call in flight at that moment is lost.

The hub sends no program and no address, only the request, so it cannot make a computer run anything it did not download from the releases page. The row shows how it went; a request a computer does not pick up within an hour is dropped. What can stop it, and what the row says: a program built from source (update by building again), a program folder this user cannot write to (for example `/usr/local/bin` installed by root: run `install.sh` as that user, or set `HANDLOOM_INSTALL_DIR` to a folder you own), or no release yet. The signature check `install.sh` does with `cosign` is not done here; the checksum is. Windows runs the link inside Linux, so it updates like Linux.

Who may: the owner for any computer, a member for their own. The hub's own program is updated by deploying it, not from this page.

## When the page does not turn "ready"

The page shows the machine as ready when its link has called in within the last minute. If it does not: on the machine, `handloom doctor` says what is missing; `handloom link status` says whether the link answers; the log is `~/.config/handloom/link.log` (background start), `journalctl --user -u handloom-link` (systemd) or `~/Library/Logs/handloom-link.log` (macOS).

If the folder name of the link's home is very long, the unix socket path can exceed the system limit (103 characters on macOS, 107 on Linux). Handloom then uses a short path under `/tmp/handloom-<uid>/` and says so when the link starts.
