#!/usr/bin/env bash
# Benchmark baseline vs option A vs option B on Laravel GET /bench/{id}.
# RUNS (3) x variants, order rotated per run; each measurement: restart PHP-FPM, 10 s warm-up, DURATION load.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
RUNS=${RUNS:-3}; DURATION=${DURATION:-30s}; VUS=${VUS:-16}
mkdir -p "$RESULTS"
CSV="$RESULTS/cpu-mem.csv"
echo "variant,run,php_cpu_usec,forwarder_cpu_usec,workers,rss_kb" > "$CSV"
orders=("base a b" "a b base" "b base a")

for r in $(seq 1 "$RUNS"); do
  for v in ${orders[$(( (r - 1) % 3 ))]}; do
    echo "== run $r variant $v"
    "${DC[@]}" restart "php-$v" >/dev/null
    wait_http "http://127.0.0.1:$(port_of "$v")/health"
    k6 run -q -e TARGET="http://nginx-$v" -e VUS="$VUS" -e DURATION=10s -e OUT= /bench/k6-bench.js
    c0=$(cpu_usec "php-$v"); f0=$(cpu_usec forwarder)
    k6 run -q -e TARGET="http://nginx-$v" -e VUS="$VUS" -e DURATION="$DURATION" -e OUT="/results/$v-run$r.json" /bench/k6-bench.js
    c1=$(cpu_usec "php-$v"); f1=$(cpu_usec forwarder)
    fwd=0; [ "$v" = a ] && fwd=$((f1 - f0))
    echo "$v,$r,$((c1 - c0)),$fwd,$(fpm_mem "$v" laravel)" >> "$CSV"
    sleep 5 # let exporters drain before the next variant
  done
done

docker run --rm --entrypoint php -v "$RESULTS":/r -v "$PHP_DIR/bench":/b:ro openlog-phpspike/app:dev /b/summarize.php /r | tee "$RESULTS/summary.md"
