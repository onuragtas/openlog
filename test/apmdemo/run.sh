#!/usr/bin/env bash
# APM demo (test/apmdemo/README.md): the openlog `single` stack plus three OpenTelemetry-instrumented
# microservices (Node frontend, Go orders, PHP catalog), Redis, PostgreSQL, an infra agent host and a
# load generator, in the compose project openlog-apmdemo (host ports 31xxx, below the Linux ephemeral range).
#
#   test/apmdemo/run.sh up        build openlog:apm from the repository and start everything
#   test/apmdemo/run.sh verify    compare API numbers with raw spans for one transaction (+ map, errors, DB)
#   test/apmdemo/run.sh e2e       run the e2e APM phase (test/e2e/apm_test.go) against this stack
#   test/apmdemo/run.sh shots     Playwright screenshots (APMDEMO_SHOTS_DIR, needs a copy of web/ with node_modules)
#   test/apmdemo/run.sh down      remove the project and its volumes
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
ENV_FILE=$ROOT/test/apmdemo/apmdemo.env
OUT=${APMDEMO_OUT:-${TMPDIR:-/tmp}/openlog-apmdemo}
mkdir -p "$OUT"

dc() {
  docker compose -p openlog-apmdemo --project-directory "$ROOT/deploy/compose" -f "$ROOT/deploy/compose/docker-compose.yml" \
    -f "$ROOT/test/apmdemo/docker-compose.apmdemo.yml" --env-file "$ENV_FILE" "$@"
}
envv() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1; }
api() { curl -fsS -H "Authorization: Bearer $(envv OPENLOG_BOOTSTRAP_API_KEY)" "http://127.0.0.1:$(envv OPENLOG_API_PORT)$1"; }
ch() { curl -fsS -u "$(envv OPENLOG_CLICKHOUSE_USER):$(envv OPENLOG_CLICKHOUSE_PASSWORD)" "http://127.0.0.1:$(envv CLICKHOUSE_HTTP_HOST_PORT)/" --data-binary "$1"; }

phase_up() {
  (cd "$ROOT" && make docker IMAGE="$(envv OPENLOG_IMAGE)")
  dc up -d --build --wait --wait-timeout 600
  dc ps --format 'table {{.Service}}\t{{.Status}}'
}

# verify [SERVICE] [TRANSACTION]: a closed window of whole minutes (the last 5 complete minutes, ending 2
# minutes ago so the processor has written everything), API vs SQL on raw spans.
phase_verify() {
  local svc=${1:-orders} txn=${2:-GET /orders/{id\}} end start
  end=${APMDEMO_VERIFY_END:-$(($(date -u +%s) / 60 * 60 - 120))}
  start=$((end - 300))
  local q="from=$((start * 1000))&to=$((end * 1000))"
  echo "window $(date -u -r "$start" +%H:%M:%S)..$(date -u -r "$end" +%H:%M:%S) UTC (5 minutes)"
  echo "--- API: GET /apm/services/$svc/transaction?name=$txn"
  api "/api/v1/apm/services/$svc/transaction?$q&name=$(jq -rn --arg v "$txn" '$v|@uri')" |
    jq -c '{requests: .totals.requests, throughput: .totals.throughput, errors: .totals.errors, error_rate: .totals.error_rate, p50: .totals.p50_ms, p95: .totals.p95_ms, p99: .totals.p99_ms, apdex: .totals.apdex, apdex_t_ms}' | tee "$OUT/verify-api.json"
  echo "--- SQL on raw spans (attributes only, no derived APM columns)"
  ch "SELECT sum(w) AS requests, round(sum(w) / 5, 3) AS throughput, sumIf(w, err) AS errors, round(errors / requests, 5) AS error_rate,
       round(quantileExactWeighted(0.50)(d, toUInt64(w)), 2) AS p50, round(quantileExactWeighted(0.95)(d, toUInt64(w)), 2) AS p95,
       round(quantileExactWeighted(0.99)(d, toUInt64(w)), 2) AS p99,
       round((sumIf(w, NOT err AND d <= 500) + sumIf(w, NOT err AND d > 500 AND d <= 2000) / 2) / requests, 4) AS apdex_500ms
     FROM (SELECT sample_weight AS w, duration_ns / 1e6 AS d,
             status_code = 'error' OR toUInt16OrZero(attributes['http.response.status_code']) >= 500 AS err
           FROM openlog.spans
           WHERE tenant_id = '$(envv OPENLOG_BOOTSTRAP_TENANT_ID)' AND service_name = '$svc' AND kind = 'server'
             AND concat(attributes['http.request.method'], ' ', attributes['http.route']) = '$txn'
             AND timestamp >= toDateTime($start) AND timestamp < toDateTime($end))
     FORMAT JSONEachRow" | tee "$OUT/verify-sql.json"
  echo "--- services"
  api "/api/v1/apm/services?$q" | jq -r '.services[] | "\(.service_name)\t\(.throughput|.*100|round/100) rpm\terr \(.error_rate*10000|round/100)%\tp95 \(.p95_ms|.*10|round/10) ms\tapdex \(.apdex)"'
  echo "--- map edges"
  api "/api/v1/apm/map?$q" | jq -r '.edges[] | "\(.source | sub("\\|.*";"")) -> \(.target | sub("\\|.*";""))\t\(.throughput|.*10|round/10) rpm\terr \(.error_rate*10000|round/100)%\tp95 \(.p95_ms|.*10|round/10) ms"'
  echo "--- error groups"
  for s in frontend orders catalog; do
    api "/api/v1/apm/services/$s/errors?$q" | jq -r --arg s "$s" '.groups[] | "\($s)\t\(.error_type)\t\(.message)\tcount=\(.count)"'
  done
  echo "--- DB queries"
  for s in orders catalog; do
    api "/api/v1/apm/services/$s/databases?$q" | jq -r --arg s "$s" '.queries[] | "\($s)\t\(.db_system)\tcalls=\(.calls)\tp95=\(.p95_ms|.*100|round/100)ms\t\(.statement)"'
  done
  echo "--- slow transactions (p95 > 1 s)"
  for s in frontend orders; do
    api "/api/v1/apm/services/$s/transactions?$q&sort=slowest" | jq -r --arg s "$s" '.transactions[] | select(.p95_ms > 1000) | "\($s)\t\(.transaction_name)\tp95=\(.p95_ms|round) ms"'
  done
}

