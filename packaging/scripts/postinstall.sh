#!/bin/sh
# openlog-infra-agent postinstall (deb postinst / rpm %post).
#
# Switches /opt/openlog/infra-agent/current to the packaged version atomically, unless the agent
# already runs a newer version (it may have updated itself past the package), then enables and
# (re)starts the systemd service. @VERSION@ is replaced by `make release-packages`.
#
# Arguments: deb "configure <old-version>" (old version empty on first install); rpm "1" (install)
# or "2" (upgrade).
set -e

VERSION='@VERSION@'
ROOT=/opt/openlog/infra-agent
USER_NAME=openlog-agent
UNIT=openlog-infra-agent.service
CONFIG=/etc/openlog-infra-agent/config.yaml
STATE_DIR=/var/lib/openlog-infra-agent

first_install=0
case "$1" in
configure) [ -n "$2" ] || first_install=1 ;;
1) first_install=1 ;;
2) ;;
*) exit 0 ;; # abort-upgrade, abort-remove, …: leave everything as it is
esac

# semver_cmp A B prints -1, 0 or 1 following SemVer 2.0 precedence (build metadata ignored).
semver_cmp() {
	awk -v a="$1" -v b="$2" '
	function isnum(s) { return s ~ /^[0-9]+$/ }
	function cmpid(x, y) {
		if (isnum(x) && isnum(y)) {
			sub(/^0+/, "", x); sub(/^0+/, "", y)
			if (length(x) != length(y)) return length(x) < length(y) ? -1 : 1
		} else if (isnum(x)) return -1
		else if (isnum(y)) return 1
		if (x "" == y "") return 0
		return (x "" < y "") ? -1 : 1
	}
	BEGIN {
		sub(/^v/, "", a); sub(/^v/, "", b); sub(/\+.*/, "", a); sub(/\+.*/, "", b)
		pa = ""; pb = ""
		i = index(a, "-"); if (i) { pa = substr(a, i + 1); a = substr(a, 1, i - 1) }
		i = index(b, "-"); if (i) { pb = substr(b, i + 1); b = substr(b, 1, i - 1) }
		split(a, A, "."); split(b, B, ".")
		for (k = 1; k <= 3; k++) { c = cmpid(A[k], B[k]); if (c) { print c; exit } }
		if (pa == "" && pb == "") { print 0; exit }
		if (pa == "") { print 1; exit }
		if (pb == "") { print -1; exit }
		na = split(pa, PA, "."); nb = split(pb, PB, ".")
		for (k = 1; k <= na && k <= nb; k++) { c = cmpid(PA[k], PB[k]); if (c) { print c; exit } }
		print (na < nb) ? -1 : ((na > nb) ? 1 : 0)
	}'
}

install -d -m 0755 -o "$USER_NAME" -g "$USER_NAME" "$ROOT" "$ROOT/versions"
install -d -m 0750 -o "$USER_NAME" -g "$USER_NAME" "$STATE_DIR"

switch=1
if [ -L "$ROOT/current" ]; then
	running=$(readlink "$ROOT/current")
	running=${running%/}
	running=${running##*/}
	if [ -x "$ROOT/current/openlog-infra-agent" ] && [ "$(semver_cmp "$running" "$VERSION")" = 1 ]; then
		switch=0
		echo "openlog-infra-agent: the agent runs $running, newer than package $VERSION; keeping it"
	elif [ "$running" = "$VERSION" ]; then
		switch=0
	fi
fi
if [ "$switch" = 1 ]; then
	tmp="$ROOT/.current.$$"
	rm -f "$tmp"
	ln -s "versions/$VERSION" "$tmp"
	chown -h "$USER_NAME:$USER_NAME" "$tmp" 2>/dev/null || true
	mv -Tf "$tmp" "$ROOT/current"
	echo "openlog-infra-agent: $ROOT/current -> versions/$VERSION"
fi

if [ "$first_install" = 1 ] && [ -f "$CONFIG" ]; then
	chgrp "$USER_NAME" "$CONFIG" 2>/dev/null || true
	chmod 0640 "$CONFIG"
fi

has_license_key() {
	[ -n "${OPENLOG_LICENSE_KEY:-}" ] && return 0
	grep -Eq '^license_key:[[:space:]]*"?[^"[:space:]#]' "$CONFIG" 2>/dev/null
}

if command -v systemctl >/dev/null 2>&1; then
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload >/dev/null 2>&1 || true
	fi
	if [ "$first_install" = 1 ]; then
		systemctl enable "$UNIT" >/dev/null 2>&1 || true
	fi
	if [ -d /run/systemd/system ]; then
		if [ "$first_install" = 1 ]; then
			if has_license_key; then
				systemctl start "$UNIT" || true
			else
				echo "openlog-infra-agent: set license_key and endpoint in $CONFIG, then run: systemctl start $UNIT"
			fi
		elif [ "$switch" = 1 ]; then
			systemctl try-restart "$UNIT" || true
		fi
	fi
else
	echo "openlog-infra-agent: systemd not found; run $ROOT/current/openlog-infra-agent -config $CONFIG under your service manager"
fi
exit 0
