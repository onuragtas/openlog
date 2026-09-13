#!/bin/bash
# Entrypoint of the demo hosts: a distinct machine-id, the host's services (if installed), a little
# nginx traffic, and a supervisor loop in place of systemd for the agent that install.sh installs.
#
# The loop runs the agent the way the systemd unit does (unprivileged openlog-agent user with
# ambient CAP_SYS_PTRACE and CAP_DAC_READ_SEARCH) and restarts it whenever it exits, which is how
# the agent switches into a self-update (it exits with status 0 after replacing `current`).
set -uo pipefail

if [ ! -s /etc/machine-id ]; then
  tr -d '-' </proc/sys/kernel/random/uuid >/etc/machine-id
fi

if command -v nginx >/dev/null 2>&1; then
  nginx
  # Some access and error log lines every few seconds.
  (
    while true; do
      curl -s -o /dev/null http://127.0.0.1/ || :
      curl -s -o /dev/null "http://127.0.0.1/stackdemo/missing-$RANDOM" || :
      sleep 5
    done
  ) &
fi
if command -v redis-server >/dev/null 2>&1; then
  setpriv --reuid=redis --regid=redis --init-groups redis-server /etc/redis/redis.conf --daemonize yes --supervised no
fi
if [ -d /etc/postgresql/15/main ]; then
  pg_ctlcluster 15 main start
fi

config=/etc/openlog-infra-agent/config.yaml
caps=+sys_ptrace,+dac_read_search
trap 'exit 0' TERM INT
echo "stackdemo-host: waiting for the agent (install it with scripts/install.sh)"
while true; do
  if [ -x /usr/bin/openlog-infra-agent ] && grep -Eq '^license_key:[[:space:]]*"?[^"[:space:]#]' "$config" 2>/dev/null &&
    grep -Eq '^endpoint:[[:space:]]*"?http' "$config" 2>/dev/null; then
    echo "stackdemo-host: starting $(readlink /opt/openlog/infra-agent/current 2>/dev/null)"
    setpriv --reuid=openlog-agent --regid=openlog-agent --init-groups \
      --inh-caps="$caps" --ambient-caps="$caps" \
      /usr/bin/openlog-infra-agent -config "$config" &
    wait $!
    echo "stackdemo-host: agent exited with status $?; restarting"
  fi
  sleep 2
done
