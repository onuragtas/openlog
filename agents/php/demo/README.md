# openlog PHP agent demo

Five instrumentable PHP applications, their datastores and one v1 forwarder, sending traces to the shared local
openlog. Compose project **`openlog-php`** (always pass `-p openlog-php`; the `Makefile` does). The shared openlog
stack (project `openlog`) is never started, stopped or modified from here.

```
PHP worker (openlog.so) --unix datagram /run/openlog-infra-agent/php.sock (shared volume)--> forwarder
forwarder --OTLP/HTTP protobuf, openlog-license-key: dev-license-key--> http://openlog:4318 (network openlog_default)
```

## What runs

| service (service.name) | stack | host port | notes |
|---|---|---|---|
| `php-laravel-83` + `nginx-laravel-83` | Laravel 12, PHP 8.3 FPM (pm=static, 4 workers), opcache, pdo_mysql, phpredis 6.2.0, predis | 127.0.0.1:28901 | MariaDB + Redis (persistent connections) |
| `php-laravel-74` + `nginx-laravel-74` | Laravel 8, PHP 7.4 FPM (buster), phpredis 6.0.2, predis | 127.0.0.1:28902 | same routes, PHP 7.4 syntax |
| `php-symfony-83` + `nginx-symfony-83` | symfony/skeleton 7.4 + FrameworkBundle, attribute routes, PHP 8.3 FPM | 127.0.0.1:28903 | PostgreSQL (PDO pgsql + `pg_*`), phpredis, HttpClient |
| `php-wordpress-82` | official `wordpress:php8.2-apache` (mod_php), auto-installed with wp-cli | 127.0.0.1:28904 | MariaDB (mysqli), mu-plugin `openlog-demo.php` |
| `php-plain-71` | plain PHP 7.1 on `php:7.1-apache` (mod_php) | 127.0.0.1:28905 | mysqli, pdo_mysql, pgsql, pdo_pgsql, phpredis 5.3.7, curl |
| `forwarder` | `forwarder/` (Go, php-agent.md v1) | – (UDP 18127 inside the network) | joins `openlog_default` |
| `mariadb`, `postgres`, `redis` | mariadb:11, postgres:16-alpine, redis:7-alpine | – | init SQL in `datastores/` |
| `loadgen` (profile `load`) | alpine + curl running `load.sh --loop` | – | optional continuous traffic |

WordPress admin: http://127.0.0.1:28904/wp-admin (admin / openlog-demo).

## Usage

```bash
make -C agents/php/demo images                 # build (OPENLOG_EXT=0: apps without the extension)
make -C agents/php/demo images OPENLOG_EXT=1   # compile agents/php/ext (openlog.so) into every PHP image
make -C agents/php/demo up                     # needs the shared openlog running (network openlog_default)
make -C agents/php/demo status                 # containers, endpoint health, openlog.so loaded?, forwarder stats
make -C agents/php/demo load                   # one pass over every endpoint (+ CLI transaction), prints trace IDs
make -C agents/php/demo loop                   # continuous ~3 req/s from the loadgen container; loop-stop to end
make -C agents/php/demo logs                   # follow the forwarder (stats every 10 s)
make -C agents/php/demo down                   # stop (volumes kept); `clean` also removes volumes
```

Equivalent raw compose: `docker compose -p openlog-php -f agents/php/demo/docker-compose.yml …`.

Useful knobs (environment of `make up`):

| variable | default | effect |
|---|---|---|
| `OPENLOG_AGENT` | `on` | `on` / `off` (extension loaded, transaction tracer disabled) / `none` (not loaded) |
| `OPENLOG_TT_THRESHOLD_MS` | `500` | `openlog.transaction_tracer.threshold_ms` |
| `OPENLOG_DEBUG_DUMP` | `0` | `1`: forwarder prints every message and converted span; `2`: also raw JSON |
| `OPENLOG_ENDPOINT` / `OPENLOG_LICENSE_KEY` | `http://openlog:4318` / `dev-license-key` | export target |
| `OPENLOG_NETWORK` | `openlog_default` | external network of the shared openlog |

