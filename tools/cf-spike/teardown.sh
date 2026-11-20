#!/usr/bin/env bash
# Deletes every cf-spike resource and lists what is left: the bucket's objects (through the gateway's DELETE /objects, while
# the Worker still exists), the container application, the Worker with its Durable Object namespace, the registry images and
# the EU bucket. With a host, also the build host's cf-spike images, the local runner container and ~/cf-spike.
#
#   GATEWAY_URL=... GATEWAY_TOKEN=... tools/cf-spike/teardown.sh [deploy@host]
set -uo pipefail
host="${1:-}"

echo "== bucket objects"
if [ -n "${GATEWAY_URL:-}" ] && [ -n "${GATEWAY_TOKEN:-}" ]; then
	curl -s -X DELETE -H "Authorization: Bearer $GATEWAY_TOKEN" "$GATEWAY_URL/objects"
	echo
else
	echo "GATEWAY_URL/GATEWAY_TOKEN not set: objects not purged (the bucket delete below fails if any are left)"
fi

echo "== container applications"
for id in $(wrangler containers list --json 2>/dev/null | python3 -c 'import json,sys
for a in json.load(sys.stdin):
    if a.get("name", "").startswith("cf-spike-"): print(a["id"])'); do
	yes | wrangler containers delete "$id" 2>&1 | tail -1
done

echo "== worker (and its Durable Object namespace)"
wrangler delete --name cf-spike-gateway --force 2>&1 | tail -1

echo "== registry images"
for img in $(wrangler containers images list --json 2>/dev/null | python3 -c 'import json,sys
for i in json.load(sys.stdin):
    if i["name"].startswith("cf-spike-"):
        for t in i.get("tags", []): print(i["name"] + ":" + t)'); do
	yes | wrangler containers images delete "$img" 2>&1 | tail -1
done

echo "== bucket"
wrangler r2 bucket delete cf-spike-media --jurisdiction eu 2>&1 | tail -1

if [ -n "$host" ]; then
	echo "== build host"
	ssh "$host" 'docker rm -f cf-spike-local >/dev/null 2>&1; docker images --format "{{.Repository}}:{{.Tag}}" | grep -E "(^|/)cf-spike-" | xargs -r docker rmi >/dev/null; rm -rf ~/cf-spike; docker images --format "{{.Repository}}:{{.Tag}}" | grep -c cf-spike || true'
fi

echo "== what is left (each line should say none)"
wrangler containers list --json 2>/dev/null | grep -q '"cf-spike-' && echo "containers: LEFT" || echo "containers: none"
wrangler containers images list --json 2>/dev/null | grep -q '"cf-spike-' && echo "images: LEFT" || echo "images: none"
wrangler r2 bucket list --jurisdiction eu 2>/dev/null | grep -q cf-spike && echo "eu buckets: LEFT" || echo "eu buckets: none"
wrangler r2 bucket list 2>/dev/null | grep -q cf-spike && echo "buckets: LEFT" || echo "buckets: none"
wrangler deployments list --name cf-spike-gateway >/dev/null 2>&1 && echo "worker: LEFT" || echo "worker: none"
