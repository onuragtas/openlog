#!/bin/sh
# openlog-infra-agent preinstall (deb preinst / rpm %pre): create the service account before the
# package files, which are owned by it, are unpacked.
set -e

USER_NAME=openlog-agent
STATE_DIR=/var/lib/openlog-infra-agent

nologin=/usr/sbin/nologin
[ -x "$nologin" ] || nologin=/sbin/nologin
[ -x "$nologin" ] || nologin=/bin/false

if ! getent group "$USER_NAME" >/dev/null 2>&1; then
	if command -v groupadd >/dev/null 2>&1; then
		groupadd --system "$USER_NAME"
	else
		addgroup --system "$USER_NAME"
	fi
fi
if ! getent passwd "$USER_NAME" >/dev/null 2>&1; then
	if command -v useradd >/dev/null 2>&1; then
		useradd --system --gid "$USER_NAME" --home-dir "$STATE_DIR" --no-create-home \
			--shell "$nologin" --comment "openlog infrastructure agent" "$USER_NAME"
	else
		adduser --system --ingroup "$USER_NAME" --home "$STATE_DIR" --no-create-home \
			--shell "$nologin" "$USER_NAME"
	fi
fi
exit 0
