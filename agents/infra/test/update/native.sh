#!/bin/bash
# Self-update end-to-end scenario for the macOS LaunchDaemon (CI job "infra-agent (macOS)", D-104/D-113): builds
# agent versions FROM and TO trusting a fresh test key, signs darwin tar.gz releases, serves them together with a fake
# ingest (OTLP sink + scripted sync, e2e serve -scenario native), installs FROM with scripts/install.sh, lets the fake
# backend order an upgrade to TO and then a rollback to FROM, and checks the current link and the running service.
# Needs macOS, sudo without a password and Go. Changes the host: run it on a throwaway machine (CI runner).
#
#   agents/infra/test/update/native.sh            WORK=/tmp/x PORT=18474 TIMEOUT=480 FROM=0.9.0 TO=0.9.1
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
AGENT=$(cd "$HERE/../.." && pwd)
REPO=$(cd "$AGENT/../.." && pwd)
WORK=${WORK:-$(mktemp -d)}
PORT=${PORT:-18474}
TIMEOUT=${TIMEOUT:-480}
FROM=${FROM:-0.9.0}
TO=${TO:-0.9.1}
ARCH=$(uname -m | sed 's/x86_64/amd64/')
MOD=github.com/onuragtas/openlog/agents/infra/internal
BASE=http://127.0.0.1:$PORT/releases
ROOT=/opt/openlog/infra-agent
CONFIG=/etc/openlog-infra-agent/config.yaml
STATE=/var/lib/openlog-infra-agent
LABEL=org.openlog.infra-agent
LOG=$WORK/server.log
start=$(date +%s)

[ "$(uname -s)" = Darwin ] || { echo "native.sh runs on macOS"; exit 2; }
rm -rf "${WORK:?}/bin" "$WORK/releases" "$WORK/keys"
mkdir -p "$WORK/bin" "$WORK/releases"
cd "$AGENT"

say() { echo "== $(date -u +%H:%M:%S) $*"; }
diagnostics() {
	echo "---- server log"
	tail -n 40 "$LOG" 2>/dev/null || true
	echo "---- agent log (/var/log/openlog-infra-agent.log)"
	sudo tail -n 80 /var/log/openlog-infra-agent.log 2>/dev/null || true
	echo "---- install root"
	sudo ls -la "$ROOT" "$ROOT/versions" 2>/dev/null || true
	sudo cat "$ROOT/apply-status.json" "$ROOT/reconcile-status.json" "$STATE/update-state.json" 2>/dev/null || true
	echo "---- launchd"
	sudo launchctl print "system/$LABEL" 2>/dev/null | grep -E 'state|pid|last exit|runs' || true
}
fail() {
	echo "NATIVE SCENARIO FAILED: $*"
	diagnostics
	exit 1
}
server_pid=
cleanup() {
	sudo "$ROOT/current/openlog-infra-agent" -uninstall-service >/dev/null 2>&1 || true
	sudo rm -rf "$ROOT" /usr/local/bin/openlog-infra-agent /etc/newsyslog.d/openlog-infra-agent.conf /etc/openlog-infra-agent "$STATE" || true
	[ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
}
trap cleanup EXIT

say "test key, agents $FROM and $TO (darwin/$ARCH), signed releases"
go run ./test/update/e2e keygen -out "$WORK/keys"
PUB=$(tr -d '\n' <"$WORK/keys/key.pub")
for v in "$FROM" "$TO"; do
	CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X $MOD/version.Version=$v -X $MOD/version.Commit=e2e -X $MOD/release.trustedKeys=$PUB" \
		-o "$WORK/bin/$v/openlog-infra-agent" ./cmd/openlog-infra-agent
done
CGO_ENABLED=0 go build -o "$WORK/bin/e2e" ./test/update/e2e
E2E=("$WORK/bin/e2e" release -seed "$WORK/keys/key.seed" -os darwin -arch "$ARCH" -layout v -out "$WORK/releases" -base-url "$BASE")
"${E2E[@]}" -version "$FROM" -binary "$WORK/bin/$FROM/openlog-infra-agent" -floor 0.8.0
"${E2E[@]}" -version "$TO" -binary "$WORK/bin/$TO/openlog-infra-agent" -floor "$FROM"

say "fake ingest on 127.0.0.1:$PORT"
"$WORK/bin/e2e" serve -scenario native -os darwin -from "$FROM" -to "$TO" -arch "$ARCH" \
	-listen "127.0.0.1:$PORT" -releases "$WORK/releases" >"$LOG" 2>&1 &
server_pid=$!
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$BASE/v$FROM/manifest.json" && break; sleep 1; done

say "install.sh --version $FROM"
sudo sh "$REPO/scripts/install.sh" --base-url "$BASE" --version "$FROM" --license-key e2e-license \
	--endpoint "http://127.0.0.1:$PORT" --no-start
# Short collection interval: the update is confirmed by the first successful export.
printf '\ninterval: 10s\n' | sudo tee -a "$CONFIG" >/dev/null
[ -f "$ROOT/versions/$FROM/manifest.json" ] || fail "install.sh did not store the manifest of $FROM"
sudo launchctl bootstrap system "/Library/LaunchDaemons/$LABEL.plist"

wait_step() { # n
	until grep -q "STEP $1 PASSED" "$LOG"; do
		grep -q "SCENARIO FAILED" "$LOG" && fail "server: $(grep 'SCENARIO FAILED' "$LOG")"
		kill -0 "$server_pid" 2>/dev/null || fail "fake ingest exited"
		[ $(($(date +%s) - start)) -lt "$TIMEOUT" ] || fail "timeout (${TIMEOUT}s) waiting for step $1"
		sleep 3
	done
	grep "STEP $1 PASSED" "$LOG"
}
wait_step 1
wait_step 2
[ "$(readlink "$ROOT/current")" = "versions/$TO" ] || fail "current is not versions/$TO after the upgrade"
[ -f "$ROOT/versions/$TO/manifest.json" ] || fail "the self-update did not store the manifest of $TO"
[ -z "$(sudo find "$ROOT" ! -user root -print -quit)" ] || fail "install root not root-owned after the upgrade"
wait_step 3
grep -q "SCENARIO PASSED" "$LOG" || fail "server did not finish"
[ "$(readlink "$ROOT/current")" = "versions/$FROM" ] || fail "current is not versions/$FROM after the rollback"
sudo launchctl print "system/$LABEL" | grep -q 'state = running' || fail "LaunchDaemon not running after the rollback"
"$ROOT/current/openlog-infra-agent" -version | grep -F "$FROM" || fail "-version does not report $FROM"
say "result: PASSED after $(($(date +%s) - start))s"
diagnostics
