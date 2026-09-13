#!/bin/bash
# php-host entrypoint: PHP-FPM + nginx + a light request loop, then openlog-infra-agent the way the systemd unit runs it:
# as the unprivileged openlog-agent user (member of www-data, like the documented SupplementaryGroups drop-in) with
# ambient CAP_SYS_PTRACE and CAP_DAC_READ_SEARCH.
set -euo pipefail

: "${PHP_HOST_MACHINE_ID:?PHP_HOST_MACHINE_ID is required}"
echo "$PHP_HOST_MACHINE_ID" >/etc/machine-id

# systemd RuntimeDirectory=openlog-infra-agent / RuntimeDirectoryMode=0755 equivalent.
install -d -o openlog-agent -g openlog-agent -m 0755 /run/openlog-infra-agent

php-fpm -D
nginx

rps="${PHP_HOST_LOADGEN_RPS:-2}"
if [ "$rps" != "0" ]; then
  (
    sleep 5
    paths=(/ /users/7/orders /users/42/orders /checkout /users/7/orders /error)
    delay=$(awk "BEGIN { print 1 / $rps }")
    i=0
    while :; do
      curl -s -o /dev/null "http://127.0.0.1${paths[i % ${#paths[@]}]}" || :
      i=$((i + 1))
      sleep "$delay"
    done
  ) &
fi

# php_forwarder.enabled is deliberately not set: the module starts because discovery finds php-fpm.
cat >/etc/openlog-infra-agent/config.yaml <<EOF
interval: 10s
inventory_interval: ${PHP_HOST_INVENTORY_INTERVAL:-5m}
log_level: ${PHP_HOST_AGENT_LOG_LEVEL:-info}
host:
  root_path: /
  extra_attributes:
    env: local
EOF
chmod 0644 /etc/openlog-infra-agent/config.yaml

caps=+sys_ptrace,+dac_read_search
exec setpriv --reuid=openlog-agent --regid=openlog-agent --init-groups \
  --inh-caps="$caps" --ambient-caps="$caps" \
  /usr/bin/openlog-infra-agent -config /etc/openlog-infra-agent/config.yaml
