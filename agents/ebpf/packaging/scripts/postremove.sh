#!/bin/sh
# openlog-ebpf-profiler postremove (deb postrm / rpm %postun).
#
# remove (deb "remove", rpm "0"): delete the installed binary and its directory. The configuration is
#   kept, because it holds the license key and endpoint an operator would have to type again.
# purge (deb only): also delete the configuration.
# Upgrades (deb "upgrade", rpm "1") change nothing.
#
# The openlog-ebpf account is kept either way: removing it would orphan the ownership of anything it
# still owns, and a system account costs nothing. The infra agent's packages take the same view.
set -e

ROOT=/opt/openlog/ebpf-profiler

case "$1" in
remove | 0)
	rm -rf "$ROOT"
	rmdir /opt/openlog 2>/dev/null || true
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload >/dev/null 2>&1 || true
	fi
	;;
purge)
	rm -rf "$ROOT" /etc/openlog-ebpf-profiler
	rmdir /opt/openlog 2>/dev/null || true
	;;
esac
exit 0
