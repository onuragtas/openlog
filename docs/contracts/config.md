# Contract: Service Configuration (v1)

All backend services are configured **only via environment variables** (12-factor), so the same image runs in Compose and Kubernetes.
Durations use Go syntax (`2s`, `500ms`). Lists are comma-separated.

## Common (all services)

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (JSON logs to stdout) |
| `OPENLOG_ADMIN_ADDR` | `:9464` | Admin HTTP: `/healthz`, `/readyz`, `/metrics` (Prometheus) |
| `OPENLOG_KAFKA_BROKERS` | `localhost:9092` | Kafka bootstrap brokers |
| `OPENLOG_KAFKA_TOPIC_PREFIX` | `openlog` | See [kafka.md](kafka.md) |
| `OPENLOG_CLICKHOUSE_ADDR` | `localhost:9000` | ClickHouse native protocol addresses |
| `OPENLOG_CLICKHOUSE_DATABASE` | `openlog` | Must be `openlog` (the schema hard-codes it; any other value is rejected) |
| `OPENLOG_CLICKHOUSE_USER` | `default` | |
| `OPENLOG_CLICKHOUSE_PASSWORD` | `` | |
| `OPENLOG_CLICKHOUSE_CLUSTER` | `openlog` | Cluster name used in `ON CLUSTER` DDL |
| `OPENLOG_AUTH_MODE` | `postgres` | `postgres`: tenants, users, sessions and keys in PostgreSQL ([postgres.md](postgres.md)). `static`: **development and tests only** — license keys from `OPENLOG_LICENSE_KEYS`, no users; the API accepts the same license keys (as viewer) and has no management endpoints, so the web UI cannot sign in |
| `OPENLOG_LICENSE_KEYS` | `` | `static` mode only: `key1=tenant1,key2=tenant2`, used by ingest and api. Ignored in `postgres` mode |
| `OPENLOG_POSTGRES_DSN` | `` | `postgres` mode (ingest, api, migrate, allinone, admin): e.g. `postgres://openlog@postgres:5432/openlog?sslmode=require` (libpq URL or key=value form; `sslmode`, `connect_timeout`, … are honoured) |
| `OPENLOG_POSTGRES_PASSWORD` | `` | Overrides the password in the DSN (lets the password come from a separate Secret) |
| `OPENLOG_POSTGRES_MAX_CONNS` | `10` | Connection pool size per process |

### TLS and SASL (all services that use the dependency)

