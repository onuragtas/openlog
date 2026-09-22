#!/bin/sh
# openlog-ebpf-profiler preinstall (deb preinst / rpm %pre): create the service account before the
# package files are unpacked, because the configuration file is owned by its group.
#
# Its own account, not the infra agent's. The two components hold different privileges on purpose
# (docs/contracts/ebpf-profiler.md §2), and sharing a user would hand the infra agent's account
# CAP_BPF by way of the profiler's unit.
set -e

USER_NAME=openlog-ebpf

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
		useradd --system --gid "$USER_NAME" --home-dir /nonexistent --no-create-home \
			--shell "$nologin" --comment "openlog eBPF profiler" "$USER_NAME"
	else
		adduser --system --ingroup "$USER_NAME" --home /nonexistent --no-create-home \
			--shell "$nologin" "$USER_NAME"
	fi
fi
exit 0