phase_e2e() {
  local f=$OUT/e2e-apmdemo.env
  {
    echo "OPENLOG_API_PORT=$(envv OPENLOG_API_PORT)"
    echo "OPENLOG_OTLP_HTTP_PORT=$(envv OPENLOG_OTLP_HTTP_PORT)"
    echo "CLICKHOUSE_HTTP_HOST_PORT=$(envv CLICKHOUSE_HTTP_HOST_PORT)"
    echo "OPENLOG_CLICKHOUSE_USER=$(envv OPENLOG_CLICKHOUSE_USER)"
    echo "OPENLOG_CLICKHOUSE_PASSWORD=$(envv OPENLOG_CLICKHOUSE_PASSWORD)"
    echo "OPENLOG_BOOTSTRAP_OWNER_EMAIL=$(envv OPENLOG_BOOTSTRAP_OWNER_EMAIL)"
    echo "OPENLOG_BOOTSTRAP_OWNER_PASSWORD=$(envv OPENLOG_BOOTSTRAP_OWNER_PASSWORD)"
    echo "E2E_API_KEY=$(envv OPENLOG_BOOTSTRAP_API_KEY)"
    echo "E2E_LICENSE_KEY=$(envv OPENLOG_BOOTSTRAP_LICENSE_KEY)"
    echo "E2E_TARGET_MACHINE_ID=$(envv APMDEMO_HOST_ID)"
  } >"$f"
  (cd "$ROOT" && E2E_APM_ONLY=1 E2E_SKIP_UP=1 E2E_KEEP=1 E2E_ENV_FILE="$f" go test -tags e2e -v -count=1 -timeout 15m -run TestAPMStandalone ./test/e2e)
}

phase_shots() {
  local web=${APMDEMO_WEB_WORKDIR:?set APMDEMO_WEB_WORKDIR to a copy of web/ with node_modules}
  cp "$ROOT/test/apmdemo/shots.mjs" "$web/apmdemo-shots.mjs"
  (cd "$web" && APMDEMO_BASE_URL="http://127.0.0.1:$(envv OPENLOG_API_PORT)" APMDEMO_EMAIL="$(envv OPENLOG_BOOTSTRAP_OWNER_EMAIL)" \
    APMDEMO_PASSWORD="$(envv OPENLOG_BOOTSTRAP_OWNER_PASSWORD)" APMDEMO_HOST_ID="$(envv APMDEMO_HOST_ID)" \
    APMDEMO_SHOTS_DIR="${APMDEMO_SHOTS_DIR:-$OUT/shots}" node apmdemo-shots.mjs)
}

phase_down() { dc --profile loadgen down -v --remove-orphans; }

[ $# -gt 0 ] || set -- up
phase=$1
shift
"phase_$phase" "$@"