Kafka settings apply to every Kafka client (ingest producer, processor consumer group, migrate topic creation);
ClickHouse settings to every native connection (processor bootstrap **and direct shard/replica connections**, api,
migrate); PostgreSQL settings to ingest, api, migrate, allinone and admin. Certificate files are read at start-up
(ClickHouse replica pools also when they are opened after a topology change): **restart the pods after rotating
certificates**. Invalid combinations or unreadable files stop the service at start-up with a configuration error.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_KAFKA_TLS_ENABLED` | `false` | TLS to the brokers (listener `SSL` or `SASL_SSL`). The other `OPENLOG_KAFKA_TLS_*` variables require it |
| `OPENLOG_KAFKA_TLS_CA_FILE` | `` | PEM CA bundle that signs the broker certificates; empty = system roots |
| `OPENLOG_KAFKA_TLS_CERT_FILE` / `OPENLOG_KAFKA_TLS_KEY_FILE` | `` | PEM client certificate and key (mutual TLS, `ssl.client.auth=required`); both or neither |
| `OPENLOG_KAFKA_TLS_SERVER_NAME` | `` | Name verified in broker certificates; empty = the host of each broker address (bootstrap and advertised listeners) |
| `OPENLOG_KAFKA_TLS_INSECURE_SKIP_VERIFY` | `false` | Skip broker certificate verification. **Testing only**: allows man-in-the-middle |
| `OPENLOG_KAFKA_SASL_MECHANISM` | `` | `PLAIN`, `SCRAM-SHA-256` or `SCRAM-SHA-512` (case-insensitive); empty = no SASL. Without TLS this is `SASL_PLAINTEXT` (PLAIN then sends the password in clear text) |
| `OPENLOG_KAFKA_SASL_USERNAME` / `OPENLOG_KAFKA_SASL_PASSWORD` | `` | Required with a mechanism |
| `OPENLOG_CLICKHOUSE_TLS_ENABLED` | `false` | Native protocol over TLS. `OPENLOG_CLICKHOUSE_ADDR` must use the secure native port (`tcp_port_secure`, usually `9440`) |
| `OPENLOG_CLICKHOUSE_TLS_CA_FILE` | `` | PEM CA bundle; empty = system roots |
| `OPENLOG_CLICKHOUSE_TLS_CERT_FILE` / `OPENLOG_CLICKHOUSE_TLS_KEY_FILE` | `` | Client certificate and key (server `verificationMode` `strict`); both or neither |
| `OPENLOG_CLICKHOUSE_TLS_SERVER_NAME` | `` | Name verified in the server certificate; empty = the dialed host. When set it applies to every replica connection too, so leave it empty for multi-shard clusters unless all replicas share one certificate name |
| `OPENLOG_CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY` | `false` | **Testing only** |
| `OPENLOG_POSTGRES_TLS_CA_FILE` | `` | Sets `sslrootcert` (overrides the DSN's). Use with `sslmode=verify-full` in the DSN |
| `OPENLOG_POSTGRES_TLS_CERT_FILE` / `OPENLOG_POSTGRES_TLS_KEY_FILE` | `` | Set `sslcert` / `sslkey` (client certificate authentication); both or neither |

TLS clients require TLS 1.2 or newer. Error messages name the variable (`OPENLOG_KAFKA_TLS_CA_FILE: open …: no such
file or directory`); a server certificate that does not verify shows up in the logs and readiness checks as
`x509: certificate signed by unknown authority` (wrong CA) or `x509: certificate is valid for …, not …` (name).

**PostgreSQL.** `sslmode` is always taken from `OPENLOG_POSTGRES_DSN`. Production: `sslmode=verify-full` (encrypts and
verifies the CA and the host name) with `OPENLOG_POSTGRES_TLS_CA_FILE` or `sslrootcert=` in the DSN. `require` only
encrypts (no server verification unless a root certificate is given, libpq semantics); `prefer`/`allow` may fall back
to plaintext and `disable` never encrypts — use those only on trusted networks.

**Kafka.** In the cluster profile use a `SASL_SSL` listener with `SCRAM-SHA-512` (Strimzi: listener `tls: true` +
`authentication: {type: scram-sha-512}` and a `KafkaUser`), or mutual TLS (`authentication: {type: tls}`), see the Helm
chart README.

## `openlog-ingest`

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_INGEST_HTTP_ADDR` | `:4318` | OTLP/HTTP (`/v1/metrics`, `/v1/logs`, `/v1/traces`; protobuf and JSON; gzip) |
| `OPENLOG_INGEST_GRPC_ADDR` | `:4317` | OTLP/gRPC |
| `OPENLOG_INGEST_MAX_BODY_BYTES` | `10485760` | Max **decompressed** request size; larger → `413` / `RESOURCE_EXHAUSTED` |
| `OPENLOG_INGEST_PRODUCE_TIMEOUT` | `10s` | Kafka produce ack timeout; timeout or Kafka unavailable → `503` / `UNAVAILABLE` with `Retry-After` (D-014) |

| `OPENLOG_AUTH_CACHE_TTL` | `60s` | `postgres` mode: a resolved license key is re-checked against PostgreSQL after this long. **A revoked key keeps being accepted by an ingest pod for up to this long** |
| `OPENLOG_AUTH_NEGATIVE_CACHE_TTL` | `10s` | Unknown keys are re-checked after this long (a newly created key works within this delay on pods that rejected it before) |
| `OPENLOG_AUTH_CACHE_MAX_STALE` | `15m` | While PostgreSQL is unreachable, keys resolved successfully within this window keep being accepted (`0` = never serve stale entries) |

License key is read from header `openlog-license-key`, or `Authorization: Bearer <key>` (gRPC: metadata with the same names). Unknown/missing/revoked key → `401` / `UNAUTHENTICATED`.

**License key cache (`postgres` mode).** Each ingest pod keeps an in-memory cache keyed by `sha256(key)`
(bounded to 100 000 entries; concurrent misses for one key share a single query). Ingest does not wait
for PostgreSQL at start-up and has no PostgreSQL readiness check, so a PostgreSQL outage does not take
ingest out of the load balancer: cached keys keep working for `OPENLOG_AUTH_CACHE_MAX_STALE` (re-tried at
most every 5 s), while keys that are not cached get `503` / `UNAVAILABLE` with `Retry-After` (agents buffer
and retry, D-014) instead of `401`. `last_used_at` is written asynchronously, at most once a minute per key
per pod. Metric: `openlog_license_key_resolutions_total{result="hit|miss|negative|stale|unavailable"}`.