The PHP images write `conf.d/zz-openlog.ini` at start (`php/openlog-entrypoint.sh`): `extension=openlog.so`,
`openlog.service_name`, `openlog.transport=unix:///run/openlog-infra-agent/php.sock`,
`openlog.transaction_tracer.*`. Without `openlog.so` in the image it logs that and runs uninstrumented. FPM pools also
pass `env[OPENLOG_SERVICE_NAME]`.

## Endpoints

**php-laravel-83 (28901) / php-laravel-74 (28902)** – `routes/web.php`, `DemoController`
- `/health` → 200
- `/bench/{id}` → 200: one Eloquent query + 2 Redis calls (overhead benchmark endpoint)
- `/users/{id}/orders` → 200: query builder + Eloquent + Redis + `Http::get` (Symfony) + `Http::pool` (curl_multi) +
  `file_get_contents('http://…')`
- `/slow/report` → 200 in ~750 ms: nested `SlowReportService` → `OrderAggregator` → `PriceCalculator` /
  `ReportRenderer`, loops, `usleep`, `SELECT SLEEP(0.12)` and joins (function-level trace at 500 ms)
- `/boom` → 500 uncaught `RuntimeException`
- `/reported` → 200, caught `PaymentDeclinedException` passed to `report($e)`
- `/fatal` → 500 undefined function (`Error`); `/fatal/user-error` → 500 `E_USER_ERROR`; `/fatal/memory` → 500
  "Allowed memory size exhausted" (engine fatal)
- `/predis` → 200 Predis client calls

**php-symfony-83 (28903)** – attribute routes in `symfony/app/src/Controller`
- `/health` → 200
- `/api/products/{id}` → 200 PDO pgsql prepared statement + phpredis
- `/api/products/{id}/reviews` → 200 `pg_query_params` / `pg_query`
- `/api/external` → 200 HttpClient (curl_multi) to both Laravel apps concurrently
- `/api/reports/slow` (~800 ms), `/api/reports/inventory` (~650 ms) → 200 nested services, `pg_sleep`, Redis
- `/api/boom` → 500 `LogicException` through kernel.exception
- `/api/products/0/missing` → 404

**php-wordpress-82 (28904)** – `wordpress/mu-plugins/openlog-demo.php`
- `/`, `/demo-post-1/` … `/demo-post-5/` → 200 (pretty permalinks)
- `/wp-json/demo/v1/items/{id}` → 200 `$wpdb` queries (mysqli) + `wp_remote_get` to Symfony (curl)
- `/?slow=1`, `/slow-report/` → 200 in ~750 ms (nested functions, `SELECT SLEEP`, `usleep`)
- `/wp-json/demo/v1/boom`, `/?boom=1` → 500 uncaught exception
- `/wp-json/demo/v1/health` → 200

**php-plain-71 (28905)** – `plain71/www`
- `/index.php` (PDO), `/users.php?id=` (mysqli procedural + OO + prepared + PDO), `/pg.php?id=` (`pg_*` + pdo_pgsql),
  `/redis.php` (phpredis incl. pipeline), `/http.php` (`curl_exec`, `curl_multi_*`, `file_get_contents`),
  `/slow.php` (~750 ms nested functions) → 200
- `/boom.php` (uncaught exception), `/fatal.php` (undefined function), `/fatal.php?type=memory`,
  `/fatal.php?type=user` → 500
- CLI: `docker compose -p openlog-php exec php-plain-71 php /var/www/html/cli.php [iterations]`

## Load

`load.sh` runs from the host (127.0.0.1 ports) or inside the `loadgen` container (same URLs, routed with
`curl --connect-to`, so the WordPress Host header stays valid):

