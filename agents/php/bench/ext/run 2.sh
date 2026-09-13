#!/usr/bin/env bash
# openlog.so overhead benchmark: base (no extension) vs off (loaded, tracer disabled) vs on (tracer defaults) on the
# demo Laravel 12 / PHP 8.3 image, GET /bench/{id} (one Eloquent query + two Redis calls).
# RUNS (3) x variants, order rotated per run; each measurement: restart PHP-FPM, WARMUP (10 s) warm-up, DURATION (30 s)
# closed-loop k6 with VUS (16). Results: bench/ext/results/ (k6 JSON, cpu.csv, summary.md).
#
#   make -C agents/php/demo images OPENLOG_EXT=1      # image with the extension
#   bash agents/php/bench/ext/run.sh                  # full run (~6 min)
#   DURATION=5s WARMUP=2s RUNS=1 bash agents/php/bench/ext/run.sh   # plumbing smoke test
#   KEEP_UP=1 ...                                     # leave the bench stack running afterwards
set -euo pipefail
BENCH_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export RESULTS=${RESULTS:-$BENCH_DIR/results} # also the k6 container's /results mount
RUNS=${RUNS:-3}; DURATION=${DURATION:-30s}; WARMUP=${WARMUP:-10s}; VUS=${VUS:-16}
DC=(docker compose -p openlog-php-bench -f "$BENCH_DIR/docker-compose.yml")
VARIANTS=(base off on)
ORDERS=("base off on" "off on base" "on base off")

port_of() { case "$1" in base) echo 28906;; off) echo 28907;; on) echo 28908;; esac; }
cpu_usec() { docker exec "openlog-php-bench-$1-1" awk '/^usage_usec/{print $2}' /sys/fs/cgroup/cpu.stat; }
k6() { "${DC[@]}" --profile bench run --rm -T k6 "$@"; }
wait_http() {
  for _ in $(seq 1 120); do
    curl -fsS -m 3 -o /dev/null "$1" 2>/dev/null && return 0
    sleep 1
  done
  echo "timeout waiting for $1" >&2; return 1
}

docker network inspect "${OPENLOG_NETWORK:-openlog_default}" >/dev/null 2>&1 || {
  echo "network ${OPENLOG_NETWORK:-openlog_default} not found (shared openlog not running)" >&2; exit 1; }
mkdir -p "$RESULTS"
"${DC[@]}" up -d --wait mariadb redis forwarder php-base nginx-base php-off nginx-off php-on nginx-on

ext=$(docker exec openlog-php-bench-php-on-1 php -m | grep -cx openlog || true)
echo "openlog.so loaded in php-on: $([ "$ext" = 1 ] && echo yes || echo 'NO (image built with OPENLOG_EXT=0: plumbing test only)')"
{
  echo "date=$(date -u +%FT%TZ) runs=$RUNS duration=$DURATION warmup=$WARMUP vus=$VUS extension_loaded=$ext"
  docker exec openlog-php-bench-php-on-1 php -r 'echo "php=", PHP_VERSION, " openlog=", phpversion("openlog") ?: "-", "\n";'
} > "$RESULTS/meta.txt"

CSV="$RESULTS/cpu.csv"
echo "variant,run,php_cpu_usec,forwarder_cpu_usec" > "$CSV"
for r in $(seq 1 "$RUNS"); do
  for v in ${ORDERS[$(( (r - 1) % 3 ))]}; do
    echo "== run $r variant $v"
    "${DC[@]}" restart "php-$v" >/dev/null
    wait_http "http://127.0.0.1:$(port_of "$v")/health"
    k6 run -q -e TARGET="http://nginx-$v" -e VUS="$VUS" -e DURATION="$WARMUP" -e OUT= /bench/k6-bench.js
    c0=$(cpu_usec "php-$v"); f0=$(cpu_usec forwarder)
    k6 run -q -e TARGET="http://nginx-$v" -e VUS="$VUS" -e DURATION="$DURATION" -e OUT="/results/$v-run$r.json" /bench/k6-bench.js
    c1=$(cpu_usec "php-$v"); f1=$(cpu_usec forwarder)
    echo "$v,$r,$((c1 - c0)),$((f1 - f0))" >> "$CSV"
    sleep 3 # let the forwarder drain before the next variant
  done
done

docker run --rm --entrypoint php -v "$RESULTS":/r -v "$BENCH_DIR":/b:ro openlog-php/laravel-83:dev /b/summarize.php /r \
  | tee "$RESULTS/summary.md"

[ "${KEEP_UP:-0}" = 1 ] || "${DC[@]}" --profile bench down --remove-orphans --volumes
