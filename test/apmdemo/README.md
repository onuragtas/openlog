# APM demo

Sample microservices instrumented with OpenTelemetry, sending traces (and logs) over OTLP to an openlog stack
in the compose project `openlog-apmdemo` (host ports 3xxxx, see `apmdemo.env`). Used as the M2 APM acceptance
proof ([docs/plan/10-m2.md](../../docs/plan/10-m2.md) §6, [apm.md](../../docs/contracts/apm.md)).

```
loadgen ─▶ frontend (Node 22, Express 5) ─┬─▶ orders (Go, net/http + database/sql/pgx) ─▶ orders-db (PostgreSQL 16)
                                          └─▶ catalog (PHP 8.3, Slim 4)                   ─▶ redis (7)
apmdemo-host: openlog infra agent (machine-id = host.id of the services)
```

```sh
test/apmdemo/run.sh up       # build openlog:apm, start the stack and the demo (UI: http://127.0.0.1:31080, admin@apmdemo.local / apmdemo-password)
test/apmdemo/run.sh verify   # API numbers vs SQL on raw spans, map edges, error groups, DB queries, slow transactions
test/apmdemo/run.sh e2e      # the e2e APM phase against this stack
test/apmdemo/run.sh down
```

| Endpoint (frontend) | Calls | Injected behavior |
|---|---|---|
| `GET /api/products`, `/api/products/:id` | catalog `/products[/{id}]` (Redis cache) | product 13 throws `RuntimeException("Product 13 is discontinued")` → catalog 500, frontend 502 |
| `POST /api/orders`, `GET /api/orders/:id` | orders (INSERT / SELECT) | order ids divisible by 17: `order <id>: inventory shard <n> unavailable` → 500 |
| `GET /api/orders/report` | orders `pg_sleep(1.2 + random() * 0.6)` | slow transaction (p95 ≈ 1.7 s) |
| `GET /api/orders/recent` | orders, SQL with inline literals | shows statement normalization |
| `GET /api/checkout` | catalog + orders | |
| `GET /api/flaky` | — | ~20 % `TypeError: Cannot read properties of undefined (reading 'price')` |

The load generator (`apmdemo-loadgen`, `APMDEMO_LOADGEN_RPS`, default 8/s) mixes these calls.

Instrumentation: frontend `@opentelemetry/sdk-node` + `auto-instrumentations-node` (http, express, undici) and OTLP
logs; orders `otelhttp`, `github.com/XSAM/otelsql`, `otelslog` (OTLP logs with trace context); catalog `ext-opentelemetry`
+ `open-telemetry/opentelemetry-auto-slim`. There is no OpenTelemetry auto-instrumentation for ext-redis/predis on
Packagist, so `RedisCache.php` creates the Redis CLIENT spans with the OTel API (`db.system=redis`, `db.query.text`).
Every resource carries `service.namespace=shop`, `deployment.environment=demo` and `host.id` = the agent host's
machine-id (`APMDEMO_HOST_ID`).

Licenses of the demo dependencies: OpenTelemetry JS/Go/PHP (Apache-2.0), Express (MIT), `XSAM/otelsql` (Apache-2.0),
pgx (MIT), Slim (MIT), Guzzle adapter (MIT). The demo code itself is AGPL-3.0-only like the repository.
