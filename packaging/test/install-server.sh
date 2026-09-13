#!/bin/sh
# Runs scripts/install-server.sh end to end inside a privileged docker:dind container (its own Docker
# daemon: no port or project collisions with stacks on the host) against a published release.
#
#   packaging/test/install-server.sh [VERSION]      (default 0.1.6; needs internet access)
#
# Checks: fresh install of VERSION (updater notify), all services healthy, owner login, .env mode 0600;
# idempotent re-run (secrets unchanged); re-run without --version upgrades to the latest stable release
# with the updater in auto mode (secrets unchanged, login works); an explicit older --version is refused.
# The container and its volumes (images, data) are removed afterwards.
#
# Checks run inside the container are single-quoted on purpose (they expand there).
# shellcheck disable=SC2016
set -eu

version=${1:-0.1.6}
repo=$(cd "$(dirname "$0")/../.." && pwd)
name=openlog-installtest-$$
DIND_IMAGE=${DIND_IMAGE:-docker:28-dind}
password=$(od -An -tx1 -N12 /dev/urandom | tr -d ' \n')
email=admin@example.com

cleanup() { docker rm -fv "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

say() { printf '\n=== %s\n' "$*"; }
in_dind() { docker exec "$name" sh -c "$1"; }
install() { docker exec "$name" sh /repo/scripts/install-server.sh "$@"; }

# Shell helpers run inside the container.
# shellcheck disable=SC2016
helpers='
D=/opt/openlog-server
dc() { docker compose -p openlog -f $D/docker-compose.yml --env-file $D/.env "$@"; }
secrets() { grep -E "^(OPENLOG_SECRETS_KEY|OPENLOG_POSTGRES_PASSWORD|OPENLOG_CLICKHOUSE_PASSWORD|OPENLOG_BOOTSTRAP_LICENSE_KEY|OPENLOG_BOOTSTRAP_OWNER_PASSWORD)=" $D/.env | sha256sum | cut -c1-16; }
running_version() { wget -qO- http://127.0.0.1:9464/readyz | sed -n "s/.*\"version\":\"\([^\"]*\)\".*/\1/p"; }
check_healthy() {
	for s in postgres kafka clickhouse openlog; do
		st=$(dc ps --format "{{.State}} {{.Health}}" "$s")
		[ "$st" = "running healthy" ] || { echo "service $s: $st"; dc ps -a; exit 1; }
	done
	[ "$(dc ps -a --format "{{.State}} {{.ExitCode}}" bootstrap)" = "exited 0" ] || { echo "bootstrap did not exit 0"; exit 1; }
	[ "$(stat -c %a $D/.env)" = 600 ] || { echo ".env mode is not 0600"; exit 1; }
	echo "services healthy, .env 0600"
}
check_login() {
	wget -qO /dev/null --header "Content-Type: application/json" \
		--post-data "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" http://127.0.0.1:8080/api/v1/auth/login ||
		{ echo "login failed"; exit 1; }
	echo "login ok"
}
'
check() { docker exec -e EMAIL="$email" -e PASSWORD="$password" -e VERSION="$version" "$name" sh -c "$helpers $1"; }

say "starting $DIND_IMAGE"
docker run -d --privileged --name "$name" -v "$repo:/repo:ro" "$DIND_IMAGE" >/dev/null
i=0
until in_dind 'docker info >/dev/null 2>&1'; do
	i=$((i + 1))
	[ $i -lt 60 ] || {
		docker logs "$name" | tail -20
		exit 1
	}
	sleep 1
done

say "fresh install of $version"
install --version "$version" --email "$email" --password "$password" --updater notify
check 'check_healthy; check_login
[ "$(running_version)" = "$VERSION" ] || { echo "running $(running_version), want $VERSION"; exit 1; }
grep -q "^OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:$VERSION\$" $D/.env
grep -q "^OPENLOG_UPDATER_MODE=notify$" $D/.env
[ "$(dc ps --format "{{.State}}" openlog-updater)" = running ] || { echo "updater not running"; exit 1; }
secrets > /tmp/secrets.1'

say "re-run is idempotent"
install --version "$version" --email "$email" --password "$password" --updater notify
check 'check_healthy; check_login
secrets > /tmp/secrets.2; cmp /tmp/secrets.1 /tmp/secrets.2 && echo "secrets unchanged"
[ "$(running_version)" = "$VERSION" ]'

say "re-run without --version upgrades to the latest stable release (updater auto)"
install --updater auto
check 'check_healthy; check_login
secrets > /tmp/secrets.3; cmp /tmp/secrets.1 /tmp/secrets.3 && echo "secrets unchanged"
grep -q "^OPENLOG_UPDATER_MODE=auto$" $D/.env
echo "running $(running_version)"; running_version > /tmp/latest'

latest=$(docker exec "$name" cat /tmp/latest)
if [ "$latest" != "$version" ]; then
	say "explicit downgrade to $version is refused"
	err=$(install --version "$version" 2>&1) && {
		echo "downgrade was not refused" >&2
		exit 1
	}
	case $err in *"refusing to downgrade"*) echo "refused" ;; *)
		echo "$err" >&2
		exit 1
		;;
	esac
fi

say "OK: install-server.sh $version -> $latest"
