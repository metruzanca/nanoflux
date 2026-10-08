#!/usr/bin/env bash
# Sample nanoflux page latency (and, optionally, host load) so a change can be
# compared against the last run. Run it on the machine hosting nanoflux.
#
# Usage:
#   scripts/bench.sh
#   BENCH_URL=http://localhost:20310 scripts/bench.sh
#
# Environment:
#   BENCH_URL   base URL        (default http://localhost:20310)
#   BENCH_N     requests/path   (default 30)
#   BENCH_USER  username; if set, logs in and also measures /authors
#   BENCH_PASS  password for BENCH_USER
#   BENCH_STATS 1 to also print uptime/free and per-container CPU
#
# Dependencies: curl, awk, sort.
set -euo pipefail

BENCH_URL="${BENCH_URL:-http://localhost:20310}"
BENCH_N="${BENCH_N:-30}"

jar="$(mktemp)"
lat="$(mktemp)"
trap 'rm -f "$jar" "$lat"' EXIT

paths="/healthz /login /"
if [ -n "${BENCH_USER:-}" ]; then
	curl -s -c "$jar" -o /dev/null -X POST -d "username=${BENCH_USER}&password=${BENCH_PASS:-}" "$BENCH_URL/login"
	paths="$paths /authors"
fi

pct() {
	awk '{a[NR]=$1} END{printf "n=%d p50=%.3fs p95=%.3fs max=%.3fs", NR, a[int(NR*0.5)+1], a[int(NR*0.95)+1], a[NR]}'
}

echo "# nanoflux bench: $BENCH_URL (${BENCH_N} req/path)"
printf '%-12s %s\n' "path" "latency"
for p in $paths; do
	: >"$lat"
	for _ in $(seq 1 "$BENCH_N"); do
		curl -s -b "$jar" -o /dev/null -w "%{time_total}\n" "$BENCH_URL$p" >>"$lat"
		sleep 0.15
	done
	printf '%-12s ' "$p"
	sort -n "$lat" | pct
	echo
done

if [ "${BENCH_STATS:-0}" = 1 ]; then
	echo
	echo "# host"
	uptime
	free -m | awk 'NR==2{printf "mem used=%sMB avail=%sMB\n",$3,$7} NR==3{printf "swap used=%sMB\n",$3}'
	if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -q nanoflux; then
		name="$(docker ps --format '{{.Names}}' | grep nanoflux | head -1)"
		echo "container $name: $(docker stats --no-stream --format 'cpu={{.CPUPerc}}' "$name")"
	fi
fi
