#!/bin/sh
# Runs scripts/install.sh in debian:12, ubuntu:24.04, rockylinux:9 (packages) and alpine (tarball)
# against a local HTTP server serving DIST (built with
# `make release-local RELEASE_BASE_URL=http://host.docker.internal:18080`).
#
#   packaging/test/install.sh dist 0.9.0 [0.10.0-beta.1]
#
# Checks: rejection of a tampered artifact, latest stable install, idempotent re-run, beta channel
# upgrade, keeping a newer version, explicit --version downgrade. Containers are removed afterwards.
set -eu

[ $# -ge 2 ] || {
	echo "usage: $0 DIST STABLE_VERSION [BETA_VERSION]" >&2
	exit 2
}
dist=$(cd "$1" && pwd)
stable=$2
beta=${3:-}
port=${RELEASE_SERVE_PORT:-18080}
base=http://host.docker.internal:$port
repo=$(cd "$(dirname "$0")/../.." && pwd)

server_pid=
tamper="$dist/.tamper"
cleanup() {
	[ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
	rm -rf "$tamper"
}
trap cleanup EXIT INT TERM

# A mirror whose artifacts do not match the (genuine) manifest.
mkdir -p "$tamper/v$stable"
cp "$dist/v$stable/manifest.json" "$tamper/v$stable/"
for f in "$dist/v$stable"/openlog-infra-agent_*; do
	{
		cat "$f"
		printf x
	} >"$tamper/v$stable/${f##*/}"
done

python3 -m http.server --bind 0.0.0.0 --directory "$dist" "$port" >/dev/null 2>&1 &
server_pid=$!
sleep 1

# Shell run inside the container: $1 = expected method.
# shellcheck disable=SC2016
checks='
set -eu
method=$1
say() { printf "\n--- %s\n" "$*"; }
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
	say "installing curl"
	apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates >/dev/null 2>&1
fi
I="sh /repo/scripts/install.sh --base-url $BASE"
current() { readlink /opt/openlog/infra-agent/current; }
in_docker() { getent group docker | cut -d: -f4 | tr "," "\n" | grep -qx openlog-agent; }
getent group docker >/dev/null || groupadd --system docker 2>/dev/null || addgroup -S docker

say "tampered artifact is rejected"
if sh /repo/scripts/install.sh --base-url "$BASE/.tamper" --version "$STABLE" --license-key k 2>/tmp/tamper.log; then
	cat /tmp/tamper.log; echo "FAIL: tampered artifact accepted"; exit 1
fi
tail -n 1 /tmp/tamper.log
grep -Eq "(size|sha256) .* does not match" /tmp/tamper.log
test ! -e /opt/openlog/infra-agent/current

say "install latest stable"
$I --license-key test-license-key --endpoint https://ingest.example.com:4318
test "$(current)" = "versions/$STABLE"
grep -E "^(license_key|endpoint):" /etc/openlog-infra-agent/config.yaml
grep -q "^license_key: \"test-license-key\"" /etc/openlog-infra-agent/config.yaml
/usr/bin/openlog-infra-agent -version | head -n 1
case $method in
deb) dpkg -s openlog-infra-agent | grep -E "^(Status|Version):" ;;
rpm) rpm -q openlog-infra-agent ;;
tarball) ls -l /etc/systemd/system/openlog-infra-agent.service 2>/dev/null || echo "no /etc/systemd/system (no systemd): unit not installed" ;;
esac
stat -c "%n %U:%G %a" /etc/openlog-infra-agent/config.yaml /opt/openlog/infra-agent/current
test -s "/opt/openlog/infra-agent/versions/$STABLE/manifest.json" && test -s "/opt/openlog/infra-agent/versions/$STABLE/manifest.json.sig" && echo "signed manifest in versions/$STABLE"
test "$(stat -c %U /opt/openlog/infra-agent/versions)" = openlog-agent

say "re-run (idempotent), new endpoint"
$I --endpoint https://ingest2.example.com:4318 2>&1 | tee /tmp/rerun.log
grep -q "already installed" /tmp/rerun.log
grep -q "^endpoint: \"https://ingest2.example.com:4318\"" /etc/openlog-infra-agent/config.yaml
grep -q "^license_key: \"test-license-key\"" /etc/openlog-infra-agent/config.yaml

say "docker access (default on, --no-docker-access persists)"
in_docker
test "$(getent group docker | cut -d: -f4)" = openlog-agent
echo "openlog-agent is in the docker group (once)"
gpasswd -d openlog-agent docker 2>/dev/null || delgroup openlog-agent docker
$I --no-docker-access
test -e /etc/openlog-infra-agent/no-docker-access
if in_docker; then echo "FAIL: --no-docker-access ignored"; exit 1; fi
$I
if in_docker; then echo "FAIL: opt-out not kept on re-run"; exit 1; fi
echo "opt-out kept on re-run"

if [ -n "$BETA" ]; then
	say "beta channel upgrade"
	$I --channel beta
	test "$(current)" = "versions/$BETA"
	/usr/bin/openlog-infra-agent -version | grep -F "$BETA"
	say "stable re-run keeps the newer beta"
	$I 2>&1 | tee /tmp/keep.log
	grep -q "newer than" /tmp/keep.log
	test "$(current)" = "versions/$BETA"
	say "explicit downgrade"
	$I --version "v$STABLE"
	test "$(current)" = "versions/$STABLE"
	/usr/bin/openlog-infra-agent -version | head -n 1
fi
echo "PASS $method"
'

run() { # image method
	echo "=== install.sh in $1 (expect $2)"
	docker run --rm --name "openlog-installtest-$2-$$" --add-host host.docker.internal:host-gateway \
		-v "$repo/scripts:/repo/scripts:ro" -e BASE="$base" -e STABLE="$stable" -e BETA="$beta" \
		"$1" sh -c "$checks" sh "$2"
}

run "${DEBIAN_IMAGE:-debian:12}" deb
run "${UBUNTU_IMAGE:-ubuntu:24.04}" deb
run "${ROCKY_IMAGE:-rockylinux:9}" rpm
run "${ALPINE_IMAGE:-alpine:3.22}" tarball
echo "all install.sh tests passed"
