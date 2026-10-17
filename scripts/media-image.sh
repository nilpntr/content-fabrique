#!/usr/bin/env bash
# The local media-tools image (packages/media-tools), built as media-tools:dev: `task media:build` builds it, and `task dev` runs
# `media-image.sh ensure` first, which rebuilds it when it is missing or stale.
#
# Stale means: it was built from other sources than the ones in the working tree now. The build records the git tree hash of
# packages/media-tools (the working tree's, so uncommitted edits count) in the label org.contentfabrique.src-hash, and this
# script compares the label with the hash of the tree as it is. A rebuild that hits Docker's layer cache, a change to some other
# directory and a commit that only touches docs outside the directory change nothing, so they never cause a rebuild loop. An
# image without the label (built before it existed) is stale, and so is one without the org.contentfabrique.fonts label (built
# before the title and caption fonts were added: renders with text would fail on it).
#
# Without Docker it prints a warning and the one-line fix, and succeeds, so `task dev` still starts (everything but the media jobs
# works). Outside a git checkout it cannot hash the tree and only checks that the image exists and has the fonts label.
#
#   scripts/media-image.sh build     docker build -t media-tools:dev packages/media-tools
#   scripts/media-image.sh ensure    check, rebuild if needed
#   scripts/media-image.sh status    check only; exit 0 when fresh, 3 when it would rebuild, 0 without Docker
#   scripts/media-image.sh tree      print the hash of the sources
#
# Environment: MEDIA_IMAGE (default media-tools:dev), MEDIA_IMAGE_SKIP_BUILD=1 (only warn, never build), MEDIA_IMAGE_DRY_RUN=1
# (say what would be built, build nothing). Works with the bash 3.2 that macOS ships.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE="${MEDIA_IMAGE:-media-tools:dev}"
CONTEXT="packages/media-tools"
FIX="task media:build"

say() { echo "media-tools: $*" >&2; }

# The git tree hash of the build context as it is now, uncommitted changes included: the tree of HEAD with the working copy of the
# directory added, built in a throwaway index so the real one is not touched. Prints nothing outside a git checkout.
src_hash() {
	git rev-parse --git-dir >/dev/null 2>&1 || return 0
	local idx
	idx="$(mktemp "${TMPDIR:-/tmp}/cf-media-index.XXXXXX")"
	rm -f "$idx"
	GIT_INDEX_FILE="$idx" git read-tree HEAD >/dev/null 2>&1 \
		&& GIT_INDEX_FILE="$idx" git add -A -- "$CONTEXT" >/dev/null 2>&1 \
		&& GIT_INDEX_FILE="$idx" git write-tree --prefix="$CONTEXT/" 2>/dev/null || true
	rm -f "$idx"
}

label() { docker image inspect --format "{{index .Config.Labels \"$1\"}}" "$IMAGE" 2>/dev/null; }

build() {
	local hash args
	hash="$(src_hash)"
	args=()
	[ -z "$hash" ] || args=(--build-arg "SRC_HASH=$hash")
	if [ "${MEDIA_IMAGE_DRY_RUN:-}" = 1 ]; then
		say "dry run: would run: docker build ${args[*]-} -t $IMAGE $CONTEXT"
		return 0
	fi
	say "building $IMAGE (this compiles ffmpeg: the first build takes several minutes, a rebuild after a small change is quick)"
	docker build ${args[@]+"${args[@]}"} -t "$IMAGE" "$CONTEXT"
}

# Prints why the image has to be rebuilt, or nothing when it is fine. Needs a working docker.
stale_reason() {
	local have want
	if ! docker image inspect --format '{{.Id}}' "$IMAGE" >/dev/null 2>&1; then
		echo "the image $IMAGE does not exist on this machine"
		return
	fi
	if [ "$(label org.contentfabrique.fonts || true)" != 1 ]; then
		echo "the image $IMAGE was built before the title and caption fonts were added to it (no org.contentfabrique.fonts label)"
		return
	fi
	want="$(src_hash)"
	[ -n "$want" ] || return 0
	have="$(label org.contentfabrique.src-hash || true)"
	case "$have" in
		"$want") ;;
		"" | "<no value>" | unknown) echo "the image $IMAGE does not record which sources it was built from (built before $CONTEXT was hashed into it)" ;;
		*) echo "the image $IMAGE was built from other sources than the current $CONTEXT (image ${have:0:12}, sources ${want:0:12})" ;;
	esac
}

docker_ok() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }

cmd="${1:-ensure}"
case "$cmd" in
	tree)
		src_hash
		;;
	build)
		docker_ok || { say "Docker is not running; start it, then run: $FIX"; exit 1; }
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
		echo "usage: $0 build|ensure|status|tree" >&2
		exit 2
		;;
esac
