#!/usr/bin/env bash
# The local media-tools image (packages/media-tools), built as media-tools:dev: `task media:build` builds it, and `task dev` runs
# `media-image.sh ensure` first, which rebuilds it when it is missing or stale.
#
# Stale means: it was built before the newest commit that touched packages/media-tools, or it has no org.contentfabrique.fonts
# label (built before the title and caption fonts were added: renders with text would fail on it). Without Docker it prints a
# warning and the one-line fix, and succeeds, so `task dev` still starts (everything but the media jobs works).
#
#   scripts/media-image.sh build     docker build -t media-tools:dev packages/media-tools
#   scripts/media-image.sh ensure    check, rebuild if needed
#   scripts/media-image.sh status    check only; exit 0 when fresh, 3 when it would rebuild, 0 without Docker
#
# Environment: MEDIA_IMAGE (default media-tools:dev), MEDIA_IMAGE_SKIP_BUILD=1 (only warn, never build), MEDIA_IMAGE_DRY_RUN=1
# (say what would be built, build nothing). Works with the bash 3.2 and the BSD date that macOS ships.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE="${MEDIA_IMAGE:-media-tools:dev}"
CONTEXT="packages/media-tools"
FIX="task media:build"

say() { echo "media-tools: $*" >&2; }

# Seconds since the epoch of a docker timestamp (2026-10-17T13:01:02.123456789Z): GNU date first, then BSD date.
epoch() {
	local ts="${1:0:19}"
	date -u -d "${ts}Z" +%s 2>/dev/null || date -j -u -f '%Y-%m-%dT%H:%M:%S' "$ts" +%s 2>/dev/null
}

build() {
	if [ "${MEDIA_IMAGE_DRY_RUN:-}" = 1 ]; then
		say "dry run: would run: docker build -t $IMAGE $CONTEXT"
		return 0
	fi
	say "building $IMAGE (this compiles ffmpeg: the first build takes several minutes, a rebuild after a small change is quick)"
	docker build -t "$IMAGE" "$CONTEXT"
}

# Prints why the image has to be rebuilt, or nothing when it is fine. Needs a working docker.
stale_reason() {
	local created fonts newest created_s
	if ! created="$(docker image inspect --format '{{.Created}}' "$IMAGE" 2>/dev/null)"; then
		echo "the image $IMAGE does not exist on this machine"
		return
	fi
	fonts="$(docker image inspect --format '{{index .Config.Labels "org.contentfabrique.fonts"}}' "$IMAGE" 2>/dev/null || true)"
	if [ "$fonts" != 1 ]; then
		echo "the image $IMAGE was built before the title and caption fonts were added to it (no org.contentfabrique.fonts label)"
		return
	fi
	# Committer time of the newest commit that touched the image's sources; empty outside a git checkout.
	newest="$(git log -1 --format=%ct -- "$CONTEXT" 2>/dev/null || true)"
	[ -n "$newest" ] || return 0
	created_s="$(epoch "$created" || true)"
	[ -n "$created_s" ] || return 0
	if [ "$created_s" -lt "$newest" ]; then
		echo "the image $IMAGE is older than the newest commit that touched $CONTEXT"
	fi
}

docker_ok() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }

cmd="${1:-ensure}"
case "$cmd" in
	build)
		docker_ok || { say "Docker is not running; start it, then run: $FIX" >&2; exit 1; }
		build
		;;
	ensure | status)
		if ! docker_ok; then
			say "WARNING: Docker is not available, so the $IMAGE image cannot be checked. Renders and asset ingests need it. Start Docker and run: $FIX"
			exit 0
		fi
		reason="$(stale_reason)"
		if [ -z "$reason" ]; then
			say "$IMAGE is up to date"
			exit 0
		fi
		say "$reason"
		[ "$cmd" = status ] && exit 3
		if [ "${MEDIA_IMAGE_SKIP_BUILD:-}" = 1 ]; then
			say "WARNING: not rebuilding (MEDIA_IMAGE_SKIP_BUILD=1). Renders with text fail on an old image. Fix: $FIX"
			exit 0
		fi
		if ! build; then
			say "WARNING: the build failed, so the old $IMAGE stays. Retry with: $FIX"
		fi
		;;
	*)
		echo "usage: $0 build|ensure|status" >&2
		exit 2
		;;
esac
