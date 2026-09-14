#!/usr/bin/env bash
# Build matrix for agents/php/ext: builds openlog.so and runs the phpt suite for every PHP version.
#
#   build/run-matrix.sh                         # NTS glibc 7.1 ... 8.4 + ZTS/musl 7.4 and 8.3
#   VERSIONS="8.3" EXTRA="" build/run-matrix.sh # one version
#   VERSIONS="" EXTRA="8.3-asan" build/run-matrix.sh
#       AddressSanitizer + UBSan debug PHP built from source (Dockerfile.asan, ~10 min the first time); openlog.so
#       is built with the same sanitizers and every sanitizer report fails the run
#   VERSIONS="" EXTRA="8.3-xdebug 8.3-ddtrace 8.3-newrelic 8.3-jit" build/run-matrix.sh
#       compatibility: Xdebug (develop,coverage), Datadog ddtrace, New Relic, OPcache JIT (Dockerfile.compat; needs
#       network access to pecl / GitHub / download.newrelic.com)
#   VERSIONS="" EXTRA="8.4-frankenphp" build/run-matrix.sh
#       the suite in the official FrankenPHP image (ZTS PHP + the frankenphp binary): tests/044-frankenphp.phpt runs a
#       real FrankenPHP server in worker and classic mode
#   PARALLEL=3 (default) versions at a time; PLATFORM=linux/amd64 for QEMU builds
#
# Datastore tests (mysqli, pgsql, phpredis) run against MariaDB / PostgreSQL / Redis containers on a private
# network (openlog-php-test); nothing touches any other compose project. Results: $OUT/<tag>.log and a summary.
set -euo pipefail
EXT_DIR=$(cd "$(dirname "$0")/.." && pwd)
VERSIONS=${VERSIONS-"7.1 7.2 7.3 7.4 8.0 8.1 8.2 8.3 8.4"}
EXTRA=${EXTRA-"7.4-zts 8.3-zts 7.4-cli-alpine 8.3-cli-alpine"}
PARALLEL=${PARALLEL:-3}
PLATFORM=${PLATFORM:-}
WORK=${WORK:-${TMPDIR:-/tmp}/openlog-ext-matrix}
OUT=${OUT:-$WORK/results}
NET=openlog-php-test
KEEP_DB=${KEEP_DB:-0}
mkdir -p "$WORK" "$OUT"
plat=()
[ -n "$PLATFORM" ] && plat=(--platform "$PLATFORM")

redis_for() {
  case "$1" in
    7.1*|7.2*|7.3*) echo 5.3.7 ;;
    7.4*) echo 6.0.2 ;;
    *) echo 6.2.0 ;;
  esac
}

start_datastores() {
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  if ! docker inspect -f '{{.State.Running}}' openlog-php-test-mariadb 2>/dev/null | grep -q true; then
    docker rm -f openlog-php-test-mariadb >/dev/null 2>&1 || true
    docker run -d --name openlog-php-test-mariadb --network "$NET" -e MARIADB_ROOT_PASSWORD=root \
      -e MARIADB_DATABASE=test -e MARIADB_USER=test -e MARIADB_PASSWORD=test mariadb:11 >/dev/null
  fi
  if ! docker inspect -f '{{.State.Running}}' openlog-php-test-postgres 2>/dev/null | grep -q true; then
    docker rm -f openlog-php-test-postgres >/dev/null 2>&1 || true
    docker run -d --name openlog-php-test-postgres --network "$NET" -e POSTGRES_DB=test -e POSTGRES_USER=test \
      -e POSTGRES_PASSWORD=test postgres:16-alpine >/dev/null
  fi
  if ! docker inspect -f '{{.State.Running}}' openlog-php-test-redis 2>/dev/null | grep -q true; then
    docker rm -f openlog-php-test-redis >/dev/null 2>&1 || true
    docker run -d --name openlog-php-test-redis --network "$NET" redis:7-alpine >/dev/null
  fi
  for _ in $(seq 1 60); do
    if docker exec openlog-php-test-mariadb healthcheck.sh --connect >/dev/null 2>&1 &&
       docker exec openlog-php-test-postgres pg_isready -U test >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "datastores not ready" >&2
}

