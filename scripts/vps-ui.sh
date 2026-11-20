#!/usr/bin/env bash
# Runs the web UI (vite dev), the api and the worker on a shared Linux VPS, bound to 127.0.0.1 only, each in its own
# capped systemd user service (memory, CPU, nice). Postgres and MinIO come from the capped compose overlay. Reach it from
# the Mac through an SSH tunnel (docs/06, "Running the web UI on the VPS"):
#
#   ssh -N -L 3000:127.0.0.1:3000 -L 8080:127.0.0.1:8080 -L 9000:127.0.0.1:9000 <user>@<host>
#
# Usage: scripts/vps-ui.sh start | stop | status | logs [web|api|worker]
#
# start builds the api, worker and cfctl into $CF_UI_STATE/bin, installs the bun workspace, starts Postgres and MinIO only
# when they are not running (never recreates them: another checkout may be using them), runs the migrations and starts
# the three services. stop stops the three services and leaves Postgres and MinIO alone. Reads .env at the repository
# root; prints no values from it.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SELF="$ROOT/scripts/vps-ui.sh"
STATE="${CF_UI_STATE:-$HOME/content-fabrique-test/ui-run}"
BIN="$STATE/bin"
LOGS="$STATE/logs"
# Compose project of the Postgres and MinIO containers. The first VPS checkout is called content-fabrique, so its
# containers are content-fabrique-postgres-1 and content-fabrique-minio-1; a second checkout must reuse them, not start
# its own on the same ports.
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-content-fabrique}"
# The rootless-Docker shim for the media sandbox (docs/06, "Running on a VPS", step 5). Empty or missing: none.
SHIM_DIR="${CF_DOCKER_SHIM_DIR:-$HOME/content-fabrique-test/shim}"
WORKER_HEALTH_ADDR="${CF_UI_WORKER_HEALTH_ADDR:-127.0.0.1:8083}"
UNITS=(cf-ui-api cf-ui-worker cf-ui-web)

eval "$(sed -n '/^load_env() {/,/^}/p' "$ROOT/scripts/dev.sh")"

tools_path() {
	local p
	for p in "$HOME/node22/bin" "$HOME/.bun/bin" "$HOME/go/bin" /usr/local/go/bin; do
		[ -d "$p" ] && PATH="$p:$PATH"
	done
	export PATH
}

port_busy() { ss -ltnH "sport = :$1" | grep -q .; }

# _run <name>: what each systemd unit executes. Loads .env, then execs the process.
run_one() {
	tools_path
	cd "$ROOT"
	load_env .env
	case "$1" in
		api)
			cd apps/server
			exec "$BIN/api" -addr 127.0.0.1:8080
			;;
		worker)
			[ -d "$SHIM_DIR" ] && export PATH="$SHIM_DIR:$PATH"
			# Its own temp directory: the worker clears leftover job directories there at start, and another checkout's
			# cfctl may be using the default one. The whisper models are reused from the default place when present.
			export MEDIA_TEMP_DIR="${MEDIA_TEMP_DIR:-$STATE/media-tmp}"
			if [ -z "${WHISPER_MODELS_DIR:-}" ] && [ -d /tmp/cf-media/whisper-models ]; then export WHISPER_MODELS_DIR=/tmp/cf-media/whisper-models; fi
			mkdir -p "$MEDIA_TEMP_DIR"
			cd apps/server
			exec "$BIN/worker" -health-addr "$WORKER_HEALTH_ADDR"
			;;
		web)
			cd apps/web
			exec bun run dev --host 127.0.0.1
			;;
		*) echo "vps-ui: unknown service $1" >&2; exit 2 ;;
	esac
}

