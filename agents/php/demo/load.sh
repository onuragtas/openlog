#!/usr/bin/env bash
# Load for the openlog PHP demo: hits every endpoint of every service (normal, slow, error, fatal), some requests
# with a W3C traceparent header whose trace IDs are printed for lookup in the openlog UI.
#
#   ./load.sh                 one pass (ROUNDS=1) from the host against 127.0.0.1:28901-28905, summary of status codes
#   ROUNDS=5 ./load.sh        five passes
#   ./load.sh --loop          continuous low-rate mixed traffic (LOAD_RATE req/s, default 3) until interrupted
#   ./load.sh --cli           also run the PHP CLI transaction (docker exec, host mode only)
#
# In the compose `loadgen` container (profile "load", LOAD_IN_COMPOSE=1) the same 127.0.0.1:<port> URLs are used and
# curl --connect-to routes them to the compose services, so the Host header (WordPress site URL) stays identical.
set -uo pipefail

LOOP=0; CLI=0
for a in "$@"; do
  case "$a" in
    --loop) LOOP=1 ;;
    --cli) CLI=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown argument $a" >&2; exit 2 ;;
  esac
done
ROUNDS=${ROUNDS:-1}
LOAD_RATE=${LOAD_RATE:-3}
TRACE_EVERY=${TRACE_EVERY:-4} # every n-th request carries a traceparent

L83=127.0.0.1:28901; L74=127.0.0.1:28902; SYM=127.0.0.1:28903; WP=127.0.0.1:28904; P71=127.0.0.1:28905
CONNECT_TO=()
if [ "${LOAD_IN_COMPOSE:-0}" = 1 ]; then
  CONNECT_TO=(--connect-to "$L83:nginx-laravel-83:80" --connect-to "$L74:nginx-laravel-74:80"
    --connect-to "$SYM:nginx-symfony-83:80" --connect-to "$WP:php-wordpress-82:80" --connect-to "$P71:php-plain-71:80")
fi

# "weight service host path expected-status" (expected: 2xx, 5xx, or a code). {id} is replaced by a random id.
ENDPOINTS=(
  "2 php-laravel-83 $L83 /health 2xx"
  "8 php-laravel-83 $L83 /bench/{id} 2xx"
  "4 php-laravel-83 $L83 /users/{id}/orders 2xx"
  "1 php-laravel-83 $L83 /slow/report 2xx"
  "1 php-laravel-83 $L83 /boom 5xx"
  "1 php-laravel-83 $L83 /reported 2xx"
  "1 php-laravel-83 $L83 /fatal 5xx"
  "1 php-laravel-83 $L83 /fatal/user-error 5xx"
  "1 php-laravel-83 $L83 /fatal/memory 5xx"
  "2 php-laravel-83 $L83 /predis 2xx"
  "2 php-laravel-74 $L74 /health 2xx"
  "6 php-laravel-74 $L74 /bench/{id} 2xx"
  "3 php-laravel-74 $L74 /users/{id}/orders 2xx"
  "1 php-laravel-74 $L74 /slow/report 2xx"
  "1 php-laravel-74 $L74 /boom 5xx"
  "1 php-laravel-74 $L74 /reported 2xx"
  "1 php-laravel-74 $L74 /fatal 5xx"
  "1 php-laravel-74 $L74 /fatal/user-error 5xx"
  "1 php-laravel-74 $L74 /fatal/memory 5xx"
  "1 php-laravel-74 $L74 /predis 2xx"
  "1 php-symfony-83 $SYM /health 2xx"
  "6 php-symfony-83 $SYM /api/products/{id} 2xx"
  "3 php-symfony-83 $SYM /api/products/{id}/reviews 2xx"
  "2 php-symfony-83 $SYM /api/external 2xx"
  "1 php-symfony-83 $SYM /api/reports/slow 2xx"
  "1 php-symfony-83 $SYM /api/reports/inventory 2xx"
  "1 php-symfony-83 $SYM /api/boom 5xx"
  "1 php-symfony-83 $SYM /api/products/0/missing 404"
  "2 php-wordpress-82 $WP / 2xx"
  "2 php-wordpress-82 $WP /demo-post-1/ 2xx"
  "4 php-wordpress-82 $WP /wp-json/demo/v1/items/{id} 2xx"
  "1 php-wordpress-82 $WP /?slow=1 2xx"
  "1 php-wordpress-82 $WP /slow-report/ 2xx"
  "1 php-wordpress-82 $WP /wp-json/demo/v1/boom 5xx"
  "1 php-wordpress-82 $WP /?boom=1 5xx"
  "1 php-plain-71 $P71 /index.php 2xx"
  "4 php-plain-71 $P71 /users.php?id={id} 2xx"
  "3 php-plain-71 $P71 /pg.php?id={id} 2xx"
  "3 php-plain-71 $P71 /redis.php 2xx"
  "2 php-plain-71 $P71 /http.php 2xx"
  "1 php-plain-71 $P71 /slow.php 2xx"
  "1 php-plain-71 $P71 /boom.php 5xx"
  "1 php-plain-71 $P71 /fatal.php 5xx"
  "1 php-plain-71 $P71 /fatal.php?type=memory 5xx"
  "1 php-plain-71 $P71 /fatal.php?type=user 5xx"
)

