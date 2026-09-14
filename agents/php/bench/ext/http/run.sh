#!/usr/bin/env bash
# HTTP overhead benchmark of openlog.so: PHP-FPM + nginx + wrk in plain containers (names openlog-php-http-*, own
# network; the shared openlog stack is not used). Telemetry goes to a datagram sink, so neither a forwarder nor a
# backend competes for CPU.
#
#   bash agents/php/bench/ext/http/run.sh                          # base, b-off, b-on, b-lean
#   SRC_A=/path/to/older/ext bash agents/php/bench/ext/http/run.sh # adds a-off, a-on (A/B in the same run)
#
# Apps: laravel = Laravel 12 GET /bench/{id} (Eloquent query + 2 Redis calls; MariaDB + Redis containers),
#       plain   = bench/ext/micro/plain.php (57 PDO SQLite statements, ~2 000 calls).
# Variants: base (no extension), <src>-off (tracer disabled), <src>-on (defaults), b-lean (openlog.userland_hooks=0).
# ROUNDS (5) interleaved rounds (variant order rotated); per measurement WARMUP (5s) + DURATION (15s) of wrk with
# CONNS (16) connections. PHP-FPM: 8 static workers pinned to CPU_FPM (4 CPUs); nginx, wrk, sink and datastores on
# other CPUs. Metrics: RPS, p50/p99 and PHP CPU per request (cgroup cpu.stat of the FPM container).
# Results: $OUT/http.tsv + summary.md (default bench/ext/results/http-<date>).
set -euo pipefail
HTTP_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PHP_DIR=$(cd "$HTTP_DIR/../../.." && pwd)
SRC_B=${SRC_B:-$PHP_DIR/ext}
SRC_A=${SRC_A:-}
OUT=${OUT:-$HTTP_DIR/../results/http-$(date -u +%Y%m%dT%H%M%SZ)}
ROUNDS=${ROUNDS:-5}; WARMUP=${WARMUP:-5s}; DURATION=${DURATION:-15s}; CONNS=${CONNS:-16}
CPU_FPM=${CPU_FPM:-2-5}; CPU_NGINX=${CPU_NGINX:-6,7}; CPU_WRK=${CPU_WRK:-8,9}; CPU_AUX=${CPU_AUX:-0,1}
APPS=${APPS:-"laravel plain"}
P=openlog-php-http
NET=$P-net
mkdir -p "$OUT"

variants=(base)
[ -n "$SRC_A" ] && variants+=(a-off a-on)
variants+=(b-off b-on b-lean)
[ -n "${VARIANTS:-}" ] && read -ra variants <<<"$VARIANTS"

cleanup() {
  if [ "${KEEP_UP:-0}" = 1 ] && [ -n "${work:-}" ]; then
    echo "KEEP_UP=1: containers $P-* and $work kept (remove: docker rm -f \$(docker ps -aq --filter name=^$P-))"
    return 0
  fi
  # shellcheck disable=SC2046
  docker rm -f $(docker ps -aq --filter "name=^$P-") >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker volume rm "$P-sock" >/dev/null 2>&1 || true
  if [ -n "${work:-}" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT
cleanup

work=$(mktemp -d)
ctx="$work/ctx"
mkdir -p "$ctx" "$work/ini"
rsync -a --exclude modules --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude autom4te.cache "$SRC_B/" "$ctx/b/"
[ -n "$SRC_A" ] && rsync -a --exclude modules --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude autom4te.cache "$SRC_A/" "$ctx/a/"
cp "$HTTP_DIR/../micro/plain.php" "$ctx/plain.php"
cp "$HTTP_DIR/Dockerfile" "$ctx/Dockerfile"
echo "building images"
docker build -q -t "$P:app" "$ctx" >/dev/null
docker build -q -t "$P:wrk" - >/dev/null <<'EOF'
FROM alpine:3.22
RUN apk add --no-cache wrk
EOF

for v in "${variants[@]}"; do
  f="$work/ini/$v.ini"
  case "$v" in
    base) echo "; no extension" > "$f" ;;
    *)
      s=${v%%-*}; mode=${v#*-}
      {
        echo "extension=openlog-$s.so"
        echo "openlog.service_name=http-bench-$v"
        echo "openlog.transport=unix:///run/openlog-infra-agent/php.sock"
        case "$mode" in
          off) echo "openlog.transaction_tracer.enabled=0" ;;
          lean) echo "openlog.transaction_tracer.enabled=0"; echo "openlog.userland_hooks=0" ;;
        esac
      } > "$f" ;;
  esac
done

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

