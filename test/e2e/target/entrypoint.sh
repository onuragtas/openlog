#!/bin/bash
# Entrypoint of the e2e target hosts: set a distinct machine-id, start the services (services
# image only), write the agent config and run the agent the way the systemd unit does: as the
# unprivileged openlog-agent user with ambient CAP_SYS_PTRACE and CAP_DAC_READ_SEARCH.
set -euo pipefail

: "${E2E_MACHINE_ID:?E2E_MACHINE_ID is required}"
: "${E2E_ROLE:?E2E_ROLE is required}"
echo "$E2E_MACHINE_ID" >/etc/machine-id

start_docker() {
  # cgroup v2 delegation for the nested dockerd (same as docker:dind's `dind` script): move every
  # process out of the root cgroup, then enable all controllers for child cgroups. Containers get
  # /sys/fs/cgroup/docker/<id>, which the agent finds like on a real host.
  if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    mkdir -p /sys/fs/cgroup/init
    for _ in 1 2 3 4 5; do
      xargs -rn1 </sys/fs/cgroup/cgroup.procs >/sys/fs/cgroup/init/cgroup.procs 2>/dev/null || :
      if sed -e 's/ / +/g' -e 's/^/+/' </sys/fs/cgroup/cgroup.controllers >/sys/fs/cgroup/cgroup.subtree_control 2>/dev/null; then
        break
      fi
    done
  fi
  # Private daemon: no bridge/iptables (the host network is not touched), vfs storage on a volume.
  # The socket group lets the unprivileged agent read the Docker Engine API (container inventory).
  dockerd --host=unix:///var/run/docker.sock --group=openlog-agent --bridge=none --iptables=false --ip6tables=false \
    --storage-driver=vfs --data-root=/var/lib/docker >/var/log/e2e-dockerd.log 2>&1 &
  for _ in $(seq 1 60); do
    docker info >/dev/null 2>&1 && break
    sleep 1
  done
  if ! docker image inspect openlog-e2e/sleeper:1 >/dev/null 2>&1; then
    tar -C /opt/e2e-sleeper -c . | docker import -c 'ENTRYPOINT ["/sleep"]' - openlog-e2e/sleeper:1 >/dev/null
  fi
  docker rm -f e2e-sleeper >/dev/null 2>&1 || :
  docker run -d --name e2e-sleeper --network none --label e2e=openlog openlog-e2e/sleeper:1 >/dev/null
}

extra_config=""
if [ "${E2E_SERVICES:-0}" = "1" ]; then
  if [ "${E2E_DOCKER:-0}" = "1" ]; then
    start_docker
  fi
  nginx
  setpriv --reuid=redis --regid=redis --init-groups \
    redis-server /etc/redis/redis.conf --daemonize yes --supervised no
  pg_ctlcluster 15 main start
  # Password-like arguments; every form must be masked by the agent (semantic-conventions §3.5).
  nohup /usr/local/bin/mysql -uapp -pE2eSecretPw1 --password=E2eSecretPw2 DB_TOKEN=E2eSecretPw3 \
    mysql://app:E2eSecretPw4@db.internal:3306/app >/dev/null 2>&1 &
  # systemd-journald without systemd as PID 1: it creates its sockets (/run/systemd/journal, /dev/log)
  # and a volatile journal in /run/log/journal; systemd-cat and logger write to it and the agent reads
  # it through journalctl (test/e2e agent_journald).
  /lib/systemd/systemd-journald &
  for _ in $(seq 1 30); do
    [ -S /run/systemd/journal/stdout ] && break
    sleep 1
  done
  # The syslog socket; systemd-journald-dev-log.socket creates this link on a systemd host.
  ln -sf /run/systemd/journal/dev-log /dev/log
  # Directory of the forced-rotation log (test/e2e agent_log_rotation_unread).
  install -d -m 0755 /var/log/e2e-rotate
  # Logs: the nginx access log is a configured file (logs.files, with a user attribute); the error
  # log (and redis/postgresql logs) come only from the discovered services' log_paths
  # (auto_from_discovery). Both get openlog.discovery.id=nginx.
  extra_config=$(
    cat <<'EOF'
process_metrics:
  top_n_memory: 50
logs:
  enabled: true
  auto_from_discovery: true
  start_at: beginning
  files:
    - path: /var/log/nginx/access.log
      attributes:
        e2e.source: logs.files
    - path: /var/log/e2e-rotate/*.log
      attributes:
        e2e.source: rotation
  journald:
    enabled: true
EOF
  )
fi

cat >/etc/openlog-infra-agent/config.yaml <<EOF
interval: 10s
inventory_interval: ${E2E_INVENTORY_INTERVAL:-2m}
log_level: ${E2E_AGENT_LOG_LEVEL:-info}
host:
  root_path: /
  extra_attributes:
    env: e2e
    e2e.role: ${E2E_ROLE}
${extra_config}
EOF
chmod 0644 /etc/openlog-infra-agent/config.yaml

caps=+sys_ptrace,+dac_read_search
exec setpriv --reuid=openlog-agent --regid=openlog-agent --init-groups \
  --inh-caps="$caps" --ambient-caps="$caps" \
  /usr/bin/openlog-infra-agent -config /etc/openlog-infra-agent/config.yaml
