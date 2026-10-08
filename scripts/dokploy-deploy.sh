#!/usr/bin/env bash
# Build the image for the Dokploy host, load it there, point the Handloom
# compose app at it and redeploy. Data (the /data volume) is untouched.
#
#   DOKPLOY_URL=http://dokploy-host.example:3000 DOKPLOY_API_KEY=... \
#   DOKPLOY_SSH=dokploy COMPOSE_ID=<composeId> HUB_URL=https://handloom.example.com \
#   scripts/dokploy-deploy.sh
#
# This is how the live hub is deployed: the image is built here from the working
# tree and goes over ssh (`docker save | ssh host docker load`); the compose file
# says `pull_policy: never`, so Dokploy uses the loaded image. Nothing is pulled
# from GitHub, and a push to the repository deploys nothing.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${DOKPLOY_URL:?}" "${DOKPLOY_API_KEY:?}" "${DOKPLOY_SSH:?}" "${COMPOSE_ID:?}" "${HUB_URL:?}"
PLATFORM="${PLATFORM:-linux/amd64}"
TAG="a-$(git -C "$ROOT" rev-parse --short HEAD)$(git -C "$ROOT" diff --quiet || echo -dirty)"
say() { printf '\n== %s\n' "$*"; }
api() { # api <get|post> <procedure> [json body]
  if [ "$1" = get ]; then
    curl -fsS -m 60 -G -H "x-api-key: $DOKPLOY_API_KEY" --data-urlencode "input=$3" "$DOKPLOY_URL/api/trpc/$2"
  else
    curl -fsS -m 90 -X POST -H "x-api-key: $DOKPLOY_API_KEY" -H 'Content-Type: application/json' -d "$3" "$DOKPLOY_URL/api/trpc/$2"
  fi
}

say "build $PLATFORM image handloom-hub:$TAG"
docker buildx build --platform "$PLATFORM" --build-arg VERSION="$TAG" -t "handloom-hub:$TAG" --load "$ROOT" >/dev/null
say "load it on $DOKPLOY_SSH"
docker save "handloom-hub:$TAG" | ssh -o BatchMode=yes "$DOKPLOY_SSH" docker load

say "point the compose app at it"
# The compose file is written here in full, so the app is always a raw compose app (not a Git one).
BODY="$(TAG=$TAG COMPOSE_ID=$COMPOSE_ID HUB_URL=$HUB_URL python3 - <<'PY'
import json, os
compose = f"""services:
  handloom:
    image: handloom-hub:{os.environ["TAG"]}
    pull_policy: never
    restart: unless-stopped
    environment:
      HANDLOOM_BASE_URL: {os.environ["HUB_URL"]}
      HANDLOOM_TRUST_PROXY: "1"
    volumes:
      - handloom-data:/data
    expose:
      - "7420"
volumes:
  handloom-data:
"""
print(json.dumps({"json": {"composeId": os.environ["COMPOSE_ID"], "sourceType": "raw", "composeFile": compose,
                           "customGitUrl": None, "customGitBranch": None, "env": ""}}))
PY
)"
api post compose.update "$BODY" >/dev/null
api post compose.deploy "{\"json\":{\"composeId\":\"$COMPOSE_ID\",\"title\":\"handloom-hub:$TAG\"}}" >/dev/null

say "wait for it"
for i in $(seq 1 40); do
  st="$(api get compose.one "{\"json\":{\"composeId\":\"$COMPOSE_ID\"}}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["data"]["json"].get("composeStatus"))')"
  [ "$st" = error ] && { echo "deployment failed (see the Dokploy deployment log)"; exit 1; }
  if [ "$st" = done ] && curl -fsS -m 10 "$HUB_URL/healthz" >/dev/null 2>&1; then
    echo "up: $(curl -fsS "$HUB_URL/healthz")  image handloom-hub:$TAG"; exit 0
  fi
  sleep 4
done
echo "timed out waiting for $HUB_URL/healthz"; exit 1
