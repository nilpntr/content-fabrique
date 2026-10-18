#!/usr/bin/env bash
# Runs the web dev server, the API and the worker together, with the values from .env. Used by `task dev`, which has
# already started Postgres and MinIO. Ctrl-C (or SIGTERM) stops all three, each with everything it started.
#
# Works with the bash 3.2 that macOS ships, so: no `wait -n`, no associative arrays.
set -euo pipefail
cd "$(dirname "$0")/.."

ENV_FILE="${ENV_FILE:-.env}"
BIN_DIR=".dev/bin"

# Exports the NAME=value lines of an env file. It is not `source`: the file has `NAME=   # [AUTO] comment` lines and
# values with spaces, which a shell would run as commands. A non-empty value in the file wins over the process
# environment; an empty one only fills a name that is not set (the same rule as `cfctl doctor`).
load_env() {
	local line key val rest
	[ -f "$1" ] || { echo "dev: $1 not found, run: task env:init" >&2; exit 1; }
	while IFS= read -r line || [ -n "$line" ]; do
		line="${line#"${line%%[![:space:]]*}"}" # trim leading whitespace
		case "$line" in '' | '#'*) continue ;; esac
		line="${line#export }"
		key="${line%%=*}"
		[[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
		val="${line#*=}"
		val="${val#"${val%%[![:space:]]*}"}"
		case "$val" in
			'#'*) val="" ;;
			\"*) rest="${val#\"}"; val="${rest%%\"*}" ;;
			\'*) rest="${val#\'}"; val="${rest%%\'*}" ;;
			*)
				val="${val%%[[:space:]]#*}"
				val="${val%"${val##*[![:space:]]}"}" # trim trailing whitespace
				;;
		esac
		if [ -n "$val" ] || [ -z "${!key+x}" ]; then
			export "$key=$val"
		fi
	done <"$1"
}

load_env "$ENV_FILE"

# `task dev:obs` (D-39): the three processes send their spans to the local Phoenix, with content (prompts, tool results, thumbnails). This
# wins over the env file, so a hosted endpoint in .env never receives content from here; the headers (a hosted token) are dropped.
if [ "${DEV_OBS:-}" = "1" ]; then
	if [ "${CF_ENV:-local}" != "local" ]; then
		echo "dev: task dev:obs is for CF_ENV=local only (CF_ENV is ${CF_ENV})" >&2
		exit 1
	fi
	export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:6006" OTEL_TRACE_CONTENT=1 OTEL_EXPORTER_OTLP_HEADERS=""
	echo "dev: traces go to Phoenix at http://localhost:6006 (with content)"
fi

mkdir -p "$BIN_DIR"
# Built binaries, not `go run`: stopping a `go run` can leave its child process behind.
(cd apps/server && go build -o "../../$BIN_DIR/" ./cmd/api ./cmd/worker)

pids=()
names=()
start() {
	local name="$1"
	shift
	# stdin is /dev/null: a background job that keeps the terminal as stdin gets SIGTTIN when it reads it (vite does,
	# for its keyboard shortcuts), and sits stopped while still looking alive.
	"$@" </dev/null &
	pids+=("$!")
	names+=("$name")
	echo "dev: started $name (pid $!)"
}

stopping=""
cleanup() {
	[ -z "$stopping" ] || return 0
	stopping=1
	trap '' INT TERM
	echo
	echo "dev: stopping..."
	local pid i
	for pid in ${pids[@]+"${pids[@]}"}; do kill -TERM -- "-$pid" 2>/dev/null || true; done
	for ((i = 0; i < 50; i++)); do # up to 5 s to exit on their own
		local alive=""
		for pid in ${pids[@]+"${pids[@]}"}; do kill -0 "$pid" 2>/dev/null && alive=1; done
		[ -n "$alive" ] || break
		sleep 0.1
	done
	for pid in ${pids[@]+"${pids[@]}"}; do kill -KILL -- "-$pid" 2>/dev/null || true; done
	wait 2>/dev/null || true
	echo "dev: stopped"
}
trap 'cleanup; exit 0' INT TERM
trap cleanup EXIT

# Job control is on only while the jobs start. It gives every job its own process group, so stopping one stops everything
# it started (bun starts vite, vite starts the Workers runtime). It is off again afterwards so that this script's own
# sleep and the cleanup commands stay in the script's group, which is the one Ctrl-C signals.
set -m
start web bash -c 'cd apps/web && exec bun run dev'
start api "$BIN_DIR/api"
# River UI on loopback only, behind Basic auth (user river, password RIVER_UI_PASSWORD from .env). The worker refuses to
# start when that password is empty, which `task env:sync` prevents by generating it.
start worker "$BIN_DIR/worker" -river-ui-addr 127.0.0.1:8082
set +m

echo "dev: web ${APP_BASE_URL:-http://localhost:3000}   api ${API_BASE_URL:-http://localhost:8080}   worker http://localhost:8081   River UI http://localhost:8082 (user river)   (Ctrl-C stops all)"

# If one process dies, stop the others: a half-running stack hides the problem.
status=0
while [ -z "$stopping" ]; do
	for i in "${!pids[@]}"; do
		# Gone, or stopped by a signal (state T): either way it is not serving.
		state="$(ps -o stat= -p "${pids[$i]}" 2>/dev/null || true)"
		case "$state" in
			'' | T* | Z*)
				echo "dev: ${names[$i]} exited or stopped, stopping the rest" >&2
				status=1
				break 2
				;;
		esac
	done
	sleep 1
done
exit "$status"