run_one() {
  local tag=$1 image src log dockerfile=Dockerfile.test base="" flavor=""
  case "$tag" in
    *-asan) image="php:${tag%-asan}-cli"; dockerfile=Dockerfile.asan ;;
    *-xdebug|*-ddtrace|*-newrelic|*-jit|*-swoole) flavor=${tag##*-}; base=${tag%-*}; image="php:$base-cli" ;;
    *-frankenphp) image="dunglas/frankenphp:1-php${tag%-frankenphp}-bookworm" ;;
    *-*) image="php:$tag" ;;
    *) image="php:$tag-cli" ;;
  esac
  src="$WORK/src-$tag"
  log="$OUT/$tag.log"
  rsync -a --delete --delete-excluded --exclude build/out --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude modules \
    --exclude autom4te.cache "$EXT_DIR/" "$src/"
  if [ -n "$flavor" ]; then
    if ! docker build "${plat[@]}" -q -t "openlog-php-test:$base" --build-arg "PHP_IMAGE=$image" \
        --build-arg "REDIS_VERSION=$(redis_for "$base")" -f "$EXT_DIR/build/Dockerfile.test" "$EXT_DIR/build" >"$log" 2>&1 ||
       ! docker build "${plat[@]}" -q -t "openlog-php-test:$tag" --build-arg "BASE_IMAGE=openlog-php-test:$base" \
        --build-arg "FLAVOR=$flavor" -f "$EXT_DIR/build/Dockerfile.compat" "$EXT_DIR/build" >>"$log" 2>&1; then
      echo "$tag IMAGE-BUILD-FAILED" >"$OUT/$tag.summary"
      return 0
    fi
  elif ! docker build "${plat[@]}" -q -t "openlog-php-test:$tag" --build-arg "PHP_IMAGE=$image" \
      --build-arg "REDIS_VERSION=$(redis_for "$tag")" -f "$EXT_DIR/build/$dockerfile" "$EXT_DIR/build" >"$log" 2>&1; then
    echo "$tag IMAGE-BUILD-FAILED" >"$OUT/$tag.summary"
    return 0
  fi
  docker run --rm "${plat[@]}" --network "$NET" -v "$src":/src -w /src \
    -e OPENLOG_TEST_MYSQL=openlog-php-test-mariadb -e OPENLOG_TEST_PGSQL=openlog-php-test-postgres \
    -e OPENLOG_TEST_REDIS=openlog-php-test-redis \
    "openlog-php-test:$tag" sh -c '
      set -e
      mkdir -p /tmp/sanitizer
      if [ -s /etc/openlog-test-ini ]; then export OPENLOG_TEST_INI="$(cat /etc/openlog-test-ini)"; else unset OPENLOG_TEST_INI; fi
      if [ -n "${OPENLOG_EXT_CFLAGS:-}" ]; then export CFLAGS="$OPENLOG_EXT_CFLAGS" LDFLAGS="$OPENLOG_EXT_CFLAGS"; fi
      phpize >/dev/null && ./configure --enable-openlog >/dev/null
      if ! make -j"$(nproc)" >build.log 2>&1; then cat build.log; echo "BUILD FAILED"; exit 0; fi
      echo "--- compiler warnings:"; grep -E "warning:" build.log || echo "none"
      php -d extension=/src/modules/openlog.so -r "echo \"loaded: \", extension_loaded(\"openlog\") ? \"yes\" : \"no\", \" zts=\", PHP_ZTS, \" with: \", implode(\",\", array_intersect([\"xdebug\", \"ddtrace\", \"newrelic\", \"Zend OPcache\"], get_loaded_extensions())), \"\n\";"
      export OPENLOG_EXT_SO=/src/modules/openlog.so NO_INTERACTION=1 TEST_PHP_EXECUTABLE=$(command -v php)
      jobs=""; php -r "exit(PHP_VERSION_ID >= 70400 ? 0 : 1);" && jobs="-j4"
      timeout=""; [ -n "${OPENLOG_EXT_CFLAGS:-}" ] && timeout="--set-timeout 600"
      php "$(php-config --prefix)/lib/php/build/run-tests.php" -q $jobs $timeout --show-diff -p "$TEST_PHP_EXECUTABLE" tests || true
      if ls /tmp/sanitizer/* >/dev/null 2>&1; then echo "--- sanitizer reports"; cat /tmp/sanitizer/*; fi
    ' >>"$log" 2>&1 || true
  sed -i.bak -e 's/\x1b\[[0-9;]*m//g' "$log" && rm -f "$log.bak"
  local passed failed skipped reports
  passed=$(grep -E '^Tests passed' "$log" | head -1 | awk -F: '{print $2}' | awk '{print $1}')
  failed=$(grep -E '^Tests failed' "$log" | head -1 | awk -F: '{print $2}' | awk '{print $1}')
  skipped=$(grep -E '^Tests skipped' "$log" | head -1 | awk -F: '{print $2}' | awk '{print $1}')
  reports=$(grep -cE 'ERROR: (Address|Leak)Sanitizer|runtime error:' "$log" || true)
  if grep -q "BUILD FAILED" "$log"; then
    echo "$tag BUILD-FAILED" >"$OUT/$tag.summary"
  elif [ "${reports:-0}" -gt 0 ]; then
    echo "$tag passed=${passed:-?} failed=${failed:-?} skipped=${skipped:-?} SANITIZER-REPORTS=$reports" >"$OUT/$tag.summary"
  else
    echo "$tag passed=${passed:-?} failed=${failed:-?} skipped=${skipped:-?}" >"$OUT/$tag.summary"
  fi
  cat "$OUT/$tag.summary"
}

export -f run_one redis_for
export EXT_DIR WORK OUT NET PLATFORM
start_datastores
rm -f "$OUT"/*.summary
# shellcheck disable=SC2086
printf '%s\n' $VERSIONS $EXTRA | xargs -P "$PARALLEL" -I{} bash -c 'plat=(); [ -n "$PLATFORM" ] && plat=(--platform "$PLATFORM"); run_one "$@"' _ {}

echo
echo "== summary ($OUT)"
status=0
for t in $VERSIONS $EXTRA; do
  s=$(cat "$OUT/$t.summary" 2>/dev/null || echo "$t MISSING")
  echo "$s"
  case "$s" in *SANITIZER-REPORTS*) status=1 ;; *"failed=0"*) ;; *) status=1 ;; esac
done
if [ "$KEEP_DB" != 1 ]; then
  docker rm -f openlog-php-test-mariadb openlog-php-test-postgres openlog-php-test-redis >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
fi
exit $status
