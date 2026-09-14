#!/usr/bin/env bash
# Soak test of openlog.so: MINUTES (10) of constant load on PHP-FPM (4 static workers, pm.max_requests=0, so the
# same processes serve every request) with the extension at its defaults (tracer on, threshold lowered to 50 ms so
# function traces are emitted regularly). Load: Laravel mix (bench/ext/soak/soak.lua: queries, uncaught and reported
# exceptions, slow report) + the plain PHP app (57 PDO statements per request). Telemetry goes to a datagram sink.
#
#   bash agents/php/bench/ext/soak/run.sh
#   MINUTES=60 MAX_GROWTH_KB=4096 bash agents/php/bench/ext/soak/run.sh
#
# Every SAMPLE (10) seconds the RSS of each FPM worker is recorded ($OUT/rss.csv). Fails when a worker was replaced
# (crash), FPM logged a signal, or a worker's RSS grew by more than MAX_GROWTH_KB (8192) between the end of the
# first quarter (allocators and opcache warmed up) and the end. Containers: openlog-php-soak-*.
set -euo pipefail
SOAK_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PHP_DIR=$(cd "$SOAK_DIR/../../.." && pwd)
SRC=${SRC:-$PHP_DIR/ext}
OUT=${OUT:-$SOAK_DIR/../results/soak-$(date -u +%Y%m%dT%H%M%SZ)}
MINUTES=${MINUTES:-10}; SAMPLE=${SAMPLE:-10}; MAX_GROWTH_KB=${MAX_GROWTH_KB:-8192}; CONNS=${CONNS:-8}
CPU_FPM=${CPU_FPM:-2-5}; CPU_NGINX=${CPU_NGINX:-6,7}; CPU_WRK=${CPU_WRK:-8,9}; CPU_AUX=${CPU_AUX:-0,1}
P=openlog-php-soak
NET=$P-net
mkdir -p "$OUT"

