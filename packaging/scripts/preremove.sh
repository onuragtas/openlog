#!/bin/sh
# openlog-infra-agent preremove (deb prerm / rpm %preun): stop and disable the service when the
# package is removed. Upgrades (deb "upgrade", rpm "1") keep it running; postinstall restarts it.
set -e

UNIT=openlog-infra-agent.service

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
