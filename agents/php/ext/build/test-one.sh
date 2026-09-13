#!/usr/bin/env bash
# Builds openlog.so for one official PHP image and runs the phpt tests in it (development loop).
#   build/test-one.sh 8.3 [tests/020-pdo-sqlite.phpt ...]
# Without an image that has mysqli/pgsql/redis (build/run-matrix.sh builds those) the datastore tests skip.
set -euo pipefail
TAG=${1:-8.3}
shift || true
case "$TAG" in *-*) IMAGE=${IMAGE:-php:$TAG} ;; *) IMAGE=${IMAGE:-php:$TAG-cli} ;; esac
EXT_DIR=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-${TMPDIR:-/tmp}/openlog-ext-build}
mkdir -p "$WORK/test-$TAG"
rsync -a --delete --delete-excluded --exclude build/out --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude modules \
  --exclude autom4te.cache "$EXT_DIR/" "$WORK/test-$TAG/"
TESTS=${*:-tests}
docker run --rm ${DOCKER_ARGS:-} -v "$WORK/test-$TAG":/src -w /src "$IMAGE" sh -ec '
  if command -v apk >/dev/null; then apk add --no-cache $PHPIZE_DEPS >/dev/null; fi
  phpize >/dev/null && ./configure --enable-openlog >/dev/null && make -j"$(nproc)" >/dev/null 2>build.log || { cat build.log; exit 1; }
  grep -E "warning" build.log || true
  export OPENLOG_EXT_SO=/src/modules/openlog.so TEST_PHP_EXECUTABLE=$(command -v php) NO_INTERACTION=1
  jobs=""; php -r "exit(PHP_VERSION_ID >= 70400 ? 0 : 1);" && jobs="-j$(nproc)"
  php /usr/local/lib/php/build/run-tests.php -q $jobs --show-diff -p "$TEST_PHP_EXECUTABLE" '"$TESTS"' 2>&1 | grep -vE "^(PASS|SKIP) " | tail -${TAIL:-80}'
