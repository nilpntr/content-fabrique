#!/usr/bin/env bash
# Runs on the Docker host (push-image.sh copies it there): reads the registry password from stdin, builds both images and pushes.
set -euo pipefail
tag="$1"
ref="$2"
cd ~/cf-spike
# Keep the host's Docker endpoint (rootless on the VPS) while the credential goes to a throwaway config.
DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export DOCKER_HOST
DOCKER_CONFIG="$(mktemp -d ~/cf-spike/docker-config.XXXX)"
export DOCKER_CONFIG
trap 'rm -rf "$DOCKER_CONFIG"' EXIT
docker login registry.cloudflare.com -u v1 --password-stdin >/dev/null 2>&1
t0=$(date +%s)
docker build -q --platform linux/amd64 -t "cf-spike-media-base:$tag" src/media-tools >/dev/null
docker build -q --platform linux/amd64 --build-arg "BASE=cf-spike-media-base:$tag" -t "$ref" src/container >/dev/null
t1=$(date +%s)
docker push -q "$ref" >/dev/null
t2=$(date +%s)
docker logout registry.cloudflare.com >/dev/null 2>&1 || true
echo "build_s=$((t1 - t0)) push_s=$((t2 - t1)) size_bytes=$(docker image inspect "$ref" --format '{{.Size}}')"
