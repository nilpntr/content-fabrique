#!/usr/bin/env bash
# Builds the cf-spike job image on a Docker host over SSH (linux/amd64; this Mac runs no Docker) and pushes it to the account's
# Cloudflare managed registry with a short-lived push credential from wrangler. The credential travels over SSH stdin into a
# throwaway DOCKER_CONFIG that remote-push.sh deletes; it is never printed or written to the repository.
#
#   tools/cf-spike/push-image.sh deploy@host v1
#
# Needs: wrangler logged in on this machine, Docker on the host. Everything lands in ~/cf-spike on the host.
set -euo pipefail
host="${1:?usage: push-image.sh user@host tag}"
tag="${2:?usage: push-image.sh user@host tag}"
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"

ssh "$host" 'mkdir -p ~/cf-spike/src'
rsync -az --delete "$root/packages/media-tools/" "$host:cf-spike/src/media-tools/"
rsync -az --delete "$here/container/" "$host:cf-spike/src/container/"
rsync -az "$here/remote-push.sh" "$host:cf-spike/remote-push.sh"

creds="$(wrangler containers registries credentials registry.cloudflare.com --push --expiration-minutes 30 --json 2>/dev/null)"
account="$(printf '%s' "$creds" | python3 -c 'import json,sys; print(json.load(sys.stdin)["account_id"])')"
ref="registry.cloudflare.com/$account/cf-spike-media:$tag"
printf '%s' "$creds" | python3 -c 'import json,sys; print(json.load(sys.stdin)["password"])' |
	ssh "$host" "bash ~/cf-spike/remote-push.sh $tag $ref"
echo "image=$ref"
