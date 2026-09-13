#!/usr/bin/env bash
# Per-worker memory of the Laravel pool after load: restart PHP-FPM, 10 s warm-up, 20 s load, read VmRSS.
# RUNS (3) x variants, order rotated per run. Writes spike/results/mem.csv.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
RUNS=${RUNS:-3}; VUS=${VUS:-16}
mkdir -p "$RESULTS"
CSV="$RESULTS/mem.csv"
echo "variant,run,workers,rss_kb" > "$CSV"
orders=("base a b" "a b base" "b base a")

for r in $(seq 1 "$RUNS"); do
  for v in ${orders[$(( (r - 1) % 3 ))]}; do
    echo "== mem run $r variant $v"
    "${DC[@]}" restart "php-$v" >/dev/null
    wait_http "http://127.0.0.1:$(port_of "$v")/health"
    k6 run -q -e TARGET="http://nginx-$v" -e VUS="$VUS" -e DURATION=10s -e OUT= /bench/k6-bench.js
    k6 run -q -e TARGET="http://nginx-$v" -e VUS="$VUS" -e DURATION=20s -e OUT= /bench/k6-bench.js
    echo "$v,$r,$(fpm_mem "$v" laravel)" >> "$CSV"
  done
done
cat "$CSV"