```bash
bash agents/php/demo/load.sh            # one weighted pass (~140 requests) incl. slow/error/fatal endpoints
ROUNDS=5 bash agents/php/demo/load.sh   # five passes
bash agents/php/demo/load.sh --cli      # + php-plain-71 CLI transaction
bash agents/php/demo/load.sh --loop     # continuous, LOAD_RATE req/s (default 3)
```

Every `TRACE_EVERY`-th request (default 4) carries `traceparent: 00-<trace_id>-<span_id>-01`; the script prints those
trace IDs and a status-code summary, and exits non-zero on unexpected status codes. Look traces up at
http://localhost:8080 (admin@openlog.local / openlog-dev-password) or `GET /api/v1/traces/{trace_id}`.

## Forwarder (`forwarder/`)

Standalone Go forwarder implementing docs/contracts/php-agent.md v1 §1, §2, §2.3 and the §6 validation semantics. The
decode/reassembly/conversion files are copies of `agents/infra/internal/phpforwarder` (keep them in sync); `main.go`
and `export.go` replace the infra agent pipeline with a small OTLP/HTTP exporter.

- listens on `OPENLOG_PHP_SOCKET` (default `/run/openlog-infra-agent/php.sock`, directory 0755, socket mode
  `OPENLOG_PHP_SOCKET_MODE`, demo `0666`) and optionally UDP `OPENLOG_PHP_UDP`
- validates (60 000-byte datagrams, lowercase hex IDs, ≤ 128 attributes, strings ≤ 4 KiB, value types), drops unknown
  `v`; integers stay int64, floats become doubles; unknown fields and any field order are accepted
- reassembles split messages per (`pid`, `trace_id`); exports on `last` or after `OPENLOG_PHP_REASSEMBLY_TIMEOUT` (5 s)
  with `openlog.php.incomplete=true` on the root (on every span when the root is missing)
- root span: `sampling.ratio`, `openlog.php.dropped_spans` (> 0); resource = allowed extension keys + forwarder
  `host.name`, `host.id` (machine-id if readable), `os.type`, `openlog.agent.*`, `telemetry.sdk.language=php`
  (host keys from the extension are ignored); one ResourceSpans per distinct resource
- batches (`OPENLOG_BATCH_BYTES`, `OPENLOG_FLUSH_INTERVAL`) and POSTs protobuf to `OPENLOG_ENDPOINT/v1/traces` with
  `openlog-license-key`; retries network errors, 429/502/503/504 (Retry-After honored)
- logs stats every 10 s; `OPENLOG_DEBUG_DUMP=1|2`

Tests (in Docker): `docker run --rm -v "$PWD/agents/php/demo/forwarder":/src -w /src golang:1.26 go test -race ./...`

## Overhead benchmark

`agents/php/bench/ext/` (project `openlog-php-bench`, ports 28906–28908): base / off / on variants of the
`openlog-php/laravel-83:dev` image, 4 CPUs and 8 static FPM workers each, shared MariaDB/Redis/forwarder, k6 closed loop
on `/bench/{id}`. See the header of `bench/ext/run.sh`.

## Caveats

- Images built with `OPENLOG_EXT=0` contain no extension; everything works but no PHP spans are produced.
- `php-laravel-74` uses `php:7.4-fpm-buster` (archive.debian.org) because `bullseye-security` currently returns 404
  for arm64 packages; `php-plain-71` (buster) also uses archive.debian.org (`php/apt-update.sh`).
- Composer reports open security advisories for Laravel 8 (EOL); resolution is not blocked today. If Composer starts
  blocking it, build with `COMPOSER_BLOCK_INSECURE=false make images` (demo only, never for real apps).
- PHP containers run with `dns_opt: single-request`: without it the old libcurl of the buster images (7.1/7.4)
  intermittently waited the 5 s glibc resolver timeout for service names (`cURL error 28: Resolving timed out`).
- Datastore data lives in named volumes; after changing `datastores/*.sql` run `make clean` so the init scripts re-run.
- The socket is world-writable (`0666`) for the demo; the infra agent uses `0660` + the PHP group.
