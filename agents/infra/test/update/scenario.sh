#!/bin/bash
# Runs inside a Debian 12 container with systemd as PID 1. /work holds bin/, releases/, keys/,
# legacy-unit.service and this script, prepared by run.sh. The fake ingest (e2e serve) scripts the
# update instructions; this script performs the host-side actions between its steps and checks the
# host (unit, groups, ownership) after them.
set -euo pipefail

ROOT=/opt/openlog/infra-agent
ARCH=$(dpkg --print-architecture)
UNIT_NAME=openlog-infra-agent.service
UNIT=/etc/systemd/system/$UNIT_NAME
STATE=/var/lib/openlog-infra-agent
CONFIG=/etc/openlog-infra-agent/config.yaml
LOG=/work/out/server.log
GATES=/work/out/gates
TIMEOUT=${SCENARIO_TIMEOUT:-1200}
start=$(date +%s)

say() { echo "== $*"; }
fail() {
  echo "SCENARIO CHECK FAILED: $*"
  ls -la "$ROOT" "$ROOT/versions" || true
  cat "$ROOT/apply-status.json" "$ROOT/reconcile-status.json" "$STATE/update-state.json" 2>/dev/null || true
  journalctl -u openlog-infra-agent --no-pager -n 60 || true
  exit 1
}
wait_step() { # n
  until grep -q "STEP $1 PASSED" "$LOG"; do
    grep -q "SCENARIO FAILED" "$LOG" && fail "server: $(grep 'SCENARIO FAILED' "$LOG")"
    [ $(($(date +%s) - start)) -lt "$TIMEOUT" ] || fail "timeout waiting for step $1"
    sleep 2
  done
  grep "STEP $1 PASSED" "$LOG"
}
gate() { touch "$GATES/$1"; }
main_pid() { systemctl show -p MainPID --value openlog-infra-agent; }
root_owned() { [ -z "$(find "$ROOT" ! -user root -print -quit)" ] && [ -z "$(find "$ROOT" ! -type l -perm -0002 -print -quit)" ]; }
as_agent() { setpriv --reuid=openlog-agent --regid=openlog-agent --clear-groups "$@"; }

say "legacy tarball install of 0.9.0: versions owned by openlog-agent, unit without the privileged pre-start step ($ARCH)"
useradd --system --no-create-home --home-dir "$STATE" --shell /usr/sbin/nologin openlog-agent
mkdir -p "$ROOT/versions/0.9.0" /etc/openlog-infra-agent "$STATE"
install -m 0755 /work/bin/0.9.0/openlog-infra-agent "$ROOT/versions/0.9.0/"
cp /work/releases/0.9.0/manifest.json /work/releases/0.9.0/manifest.json.sig "$ROOT/versions/0.9.0/"
ln -s versions/0.9.0 "$ROOT/current"
chown -R openlog-agent:openlog-agent "$ROOT" "$STATE"
cat >"$CONFIG" <<'EOF'
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
chown root:openlog-agent "$CONFIG"
chmod 0640 "$CONFIG"
install -m 0644 /work/legacy-unit.service "$UNIT"
# Drop-ins survive unit updates: the tarball layout runs inside a container here; restart quickly.
mkdir -p "$UNIT.d"
printf '[Service]\nEnvironment=OPENLOG_AGENT_CONTAINER=0\nRestartSec=1\n' >"$UNIT.d/10-e2e.conf"

systemd-run --unit=openlog-e2e-server -p StandardOutput=append:$LOG -p StandardError=append:$LOG \
  /work/bin/e2e serve -listen 127.0.0.1:8080 -releases /work/releases -arch "$ARCH" -gates "$GATES" >/dev/null
sleep 1
systemctl daemon-reload
systemctl enable --now openlog-infra-agent

wait_step 1

say "bootstrap once, like re-running install.sh: -reconcile installs the new unit and secures the layout"
"$ROOT/current/openlog-infra-agent" -reconcile -reconcile-context install -config "$CONFIG"
systemctl daemon-reload
systemctl restart openlog-infra-agent
grep -q '^ExecStartPre=-+/opt/openlog/infra-agent/current/openlog-infra-agent -apply' "$UNIT" || fail "unit not updated by -reconcile"
root_owned || fail "install root not root-owned after -reconcile"
[ "$(ls "$ROOT/versions")" = 0.9.1 ] || fail "untrusted legacy versions not removed: $(ls "$ROOT/versions")"
grep -q '"docker": "no_group"' "$ROOT/reconcile-status.json" || fail "docker result"

wait_step 2
say "docker group appears; the next self-update reconciles group membership"
groupadd --system docker
gate docker

