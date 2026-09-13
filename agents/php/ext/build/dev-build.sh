#!/usr/bin/env bash
# Quick compile of agents/php/ext for one PHP image (development loop).
#   build/dev-build.sh 8.3            # php:8.3-cli
#   build/dev-build.sh 7.4-zts        # php:7.4-zts
#   build/dev-build.sh 8.3-cli-alpine
# The source is copied to a scratch directory first (the repository may live on a slow network/cloud drive) and
# compiled inside the container; the resulting openlog.so is left in $OUT/<tag>/openlog.so.
set -euo pipefail
TAG=${1:-8.3}
case "$TAG" in *-*) IMAGE="php:$TAG" ;; *) IMAGE="php:$TAG-cli" ;; esac
EXT_DIR=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-${TMPDIR:-/tmp}/openlog-ext-build}
OUT=${OUT:-$WORK/out}
mkdir -p "$WORK/src-$TAG" "$OUT/$TAG"
rsync -a --delete --delete-excluded --exclude build/out --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude modules \
  --exclude autom4te.cache --exclude tests/output "$EXT_DIR/" "$WORK/src-$TAG/"
docker run --rm -v "$WORK/src-$TAG":/src -v "$OUT/$TAG":/out -w /src "$IMAGE" sh -ec '
  if command -v apk >/dev/null; then apk add --no-cache $PHPIZE_DEPS >/dev/null; fi
  phpize >/dev/null && ./configure --enable-openlog >/dev/null && make -j"$(nproc)" 2>&1 | grep -E "warning|error" || true
  test -f modules/openlog.so && cp modules/openlog.so /out/ && php -d extension=/out/openlog.so -m | grep -x openlog'