## `openlog-processor`

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_PROCESSOR_GROUP` | `openlog-processor` | Kafka consumer group |
| `OPENLOG_PROCESSOR_BATCH_ROWS` | `50000` | Maximum rows per insert block. A chunk's rows for one table beyond this are split into deterministic blocks (token suffix `#<i>/<batch_rows>`, see [kafka.md](kafka.md)) |
| `OPENLOG_PROCESSOR_FLUSH_INTERVAL` | `2s` | Length of the record-timestamp window that bounds a per-partition chunk. A caught-up partition's chunk is inserted about `1.5 ×` this after its window starts (window + half a window of grace), so this is also the ingest-to-queryable delay added by the processor |
| `OPENLOG_PROCESSOR_INSERT_TIMEOUT` | `30s` | Per insert attempt; also bounds the final flush on shutdown |
| `OPENLOG_PROCESSOR_BATCH_BYTES` | `8388608` | Close a per-partition chunk once its records reach this many (uncompressed) bytes |
| `OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES` | `134217728` | Record bytes buffered over all partitions before chunks are flushed early (timing-dependent cut, see kafka.md); must be ≥ `OPENLOG_PROCESSOR_BATCH_BYTES`. Processor memory is roughly this plus `OPENLOG_PROCESSOR_INSERT_CONCURRENCY × BATCH_BYTES ×` ~10 (decoded rows) plus the Kafka fetch buffer (~26 MiB) |
| `OPENLOG_PROCESSOR_INSERT_CONCURRENCY` | `4` | Chunks decoded and inserted in parallel |
| `OPENLOG_PROCESSOR_INSERT_MODE` | `direct` | `direct`: insert into the `*_local` table of each row's shard (D-018; see below). `distributed`: insert into the Distributed tables with `distributed_foreground_insert=1` |
| `OPENLOG_PROCESSOR_TOPOLOGY_REFRESH` | `1m` | `direct` mode: how often `system.clusters` is re-read; new shards/replicas are used after the next refresh |
| `OPENLOG_PROCESSOR_INSERT_QUORUM` | `0` | `direct` mode: `insert_quorum` for local inserts (`0` = off). `2` makes an insert succeed only after 2 replicas of the shard have the part; it then fails while fewer replicas are available |

**`direct` insert mode.** At start-up the processor checks that every Distributed table uses the sharding key it computes (`cityHash64(tenant_id, host_id)`, spans `cityHash64(tenant_id, trace_id)`; a mismatch stops the processor, missing tables are waited for), then reads the shards, weights and replicas of `OPENLOG_CLICKHOUSE_CLUSTER` from `system.clusters` through `OPENLOG_CLICKHOUSE_ADDR`. Rows go to shard `slot[cityHash64(...) % total_weight]`, exactly like the Distributed engine, on one replica of that shard (rotating; a failed replica is tried last for 10 s and the next replica is used). Replica `host_name:port` from `system.clusters` must be reachable from the processor (true in the Helm chart and the compose stack); for a cluster of 1 shard × 1 replica the `OPENLOG_CLICKHOUSE_ADDR` connection is used instead. With `OPENLOG_CLICKHOUSE_TLS_ENABLED` the replica connections use TLS as well: `remote_servers` must list the secure port (`<port>9440</port><secure>1</secure>`) and each replica certificate must be valid for its `host_name`. Replication inside a shard is done by ReplicatedMergeTree (the `internal_replication` flag only affects Distributed inserts; the processor logs a warning when it is `false`). Without `OPENLOG_PROCESSOR_INSERT_QUORUM` an insert is acknowledged by one replica, the same durability as a Distributed insert with `internal_replication=true`.

