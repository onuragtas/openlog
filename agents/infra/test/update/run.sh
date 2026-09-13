#!/bin/bash
# Self-update end-to-end scenario: builds agent 0.9.0, 0.9.1, a broken 0.9.2 and helper releases
# signed with a fresh test key, then runs scenario.sh in a Debian 12 container.
#
#   WORK=/tmp/openlog-update-e2e ./test/update/run.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
AGENT=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-$(mktemp -d)}
ARCH=${ARCH:-$(docker version -f '{{.Server.Arch}}')}
CONTAINER=openlog-infra-update-e2e
BASE=http://127.0.0.1:8080/releases
MOD=github.com/onuragtas/openlog/agents/infra/internal

rm -rf "$WORK/bin" "$WORK/releases" "$WORK/keys" "$WORK/out"
mkdir -p "$WORK/bin" "$WORK/releases" "$WORK/out"
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
build 0.9.2 openlog_e2e_broken
CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o "$WORK/bin/e2e" ./test/update/e2e

echo "== releases"
E2E="go run ./test/update/e2e release -seed $WORK/keys/key.seed -arch $ARCH -out $WORK/releases -base-url $BASE"
$E2E -version 0.9.0 -binary "$WORK/bin/0.9.0/openlog-infra-agent" -floor 0.8.0
$E2E -version 0.9.1 -binary "$WORK/bin/0.9.1/openlog-infra-agent" -floor 0.9.0 -min-upgrade-from 0.9.0
$E2E -version 0.9.2 -binary "$WORK/bin/0.9.2/openlog-infra-agent" -floor 0.9.1 -min-upgrade-from 0.9.0
# Never installed: rejected before (0.9.3 signature, 0.8.0 floor) or while (0.9.4 sha256) downloading.
$E2E -version 0.9.3 -binary "$WORK/bin/0.9.1/openlog-infra-agent" -floor 0.9.0
$E2E -version 0.9.4 -binary "$WORK/bin/0.9.1/openlog-infra-agent" -floor 0.9.0 -corrupt
$E2E -version 0.8.0 -binary "$WORK/bin/0.9.0/openlog-infra-agent"

cp "$HERE/scenario.sh" "$WORK/scenario.sh"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
echo "== container scenario"
set +e
docker run --rm --name "$CONTAINER" -e OPENLOG_AGENT_CONTAINER=0 -v "$WORK":/work debian:12 bash /work/scenario.sh
code=$?
set -e
echo "== logs: $WORK/out/server.log $WORK/out/agent.log"
exit $code
