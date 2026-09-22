#!/bin/sh
# openlog-ebpf-profiler postinstall (deb postinst / rpm %post).
#
# Deliberately much smaller than the infra agent's. This component does not update itself, has no
# versions/current symlink and joins no groups: there is one binary, one unit and one environment file.
#
# Arguments: deb "configure <old-version>" (old version empty on first install); rpm "1" (install) or
# "2" (upgrade).
set -e

UNIT=openlog-ebpf-profiler.service
ENV_FILE=/etc/openlog-ebpf-profiler/openlog-ebpf-profiler.env

first_install=0
case "$1" in
configure) [ -n "$2" ] || first_install=1 ;;
1) first_install=1 ;;
2) ;;
*) exit 0 ;; # abort-upgrade, abort-remove, …: leave everything as it is
esac

# A key that is still the placeholder is not a key. Starting without one would restart-loop, because the
# profiler refuses to run rather than sampling a machine and throwing every profile away.
has_license_key() {
	grep -Eq '^OPENLOG_LICENSE_KEY=[^[:space:]#]' "$ENV_FILE" 2>/dev/null &&
		! grep -Eq '^OPENLOG_LICENSE_KEY=olk_replace_me[[:space:]]*$' "$ENV_FILE" 2>/dev/null
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
				echo "openlog-ebpf-profiler: set OPENLOG_LICENSE_KEY and OPENLOG_ENDPOINT in $ENV_FILE, then run: systemctl start $UNIT"
			fi
		else
			systemctl try-restart "$UNIT" || true
		fi
	fi
	# Said on every install, because it is the part an operator is entitled to know without reading a
	# contract: this unit holds capabilities the infra agent deliberately does not.
	echo "openlog-ebpf-profiler: this service runs with CAP_BPF and CAP_PERFMON (kernel >= 5.8) to sample every process on this host"
else
	echo "openlog-ebpf-profiler: systemd not found; run /opt/openlog/ebpf-profiler/openlog-ebpf-profiler under your service manager"
fi
exit 0