cleanup() {
  # shellcheck disable=SC2046
  docker rm -f $(docker ps -aq --filter "name=^$P-") >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker volume rm "$P-sock" >/dev/null 2>&1 || true
  if [ -n "${work:-}" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT
cleanup

work=$(mktemp -d)
mkdir -p "$work/ctx"
rsync -a --exclude modules --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude autom4te.cache "$SRC/" "$work/ctx/b/"
cp "$SOAK_DIR/../micro/plain.php" "$SOAK_DIR/../http/Dockerfile" "$work/ctx/"
echo "building images"
docker build -q -t "$P:app" "$work/ctx" >/dev/null
docker build -q -t "$P:wrk" - >/dev/null <<'EOF'
FROM alpine:3.22
RUN apk add --no-cache wrk
EOF
cat > "$work/openlog.ini" <<'EOF'
extension=openlog-b.so
openlog.service_name=soak
openlog.transport=unix:///run/openlog-infra-agent/php.sock
openlog.transaction_tracer.threshold_ms=50
EOF
cat > "$work/nginx.conf" <<'EOF'
worker_processes 2; events { worker_connections 4096; }
http { access_log off; keepalive_requests 1000000;
  upstream fpm { server fpm:9000; keepalive 16; }
  server { listen 8100; location / { include fastcgi_params; fastcgi_keep_conn on; fastcgi_pass fpm;
    fastcgi_param SCRIPT_FILENAME /srv/app/public/index.php; fastcgi_param SCRIPT_NAME /index.php; } }
  server { listen 8200; location / { include fastcgi_params; fastcgi_keep_conn on; fastcgi_pass fpm;
    fastcgi_param SCRIPT_FILENAME /srv/plain/index.php; fastcgi_param SCRIPT_NAME /index.php; } }
}
EOF

docker network create "$NET" >/dev/null
docker volume create "$P-sock" >/dev/null
docker run -d --name "$P-mariadb" --network "$NET" --network-alias mariadb --cpuset-cpus "$CPU_AUX" \
  -e MARIADB_ROOT_PASSWORD=root -e MARIADB_DATABASE=demo -e MARIADB_USER=demo -e MARIADB_PASSWORD=demo \
  -v "$PHP_DIR/demo/datastores/mariadb-init.sql:/docker-entrypoint-initdb.d/init.sql:ro" mariadb:11 >/dev/null
docker run -d --name "$P-redis" --network "$NET" --network-alias redis --cpuset-cpus "$CPU_AUX" redis:7-alpine >/dev/null
docker run -d --name "$P-sink" --cpuset-cpus "${CPU_AUX##*,}" -v "$P-sock:/run/openlog-infra-agent" --entrypoint php "$P:app" -n -r '
  $p = "/run/openlog-infra-agent/php.sock"; @unlink($p);
  $s = stream_socket_server("udg://$p", $e, $es, STREAM_SERVER_BIND); chmod($p, 0666);
  while (true) { stream_socket_recvfrom($s, 65536); }' >/dev/null
for _ in $(seq 1 90); do
  docker exec "$P-mariadb" healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1 && break
  sleep 2
done
docker run -d --name "$P-fpm" --network "$NET" --network-alias fpm --cpuset-cpus "$CPU_FPM" -e FPM_WORKERS=4 \
  -v "$P-sock:/run/openlog-infra-agent" -v "$work/openlog.ini:/usr/local/etc/php/conf.d/zz-openlog.ini:ro" \
  --entrypoint /usr/local/bin/laravel-start "$P:app" >/dev/null
docker run -d --name "$P-nginx" --network "$NET" --network-alias nginx --cpuset-cpus "$CPU_NGINX" -v "$work/nginx.conf:/etc/nginx/nginx.conf:ro" \
  nginx:1.29-alpine >/dev/null
for url in http://nginx:8100/bench/1 http://nginx:8200/items/7; do
  for _ in $(seq 1 60); do
    docker run --rm --network "$NET" "$P:wrk" wget -q -O /dev/null "$url" 2>/dev/null && break
    sleep 2
  done
done

rss() { # "<pid> <rss_kb>" per FPM worker
  docker exec "$P-fpm" sh -c 'for d in /proc/[0-9]*; do c=$(tr "\0" " " < "$d/cmdline" 2>/dev/null); case "$c" in "php-fpm: pool"*) echo "${d#/proc/} $(awk "/VmRSS/{print \$2}" "$d/status")";; esac; done'
}

secs=$((MINUTES * 60))
echo "soak: $MINUTES min, sampling every ${SAMPLE}s -> $OUT"
docker run -d --name "$P-wrk-laravel" --network "$NET" --cpuset-cpus "$CPU_WRK" -v "$SOAK_DIR/soak.lua:/soak.lua:ro" "$P:wrk" \
  wrk -t1 -c"$CONNS" -d"${secs}s" -s /soak.lua http://nginx:8100 >/dev/null
docker run -d --name "$P-wrk-plain" --network "$NET" --cpuset-cpus "$CPU_WRK" "$P:wrk" \
  wrk -t1 -c"$((CONNS / 2))" -d"${secs}s" http://nginx:8200/items/7 >/dev/null

echo "elapsed_s,pid,rss_kb" > "$OUT/rss.csv"
start=$(date +%s)
while [ "$(docker inspect -f '{{.State.Running}}' "$P-wrk-laravel" 2>/dev/null)" = true ]; do
  now=$(( $(date +%s) - start ))
  rss | awk -v t="$now" '{print t "," $1 "," $2}' >> "$OUT/rss.csv"
  sleep "$SAMPLE"
done
docker logs "$P-wrk-laravel" > "$OUT/wrk-laravel.txt" 2>&1 || true
docker wait "$P-wrk-plain" >/dev/null 2>&1 || true
docker logs "$P-wrk-plain" > "$OUT/wrk-plain.txt" 2>&1 || true
docker logs "$P-fpm" > "$OUT/fpm.log" 2>&1 || true

docker run --rm -e MAX_GROWTH_KB="$MAX_GROWTH_KB" -v "$OUT":/r --entrypoint php "$P:app" -n -r '
$rows = array_slice(file("/r/rss.csv", FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES), 1);
$by = []; $times = []; $end = 0;
foreach ($rows as $l) { [$t, $pid, $kb] = explode(",", $l); $by[$pid][(int) $t] = (int) $kb; $times[(int) $t] = true; $end = max($end, (int) $t); }
$q = (int) ($end / 4); $fail = []; $lines = [];
$pids = array_keys($by);
foreach ($by as $pid => $s) {
    ksort($s);
    $first = null; foreach ($s as $t => $kb) { if ($t >= $q) { $first = $kb; break; } }
    $last = end($s);
    $grow = $first === null ? 0 : $last - $first;
    $lines[] = sprintf("| %d | %d | %d | %d | %+d |", $pid, reset($s), $first, $last, $grow);
    if (count($s) < count($times) * 0.9) $fail[] = "worker $pid did not live through the run (replaced: crash?)";
    if ($grow > (int) getenv("MAX_GROWTH_KB")) $fail[] = "worker $pid grew by $grow KiB";
}
$log = file_get_contents("/r/fpm.log");
if (preg_match("/exited on signal|SIGSEGV|core dumped|zend_mm_heap corrupted/i", $log)) $fail[] = "FPM log reports a crashed worker";
$wrk = file_get_contents("/r/wrk-laravel.txt") . file_get_contents("/r/wrk-plain.txt");
preg_match_all("/(\d+) requests in/", $wrk, $m);
echo "# openlog.so soak test\n\nDuration ", round($end / 60, 1), " min, requests ", array_sum($m[1]), ", workers ", count($pids), "\n\n";
echo "| worker pid | RSS start KiB | RSS at 25 % | RSS end | growth after 25 % |\n|---|---|---|---|---|\n", implode("\n", $lines), "\n\n";
echo $fail ? "FAIL: " . implode("; ", $fail) . "\n" : "PASS (max growth " . getenv("MAX_GROWTH_KB") . " KiB)\n";
exit($fail ? 1 : 0);
' | tee "$OUT/summary.md"
