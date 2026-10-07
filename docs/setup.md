# Setting up Handloom from nothing

You downloaded the repository and want a working system. This is the whole path, in the order you do it. Each stage says where you are (a server, a machine, the browser) and what you type.

You need:

- **A server** that is on all the time, with Docker. A small VPS is enough. This runs the hub.
- **A domain name** you control (for example `handloom.example.com`), for https. The web UI refuses plain http on the internet. Without a domain, see "No domain" below.
- **One or more machines** that will run agents (the server itself can be one). Each needs `git`, `tmux` and at least one agent CLI you are logged in to: Claude Code, Codex, Pi, OMP or OpenCode.

## Stage 1: the hub, on the server

### 1a. Point the domain at the server

At your DNS provider add an **A record**: name `handloom` (or whatever you chose), value the server's public IP. Wait until `ping handloom.example.com` shows that IP. On the server's firewall open ports **80 and 443**.

### 1b. Start it

```
git clone <this repository> handloom && cd handloom
HANDLOOM_DOMAIN=handloom.example.com docker compose -f docker-compose.caddy.yml up -d --build
```

This starts the hub and Caddy, which fetches and renews the https certificate by itself. Check it: `curl https://handloom.example.com/healthz` should print `{"ok":true,...}`.

If the certificate does not appear, the cause is almost always DNS (the name does not reach this server yet) or a closed port 80/443.

*Using Dokploy instead?* Use `docker-compose.yml` as a Compose application, give it the domain with port 7420 in Dokploy, and set `HANDLOOM_BASE_URL=https://handloom.example.com` and `HANDLOOM_TRUST_PROXY=1`. Dokploy's own proxy then does the https. Details: [deploy.md](deploy.md).

### 1c. Make your owner account

```
docker compose -f docker-compose.caddy.yml logs handloom | grep -E "setup code|Admin token"
```

The log shows two things:

- a **setup code** (like `YDJU-GMEG-RS3C`). Open `https://handloom.example.com/setup`, enter it, choose your name and a long password. This account is the **owner**. The setup page closes for good afterwards.
- an **admin token** (starts with `hva_`), shown once. Save it in a password manager. You rarely need it: it is the emergency key for the command line if you lose web access.

Sign in. The inbox shows a **Get started** checklist; the next stages tick it off.

## Stage 2: each machine that will run agents

Do this on every machine. Nothing needs to be open on these machines: they only call out to the hub.

### 2a. Install the program

On a machine with Go: `go build -o handloom ./cmd/handloom`, then put the binary in your PATH (`sudo install handloom /usr/local/bin/`). Or from the repository, over ssh: `scripts/setup.sh device <ssh-name> --hub https://handloom.example.com` builds it for that machine, copies it and installs the link service (it needs the admin token in `HANDLOOM_TOKEN`).

### 2b. Join it to the hub

In the web UI: **Devices, Add a device**. Give it a name (anything: `laptop`, `build-box`), confirm with your password, and copy the command it shows. It looks like this, with a one-time token that expires:

```
handloom link join https://handloom.example.com hvj_...
```

Paste it on the machine, then keep the link running across reboots:

```
handloom link install
```

### 2c. Check it

```
handloom doctor
```

It checks the link and hub connection, git, tmux, and each agent CLI (installed, logged in), and prints one fix per problem. Fix what it says until the last line is "This machine is ready." The device now shows as connected in the Devices page.

## Stage 3: profiles (in the web UI)

A profile says which CLI an agent uses and what it may do. The quickest way is the **starter library** (**Profiles, Starter library**): 41 ready-made profiles for engineering, design, research and writing, consulting, marketing, sales, finance and HR, with the skills they use. Open one, read what it can do and the instructions it carries, choose the CLI (Claude Code or Codex) and where the model runs, and add it. It becomes an ordinary profile of yours that you can edit.

Add at least two to begin:

- a **lead**: `lead` (or `lead-engineering`, `lead-consulting`). It reads and plans, and never closes a job or answers for you.
- a **worker** for the kind of work you do: for example `coder`, `researcher` or `writer`.

Prefer to write your own? **Profiles, New profile** guides you: start from a starter, pick quick settings (reads only, writes files, runs commands), and press **Check before saving** to see warnings, what will be enforced and the exact instructions. From the command line: `handloom starters`, then `handloom profile add coder --kind claude --runtime cloud`. Codex cannot load skills or refuse specific commands yet; adding a starter as Codex says what it would leave out.

## Stage 4: your first job (in the web UI)

**Jobs, New job.** Fill in a title and a brief (what to do, what done looks like). Pick the device, the repository path on that device and a check command (for example `./check.sh` or `go test ./...`), and the lead's profile. Start it. The lead plans tasks and starts workers on that device; each worker gets its own copy of the repository. You get inbox items when something needs you. When every task is done, look at the branch `job/<id>/integration` in the repository and merge it yourself.

## What every token is for

You will see five kinds of secret. Almost all of them are created and handled for you.

| What | Looks like | Who makes it | What it is for |
|---|---|---|---|
| Setup code | `YDJU-GMEG-RS3C` | the hub, at first start | Making the owner account, once. |
| Owner password | your choice | you | Signing in to the web UI. Also asked again for risky actions. |
| Admin token | `hva_...` | the hub, at first start, shown once | Emergency command-line access. Not needed day to day. |
| Join token | `hvj_...` | the Devices page | Joining one machine, once. It expires. |
| API token | `hvh_...` | Settings, if you want the CLI | Using `handloom` as yourself from a terminal. |

Behind the scenes the device keeps its own credential (`hvd_...`, in `~/.config/handloom`) and each agent gets a one-run token (`hvr_...`). Agents never see the device credential, and you never type either one.

## Working with other people

Add people in **Settings, Add a person**: you get a one-time invite link to send them. They choose their own password.

- **Owner**: everything, including devices, profiles and people.
- **Member**: starts and closes jobs, answers questions, reviews work.
- **Viewer**: can look at everything and change nothing.

Everyone signs in to the same hub and sees the same inbox, jobs and machines. See the "Teams" part of the overview diagram for what is shared and what is not.

## No domain

On a private network (Tailscale, WireGuard, a home LAN) you can skip the domain: use `docker-compose.yml`, set `HANDLOOM_PUBLISH=0.0.0.0` and `HANDLOOM_INSECURE=1`, and use `http://<private-ip>:7420` as the hub address everywhere. Never do this on the open internet: the hub would speak unencrypted and anyone who reaches it could try to log in.

## Optional: phone alerts

Pick a long random topic name, subscribe to it in the ntfy app, and enter the server and topic in **Settings, Phone notifications**. A push says "your agent needs you" with a link; it never contains the question.

## If you get stuck

- `handloom doctor` on a machine, and `docker compose logs handloom` on the server.
- Forgot the owner password: `docker compose exec handloom /handloom hub reset-password <name>`.
- Back up before upgrades: `docker compose exec handloom /handloom hub backup --to /data/backups/manual.db`.
