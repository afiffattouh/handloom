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

Create a project, add a **Compose** application from the Git repo (this `docker-compose.yml`), set the domain and container port `7420` in Dokploy, and set the environment variables below. Dokploy's Traefik issues the certificate. Status: this path is written from the Dokploy docs and has **not been tested on a Dokploy install yet**; the Mantis VPS does not run Dokploy today.

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