nginx_conf="$work/nginx.conf"
{
  echo "worker_processes 2; events { worker_connections 4096; }"
  echo "http { access_log off; keepalive_requests 1000000;"
  i=0
  for v in "${variants[@]}"; do
    echo "  upstream fpm_$i { server fpm-$v:9000; keepalive 32; }"
    echo "  server { listen $((8100 + i)); location / { include fastcgi_params; fastcgi_keep_conn on; fastcgi_pass fpm_$i;"
    echo "    fastcgi_param SCRIPT_FILENAME /srv/app/public/index.php; fastcgi_param SCRIPT_NAME /index.php; } }"
    echo "  server { listen $((8200 + i)); location / { include fastcgi_params; fastcgi_keep_conn on; fastcgi_pass fpm_$i;"
    echo "    fastcgi_param SCRIPT_FILENAME /srv/plain/index.php; fastcgi_param SCRIPT_NAME /index.php; } }"
    i=$((i + 1))
  done
  echo "}"
} > "$nginx_conf"

for _ in $(seq 1 90); do
  docker exec "$P-mariadb" healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1 && break
  sleep 2
done
for v in "${variants[@]}"; do
  docker run -d --name "$P-fpm-$v" --network "$NET" --network-alias "fpm-$v" --cpuset-cpus "$CPU_FPM" \
    -e FPM_WORKERS=8 -e OPENLOG_SERVICE_NAME="http-bench-$v" -v "$P-sock:/run/openlog-infra-agent" \
    -v "$work/ini/$v.ini:/usr/local/etc/php/conf.d/zz-openlog.ini:ro" --entrypoint /usr/local/bin/laravel-start "$P:app" >/dev/null
done
docker run -d --name "$P-nginx" --network "$NET" --network-alias nginx --cpuset-cpus "$CPU_NGINX" -v "$nginx_conf:/etc/nginx/nginx.conf:ro" \
  nginx:1.29-alpine >/dev/null

wrk() { docker run --rm --network "$NET" --cpuset-cpus "$CPU_WRK" "$P:wrk" wrk "$@"; }
port_of() { local app=$1 v=$2 i=0 x; for x in "${variants[@]}"; do [ "$x" = "$v" ] && break; i=$((i + 1)); done
  [ "$app" = laravel ] && echo $((8100 + i)) || echo $((8200 + i)); }
path_of() { [ "$1" = laravel ] && echo /bench/42 || echo /items/7; }
cpu_usec() { docker exec "$P-fpm-$1" awk '/^usage_usec/{print $2}' /sys/fs/cgroup/cpu.stat; }

for v in "${variants[@]}"; do
  for app in $APPS; do
    url="http://nginx:$(port_of "$app" "$v")$(path_of "$app")"
    ok=0
    for _ in $(seq 1 60); do
      if docker run --rm --network "$NET" "$P:wrk" wget -q -O /dev/null "$url" 2>/dev/null; then ok=1; break; fi
      sleep 2
    done
    [ "$ok" = 1 ] || { echo "$app/$v not ready ($url)"; docker logs --tail 30 "$P-fpm-$v"; exit 1; }
  done
  docker exec "$P-fpm-$v" php -r 'echo "fpm-'"$v"': openlog=", extension_loaded("openlog") ? phpversion("openlog") : "-", " userland_hooks=", ini_get("openlog.userland_hooks"), " tracer=", ini_get("openlog.transaction_tracer.enabled"), "\n";'
done | tee "$OUT/meta.txt"

TSV="$OUT/http.tsv"
echo -e "app\tvariant\tround\trequests\trps\tp50_ms\tp99_ms\tcpu_us_per_req\tnon2xx" > "$TSV"
for r in $(seq 1 "$ROUNDS"); do
  k=$(( (r - 1) % ${#variants[@]} ))
  for app in $APPS; do
    for v in "${variants[@]:$k}" "${variants[@]:0:$k}"; do
      url="http://nginx:$(port_of "$app" "$v")$(path_of "$app")"
      wrk -t2 -c"$CONNS" -d"$WARMUP" "$url" >/dev/null
      c0=$(cpu_usec "$v")
      res=$(wrk -t2 -c"$CONNS" -d"$DURATION" --latency "$url")
      c1=$(cpu_usec "$v")
      echo "$res" | awk -v app="$app" -v v="$v" -v r="$r" -v cpu=$((c1 - c0)) '
        function ms(x) { if (x ~ /ms$/) return x + 0; if (x ~ /us$/) return x / 1000; if (x ~ /s$/) return x * 1000; return x }
        /requests in/ { req = $1 } /Requests\/sec/ { rps = $2 } $1 == "50%" { p50 = ms($2) } $1 == "99%" { p99 = ms($2) }
        /Non-2xx/ { bad = $NF }
        END { printf "%s\t%s\t%s\t%d\t%.1f\t%.2f\t%.2f\t%.0f\t%d\n", app, v, r, req, rps, p50, p99, req ? cpu / req : 0, bad }' | tee -a "$TSV"
    done
  done
done

docker run --rm -v "$OUT":/r -v "$HTTP_DIR":/b:ro --entrypoint php "$P:app" -n /b/summarize.php /r/http.tsv | tee "$OUT/summary.md"
