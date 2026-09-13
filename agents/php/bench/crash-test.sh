#!/usr/bin/env bash
# Failure tests under load.
#  A: forwarder up -> killed (SIGKILL) -> restarted; the app must keep serving and spans must resume.
#  B: OTLP endpoint black-holed (non-routable IP); shows what an unreachable backend does to option B.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
VUS=${VUS:-16}; PHASE=${PHASE:-15s}
mkdir -p "$RESULTS"
out="$RESULTS/crash-test.txt"; : > "$out"

phase() { # variant label
  printf '%-4s %-28s ' "$1" "$2" | tee -a "$out"
  k6 run -q -e TARGET="http://nginx-$1" -e VUS="$VUS" -e DURATION="$PHASE" -e OUT= /bench/k6-bench.js | tee -a "$out"
}

"${DC[@]}" up -d forwarder php-a nginx-a >/dev/null
wait_http "http://127.0.0.1:28802/health"
phase a "forwarder running"
docker kill openlog-phpspike-forwarder-1 >/dev/null
phase a "forwarder killed (SIGKILL)"
"${DC[@]}" up -d forwarder >/dev/null
sleep 2
phase a "forwarder restarted"
sleep 12
echo "forwarder stats after restart: $(docker logs --tail 1 openlog-phpspike-forwarder-1 2>&1)" | tee -a "$out"

phase b "endpoint openlog:4318"
SPIKE_B_ENDPOINT=http://10.255.255.1:4318 "${DC[@]}" up -d php-b >/dev/null
wait_http "http://127.0.0.1:28803/health"
phase b "endpoint black-holed"
"${DC[@]}" up -d php-b >/dev/null
wait_http "http://127.0.0.1:28803/health"
echo "done -> $out"