wait_step 3
say "check the self-update to 0.9.2: new unit active, docker group, root-owned layout"
grep -q '^Environment=OPENLOG_E2E_UNIT=changed' "$UNIT" || fail "unit of 0.9.2 not installed"
pid=$(main_pid)
tr '\0' '\n' <"/proc/$pid/environ" | grep -qx OPENLOG_E2E_UNIT=changed || fail "agent does not run under the new unit"
[ "$(readlink "/proc/$pid/exe")" = "$ROOT/versions/0.9.2/openlog-infra-agent" ] || fail "main process is not 0.9.2"
[ "$(stat -c %U "/proc/$pid")" = openlog-agent ] || fail "agent does not run as openlog-agent"
getent group docker | cut -d: -f4 | tr ',' '\n' | grep -qx openlog-agent || fail "openlog-agent not in the docker group"
grep -qw "$(getent group docker | cut -d: -f3)" <(grep '^Groups:' "/proc/$pid/status") || fail "agent process lacks the docker group"
journalctl -u openlog-infra-agent --no-pager | grep -q "systemd unit was updated before this start" || fail "no restart for the new unit"
root_owned || fail "install root not root-owned after the self-update"
[ "$(stat -c %U:%G:%a "$ROOT/versions/0.9.2/openlog-infra-agent")" = root:root:755 ] || fail "binary ownership"
[ "$(stat -c %U:%G:%a "$STATE")" = openlog-agent:openlog-agent:750 ] || fail "state dir ownership"
[ "$(stat -c %U:%G:%a "$CONFIG")" = root:openlog-agent:640 ] || fail "config ownership"
[ -f "$UNIT.d/10-e2e.conf" ] || fail "drop-in removed"
as_agent sh -c "touch $ROOT/versions/0.9.2/x" 2>/dev/null && fail "openlog-agent can write versions/"
echo "host checks passed"

wait_step 8
say "hostile staging by the agent user: forged signature"
systemctl stop openlog-infra-agent
as_agent sh -c "rm -rf $STATE/updates && mkdir -p $STATE/updates/0.9.9 &&
  cp /work/releases/openlog-infra-agent_0.9.1_linux_${ARCH}.tar.gz $STATE/updates/0.9.9/archive.tar.gz &&
  sed 's/\"0.9.1\"/\"0.9.9\"/' /work/releases/0.9.1/manifest.json >$STATE/updates/0.9.9/manifest.json &&
  cp /work/releases/0.9.1/manifest.json.sig $STATE/updates/0.9.9/ &&
  printf '{\"state\":\"restarting\",\"staged\":\"0.9.9\",\"candidate\":\"0.9.9\",\"action\":\"upgrade\",\"from_version\":\"0.9.2\",\"to_version\":\"0.9.9\",\"staged_at\":\"2026-09-14T00:00:01Z\",\"changed_at\":\"2026-09-14T00:00:01Z\"}' >$STATE/update-state.json"
systemctl start openlog-infra-agent
wait_step 9
[ "$(readlink "$ROOT/current")" = versions/0.9.2 ] || fail "forged update switched current"

say "hostile staging by the agent user: validly signed manifest, archive symlinked to /etc/shadow"
systemctl stop openlog-infra-agent
as_agent sh -c "rm -rf $STATE/updates && mkdir -p $STATE/updates/0.9.3 &&
  ln -s /etc/shadow $STATE/updates/0.9.3/archive.tar.gz &&
  cp /work/releases/0.9.3/manifest.json /work/releases/0.9.3/manifest.json.sig $STATE/updates/0.9.3/ &&
  printf '{\"state\":\"restarting\",\"staged\":\"0.9.3\",\"candidate\":\"0.9.3\",\"action\":\"upgrade\",\"from_version\":\"0.9.2\",\"to_version\":\"0.9.3\",\"staged_at\":\"2026-09-14T00:00:02Z\",\"changed_at\":\"2026-09-14T00:00:02Z\"}' >$STATE/update-state.json"
systemctl start openlog-infra-agent
wait_step 10
[ "$(readlink "$ROOT/current")" = versions/0.9.2 ] || fail "symlinked archive switched current"
grep -q "sha256" "$ROOT/apply-status.json" && fail "apply status leaks a digest"
gate rollback

wait_step 11
say "rollback action to 0.9.1: its unit is installed again"
grep -q OPENLOG_E2E_UNIT "$UNIT" && fail "unit of 0.9.1 not restored"
pid=$(main_pid)
tr '\0' '\n' <"/proc/$pid/environ" | grep -q OPENLOG_E2E_UNIT && fail "agent still runs under the 0.9.2 unit"
root_owned || fail "install root not root-owned at the end"

sleep 3
grep -q "SCENARIO PASSED" "$LOG" || fail "server did not finish"
say "result: PASSED after $(($(date +%s) - start))s"
ls -la "$ROOT" "$ROOT/versions"
cat "$ROOT/apply-status.json" "$ROOT/reconcile-status.json"
"$ROOT/current/openlog-infra-agent" -version
