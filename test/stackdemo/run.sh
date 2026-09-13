#!/usr/bin/env bash
# Local stack demo (docs/operations/local-stack-demo.md): the whole openlog system from zero with
# Docker, the way a self-hosted user runs it, including signed local releases, agent installation
# with install.sh, a fleet auto-update of the agents and a self-update of the backend.
#
#   test/stackdemo/run.sh                      # every phase, no screenshots
#   test/stackdemo/run.sh --screenshots        # plus Playwright screenshots (needs Node >= 20.19)
#   test/stackdemo/run.sh up agents            # selected phases only
#   make stack-demo [STACKDEMO_ARGS="--screenshots"]
#
# Phases (default order): clean env release up agents policy publish fleet backend restart
#   clean    docker compose down -v, remove the release mirror and backups
#   env      deploy/compose/.env from .env.example plus the demo settings
#   release  test keys, backend images FROM and TO (pushed to the local registry), signed releases
#   up       mirror FROM into deploy/compose/releases, start the stack (compose project openlog)
#   agents   install the agent with install.sh on both demo hosts, wait for their data
#   policy   fleet policy notify, waves [50,100], soak 1 minute
#   publish  add TO to the mirror (re-signed index); wait for the update banner and the fleet catalog
#   fleet    policy auto: agents download TO from ingest, verify, switch and confirm
#   backend  openlog-updater auto: backup, pull, migrate, recreate, health check on TO
#   restart  docker compose restart of the backend; agents buffer, no metric samples lost
#
# Environment: STACKDEMO_FROM (0.9.0), STACKDEMO_TO (0.9.1), STACKDEMO_ARCHES (Docker server arch),
# STACKDEMO_REGISTRY_PORT (5001), STACKDEMO_OUT (output: timings, screenshots; $TMPDIR/openlog-stackdemo),
# STACKDEMO_WEB_WORKDIR (copy of web/ for Playwright; $TMPDIR/openlog-stackdemo-web).
# Test keys only (dist/testkeys); never use them for a real release.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CDIR=$ROOT/deploy/compose
FROM=${STACKDEMO_FROM:-0.9.0}
TO=${STACKDEMO_TO:-0.9.1}
ARCHES=${STACKDEMO_ARCHES:-$(docker version --format '{{.Server.Arch}}')}
REG_PORT=${STACKDEMO_REGISTRY_PORT:-5001}
REG=localhost:$REG_PORT/openlog
REL=$CDIR/releases
OUT=${STACKDEMO_OUT:-${TMPDIR:-/tmp}/openlog-stackdemo}
WEB_WORKDIR=${STACKDEMO_WEB_WORKDIR:-${TMPDIR:-/tmp}/openlog-stackdemo-web}
RELEASES_URL=http://releases
HOSTS=(host-services host-plain)
HOST_NAMES=(demo-web-1 demo-plain-1)

SCREENSHOTS=0
PHASES=()
for a in "$@"; do
  case $a in
  --screenshots) SCREENSHOTS=1 ;;
  -h | --help)
    sed -n '2,27s/^# \{0,1\}//p' "$0"
    exit 0
    ;;
  clean | env | release | up | agents | policy | publish | fleet | backend | restart) PHASES+=("$a") ;;
  *)
    echo "unknown argument: $a (see --help)" >&2
    exit 2
    ;;
  esac
