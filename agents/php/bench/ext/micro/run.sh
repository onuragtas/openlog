#!/usr/bin/env bash
# Per-request cost of openlog.so without web server noise: php-cgi -T N runs N complete requests (request startup,
# script, shutdown incl. span emission) in one process, pinned to one CPU, datagrams go to a sink on another CPU.
#
#   bash agents/php/bench/ext/micro/run.sh                          # current ext only
#   SRC_A=/path/to/older/ext bash agents/php/bench/ext/micro/run.sh # A/B: older source tree vs current
#
# Variants: base (no extension), <src>-disabled (loaded, openlog.enabled=0), <src>-off (tracer off),
# <src>-on (tracer defaults). Apps: plain (bench/ext/micro/plain.php, 5 PDO SQLite spans) and laravel
# (Laravel 12 GET /health through public/index.php). Two measurements:
#   time      ROUNDS interleaved rounds (rotated order) of N requests; ns/request median, p25–p75, min–max
#   callgrind instructions per request = (Ir(N2) - Ir(N1)) / (N2 - N1), deterministic (CALLGRIND=0 to skip)
# Results: $OUT (default bench/ext/results/micro-<date>/).
set -euo pipefail
MICRO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
EXT_DIR=$(cd "$MICRO_DIR/../../../ext" && pwd)
SRC_B=${SRC_B:-$EXT_DIR}
SRC_A=${SRC_A:-}
OUT=${OUT:-$MICRO_DIR/../results/micro-$(date -u +%Y%m%dT%H%M%SZ)}
IMAGE=${IMAGE:-openlog-php-micro:8.3}
CPUS=${CPUS:-2,3}
mkdir -p "$OUT"

docker build -q -t "$IMAGE" "$MICRO_DIR" >/dev/null
vols=(-v "$SRC_B":/src/b:ro -v "$MICRO_DIR":/bench:ro -v "$OUT":/out)
[ -n "$SRC_A" ] && vols+=(-v "$SRC_A":/src/a:ro)
docker run --rm --name "openlog-php-micro-$$" --cpuset-cpus "$CPUS" "${vols[@]}" \
  -e ROUNDS="${ROUNDS:-11}" -e N_PLAIN="${N_PLAIN:-2000}" -e N_LARAVEL="${N_LARAVEL:-500}" \
  -e CALLGRIND="${CALLGRIND:-1}" -e APPS="${APPS:-plain laravel}" -e VARIANTS="${VARIANTS:-}" \
  "$IMAGE" bash /bench/inner.sh
cat "$OUT/summary.md"
