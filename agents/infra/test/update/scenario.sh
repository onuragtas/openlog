#!/bin/bash
# Runs inside a Debian 12 container (no systemd). /work holds bin/, releases/, keys/ and this
# script, prepared by run.sh.
set -euo pipefail

ROOT=/opt/openlog/infra-agent
ARCH=$(dpkg --print-architecture)
TIMEOUT=${SCENARIO_TIMEOUT:-900}

echo "== layout: tarball install of 0.9.0 ($ARCH)"
mkdir -p "$ROOT/versions/0.9.0" /etc/openlog-infra-agent /var/lib/openlog-infra-agent
install -m 0755 /work/bin/0.9.0/openlog-infra-agent "$ROOT/versions/0.9.0/"
cp /work/releases/0.9.0/manifest.json /work/releases/0.9.0/manifest.json.sig "$ROOT/versions/0.9.0/"
ln -s versions/0.9.0 "$ROOT/current"

cat >/etc/openlog-infra-agent/config.yaml <<'EOF'
license_key: e2e-license
endpoint: http://127.0.0.1:8080
interval: 10s
state_dir: /var/lib/openlog-infra-agent
buffer:
  dir: /var/lib/openlog-infra-agent/buffer
containers:
  enabled: false
update:
  enabled: true
  install_root: /opt/openlog/infra-agent
EOF

"$ROOT/current/openlog-infra-agent" -version

/work/bin/e2e serve -listen 127.0.0.1:8080 -releases /work/releases -arch "$ARCH" >/work/out/server.log 2>&1 &
sleep 1

# Emulates systemd Restart=always (RestartSec shortened to 1 s).
(
  while true; do
    set +e
    "$ROOT/current/openlog-infra-agent" -config /etc/openlog-infra-agent/config.yaml
    code=$?
    set -e
    echo "{\"supervisor\":\"agent exited\",\"code\":$code,\"current\":\"$(readlink $ROOT/current)\",\"time\":\"$(date -u +%FT%T.%3NZ)\"}"
    sleep 1
  done
) >>/work/out/agent.log 2>&1 &

start=$(date +%s)
result=TIMEOUT
while [ $(( $(date +%s) - start )) -lt "$TIMEOUT" ]; do
  if grep -q "SCENARIO PASSED" /work/out/server.log; then result=PASSED; break; fi
  if grep -q "SCENARIO FAILED" /work/out/server.log; then result=FAILED; break; fi
  sleep 2
done
sleep 3 # let the last confirmation prune

echo "== result: $result after $(( $(date +%s) - start ))s"
echo "== install root"
ls -la "$ROOT" "$ROOT/versions"
echo "== update-state.json"
cat /var/lib/openlog-infra-agent/update-state.json
echo "== current -version"
"$ROOT/current/openlog-infra-agent" -version
[ "$result" = PASSED ]
