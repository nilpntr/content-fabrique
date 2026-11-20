#!/usr/bin/env bash
# Deploys the cf-spike gateway (Worker, Durable Object, container application) with an image push-image.sh already pushed, so
# wrangler needs no Docker here. SPIKE_ENV is a file outside the repository holding GATEWAY_TOKEN=<random hex>.
#
#   SPIKE_ENV=/path/spike.env tools/cf-spike/deploy.sh v1
set -euo pipefail
tag="${1:?usage: deploy.sh tag}"
: "${SPIKE_ENV:?SPIKE_ENV must name the file with GATEWAY_TOKEN}"
cd "$(dirname "$0")/gateway"

account="$(wrangler whoami --json 2>/dev/null | python3 -c 'import json,sys; a=json.load(sys.stdin)["accounts"]; assert len(a)==1, "pick the account"; print(a[0]["id"])' 2>/dev/null ||
	wrangler containers registries credentials registry.cloudflare.com --pull --expiration-minutes 1 --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account_id"])')"
sed -e "s/\${CLOUDFLARE_ACCOUNT_ID}/$account/" -e "s/\${IMAGE_TAG}/$tag/" wrangler.template.jsonc >wrangler.jsonc

wrangler r2 bucket list --jurisdiction eu 2>/dev/null | grep -q 'cf-spike-media' || wrangler r2 bucket create cf-spike-media --jurisdiction eu
wrangler deploy
grep '^GATEWAY_TOKEN=' "$SPIKE_ENV" | cut -d= -f2- | tr -d '\n' | wrangler secret put GATEWAY_TOKEN