unit_up() { # unit name, memory max, CPU quota
	local name="$1" mem="$2" cpu="$3" svc="${1#cf-ui-}"
	systemd-run --user --quiet --unit="$name" --collect --nice=5 \
		-p MemoryMax="$mem" -p MemorySwapMax=0 -p CPUQuota="$cpu" -p KillMode=control-group -p TimeoutStopSec=10 \
		-p StandardOutput="append:$LOGS/$svc.log" -p StandardError="append:$LOGS/$svc.log" \
		"$SELF" _run "$svc"
	echo "vps-ui: started $name (memory max $mem, CPU $cpu)"
}

wait_http() { # url, seconds
	local i
	for ((i = 0; i < $2; i++)); do
		curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
		sleep 1
	done
	return 1
}

start() {
	tools_path
	cd "$ROOT"
	[ -f .env ] || { echo "vps-ui: .env missing at $ROOT (copy it with scp, chmod 600)" >&2; exit 1; }
	for u in "${UNITS[@]}"; do
		if systemctl --user is-active --quiet "$u"; then echo "vps-ui: $u is already running (scripts/vps-ui.sh stop first)" >&2; exit 1; fi
	done
	local p
	for p in 3000 8080 "${WORKER_HEALTH_ADDR##*:}"; do
		if port_busy "$p"; then echo "vps-ui: port $p is taken; not starting" >&2; exit 1; fi
	done
	mkdir -p "$BIN" "$LOGS"

	if port_busy 5432 && port_busy 9000; then
		echo "vps-ui: Postgres and MinIO already listen; leaving them as they are"
	else
		docker compose -f compose.yaml -f compose.vps.yaml up -d --no-recreate --wait postgres minio
		docker compose -f compose.yaml -f compose.vps.yaml run --rm minio-init >/dev/null
	fi

	echo "vps-ui: building api, worker and cfctl into $BIN"
	(cd apps/server && GOMAXPROCS=2 nice -n 10 go build -p 2 -o "$BIN/" ./cmd/api ./cmd/worker ./cmd/cfctl)
	echo "vps-ui: bun install"
	nice -n 10 bun install --frozen-lockfile >/dev/null
	(load_env .env && cd apps/server && "$BIN/cfctl" migrate up)

	unit_up cf-ui-api 512M 100%
	unit_up cf-ui-worker 1G 200%
	unit_up cf-ui-web 2G 200%
	wait_http http://127.0.0.1:8080/readyz 30 || echo "vps-ui: api not ready yet (scripts/vps-ui.sh logs api)" >&2
	wait_http "http://$WORKER_HEALTH_ADDR/readyz" 30 || echo "vps-ui: worker not ready yet (scripts/vps-ui.sh logs worker)" >&2
	wait_http http://127.0.0.1:3000/ 90 || echo "vps-ui: web not answering yet (scripts/vps-ui.sh logs web)" >&2
	status
}

stop() {
	local u
	for u in "${UNITS[@]}"; do
		systemctl --user stop "$u" 2>/dev/null && echo "vps-ui: stopped $u" || true
	done
}

status() {
	local u mem
	for u in "${UNITS[@]}"; do
		if systemctl --user is-active --quiet "$u"; then
			mem="$(systemctl --user show -p MemoryCurrent --value "$u")"
			echo "$u: running, $((mem / 1024 / 1024)) MB"
		else
			echo "$u: stopped"
		fi
	done
	printf 'api readyz:    %s\n' "$(curl -s -m 5 http://127.0.0.1:8080/readyz || echo down)"
	printf 'worker readyz: %s\n' "$(curl -s -m 5 "http://$WORKER_HEALTH_ADDR/readyz" || echo down)"
	printf 'web:           HTTP %s\n' "$(curl -s -m 20 -o /dev/null -w '%{http_code}' http://127.0.0.1:3000/ || echo down)"
}

case "${1:-}" in
	start) start ;;
	stop) stop ;;
	status) status ;;
	logs) tail -n 50 -f "$LOGS/${2:-web}.log" ;;
	_run) run_one "$2" ;;
	*) echo "usage: scripts/vps-ui.sh start | stop | status | logs [web|api|worker]" >&2; exit 2 ;;
esac
