#!/bin/bash
# Self-update end-to-end scenario under systemd: builds agent 0.9.0, 0.9.1, 0.9.2 (different embedded
# unit), a broken 0.9.3 and helper releases signed with a fresh test key, then runs scenario.sh in a
# Debian 12 container with systemd as PID 1 (privileged, like a host).
#
#   WORK=/tmp/openlog-update-e2e ./test/update/run.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
AGENT=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-$(mktemp -d)}
ARCH=${ARCH:-$(docker version -f '{{.Server.Arch}}')}
CONTAINER=openlog-infra-update-e2e
IMAGE=openlog-infra-update-e2e-systemd:local
BASE=http://127.0.0.1:8080/releases
MOD=github.com/onuragtas/openlog/agents/infra/internal

rm -rf "${WORK:?}/bin" "$WORK/releases" "$WORK/keys" "$WORK/out"
mkdir -p "$WORK/bin" "$WORK/releases" "$WORK/out/gates"
cd "$AGENT"

echo "== test key"
go run ./test/update/e2e keygen -out "$WORK/keys"
PUB=$(tr -d '\n' <"$WORK/keys/key.pub")

build() { # version [tags]
  local v=$1 tags=${2:-}
  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath ${tags:+-tags $tags} \
    -ldflags "-s -w -X $MOD/version.Version=$v -X $MOD/version.Commit=e2e -X $MOD/version.Date=2026-09-13T00:00:00Z -X $MOD/release.trustedKeys=$PUB" \
    -o "$WORK/bin/$v/openlog-infra-agent" ./cmd/openlog-infra-agent
}
echo "== build agents (linux/$ARCH)"
build 0.9.0
build 0.9.1
build 0.9.2 openlog_e2e_unit
build 0.9.3 openlog_e2e_broken
CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o "$WORK/bin/e2e" ./test/update/e2e

echo "== releases"
E2E="go run ./test/update/e2e release -seed $WORK/keys/key.seed -arch $ARCH -out $WORK/releases -base-url $BASE"
$E2E -version 0.9.0 -binary "$WORK/bin/0.9.0/openlog-infra-agent" -floor 0.8.0
$E2E -version 0.9.1 -binary "$WORK/bin/0.9.1/openlog-infra-agent" -floor 0.9.0 -min-upgrade-from 0.9.0
$E2E -version 0.9.2 -binary "$WORK/bin/0.9.2/openlog-infra-agent" -floor 0.9.1 -min-upgrade-from 0.9.0
$E2E -version 0.9.3 -binary "$WORK/bin/0.9.3/openlog-infra-agent" -floor 0.9.2 -min-upgrade-from 0.9.0
# Never installed: rejected before (0.9.4 signature, 0.8.0 floor) or while (0.9.5 sha256) downloading.
$E2E -version 0.9.4 -binary "$WORK/bin/0.9.1/openlog-infra-agent" -floor 0.9.0
$E2E -version 0.9.5 -binary "$WORK/bin/0.9.1/openlog-infra-agent" -floor 0.9.0 -corrupt
$E2E -version 0.8.0 -binary "$WORK/bin/0.9.0/openlog-infra-agent"

# PHP agent fleet installation: only the module Debian 12's php8.2 loads (PHP 8.2 NTS glibc) is built here; release.yml
# builds all 36. The tarball is cached in $PHP_WORK; the build images pulled for it are removed again.
PHP_WORK=${PHP_WORK:-$WORK/php}
PHP_TGZ="$PHP_WORK/openlog-php-agent_0.0.0-e2e_linux_$ARCH.tar.gz"
if [ ! -s "$PHP_TGZ" ]; then
  echo "== PHP agent module (8.2-nts-glibc)"
  pulled=""
  for img in php:8.2-cli almalinux:8; do docker image inspect "$img" >/dev/null 2>&1 || pulled="$pulled $img"; done
  TARGETS=8.2-nts-glibc PACKAGES="" PARALLEL=1 "$AGENT/../php/packaging/build-artifacts.sh" 0.0.0-e2e "$PHP_WORK"
  docker rmi -f openlog-php-build:8.2-nts >/dev/null 2>&1 || true
  # shellcheck disable=SC2086
  [ -z "$pulled" ] || docker rmi -f $pulled >/dev/null 2>&1 || true
fi
PHP_E2E="go run ./test/update/e2e php-release -seed $WORK/keys/key.seed -arch $ARCH -out $WORK/releases -base-url $BASE -from $PHP_TGZ"
$PHP_E2E -version 0.9.6
$PHP_E2E -version 0.9.7 -broken

cp "$HERE/scenario.sh" "$HERE/legacy-unit.service" "$WORK/"

echo "== systemd image"
docker build -q -t "$IMAGE" -f "$HERE/systemd.Dockerfile" "$HERE" >/dev/null

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
echo "== container scenario"
docker run -d --name "$CONTAINER" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --tmpfs /run --tmpfs /run/lock -v "$WORK":/work "$IMAGE" >/dev/null
docker exec "$CONTAINER" systemctl is-system-running --wait >/dev/null 2>&1 || true
set +e
docker exec "$CONTAINER" bash /work/scenario.sh
code=$?
set -e
docker exec "$CONTAINER" journalctl -u openlog-infra-agent --no-pager -o short-precise >"$WORK/out/agent.log" 2>&1 || true
echo "== logs: $WORK/out/server.log $WORK/out/agent.log"
exit $code