N=0
TRACES=()
# bash 3.2 compatible (macOS): results are appended to a file ("<ok|FAIL> <service> <path> -> <code>")
RESULT_FILE=$(mktemp "${TMPDIR:-/tmp}/openlog-load.XXXXXX")
trap 'rm -f "$RESULT_FILE"' EXIT

newtrace() { od -An -N16 -tx1 /dev/urandom | tr -d ' \n'; }
newspan() { od -An -N8 -tx1 /dev/urandom | tr -d ' \n'; }

matches() { # status expected
  case "$2" in
    2xx) [[ "$1" == 2?? ]] ;;
    5xx) [[ "$1" == 5?? ]] ;;
    *) [ "$1" = "$2" ] ;;
  esac
}

hit() { # service host path expected   (caller increments N)
  local svc=$1 host=$2 path=${3//\{id\}/$((RANDOM % 100 + 1))} want=$4 hdr=() tid=""
  if [ $((N % TRACE_EVERY)) -eq 0 ]; then
    tid=$(newtrace)
    hdr=(-H "traceparent: 00-$tid-$(newspan)-01")
  fi
  local code
  code=$(curl -s -o /dev/null -m 20 -w '%{http_code}' ${CONNECT_TO[@]+"${CONNECT_TO[@]}"} ${hdr[@]+"${hdr[@]}"} "http://$host$path")
  local verdict=ok
  if ! matches "$code" "$want"; then
    verdict=FAIL
    [ "$LOOP" = 1 ] || echo "UNEXPECTED $svc $path: $code (want $want)" >&2
  fi
  echo "$verdict $svc $3 -> $code" >> "$RESULT_FILE"
  if [ -n "$tid" ]; then
    TRACES+=("$tid $svc $path $code")
    [ "$LOOP" = 1 ] && echo "$(date +%T) traceparent trace_id=$tid $svc $path -> $code"
  fi
}

if [ "$LOOP" = 1 ]; then
  # Weighted random pick at ~LOAD_RATE requests per second (sequential; slow endpoints lower the rate slightly).
  POOL=()
  for e in "${ENDPOINTS[@]}"; do
    read -r w rest <<<"$e"
    for _ in $(seq 1 "$w"); do POOL+=("$rest"); done
  done
  interval=$(awk "BEGIN{print 1/$LOAD_RATE}")
  echo "load loop: ~$LOAD_RATE req/s over ${#POOL[@]} weighted endpoints (Ctrl+C to stop)"
  trap 'echo; echo "sent $N requests, $(grep -c ^FAIL "$RESULT_FILE") unexpected statuses"; rm -f "$RESULT_FILE"; exit 0' INT TERM
  while true; do
    read -r svc host path want <<<"${POOL[$((RANDOM % ${#POOL[@]}))]}"
    N=$((N + 1))
    hit "$svc" "$host" "$path" "$want" &
    sleep "$interval"
    if [ $((N % 50)) -eq 0 ]; then
      wait
      : > "$RESULT_FILE" # keep the file small in endless mode
    fi
  done
fi

for r in $(seq 1 "$ROUNDS"); do
  echo "== round $r/$ROUNDS"
  for e in "${ENDPOINTS[@]}"; do
    read -r w svc host path want <<<"$e"
    for _ in $(seq 1 "$w"); do N=$((N + 1)); hit "$svc" "$host" "$path" "$want"; done
  done
done

if [ "$CLI" = 1 ] && [ "${LOAD_IN_COMPOSE:-0}" != 1 ]; then
  echo "== CLI transaction (php-plain-71)"
  docker compose -p openlog-php exec -T php-plain-71 php /var/www/html/cli.php 2
fi

echo
echo "== status codes"
sort "$RESULT_FILE" | uniq -c | sort -k3,3 -k4
echo
echo "== requests with traceparent (look these trace IDs up in the openlog UI, http://localhost:8080)"
[ ${#TRACES[@]} -gt 0 ] && printf '%s\n' "${TRACES[@]:0:12}"
FAILS=$(grep -c '^FAIL' "$RESULT_FILE" || true)
echo
echo "sent $N requests, $FAILS unexpected statuses"
[ "$FAILS" -eq 0 ]
