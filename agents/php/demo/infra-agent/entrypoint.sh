#!/bin/sh
# Stable host identity for the demo (host.id from /etc/machine-id; host.name from the compose `hostname`), then the agent.
set -e
echo "${OPENLOG_MACHINE_ID:-0e0e0000000000000000000000000071}" > /etc/machine-id
install -d -m 0755 /run/openlog-infra-agent
exec /usr/bin/openlog-infra-agent -config /etc/openlog-infra-agent/config.yaml "$@"