done
[ ${#PHASES[@]} -gt 0 ] || PHASES=(clean env release up agents policy publish fleet backend restart)

mkdir -p "$OUT"
log() { printf '\n\033[1m== %s %s\033[0m\n' "$(date +%T)" "$*"; }
note() { printf '%s %s\n' "$(date +%T)" "$*" | tee -a "$OUT/timings.txt"; }

dc() {
  docker compose -p openlog --project-directory "$CDIR" -f "$CDIR/docker-compose.yml" \
    -f "$ROOT/test/stackdemo/docker-compose.stackdemo.yml" --profile updater "$@"
}

env_value() { sed -n "s/^$1=//p" "$CDIR/.env" | tail -n 1; }

set_env() { # KEY VALUE: set a variable in deploy/compose/.env
  local f=$CDIR/.env
  if grep -q "^$1=" "$f"; then
    awk -v k="$1" -v v="$2" 'index($0, k "=") == 1 { print k "=" v; next } { print }' "$f" >"$f.tmp"
    cat "$f.tmp" >"$f" && rm -f "$f.tmp"
  else
    printf '%s=%s\n' "$1" "$2" >>"$f"
  fi
}

api_url() { echo "http://127.0.0.1:$(env_value OPENLOG_API_PORT)"; }
admin_url() { echo "http://127.0.0.1:$(env_value OPENLOG_ADMIN_PORT)"; }

login() {
  curl -fsS -c "$OUT/cookies.txt" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$(env_value OPENLOG_BOOTSTRAP_OWNER_EMAIL)\",\"password\":\"$(env_value OPENLOG_BOOTSTRAP_OWNER_PASSWORD)\"}" \
    "$(api_url)/api/v1/auth/login" | jq -r .csrf_token >"$OUT/csrf.txt"
}
api_get() { curl -fsS -b "$OUT/cookies.txt" "$(api_url)$1"; }
api_send() { # METHOD PATH [JSON]
  local args=(-fsS -b "$OUT/cookies.txt" -X "$1" -H "X-CSRF-Token: $(cat "$OUT/csrf.txt")")
  [ $# -lt 3 ] || args+=(-H 'Content-Type: application/json' -d "$3")
  curl "${args[@]}" "$(api_url)$2"
}

wait_until() { # TIMEOUT_SECONDS DESCRIPTION COMMAND...: poll every 5 s
  local limit=$1 desc=$2 start
  shift 2
  start=$(date +%s)
  until "$@" >/dev/null 2>&1; do
    if (($(date +%s) - start >= limit)); then
      echo "timeout after ${limit}s: $desc" >&2
      return 1
    fi
    sleep 5
  done
  note "$desc (after $(($(date +%s) - start))s)"
}

ch_query() { # SQL: ClickHouse over the HTTP port
  curl -fsS -u "$(env_value OPENLOG_CLICKHOUSE_USER):$(env_value OPENLOG_CLICKHOUSE_PASSWORD)" \
    "http://127.0.0.1:$(env_value CLICKHOUSE_HTTP_HOST_PORT)/" --data-binary "$1"
}

screenshot() { # SHOT...: stack-* screenshots (test/stackdemo/shots.mjs)
  [ "$SCREENSHOTS" = 1 ] || return 0
  if [ ! -d "$WEB_WORKDIR/node_modules/@playwright/test" ] || ! cmp -s "$ROOT/web/package-lock.json" "$WEB_WORKDIR/package-lock.json"; then
    mkdir -p "$WEB_WORKDIR"
    rsync -a --delete --exclude node_modules --exclude dist --exclude 'test-results*' "$ROOT/web/" "$WEB_WORKDIR/"
    (cd "$WEB_WORKDIR" && npm ci --no-audit --no-fund && npx playwright install chromium)
  fi
  cp "$ROOT/test/stackdemo/shots.mjs" "$WEB_WORKDIR/stackdemo-shots.mjs"
  mkdir -p "$OUT/shots"
  (cd "$WEB_WORKDIR" && STACKDEMO_BASE_URL="$(api_url)" STACKDEMO_EMAIL="$(env_value OPENLOG_BOOTSTRAP_OWNER_EMAIL)" \
    STACKDEMO_PASSWORD="$(env_value OPENLOG_BOOTSTRAP_OWNER_PASSWORD)" STACKDEMO_SHOTS_DIR="$OUT/shots" STACKDEMO_TO="$TO" \
    node stackdemo-shots.mjs "$@")
}

# mirror VERSION...: the signed release mirror served to agents (ingest), install.sh, the update
# check and openlog-updater: copies of dist/v<version> plus an index over exactly these versions.
mirror() {
  local tool=$ROOT/bin/openlog-release keys v
  keys=$(cat "$ROOT/dist/testkeys/public.key")
  mkdir -p "$REL"
  for v in "$@"; do
    [ -f "$ROOT/dist/v$v/manifest.json.sig" ] || {
      echo "dist/v$v is missing: run the release phase" >&2
      return 1
    }
    rm -rf "$REL/v$v.tmp" && cp -R "$ROOT/dist/v$v" "$REL/v$v.tmp"
    rm -f "$REL/v$v.tmp"/index.json "$REL/v$v.tmp"/index.json.sig
    rm -rf "$REL/v$v" && mv "$REL/v$v.tmp" "$REL/v$v"
    cp "$ROOT/dist/v$v/install.sh" "$REL/install.sh"
  done
  cp "$ROOT/dist/testkeys/public.key" "$REL/trusted-keys.txt"
  rm -f "$REL/index.json.new" "$REL/index.json.new.sig"
  local manifests=()
  for v in "$@"; do manifests+=("$REL/v$v/manifest.json"); done
  "$tool" build-index --out "$REL/index.json.new" --base-url "$RELEASES_URL" --keys "$keys" "${manifests[@]}"
  OPENLOG_RELEASE_SIGNING_KEY=$(cat "$ROOT/dist/testkeys/signing.key") \
    "$tool" sign --key-env OPENLOG_RELEASE_SIGNING_KEY "$REL/index.json.new"
  "$tool" verify --keys "$keys" "$REL/index.json.new"
  mv "$REL/index.json.new.sig" "$REL/index.json.sig"
  mv "$REL/index.json.new" "$REL/index.json"
}

host_ids() { api_get /api/v1/hosts | jq -r '.hosts[] | "\(.host_name) \(.host_id)"'; }

hosts_visible() { # NAME...: every host is listed by the API with a last_seen time
  local names
  names=$(api_get /api/v1/hosts | jq -r '.hosts[].host_name')
  for n in "$@"; do grep -qx "$n" <<<"$names" || return 1; done
}

rollout_line() {
  api_get /api/v1/fleet/summary | jq -r '
    (.versions // [] | map("\(.version)=\(.hosts)") | join(" ")) as $v
    | if .current_rollout == null then "no rollout | versions \($v)"
      else .current_rollout as $r | $r.counters as $c
        | "\($r.action) \($r.from_version)->\($r.to_version) state=\($r.state) wave=\($r.current_wave + 1)/\($r.waves | length) (\($r.wave_percent)%) pending=\($c.pending) attempted=\($c.attempted) ok=\($c.succeeded) failed=\($c.failed) rolled_back=\($c.rolled_back) | versions \($v)"
      end'
}

all_agents_on() { # VERSION
  api_get "/api/v1/fleet/hosts?limit=100" | jq -e --arg v "$1" '[.hosts[] | select(.agent.version != $v)] | length == 0 and (length >= 0)' >/dev/null &&
    [ "$(api_get "/api/v1/fleet/hosts?limit=100" | jq --arg v "$1" '[.hosts[] | select(.agent.version == $v)] | length')" -ge ${#HOSTS[@]} ]
}

# ------------------------------------------------------------------------------------------------
phase_clean() {
  log "clean: remove the demo stack (compose project openlog), release mirror and backups"
  [ -f "$CDIR/.env" ] || cp "$CDIR/.env.example" "$CDIR/.env"
  dc down -v --remove-orphans
  rm -rf "$REL" "$CDIR/backups" "$OUT/timings.txt"
}

phase_env() {
  log "env: deploy/compose/.env"
  cp "$CDIR/.env.example" "$CDIR/.env"
  set_env OPENLOG_IMAGE "openlog:$FROM-stack"
  # Release check and fleet catalog read the local signed mirror; short intervals for the demo.
  set_env OPENLOG_UPDATE_CHECK_INTERVAL 1m
  set_env OPENLOG_RELEASE_INDEX_URL "$RELEASES_URL/index.json"
  set_env OPENLOG_RELEASE_TRUSTED_KEYS_FILE /releases/trusted-keys.txt
  set_env OPENLOG_RELEASE_MIRROR_DIR /releases
  set_env OPENLOG_RELEASE_SERVE_MIRROR true
  set_env OPENLOG_RELEASE_CATALOG_REFRESH 20s
  set_env OPENLOG_FLEET_SYNC_INTERVAL 60s
  set_env OPENLOG_FLEET_CONTROLLER_INTERVAL 10s
  set_env OPENLOG_UPDATER_MODE notify
  set_env OPENLOG_UPDATER_INTERVAL 30s
  set_env OPENLOG_UPDATER_HEALTH_TIMEOUT 3m
  grep -E '^OPENLOG_(IMAGE|UPDATE|RELEASE|FLEET|UPDATER_(MODE|INTERVAL))' "$CDIR/.env"
}

phase_release() {
  log "release: test keys, images $FROM and $TO in $REG, signed releases ($ARCHES)"
  (cd "$ROOT" && make release-testkeys)
  dc up -d --wait registry
  local v digest
  for v in "$FROM" "$TO"; do
    (cd "$ROOT" && make docker VERSION="$v" IMAGE="openlog:$v-stack" RELEASE_TESTKEYS=1)
    docker tag "openlog:$v-stack" "$REG:$v"
    docker push -q "$REG:$v"
    digest=$(docker inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$REG:$v" | grep "^$REG@sha256:" | head -n 1)
    [ -n "$digest" ] || {
      echo "no registry digest for $REG:$v" >&2
      return 1
    }
    rm -rf "$ROOT/dist/v$v"
    (cd "$ROOT" && make release-local VERSION="$v" RELEASE_TESTKEYS=1 RELEASE_BASE_URL="$RELEASES_URL" \
      RELEASE_ARCHES="$ARCHES" RELEASE_IMAGE="$digest")
    note "release $v: image $digest"
  done
}

phase_up() {
  log "up: signed mirror with $FROM only, then the stack"
  docker image inspect "openlog:$FROM-stack" >/dev/null
  rm -rf "$REL"
  mirror "$FROM"
  local t0
  t0=$(date +%s)
  dc up -d --wait
  note "stack healthy in $(($(date +%s) - t0))s"
  dc ps --format 'table {{.Service}}\t{{.Image}}\t{{.Status}}'
  curl -fsS "$(admin_url)/readyz"
  echo
}

phase_agents() {
  log "agents: install.sh on the demo hosts (tarball, supervisor loop instead of systemd)"
  login
  local i h t0 ts=()
  for i in "${!HOSTS[@]}"; do
    h=${HOSTS[$i]}
    ts[i]=$(date +%s)
    dc exec -T "$h" sh -c "curl -fsSL $RELEASES_URL/install.sh | sh -s -- --license-key '$(env_value OPENLOG_BOOTSTRAP_LICENSE_KEY)' \
      --endpoint http://openlog:4318 --base-url $RELEASES_URL --method tarball" 2>&1 | sed "s/^/[$h] /"
  done
  for i in "${!HOSTS[@]}"; do
    t0=${ts[$i]}
    until hosts_visible "${HOST_NAMES[$i]}"; do
      (($(date +%s) - t0 < 300)) || {
        echo "no data from ${HOST_NAMES[$i]} after 300s" >&2
        return 1
      }
      sleep 2
    done
    note "${HOST_NAMES[$i]}: first data in the API $(($(date +%s) - t0))s after install.sh started"
  done
  log "agents: logs.auto_from_discovery on ${HOSTS[0]} (edit config, restart the agent)"
  dc exec -T "${HOSTS[0]}" sh -c "sed -i 's/^  auto_from_discovery: false/  auto_from_discovery: true/' /etc/openlog-infra-agent/config.yaml &&
    grep -n 'auto_from_discovery' /etc/openlog-infra-agent/config.yaml && pkill -f '[o]penlog-infra-agent -config'"
  host_ids
}

phase_policy() {
  log "policy: notify, waves [50,100], soak 1 minute"
  login
  api_send PUT /api/v1/fleet/policy '{"mode":"notify","channel":"stable","target":"latest","pinned_version":null,"waves":[50,100],"wave_soak_minutes":1,"halt_failure_rate":0.05,"maintenance_windows":[]}' |
    jq -c '{mode, waves, wave_soak_minutes}'
  # The screenshots of the running system on FROM (stack-01 ... stack-14).
  sleep 60 # a few minutes of metrics make the charts worth looking at
  screenshot before
}

phase_publish() {
  log "publish: $TO into the signed mirror"
  mirror "$FROM" "$TO"
  login
  wait_until 300 "update check reports $TO available (GET /api/v1/version)" \
    sh -c "curl -fsS -b '$OUT/cookies.txt' $(api_url)/api/v1/version | jq -e '.latest_available.version == \"$TO\"'"
  wait_until 180 "fleet catalog lists $TO" \
    sh -c "curl -fsS -b '$OUT/cookies.txt' $(api_url)/api/v1/fleet/summary | jq -e '[.. | objects | select(.version? == \"$TO\")] | length > 0'"
  wait_until 120 "openlog-updater (notify) reports $TO available" \
    sh -c "curl -fsS -b '$OUT/cookies.txt' $(api_url)/api/v1/version | jq -e '.updater.state == \"available\" and .updater.target_version == \"$TO\"'"
  api_get /api/v1/version | jq -c '{version, latest_available, update_check, updater: (.updater | {mode, state, target_version})}'
  screenshot banner
}

phase_fleet() {
  log "fleet: policy auto (waves [50,100], soak 1 minute)"
  login
  api_send PUT /api/v1/fleet/policy '{"mode":"auto","channel":"stable","target":"latest","pinned_version":null,"waves":[50,100],"wave_soak_minutes":1,"halt_failure_rate":0.05,"maintenance_windows":[]}' |
    jq -c '{mode, waves, wave_soak_minutes}'
  local t0 shot=0 line
  t0=$(date +%s)
  while true; do
    line=$(rollout_line)
    note "fleet: $line"
    # Two agents update within seconds of each other, so take the in-progress screenshot as soon as
    # the rollout is active (wave 1 soaking), not when the first agent succeeded.
    if [ "$shot" = 0 ] && grep -q "state=active" <<<"$line"; then
      screenshot rollout
      shot=1
    fi
    if all_agents_on "$TO"; then break; fi
    if (($(date +%s) - t0 > 900)); then
      echo "agents did not reach $TO within 15 minutes" >&2
      return 1
    fi
    sleep 5
  done
  note "fleet: all agents on $TO after $(($(date +%s) - t0))s"
  sleep 20
  note "fleet: $(rollout_line)"
  api_get "/api/v1/fleet/hosts?limit=10" | jq -c '.hosts[] | {host_name, version: .agent.version, update_capable: .agent.update_capable, update: .update}'
  local h
  for h in "${HOSTS[@]}"; do
    echo "[$h]"
    dc exec -T "$h" sh -c 'readlink /opt/openlog/infra-agent/current; ls /opt/openlog/infra-agent/versions; /usr/bin/openlog-infra-agent -version; cat /var/lib/openlog-infra-agent/update-state.json; echo'
  done
  curl -fsS "$(admin_url)/metrics" | grep -E '^openlog_(release_mirror_requests_total|agent_updates_total|fleet_rollout_transitions_total)' || :
  screenshot fleet-done
}

phase_backend() {
  log "backend: openlog-updater auto ($FROM -> $TO)"
  login
  local t0 hosts_before
  hosts_before=$(api_get /api/v1/hosts | jq '.hosts | length')
  set_env OPENLOG_UPDATER_MODE auto
  t0=$(date +%s)
  dc up -d openlog-updater
  wait_until 900 "backend reports $TO on /readyz" sh -c "curl -fsS $(admin_url)/readyz | jq -e '.version == \"$TO\"'"
  login
  wait_until 300 "openlog-updater finished (succeeded)" \
    sh -c "curl -fsS -b '$OUT/cookies.txt' $(api_url)/api/v1/version | jq -e '(.updater.history // [])[0].result == \"succeeded\" or .updater.state == \"succeeded\" or .updater.state == \"up_to_date\"'"
  note "backend: self-update done in $(($(date +%s) - t0))s"
  api_get /api/v1/version | jq '{version, latest_available, updater: (.updater | {mode, state, current_version, previous_version, backup_file, steps: [.steps[]? | "\(.name)=\(.status)"], history: (.history // [])[:2]})}'
  dc logs --no-color openlog-updater | grep -E 'update|backup|migrat|recreat|health' | tail -n 20 || :
  ls -l "$CDIR/backups"
  grep '^OPENLOG_IMAGE=' "$CDIR/.env"
  docker ps --filter label=com.docker.compose.project=openlog --filter label=com.docker.compose.service=openlog --format '{{.Names}} {{.Image}} {{.Status}}'
  local hosts_after
  hosts_after=$(api_get /api/v1/hosts | jq '.hosts | length')
  note "backend: hosts before $hosts_before, after $hosts_after"
  ch_query "SELECT host_name, count() AS samples, min(timestamp) AS first, max(timestamp) AS last FROM openlog.metrics WHERE metric_name = 'system.uptime' GROUP BY host_name ORDER BY host_name FORMAT PrettyCompactMonoBlock"
  screenshot after-backend
}

phase_restart() {
  log "restart: docker compose restart of the backend while the agents keep sending"
  local t_restart t_restarted t_ready start end
  sleep 30
  t_restart=$(date -u +%s)
  dc restart postgres kafka clickhouse openlog openlog-updater
  t_restarted=$(date -u +%s)
  wait_until 300 "backend ready again after restart" curl -fsS "$(admin_url)/readyz"
  t_ready=$(date -u +%s)
  note "restart: docker compose restart took $((t_restarted - t_restart))s, /readyz 200 $((t_ready - t_restart))s after it began"
  sleep 150 # agents replay their buffers
  start=$((t_restart - 60))
  end=$((t_ready + 60))
  ch_query "
    SELECT host_name,
           count() AS samples,
           intDiv($end - $start, 10) AS expected_about,
           arrayMax(arrayDifference(arraySort(groupUniqArray(toUInt32(toUnixTimestamp(timestamp)))))) AS max_gap_s
    FROM openlog.metrics
    WHERE metric_name = 'system.uptime' AND timestamp >= toDateTime($start) AND timestamp < toDateTime($end)
    GROUP BY host_name ORDER BY host_name FORMAT PrettyCompactMonoBlock" | tee -a "$OUT/timings.txt"
  login
  local name id
  while read -r name id; do
    echo "$name: openlog.agent.export.items by outcome (last value)"
    api_get "/api/v1/hosts/$id/metrics?name=openlog.agent.export.items&agg=last&group_by=outcome&step=60s" |
      jq -c '[.series[] | {outcome: .attributes.outcome, last: (.points | last)}]'
  done < <(host_ids)
}

for p in "${PHASES[@]}"; do
  "phase_$p"
done
log "done. UI: $(api_url) ($(env_value OPENLOG_BOOTSTRAP_OWNER_EMAIL) / $(env_value OPENLOG_BOOTSTRAP_OWNER_PASSWORD)); output in $OUT"
