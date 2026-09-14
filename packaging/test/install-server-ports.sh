#!/bin/sh
# Unit check of the OPENLOG_*_PORT helpers of scripts/install-server.sh (no Docker, no network):
#
#   packaging/test/install-server-ports.sh
#
# Values in .env are PORT, HOST:PORT or [IPv6]:PORT (compose publishes "${OPENLOG_ADMIN_PORT:-9464}:9464"). Regression:
# OPENLOG_ADMIN_PORT=0.0.0.0:9464 made the installer probe http://127.0.0.1:0.0.0.0:9464/readyz and fail.
set -eu

script=$(cd "$(dirname "$0")/../.." && pwd)/scripts/install-server.sh
# The helpers are top-level functions ending with a "}" line; load only them.
helpers=$(awk '/^(port_parts|local_host|is_loopback)\(\) \{/,/^}/' "$script")
[ -n "$helpers" ] || {
	echo "helpers not found in $script" >&2
	exit 1
}
eval "$helpers"

fail=0
expect() { # description got want
	if [ "$2" != "$3" ]; then
		echo "FAIL $1: got '$2', want '$3'" >&2
		fail=1
	fi
}

expect "empty uses the default" "$(port_parts "" 9464)" "- 9464"
expect "port" "$(port_parts 8080 9464)" "- 8080"
expect "all interfaces" "$(port_parts 0.0.0.0:9464 1)" "0.0.0.0 9464"
expect "loopback" "$(port_parts 127.0.0.1:9464 1)" "127.0.0.1 9464"
expect "docker bridge" "$(port_parts 172.17.0.1:8080 1)" "172.17.0.1 8080"
expect "ipv6" "$(port_parts '[::1]:9464' 1)" "::1 9464"
for bad in "abc" "1:2:3" "0.0.0.0:" "70000" "0" "host name:80" "[::1]"; do
	if port_parts "$bad" 1 >/dev/null; then
		echo "FAIL '$bad' must be rejected" >&2
		fail=1
	fi
done

expect "check host for all interfaces" "$(local_host "")" "127.0.0.1"
expect "check host for 0.0.0.0" "$(local_host 0.0.0.0)" "127.0.0.1"
expect "check host for a bound address" "$(local_host 172.17.0.1)" "172.17.0.1"
expect "check host for ipv6" "$(local_host ::1)" "[::1]"

is_loopback 127.0.0.1 || {
	echo "FAIL 127.0.0.1 is loopback" >&2
	fail=1
}
is_loopback ::1 || {
	echo "FAIL ::1 is loopback" >&2
	fail=1
}
for h in "" 0.0.0.0 172.17.0.1; do
	if is_loopback "$h"; then
		echo "FAIL '$h' is not loopback" >&2
		fail=1
	fi
done

[ "$fail" = 0 ] && echo "OK: install-server.sh port helpers"
exit "$fail"
