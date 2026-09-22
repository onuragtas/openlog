#!/bin/sh
# openlog-ebpf-profiler preremove (deb prerm / rpm %preun): stop and disable the service when the
# package is removed. Upgrades (deb "upgrade", rpm "1") keep it running; postinstall restarts it.
#
# The cases are deb's prerm arguments, not postrm's: "purge" never reaches this script, and
# "deconfigure" does — missing it would leave the service running while its package is being taken
# apart.
set -e

UNIT=openlog-ebpf-profiler.service

case "$1" in
remove | deconfigure | 0) ;;
*) exit 0 ;;
esac

if command -v systemctl >/dev/null 2>&1; then
	if [ -d /run/systemd/system ]; then
		systemctl stop "$UNIT" >/dev/null 2>&1 || true
	fi
	systemctl disable "$UNIT" >/dev/null 2>&1 || true
fi
exit 0
