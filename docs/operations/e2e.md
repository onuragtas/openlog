# End-to-end test: infra agent → backend

`test/e2e` runs the real `openlog-infra-agent` on Debian 12 containers against the real backend
(compose `single` profile: Kafka, ClickHouse, `openlog-allinone`). It asserts everything through
the Query API (`docs/contracts/api.md`) and, where the API has no view, ClickHouse directly.

## Run

Requirements: Docker with Compose v2.17+ (`additional_contexts`), Go 1.26, about 4 GB free memory.

```sh
make e2e
# same as
go test -tags e2e -v -count=1 -timeout 45m ./test/e2e
```

A run takes about 10 minutes (image build excluded): ~2 min of data collection, 60 s backend
outage plus recovery, 30 s Kafka outage plus recovery. The stack is removed at the end with
`docker compose down -v`, also when the test fails.

| Variable | Effect |
|---|---|
| `E2E_KEEP=1` | keep the stack running after the test (for debugging) |
| `E2E_SKIP_UP=1` | reuse a running stack (`E2E_KEEP=1` from a previous run) |
| `E2E_SKIP_BUILD=1` | do not rebuild images |
| `E2E_SKIP_OUTAGE=1` | skip the backend and Kafka outage phases |
| `E2E_DOCKER=0` | no private dockerd in the target (the `containers` phase is skipped) |
| `E2E_UI=1` | run the Playwright UI phase against the stack (see [UI phase](#ui-phase)) |
| `E2E_UI_WORKDIR` | where `web/` is copied for the UI phase (default `$TMPDIR/openlog-e2e-ui`) |

The compose project is `openlog-e2e` with shifted host ports (`test/e2e/e2e.env`), so it can run next
to a `make compose-up` stack:

| Port | Service |
|---|---|
| 14318 | OTLP/HTTP |
| 18080 | Query API |
| 19464 | Admin |
| 18123 | ClickHouse HTTP (user/password `openlog`) |
| 15432 | PostgreSQL (user/password `openlog`) |

Manual control (from the repository root):

```sh
E2E="docker compose -p openlog-e2e -f deploy/compose/docker-compose.yml -f test/e2e/docker-compose.e2e.yml --env-file test/e2e/e2e.env"
$E2E up -d --build --wait
$E2E down -v --remove-orphans
```

## What runs

`test/e2e/docker-compose.e2e.yml` adds two hosts to the base compose file. Both images are built from
`test/e2e/target/Dockerfile`, with the agent compiled from `agents/infra` (version `0.1.0-e2e`):

- **target** (`e2e-target`, machine-id `0e2e…0001`) runs nginx, redis-server and postgresql 15. It also runs a
  fake `/usr/local/bin/mysql` started as `mysql -uapp -pE2eSecretPw1 --password=E2eSecretPw2 DB_TOKEN=E2eSecretPw3 mysql://app:E2eSecretPw4@db.internal:3306/app`.
  IPv6 is enabled, so nginx also listens on `[::]:80`. A docker volume is mounted at `/data`.
  Its agent also tails logs (`logs.files: /var/log/nginx/access.log` with the user attribute `e2e.source=logs.files`,
  `auto_from_discovery: true`, `start_at: beginning`) and reports process metrics for the top 50 processes by memory.
  With `E2E_DOCKER=1` (default) the target is `privileged` and runs its **own** dockerd (static binaries copied from
  `docker:dind`; `--bridge=none --iptables=false`, vfs storage on the `target-docker` volume, socket group
  `openlog-agent`) with one container `e2e-sleeper` (image `openlog-e2e/sleeper:1`, imported from a static binary, no
  registry access, `--network none`). The entrypoint moves the target's processes to `/sys/fs/cgroup/init` so the
  nested containers get `/sys/fs/cgroup/docker/<id>` like on a real host. The daemon never touches the host's Docker.
  A `docker:dind` *sidecar* was not used: its containers' cgroups are not visible from the target's cgroup
  namespace, so the agent could report container inventory but no `container.*` metrics.
- **plain** (`e2e-plain`, machine-id `0e2e…0002`) is Debian 12 with only `ca-certificates`/`openssl` and the agent.

The agent runs the way the systemd unit runs it: as the unprivileged `openlog-agent` user with ambient
`CAP_SYS_PTRACE` + `CAP_DAC_READ_SEARCH` (`setpriv`, see `test/e2e/target/entrypoint.sh`). Its settings are
`interval: 10s`, `inventory_interval: 2m`, extra attributes `env=e2e`, `e2e.role=<target|plain>`, and the endpoint
`http://openlog:4318` with license key `e2e-license-key` (tenant `e2e-tenant`).

Tenancy is PostgreSQL-backed (`OPENLOG_AUTH_MODE=postgres`). The base compose `bootstrap` service creates the
organization `e2e-tenant` (owner `owner@e2e.test`, ingest key `e2e-license-key`, read-only API key
`ola_e2e-api-key`); the override's `bootstrap-other` creates `e2e-other-tenant` (ingest key `e2e-other-key`,
API key `ola_e2e-other-api-key`). The test queries the API with the API keys (`Authorization: Bearer`) and
posts OTLP probes with the ingest key.

## What it checks

| Phase | Assertions |
|---|---|
| `wait_for_data` | both hosts have a complete snapshot and ≥ 60 s of metrics (up to 4 min) |
| `hosts` | exactly 2 hosts; host_name, `os_description`, arch (= Docker server arch), `agent_version`, `openlog.agent.name`, entity type, extra attributes; `GET /hosts/{id}` |
| `metrics` | `system.cpu.utilization` by `cpu.mode`: all 8 modes, values in [0,1], modes sum ≈ 1; `system.memory.usage` by state sums to `system.memory.limit` (±1 %); filesystem usage includes `/data`, utilization in [0,1]; `rate` of disk io/operations (read, write) and network io (receive, transmit, `eth0`, no `lo`); `openlog.agent.collection.interval` = 10 on both hosts; `openlog.agent.export.items{outcome=sent}` monotonic and increasing |
| `inventory` | latest complete snapshot has os, hardware (cpu, memory), package, systemd_unit, listening_port, process, user, mount, network_interface; > 100 dpkg packages (target); ports 80/6379/5432 with the right `process_name`; `tcp:0.0.0.0:80` and `tcp:[::]:80`; every IPv6 key bracketed; `item_count` of the snapshot row in ClickHouse = items returned by the API |
| `services` | target: nginx (`enabled`), redis (`enabled`), postgresql (`needs_configuration`), each with a version matching the dpkg package, the package linked and its port. Plain: no web/database services |
| `masking` | the fake mysql `cmdline` has `-p***`, `--password=***`, `DB_TOKEN=***` and `mysql://app:***@…`, and no secret. ClickHouse `inventory_items` and `logs` have no row containing `E2eSecret`; a positive control finds the masked form |
| `inventory_search` | `category=package&q=openssl` returns both hosts with host names; `category` is required |
| `logs_exclude_inventory` | `/api/v1/logs` returns no inventory events; the `logs` table holds none |
| `tenant_isolation` | the other key sees no hosts and no search items; `/hosts/{target}`, `/inventory`, `/services`, `/metrics` are `404 not_found` for the other organization and for an unknown host id (own key); logs with `attr.*` filters are empty for the other key; a bad key and an ingest key get 401 |
| `agent_logs` | 100 requests to missing nginx paths (`/e2e-log/<run>/a/<i>`), then logrotate-style rotation (`mv access.log access.log.1 && nginx -s reopen`), then 100 more (`…/b/<i>`). Every request has exactly one unique access and one error record (none lost across the rotation; duplicates are logged, delivery is at-least-once). All records: `host_id`, empty `service_name`, `openlog.log.source=file`, `openlog.discovery.id=nginx`. Access records carry `e2e.source` (logs.files) and a `log.file.path` of `access.log` (phase b) or `access.log`/`access.log.1` (phase a); error records have `log.file.path=/var/log/nginx/error.log`, severity `ERROR` and no `e2e.source` (tailed only through `auto_from_discovery`). Server-side `attr.*` filters return the same counts (error.log by path, nginx+file; redis and journald give 0; a non-allowlisted key is 400). ClickHouse: unique (request, file) rows = 400 |
| `agent_log_rotation_unread` | forced rotation with unread lines: `/var/log/e2e-rotate/*.log` (logs.files). After the first line's record arrives, one exec appends 10 000 lines (`/e2e-rotate/<run>/a/<i>`), renames `app.log` to `app.log.1` and writes 2 000 lines (`…/b/<i>`) to a new `app.log`. With `rate_limit_lines` 2000/s and the mv within 2 s of the burst start (asserted), thousands of lines are unread at the rotation and exist only in the renamed file. ClickHouse: each of the 12 001 lines exactly once, no multi-line record, all with `e2e.source=rotation`; the agent then closes `app.log.1` (no open descriptor) |
| `agent_journald` | the target runs `systemd-journald` standalone (no systemd PID 1); 20 entries (`/e2e-journal/<run>/<i>`) via `systemd-cat -p err` and `logger -p user.warning`. The agent (journalctl, `logs.journald.enabled`) delivers each: `openlog.log.source=journald`, `openlog.syslog.identifier=openlog-e2e`, severity `ERROR`/`WARN`, `host_id`, timestamp; the `attr.openlog.syslog.identifier` filter returns all 20 |
| `process_metrics` | `process.memory.usage` grouped by `process.executable.name,openlog.discovery.id`: `nginx` → `nginx`, `redis-server` → `redis`, value > 0, unit `By` |
| `containers` | (`E2E_DOCKER=1`) a `container` inventory item for `e2e-sleeper` / `openlog-e2e/sleeper` in state running; `container.memory.usage` (> 0) and `container.cpu.time` series with `container.name=e2e-sleeper` |
| `ui` | (`E2E_UI=1`) Playwright against the embedded UI, see below |
| `resilience_backend_outage` | `docker compose stop openlog` for 60 s. 20 s in, a second redis is started on 6390, which changes the port set and forces an inventory snapshot during the outage. After the restart: that snapshot (with `tcp:127.0.0.1:6390`) becomes the latest complete snapshot; `system.uptime` buckets cover the outage window with gaps ≤ 2 intervals on both hosts; `outcome=buffered` rose; `outcome=dropped` stays 0 |
| `kafka_outage_503` | `docker compose stop kafka` for 30 s. A small OTLP/HTTP request gets `503` with a numeric `Retry-After`. After Kafka returns, without restarting anything: ingest answers 200, both hosts get new points and `outcome=sent` increases. Sampling gaps during the Kafka outage are logged, not asserted: the agent collects and exports in one loop, so a slow send delays the next sample |

## UI phase

`E2E_UI=1 make e2e` runs `web/e2e-stack/stack.spec.ts` (config `web/playwright.stack.config.ts`) against the
embedded UI of the e2e stack (`http://127.0.0.1:18080`, built into the `openlog:e2e` image), after `agent_logs`.
`test/e2e/ui_test.go` copies `web/` to `E2E_UI_WORKDIR` (node tooling does not run inside iCloud-synced checkouts),
runs `npm ci` only when `package-lock.json` changed, `npx playwright install chromium`, then the suite. Requires
Node ≥ 20.19. Traces and screenshots of failures: `$E2E_UI_WORKDIR/test-results-stack`.

The test signs in as the bootstrap owner (`OPENLOG_BOOTSTRAP_OWNER_EMAIL` / `_PASSWORD` from `test/e2e/e2e.env`) and
checks: hosts list → `e2e-target` → overview charts render (canvas count > 0); services tab shows nginx, Redis,
PostgreSQL; host logs tab shows this run's nginx lines, and the *Discovered service* + *File path* filters
(`ldisc`, `lfile` → `attr.openlog.discovery.id`, `attr.log.file.path`) narrow it to error.log lines; an unknown host id
shows "Host not found"; inventory search `package` / `openssl` lists both hosts; Settings → License keys lists the
`bootstrap` key with its prefix.

Against a kept stack: `E2E_SKIP_UP=1 E2E_KEEP=1 E2E_UI=1 go test -tags e2e -v -count=1 -run 'TestE2E/(wait_for_data|agent_logs|ui)$' ./test/e2e`.

## TLS integration test

`make tlstest` (`go test -tags tlstest -v -count=1 -timeout 30m ./test/e2e/tlstest`) proves the TLS/SASL settings of
[config.md](../contracts/config.md#tls-and-sasl-all-services-that-use-the-dependency) end to end. It generates a CA,
server/client certificates and an unrelated CA with `crypto/x509` (`internal/testutil/testcerts`, written to a new
directory under `/tmp`, nothing committed) and starts compose project `openlog-tlstest`
(`test/e2e/tlstest/docker-compose.yml`, image `openlog:tlstest` built from the working tree):

| Service | TLS setup | Host ports |
|---|---|---|
| `kafka` | KRaft, client listeners SSL (mTLS required) and SASL_SSL (PLAIN, SCRAM-SHA-256/512; SCRAM users added at storage format), no plaintext client listener | 39093 (SSL), 39094 (SASL_SSL) |
| `ch1`, `ch2` | 2 shards; native protocol only on 9440 (`tcp_port` removed), client certificates required, `<secure>1</secure>` in `remote_servers` | 39440/39441 (native TLS), 38123/38124 (HTTP) |
| `postgres` | `ssl=on` | 35432 |
| `openlog`, `bootstrap`, `loadgen` | allinone with SASL_SSL SCRAM-SHA-512, ClickHouse mTLS (direct shard inserts), PostgreSQL `verify-full` | 38080 (API), 34318, 39464 |
| `badca-*` (profile `badca`) | api / processor / ingest with the unrelated CA for PostgreSQL / ClickHouse / Kafka | 39466 (ingest admin) |

| Test | Assertions |
|---|---|
| `TestKafkaClients` | `queue.ClientOptions` clients: mTLS, SASL_SSL PLAIN, SCRAM-SHA-256, SCRAM-SHA-512 list the openlog topics; `insecure_skip_verify` works with an unrelated CA; wrong CA → `certificate signed by unknown authority`; wrong SCRAM password → `SASL_AUTHENTICATION_FAILED`; no client certificate and plaintext are rejected |
| `TestClickHouseClients` | mTLS connects and sees 2 shards; `SERVER_NAME` override; wrong CA, name mismatch, no client certificate and plaintext are rejected |
| `TestPostgresClients` | `verify-full` + `OPENLOG_POSTGRES_TLS_CA_FILE` connects with `pg_stat_ssl.ssl = true`; wrong CA rejected |
| `TestDataFlowsOverTLS` | loadgen hosts and logs visible through the API; `metrics_local` rows on **both** shards; every native query of user `openlog` in `system.query_log` is `is_secure`; consumer group `openlog-processor` Stable; metrics topic has records; all `openlog-*` PostgreSQL sessions use TLS |
| `TestWrongCAFailsClearly` | `badca-postgres` and `badca-clickhouse` log `waiting for …` with `x509: certificate signed by unknown authority`; `badca-kafka`'s `/readyz` is not 200 and names that error |

`TLSTEST_KEEP=1` keeps the stack, `TLSTEST_SKIP_BUILD=1` reuses `openlog:tlstest`, `TLSTEST_WORKDIR` fixes the certificate
directory (needed with `TLSTEST_KEEP=1` to reconnect). The stack is removed with `down -v` at the end.

## Tiered storage test

`make tieredtest` (`test/e2e/tieredtest`, compose project `openlog-tiered`, host ports 37000/37123, no openlog image)
runs MinIO and a 2-shard × 2-replica ClickHouse cluster with the unchanged `deploy/compose/clickhouse/storage-tiered.xml`
and `storage-tiered-warm.xml`, applies the schema and `migrate.ApplyTableTTLs` in-process and checks moves to warm and
S3 on every replica, reads through Distributed tables after a cache drop and a restart of all servers, deletion of cold
parts, an S3 outage (inserts, hot/cold queries, a server restart) with recovery, and disabling. What it asserts and the
measured failure behaviour: [tiered-storage.md](tiered-storage.md#test). `TIEREDTEST_KEEP=1` keeps the stack.

## Debugging

Keep the stack: `E2E_KEEP=1 make e2e`, then iterate with `E2E_SKIP_UP=1 E2E_KEEP=1 go test -tags e2e -v -count=1 -run 'TestE2E/(wait_for_data|masking)' ./test/e2e`.
Note that `wait_for_data` must run first. After an outage phase, the extra redis on 6390 stays up until the stack is recreated.
When the test fails, it prints the tail of the `openlog`, `target` and `plain` logs.

```sh
$E2E ps
$E2E logs -f openlog                      # ingest/processor/api (JSON logs)
$E2E logs target plain | grep -v '"level":"INFO"'   # agent warnings: export failed; buffering, permission denied
$E2E exec target openlog-infra-agent -once -config /etc/openlog-infra-agent/config.yaml | jq .discovered_services
$E2E exec target ss -ltnp                 # what is really listening

KEY='openlog-license-key: e2e-license-key'
curl -s -H "$KEY" localhost:18080/api/v1/hosts | jq
curl -s -H "$KEY" "localhost:18080/api/v1/hosts/0e2e0000000000000000000000000001/services" | jq '.items[].data | {rule_id, version, integration}'
curl -s -H "$KEY" "localhost:18080/api/v1/hosts/0e2e0000000000000000000000000001/metrics?name=openlog.agent.export.items&agg=last&group_by=outcome&step=10s" | jq
```

ClickHouse (`curl` against the HTTP port, or `$E2E exec clickhouse clickhouse-client -u openlog --password openlog`):

```sql
-- snapshots per host, newest first (item_count vs stored items)
SELECT s.host_id, s.snapshot_id, s.snapshot_time, s.item_count, i.stored
FROM openlog.inventory_snapshots AS s
LEFT JOIN (SELECT host_id, snapshot_id, uniqExact(category, item_key) AS stored
           FROM openlog.inventory_items GROUP BY host_id, snapshot_id) AS i
  ON s.host_id = i.host_id AND s.snapshot_id = i.snapshot_id
ORDER BY s.host_id, s.snapshot_time DESC;

-- metric freshness per host
SELECT host_name, max(timestamp), count() FROM openlog.metrics WHERE timestamp > now() - INTERVAL 10 MINUTE GROUP BY host_name;

-- sample timestamps around an outage (gaps = lost or not yet replayed data)
SELECT timestamp FROM openlog.metrics WHERE metric_name = 'system.uptime' AND host_name = 'e2e-target' ORDER BY timestamp DESC LIMIT 40;

-- secret leak check
SELECT count() FROM openlog.inventory_items WHERE positionCaseInsensitive(data, 'E2eSecret') > 0;
```

```sh
curl -s -u openlog:openlog 'localhost:18123/?query=SELECT+host_name,max(timestamp)+FROM+openlog.metrics+GROUP+BY+host_name'
curl -s localhost:19464/metrics | grep -E 'openlog_(ingest|processor)_'   # ingest/processor counters
```
