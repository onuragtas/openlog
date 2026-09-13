#!/bin/sh
# openlog-infra-agent postinstall (deb postinst / rpm %post).
#
# Switches /opt/openlog/infra-agent/current to the packaged version atomically, unless the agent
# already runs a newer version (it may have updated itself past the package), then reconciles the
# installation (`openlog-infra-agent -reconcile`: systemd unit, account, docker group, ownership; the
# same step the agent's privileged pre-start step runs after every self-update) and enables and
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

# trusted_dir DIR: DIR and everything below it are owned by root and not writable by group or others.
# Root executes the current binary (ExecStartPre=+), so a version an older agent wrote itself is not kept.
trusted_dir() {
	[ -d "$1" ] && [ ! -L "$1" ] || return 1
	[ -z "$(find "$1" \( ! -user 0 -o -perm -0020 -o -perm -0002 -o -type l \) -print 2>/dev/null | head -n 1)" ]
}

# The install root is root-owned: only the privileged pre-start step and the packages install releases.
install -d -m 0755 -o root -g root "$ROOT" "$ROOT/versions"
install -d -m 0750 -o "$USER_NAME" -g "$USER_NAME" "$STATE_DIR"

switch=1
if [ -L "$ROOT/current" ]; then
	running=$(readlink "$ROOT/current")
	running=${running%/}
	running=${running##*/}
	if [ "$running" = "$VERSION" ]; then
		switch=0
	elif [ -x "$ROOT/current/openlog-infra-agent" ] && [ "$(semver_cmp "$running" "$VERSION")" = 1 ]; then
		if trusted_dir "$ROOT/versions/$running"; then
			switch=0
			echo "openlog-infra-agent: the agent runs $running, newer than package $VERSION; keeping it"
		else
			echo "openlog-infra-agent: $running is writable by $USER_NAME (installed by an older self-update) and cannot be trusted; switching to $VERSION, the agent updates itself again"
		fi
	fi
fi
if [ "$switch" = 1 ]; then
	tmp="$ROOT/.current.$$"
	rm -f "$tmp"
	ln -s "versions/$VERSION" "$tmp"
	chown -h root:root "$tmp" 2>/dev/null || true
	mv -Tf "$tmp" "$ROOT/current"
	echo "openlog-infra-agent: $ROOT/current -> versions/$VERSION"
fi

has_license_key() {
	[ -n "${OPENLOG_LICENSE_KEY:-}" ] && return 0
	grep -Eq '^license_key:[[:space:]]*"?[^"[:space:]#]' "$CONFIG" 2>/dev/null
}

# Reconcile with the release that runs from now on (the newest logic when the agent is newer than the package).
# Docker access opt-out (also respected on upgrades and self-updates): OPENLOG_AGENT_DOCKER_ACCESS=0 (recorded in
# the file) or the file /etc/openlog-infra-agent/no-docker-access.
bin=$ROOT/current/openlog-infra-agent
"$bin" -help 2>&1 | grep -q -- -reconcile || bin=$ROOT/versions/$VERSION/openlog-infra-agent
restart_needed=0
out=$("$bin" -reconcile -reconcile-context package -config "$CONFIG") || echo "openlog-infra-agent: reconcile reported errors (see above)"
case $out in *restart-required*) restart_needed=1 ;; esac

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
		elif [ "$switch" = 1 ] || [ "$restart_needed" = 1 ]; then
			# One restart for a version switch, a new unit and new supplementary groups (applied at process start).
			systemctl try-restart "$UNIT" || true
		fi
	fi
else
	echo "openlog-infra-agent: systemd not found; run $ROOT/current/openlog-infra-agent -config $CONFIG under your service manager"
fi
exit 0
