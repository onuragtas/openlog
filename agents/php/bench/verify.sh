#!/usr/bin/env bash
# Functional check against the shared local openlog (compose project `openlog`, API :8080).
# Sends requests with a known W3C traceparent to each instrumented variant, reads the traces back with
# GET /api/v1/traces/{id} (owner session login, no key is created) and lists the PHP services from
# GET /api/v1/apm/services. Writes spike/results/verify.txt and verify-*.json.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
mkdir -p "$RESULTS"
rc=0

curl -fsS -m 10 -c "$COOKIES" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$OPENLOG_EMAIL\",\"password\":\"$OPENLOG_PASSWORD\"}" "$API/api/v1/auth/login" >/dev/null
api() { curl -s -m 10 -b "$COOKIES" "$API$1" || true; }

newtrace() { od -An -N16 -tx1 /dev/urandom | tr -d ' \n'; }

call() { # variant port path -> trace id
  local tid; tid=$(newtrace)
  curl -s -m 15 -o /dev/null -w "%{http_code}" -H "traceparent: 00-$tid-00f067aa0ba902b7-01" "http://127.0.0.1:$2$3" >&2
  echo " $1 $3 trace=$tid" >&2
  echo "$tid"
}

show() { # label (reads JSON on stdin)
  docker run --rm -i --entrypoint php openlog-phpspike/app:dev -r '
    $t = json_decode(stream_get_contents(STDIN), true);
    printf("OK %s: trace %s, %d spans\n", $argv[1], $argv[2], count($t["spans"]));
    foreach ($t["spans"] as $s) {
      $a = $s["attributes"];
      $extra = array_filter([
        $a["http.route"] ?? null, $a["db.system.name"] ?? ($a["db.system"] ?? null),
        isset($a["db.query.text"]) ? substr($a["db.query.text"], 0, 60) : (isset($a["db.statement"]) ? substr($a["db.statement"], 0, 60) : null),
        $a["http.response.status_code"] ?? null, $s["events"] ? "events:" . implode(",", array_column($s["events"], "name")) : null,
      ]);
      printf("   %-20s %-8s %-36s %8.2fms %-5s %s\n", $s["service_name"], $s["kind"], substr($s["name"], 0, 36),
        $s["duration_ns"] / 1e6, $s["status_code"], implode(" | ", $extra));
    }' "$1" "$2"
}

fetch() { # label trace-id
  local body=""
  for _ in $(seq 1 40); do
    body=$(api "/api/v1/traces/$2")
    if echo "$body" | grep -q '"spans"'; then break; fi
    sleep 1
  done
  sleep 3 # late spans (other services in the same trace)
  body=$(api "/api/v1/traces/$2")
  echo "$body" > "$RESULTS/verify-$1.json"
  if ! echo "$body" | grep -q '"spans"'; then echo "FAIL $1: trace $2 not found" | tee -a "$RESULTS/verify.txt" >&2; rc=1; return; fi
  show "$1" "$2" < "$RESULTS/verify-$1.json" | tee -a "$RESULTS/verify.txt"
}

: > "$RESULTS/verify.txt"
for v in a b; do
  p=$(port_of "$v")
  wait_http "http://127.0.0.1:$p/health"
  t1=$(call "$v" "$p" /users/7/orders)
  t2=$(call "$v" "$p" /boom)
  t3=$(call "$v" $((p + 10)) /api/products/3)
  t4=$(call "$v" $((p + 10)) /api/boom)
  fetch "$v-laravel-orders" "$t1"
  fetch "$v-laravel-boom" "$t2"
  fetch "$v-symfony-product" "$t3"
  fetch "$v-symfony-boom" "$t4"
done

# Baseline traffic too, so php-laravel-base is not expected in APM (it has no agent).
curl -s -m 5 -o /dev/null "http://127.0.0.1:28801/users/7/orders" || true

from=$(date -u -v-30M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '-30 min' +%Y-%m-%dT%H:%M:%SZ)
sleep 60 # RED materialization is per minute
api "/api/v1/apm/services?from=$from&q=php-" > "$RESULTS/verify-apm-services.json"
docker run --rm -i --entrypoint php openlog-phpspike/app:dev -r '
  $d = json_decode(stream_get_contents(STDIN), true);
  echo "APM services (q=php-):\n";
  foreach ($d["services"] ?? [] as $s) {
    printf("   %-22s requests=%-6s errors=%-4s p95=%sms language=%s\n", $s["service_name"], $s["requests"], $s["errors"], $s["p95_ms"], $s["language"] ?? "");
  }
  if (!($d["services"] ?? null)) { echo "   (none: " . substr(json_encode($d), 0, 200) . ")\n"; }' < "$RESULTS/verify-apm-services.json" | tee -a "$RESULTS/verify.txt"
exit $rc
