# Deploying the hub

The hub is one container: `handloom hub serve`. Data lives in one volume at `/data`.

## Plain Docker (any VPS)

```
git clone <repo> && cd <repo>
export HANDLOOM_BASE_URL=https://handloom.example.com      # the public URL, used in notification links
docker compose up -d --build
docker compose logs handloom | grep -E "Admin token|setup code"   # both shown in the log; store the admin token
```

Open `$HANDLOOM_BASE_URL/setup`, enter the setup code from the log and create the owner account. The wizard closes for good once an owner exists. A hub without an owner prints a new code every time it starts. Forgot the password? `docker compose exec handloom /handloom hub reset-password <name>` prints a new one (there is no web reset and no email).

The web UI needs https (or `localhost`). With a plain `http://` address it stays off and says why in the log; on a private network (Tailscale, WireGuard) set `HANDLOOM_INSECURE=1` to turn it on anyway.

The compose file publishes the port on `127.0.0.1` only. Put a TLS proxy (Caddy, nginx, Traefik) in front of it. handloom speaks plain HTTP; do not publish it to the internet without TLS. Set `HANDLOOM_PUBLISH=0.0.0.0` only on a private network (Tailscale, WireGuard).

## Dokploy

Verified on 2026-10-06 against Dokploy v0.29 (the owner's instance, Tailscale-only panel, Traefik with Let's Encrypt, wildcard DNS): Handloom runs there as its own project, **Handloom**, with one **Compose** application (`raw` source) and the domain `handloom.example.com`.

How it was set up (all through the Dokploy API, nothing by hand):

1. Build the image for the Dokploy host (`linux/amd64`) and load it there: `docker save handloom-hub:TAG | ssh <dokploy-host> docker load`. There is no git remote yet, so Dokploy cannot build from source; the compose file uses `pull_policy: never`.
2. `project.create`, then `compose.create` (`sourceType: raw`, the compose file below), `domain.create` (`host`, `port: 7420`, `https: true`, `certificateType: letsencrypt`, `serviceName: handloom`, `domainType: compose`), then `compose.deploy`.
3. The setup code is in the container log (`docker logs <container>` on the Dokploy host): open `https://<domain>/setup`.

```yaml
services:
  handloom:
    image: handloom-hub:TAG
    pull_policy: never
    restart: unless-stopped
    environment:
      HANDLOOM_BASE_URL: https://handloom.example.com
      HANDLOOM_TRUST_PROXY: "1"      # Traefik sets X-Forwarded-For
    volumes:
      - handloom-data:/data
    expose:
      - "7420"
volumes:
  handloom-data:
```

To ship a new version: `scripts/dokploy-deploy.sh` (needs `DOKPLOY_URL`, `DOKPLOY_API_KEY`, `DOKPLOY_SSH`, `COMPOSE_ID`, `HUB_URL`). It builds, loads the image, updates the compose file's image tag and redeploys. The `/data` volume is kept. If you later publish the repo, Dokploy can build from Git instead and this script is not needed. A Dokploy one-click template was not made.

The admin token printed on first start stays in the container log, which anyone with access to the Dokploy host can read. Create the owner account, then treat the log as sensitive.

## Security notes

- **No lead/worker boundary on a shared device yet.** Agents are identified by a name they send, using their device's credential; any process on a device can act as any agent registered there (see D-notes in DECISIONS.md). Until per-run tokens land, treat one device as one trust domain, and do not put a confidential job's agents on a device with untrusted ones.
- Anyone who can post to the hub can in effect run commands on connected devices (agents act on messages). Keep the hub behind TLS, keep the owner password strong, and revoke devices you no longer use.
- `HANDLOOM_TRUST_PROXY=1` only behind a proxy you control; otherwise a client can spoof its address for the login rate limit.

## Environment

| Variable | Meaning |
|---|---|
| `HANDLOOM_BASE_URL` | public URL; links in notifications point here |
| `HANDLOOM_NTFY_URL`, `HANDLOOM_NTFY_TOPIC`, `HANDLOOM_NTFY_TOKEN` | phone push through ntfy (the push has a generic line and a link, never the question) |
| `HANDLOOM_WEBHOOK_URL` | the same notification as JSON |
| `HANDLOOM_INSECURE` | `1` allows the web UI over plain http on a private network |
| `HANDLOOM_TRUST_PROXY` | `1` when a proxy in front of the hub sets `X-Forwarded-For` (its last entry is used for login rate limits); leave unset otherwise |
| `HANDLOOM_DATA`, `HANDLOOM_ADDR` | set by the image (`/data`, `0.0.0.0:7420`) |

## Backup and restore

```
docker compose exec handloom /handloom hub backup --to /data/backups/manual.db     # safe while running
docker compose stop handloom
docker compose run --rm --entrypoint /handloom handloom hub restore --from /data/backups/manual.db --force
docker compose start handloom
```

A backup holds hashed tokens and all hub data (mode 0600): keep it private. Never copy `handloom.db` and its `-wal` file by hand while the hub runs.

Before an upgrade changes the schema the hub writes `/data/backups/pre-vN.db` first. A hub refuses to start on a database whose schema is newer than itself. To roll back: restore the snapshot and run the old image tag. Pin image tags; do not run `:latest`.

## Images

The Dockerfile cross-compiles in the builder stage, so `docker build --platform linux/amd64 .` works from an arm64 machine. The result is a static binary on distroless (about 14 MB), running as a non-root user.
