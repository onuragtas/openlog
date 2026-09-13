#!/bin/sh
# openlog-infra-agent postremove (deb postrm / rpm %postun).
#
# remove (deb "remove", rpm "0"): delete the `current` symlink, every installed version (including
#   versions installed by self-updates), the status files of the privileged pre-start step and staged
#   updates. Configuration and state are kept.
# purge (deb only): also delete configuration and state. The openlog-agent account is kept.
# Upgrades (deb "upgrade", rpm "1") change nothing.
set -e

ROOT=/opt/openlog/infra-agent

case "$1" in
remove | 0)
	rm -rf "$ROOT/current" "$ROOT/versions" "$ROOT"/.current.* "$ROOT/apply-status.json" "$ROOT/reconcile-status.json" \
		/var/lib/openlog-infra-agent/updates
	rmdir "$ROOT" /opt/openlog 2>/dev/null || true
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload >/dev/null 2>&1 || true
	fi
	;;
purge)
	rm -rf "$ROOT" /etc/openlog-infra-agent /var/lib/openlog-infra-agent
	rmdir /opt/openlog 2>/dev/null || true
	;;
esac
exit 0