## `openlog-api`

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_API_HTTP_ADDR` | `:8080` | See [api.md](api.md) |
| `OPENLOG_API_QUERY_TIMEOUT` | `30s` | Sets ClickHouse `max_execution_time` |
| `OPENLOG_API_MAX_ROWS` | `10000` | Upper bound for list endpoints |
| `OPENLOG_API_UI_ENABLED` | `true` | Serve the embedded web UI at `/` (SPA fallback for non-`/api` paths) |
| `OPENLOG_SESSION_TTL` | `168h` | Absolute session lifetime (`postgres` mode) |
| `OPENLOG_SESSION_IDLE_TIMEOUT` | `24h` | A session unused for this long ends (`0` disables); activity is recorded at most once a minute |
| `OPENLOG_COOKIE_SECURE` | `true` | `Secure` attribute of the session cookie. Set `false` only for plain-HTTP development (the API logs a warning) |
| `OPENLOG_COOKIE_DOMAIN` | `` | Cookie `Domain`; empty = host-only cookie (recommended) |
| `OPENLOG_SIGNUP_ENABLED` | `false` | Enables `POST /api/v1/auth/signup` (self-service organization creation, SaaS) |
| `OPENLOG_LOGIN_MAX_FAILURES` | `10` | Failed password checks per (email, client IP) within the window before `429` |
| `OPENLOG_LOGIN_WINDOW` | `15m` | Sliding window for `OPENLOG_LOGIN_MAX_FAILURES` (shared by all api pods through PostgreSQL) |
| `OPENLOG_INVITATION_TTL` | `168h` | Invitation validity |
| `OPENLOG_API_TRUSTED_PROXIES` | `` | CIDRs/addresses of reverse proxies whose `X-Forwarded-For` is trusted for the client IP (rate limiting, sessions, audit log). Empty = use the TCP peer address |

Session cookie: `openlog_session`, `HttpOnly`, `SameSite=Strict`, `Path=/api`, `Secure` per
`OPENLOG_COOKIE_SECURE`. The api waits for PostgreSQL at start-up and adds a `postgres` readiness check.

## `openlog-migrate`

Applies `migrations/postgres/*.sql` (when `OPENLOG_AUTH_MODE=postgres` or `OPENLOG_POSTGRES_DSN` is set; waits for
PostgreSQL), then `schema/clickhouse/*.sql` (both embedded in the binary) in order, then creates Kafka topics.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_KAFKA_PARTITIONS` | `6` | For topic creation |
| `OPENLOG_KAFKA_REPLICATION_FACTOR` | `1` | For topic creation |
| `OPENLOG_KAFKA_MIN_INSYNC_REPLICAS` | `1` | Topic config `min.insync.replicas`; must be between 1 and the replication factor |
| `OPENLOG_KAFKA_RETENTION_MS` | `86400000` | Topic config `retention.ms`; `> 0`, or `-1` for unlimited |
| `OPENLOG_KAFKA_MAX_MESSAGE_BYTES` | `12582912` | Topic config `max.message.bytes`; must stay ≥ `OPENLOG_INGEST_MAX_BODY_BYTES` + 2 MiB. Ingest's producer and the processor's fetch size are fixed at 12 MiB in M0, so lowering this below that can make ingest return `503` for large requests. |
| `OPENLOG_MIGRATE_SKIP_KAFKA` | `false` | Skip topic creation (Strimzi manages topics) |

Every migration file declares `-- openlog:phase expand|contract` on its first line (loading fails otherwise).
Expand migrations always run; a contract migration (`-- openlog:requires-all-at-least <version>`) runs only when
this binary and every instance in `component_heartbeats` seen within 5 minutes are at least that version, and is
skipped (retried by the next run) otherwise. Flags: `-plan` prints live instances and what would run, changing
nothing (no topics either); `-force-contract` applies blocked contract migrations anyway; `-version`.

Topics that already exist are left as they are (partition count is never changed automatically; see `docs/operations/scaling.md`).

## `openlog-allinone`

Runs ingest + processor + api in one process with all variables above; one shared admin server.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_MIGRATE_ON_START` | `true` | Run migrations (PostgreSQL + ClickHouse + topics) before starting |

## `openlog-admin`

Command-line tool for PostgreSQL-backed tenancy; uses the `OPENLOG_POSTGRES_*` variables and applies pending
PostgreSQL migrations before every command. It does not start an admin server.

| Command | What |
|---|---|
| `migrate` | Apply `migrations/postgres` only |
| `bootstrap` | Idempotently ensure the organization, owner and keys described by `OPENLOG_BOOTSTRAP_*` (exits 0 without changes when `OPENLOG_BOOTSTRAP_OWNER_EMAIL` and both keys are empty) |
| `create-owner --email E --org NAME [--tenant-id ID] [--name N] [--password-stdin] [--no-license-key]` | Self-hosted first run: creates the organization (tenant id generated unless given), the owner and an ingest license key; prints a generated password and the key once |
| `reset-password --email E [--password-stdin]` | Sets a new (generated or stdin) password and revokes all of the user's sessions |

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_BOOTSTRAP_TENANT_ID` | `default` | Tenant id of the organization to ensure |
| `OPENLOG_BOOTSTRAP_ORG_NAME` | tenant id | Name used when the organization is created |
| `OPENLOG_BOOTSTRAP_OWNER_EMAIL` | `` | Owner to ensure (created if missing, added as owner if not a member) |
| `OPENLOG_BOOTSTRAP_OWNER_PASSWORD` | `` | Required only when the owner user does not exist yet (≥ 12 characters); an existing user's password is never changed |
| `OPENLOG_BOOTSTRAP_OWNER_NAME` | `` | Display name for a new owner |
| `OPENLOG_BOOTSTRAP_LICENSE_KEY` | `` | Plaintext ingest key to ensure (≥ 8 characters; development — prefer generated keys). Fails if it belongs to another organization or was revoked |
| `OPENLOG_BOOTSTRAP_API_KEY` | `` | Plaintext read-only API key to ensure (same rules) |

## Fleet updates (`openlog-ingest`, `openlog-api`)

Agent sync, release catalog and rollouts ([releases-updates.md](releases-updates.md) §3–§4). Ingest serves
`POST /v1/openlog/agent/sync` and `GET /v1/openlog/releases/<version>/<name>` on the OTLP/HTTP port (license key
auth); the api serves `/api/v1/fleet/*` and runs the rollout controller on the leader.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_RELEASE_INDEX_URL` | `https://github.com/onuragtas/openlog/releases/latest/download/index.json` | Signed release index (shared with the update check). Manifests are fetched from the index entries' `manifest_url` (+`.sig`) and cached (immutable); the index is re-requested with `If-None-Match` |
| `OPENLOG_RELEASE_TRUSTED_KEYS_FILE` | `` | Extra release public keys, one base64 key per line (shared with the update check). **Without compiled-in keys and without this file the catalog is disabled**: sync never offers updates and the fleet summary reports `catalog.status = disabled` |
| `OPENLOG_RELEASE_MIRROR_DIR` | `` | Local release mirror used **instead of** the index URL (air-gapped): `<dir>/index.json`, `<dir>/index.json.sig`, `<dir>/v<version>/manifest.json(.sig)` and the artifacts. Ingest serves listed artifacts from it at `/v1/openlog/releases/<version>/<name>` (GET/HEAD, range requests; only file names listed in a verified manifest, size and sha256 checked; paths cannot leave the directory) |
| `OPENLOG_RELEASE_SERVE_MIRROR` | `false` | Sync answers point `download_url` at ingest's mirror endpoint instead of the release URL. Requires `OPENLOG_RELEASE_MIRROR_DIR` |
| `OPENLOG_RELEASE_MIRROR_BASE_URL` | `` | External base URL of ingest for mirror links (e.g. `https://ingest.example.com`); empty = scheme (`X-Forwarded-Proto`/TLS) and `Host` of the sync request |
| `OPENLOG_RELEASE_CATALOG_REFRESH` | `15m` | Catalog refresh period (≥ 10s); after a failure it retries every minute. The last verified catalog stays in use; an index with an older `generated_at` is rejected |
| `OPENLOG_FLEET_SYNC_INTERVAL` | `300s` | `poll_interval_seconds` returned to agents (1m–1h) |
| `OPENLOG_FLEET_POLICY_CACHE_TTL` | `15s` | Ingest caches each organization's policy, overrides and current rollout this long (> 0, ≤ 30s) |
| `OPENLOG_FLEET_CONTROLLER_INTERVAL` | `30s` | Rollout controller period on the api leader (wave advance, halt, completion, auto-created rollouts) |
| `OPENLOG_FLEET_HOST_STALE_AFTER` | `24h` | Agents that did not sync for this long are ignored by rollouts and the summary counts |
| `OPENLOG_FLEET_REPORT_QUEUE_SIZE` | `10000` | Sync reports queued per ingest pod for PostgreSQL; when full the oldest is dropped |

Metrics: ingest `openlog_agent_sync_requests_total{code}`, `openlog_agent_sync_decisions_total{reason}`,
`openlog_release_mirror_requests_total{code}`, `openlog_fleet_policy_cache_total{result}`,
`openlog_fleet_host_reports_written_total{result}`, `openlog_fleet_host_reports_dropped_total`,
`openlog_fleet_host_reports_pending`; api (leader) `openlog_fleet_rollout_transitions_total{transition}`,
`openlog_agent_updates_total{result}`, `openlog_fleet_hosts{version}`; both
`openlog_release_catalog_refreshes_total{result}`, `openlog_release_catalog_releases`. With
`OPENLOG_AUTH_MODE=static` sync answers without updates and nothing is stored.

## Versions, release check and updates

Every binary reports the product version (docs/contracts/releases-updates.md §1, §5): `-version`, `"version"` in
the `/readyz` body, `X-Openlog-Version` on every ingest/api/admin HTTP response and `x-openlog-version` gRPC header
metadata. Long-running services (ingest, processor, api, allinone) with `OPENLOG_POSTGRES_DSN` set upsert
`component_heartbeats` every 30 s (own one-connection pool; PostgreSQL outages are tolerated and logged once) and
delete their row on graceful shutdown; `openlog-migrate` uses the rows to gate contract migrations.

`openlog-api` (leader pod only, PostgreSQL advisory lock):

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_UPDATE_CHECK` | `enabled` | `enabled` or `disabled`. Checks the signed release index every `OPENLOG_UPDATE_CHECK_INTERVAL` (after a failure: 6 h or the interval, whichever is shorter) and stores the newest release in PostgreSQL (`system_state`) for `GET /api/v1/version`. No effect in `static` auth mode |
| `OPENLOG_UPDATE_CHECK_INTERVAL` | `24h` | Time between successful release checks (≥ 1m). Shorter values help with a local mirror you publish to (tests, demos) |
| `OPENLOG_RELEASE_INDEX_URL` | `https://github.com/onuragtas/openlog/releases/latest/download/index.json` | Index (or mirror); `<url>.sig` holds the signature |
| `OPENLOG_UPDATE_CHANNEL` | `stable` | `stable` or `beta` (beta also sees stable releases) |
| `OPENLOG_RELEASE_TRUSTED_KEYS_FILE` | `` | Extra trusted Ed25519 public keys (one base64 key per line, `#` comments). Builds without compiled-in keys and without this file report `update_check: failed` |

`openlog-updater` (Compose service in profile `updater`, or the Helm CronJob with `-k8s -once`) additionally reads
`OPENLOG_UPDATER_MODE` (`off`, `notify` (default), `auto`), `OPENLOG_UPDATER_INTERVAL` (`1h`),
`OPENLOG_UPDATER_MAINTENANCE_WINDOW` (UTC, e.g. `sat,sun 02:00-05:00; mon-fri 03:00-04:00`; empty = any time),
`OPENLOG_UPDATER_HEALTH_TIMEOUT` (`5m`), `OPENLOG_UPDATER_IMAGE_REPOSITORY` (mirror repository, digest kept),
Compose: `OPENLOG_UPDATER_SERVICES` (`openlog`), `OPENLOG_UPDATER_HEALTH_URLS` (`http://openlog:9464/readyz`),
`OPENLOG_UPDATER_POSTGRES_SERVICE` (`postgres`), `OPENLOG_UPDATER_PGDUMP_USER` / `_DATABASE` (`openlog`),
`OPENLOG_UPDATER_BACKUP_DIR` (`/backups`), `OPENLOG_UPDATER_BACKUP_KEEP` (`5`), `OPENLOG_UPDATER_ENV_FILE`,
`OPENLOG_UPDATER_COMPOSE_PROJECT` (detected), `DOCKER_HOST` (`unix:///var/run/docker.sock`);
Kubernetes: `OPENLOG_UPDATER_K8S_DEPLOYMENTS`, `OPENLOG_UPDATER_K8S_MIGRATE_TEMPLATE`, `OPENLOG_UPDATER_VERSION_URL`,
`OPENLOG_UPDATER_ROLLOUT_TIMEOUT` (`15m`), `OPENLOG_UPDATER_MIGRATE_TIMEOUT` (`30m`), plus the release variables
above and `OPENLOG_POSTGRES_*` for its status and audit events. Details: [upgrading.md](../operations/upgrading.md).

## Ports summary

| Port | Service |
|---|---|
| 4317 | ingest OTLP/gRPC |
| 4318 | ingest OTLP/HTTP |
| 8080 | api |
| 9464 | admin (health, readiness, metrics) |

## Not yet specified

- Certificate hot reload (a restart is required after rotation).
- Kafka `OAUTHBEARER`/`GSSAPI` SASL mechanisms and ClickHouse HTTPS (the services use the native protocol only).
