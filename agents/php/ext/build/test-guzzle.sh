#!/usr/bin/env bash
# Guzzle 7 with openlog.so: installs guzzlehttp/guzzle with the composer image into $WORK/guzzle and runs
# tests/043-guzzle-async.phpt (async promises and Pool on CurlMultiHandler, sync CurlHandler) in the test image.
#   build/test-guzzle.sh [8.3]
set -euo pipefail
TAG=${1:-8.3}
EXT_DIR=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-${TMPDIR:-/tmp}/openlog-ext-build}
mkdir -p "$WORK/guzzle"
docker run --rm --user "$(id -u):$(id -g)" -e COMPOSER_HOME=/tmp/composer -v "$WORK/guzzle":/app -w /app composer:2 \
  require --no-interaction --no-progress --ignore-platform-reqs "guzzlehttp/guzzle:^7" >/dev/null
IMAGE=${IMAGE:-openlog-php-test:$TAG} DOCKER_ARGS="-v $WORK/guzzle:/guzzle:ro -e OPENLOG_TEST_GUZZLE=/guzzle/vendor/autoload.php" \
  WORK="$WORK" bash "$EXT_DIR/build/test-one.sh" "$TAG" tests/043-guzzle-async.phpt
