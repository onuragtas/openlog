# Contract: Service Configuration (v1)

All backend services are configured **only via environment variables** (12-factor), so the same image runs in Compose and Kubernetes.
Durations use Go syntax (`2s`, `500ms`). Lists are comma-separated.

Every variable below can be set in both deployments: Compose passes each one through from `.env`
(`deploy/compose/.env.example` lists them, commented and grouped; empty = the default here) and the Helm chart
(`deploy/helm/openlog/values.yaml`) has a structured value, a component `config` entry or the top-level `config` map
for it, with secrets (SMTP password, key hash secrets, SSO secret keys, CAPTCHA secret, ClickHouse read password, S3
keys, bootstrap keys) in the chart Secret or `auth.existingSecret`. `internal/config/envcoverage_test.go` fails when a
variable read by `internal/config` is missing from this document, Compose or the chart; the few intentionally
unexposed ones (Compose: listen addresses fixed by the port mappings, dependency TLS/SASL of the bundled containers;
Helm: `openlog-allinone`-only switches) are listed there with the reason.

## Common (all services)

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (JSON logs to stdout) |
| `OPENLOG_ADMIN_ADDR` | `:9464` | Admin HTTP: `/healthz`, `/readyz`, `/metrics` (Prometheus). `/readyz` is 200 only when every registered check passes: dependencies (`kafka`, `kafka_topics`, `clickhouse`, `postgres`, …), the service's own port (`ingest_listener`, `api_listener`) and, in `openlog-allinone` with `OPENLOG_MIGRATE_ON_START`, `migrations` (`"applying migrations"`) until the start-up migrations finished |
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
| `OPENLOG_KEY_HASH_SECRET` | `` | `postgres` mode (ingest, api, allinone, admin — **same value everywhere**): ingest license keys and API keys are stored as `HMAC-SHA256(secret, key)` instead of `SHA-256(key)` (D-044). At least 32 bytes (`openssl rand -base64 48`); keep it with your backups. Rows written without a secret keep working and are rewritten to the HMAC on their first successful use (one extra `UPDATE` in the same statement, only on a cache miss). Set it **after** every pod runs a version that knows it (older pods cannot resolve rewritten keys). **Recommended for SaaS** |
| `OPENLOG_KEY_HASH_SECRET_PREVIOUS` | `` | Rotation: the previous secret. Keys hashed with it keep resolving and are rewritten to the current secret on use. Keep it until every active key was used since the rotation (`last_used_at`); removing it earlier makes unused keys invalid. Never drop a secret that still has rows (there is no plaintext to re-hash) |

### TLS and SASL (all services that use the dependency)

Kafka settings apply to every Kafka client (ingest producer, processor consumer group, migrate topic creation);
ClickHouse settings to every native connection (processor bootstrap **and direct shard/replica connections**, api,
migrate); PostgreSQL settings to ingest, api, migrate, allinone and admin. Invalid combinations or unreadable files
stop the service at start-up with a configuration error.

**Certificate rotation without restart (D-048).** The CA, certificate and key files are re-read when their content
changes (checked at most every 10 s, on the next new connection): new Kafka broker, ClickHouse (bootstrap and replica
pools) and PostgreSQL pool connections use the renewed files; open connections keep their certificates until they
close (ClickHouse pools recycle connections after 1 h, PostgreSQL after 5 min idle; Kafka connections are long-lived
and re-dial only after a broker disconnect). Replace the files atomically (Kubernetes Secret volumes and cert-manager
do). A file set that does not load (e.g. a new certificate with the old key while the rotation is in progress) is
logged once (`certificate reload failed; keeping the previous certificates`) and the previous certificates stay in
use until it loads; `certificates reloaded` is logged on success. PostgreSQL: only the `OPENLOG_POSTGRES_TLS_*` files
are watched (not `sslrootcert=` etc. written into the DSN). ingest and api serve plain HTTP/gRPC; TLS towards
clients is terminated by the ingress / Envoy.

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

### ClickHouse read-only user and per-tenant query limits (api, alert, allinone; D-047)

Tenant queries of `openlog-api` and `openlog-alert` (every `/api/v1` telemetry read, alert evaluations and previews)
use a separate connection as a read-only user. The writer (`OPENLOG_CLICKHOUSE_USER`) stays in use for openlog-migrate,
the processor, APM edge linking and alert evaluation history inserts, so api/alert pods still need both.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_CLICKHOUSE_READ_USER` | `` | Read-only user; same addresses, database and TLS as the writer. Empty = queries use `OPENLOG_CLICKHOUSE_USER` and the service logs a warning. At start-up the service warns when the user's `readonly` setting is `0` |
| `OPENLOG_CLICKHOUSE_READ_PASSWORD` | `` | Its password (requires the user) |
| `OPENLOG_QUERY_MAX_MEMORY_USAGE` | `2147483648` | `max_memory_usage` (bytes) of every tenant query; `0` = not set by openlog (the user's profile applies) |
| `OPENLOG_QUERY_MAX_ROWS_TO_READ` | `2000000000` | `max_rows_to_read` (rows read from tables, on every shard); `0` = not set |
| `OPENLOG_QUERY_MAX_BYTES_TO_READ` | `0` | `max_bytes_to_read` (uncompressed); `0` = not set |
| `OPENLOG_QUERY_TENANT_LIMITS` | `` | Per-tenant overrides: `tenant:key=value[;key=value],tenant2:…` with keys `max_memory_usage`, `max_rows_to_read`, `max_bytes_to_read` (unset keys inherit the defaults), e.g. `acme:max_memory_usage=8589934592;max_rows_to_read=0` |

Every tenant query also sets `max_execution_time` (`OPENLOG_API_QUERY_TIMEOUT` / `OPENLOG_ALERT_QUERY_TIMEOUT`),
`log_comment` = `{"component":"api"|"alert","tenant_id":"<tenant>"}` (visible in `system.query_log`) and the native
protocol `quota_key` = tenant id, so a ClickHouse quota with `<keyed/>` on the read user's profile counts every
tenant separately. Overrides are environment-only (there is no organization settings store in PostgreSQL yet).

Errors: a query stopped by `max_memory_usage`, `max_rows_to_read` or `max_bytes_to_read` (ClickHouse codes 241, 158,
307, 396) returns `422` `{"error":{"code":"resource_exhausted"}}` naming the limit; a quota (201), too many simultaneous
queries (202) or the server's total memory limit returns `429 resource_exhausted` with `Retry-After`; `max_execution_time`
stays `504 timeout`. Alert evaluations record `query limit exceeded (<limit>): …` as the evaluation error.
A query that cannot read data from storage — typically parts on S3 after tiered storage moved them (code 499
`S3_ERROR`, 86 `RECEIVED_ERROR_FROM_REMOTE_IO_SERVER`, or socket timeouts / network / Poco exceptions whose message names
S3) — returns `503` `{"error":{"code":"storage_unavailable","retryable":true}}` with `Retry-After` and is logged as
`clickhouse storage error`; alert evaluations record `cold storage unavailable: …`.

The read user needs a profile with `readonly = 2` (reads only; openlog must be able to set the limits above) and
`SELECT` on `openlog.*` only. Compose creates `openlog_reader` from `deploy/compose/clickhouse/openlog-reader.xml`
(password `OPENLOG_CLICKHOUSE_READ_PASSWORD` of the clickhouse container); the Helm chart creates it in the
ClickHouseInstallation (`clickhouse.readUser`). External ClickHouse, SQL-managed users:

```sql
CREATE SETTINGS PROFILE IF NOT EXISTS openlog_readonly ON CLUSTER openlog SETTINGS readonly = 2, allow_ddl = 0;
CREATE USER IF NOT EXISTS openlog_reader ON CLUSTER openlog IDENTIFIED WITH sha256_password BY '<password>'
    SETTINGS PROFILE 'openlog_readonly';
GRANT ON CLUSTER openlog SELECT ON openlog.* TO openlog_reader;
```

## `openlog-ingest`

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_INGEST_HTTP_ADDR` | `:4318` | OTLP/HTTP (`/v1/metrics`, `/v1/logs`, `/v1/traces`; protobuf and JSON; gzip) |
| `OPENLOG_INGEST_GRPC_ADDR` | `:4317` | OTLP/gRPC |
| `OPENLOG_INGEST_MAX_BODY_BYTES` | `10485760` | Max **decompressed** request size; larger → `413` / `RESOURCE_EXHAUSTED` |
| `OPENLOG_INGEST_PRODUCE_TIMEOUT` | `10s` | Kafka produce ack timeout; timeout or Kafka unavailable → `503` / `UNAVAILABLE` with `Retry-After` (D-014) |
| `OPENLOG_INGEST_CORS_ALLOWED_ORIGINS` | `` | CORS for browser OTLP/HTTP senders: comma-separated `*`, exact origins (`https://app.example.com`) or subdomain wildcards (`https://*.example.com`). Empty = disabled. Preflights (`OPTIONS`) from allowed origins get `204` with `Access-Control-Allow-Origin`, the requested headers and `Max-Age 7200`; other origins get `403`. No credentials (auth is by key header). |
| `OPENLOG_RUM_ENABLED` | `true` | Serve `POST /v1/rum` (browser SDK, [rum.md](rum.md), D-136) and the `/api/v1/rum/*` reads. `postgres` mode only: browser keys live in PostgreSQL. Everything else that bounds RUM — origins, rate limit, sample rate — is set per browser key by an admin, not here. Turning this off keeps existing keys but stops accepting their data |
| `OPENLOG_AUTH_CACHE_TTL` | `60s` | `postgres` mode: a resolved license key is re-checked against PostgreSQL after this long. **A revoked key keeps being accepted by an ingest pod for up to this long** (browser keys too, [rum.md](rum.md) §3.5) |
| `OPENLOG_AUTH_NEGATIVE_CACHE_TTL` | `10s` | Unknown keys are re-checked after this long (a newly created key works within this delay on pods that rejected it before) |
| `OPENLOG_AUTH_CACHE_MAX_STALE` | `15m` | While PostgreSQL is unreachable, keys resolved successfully within this window keep being accepted (`0` = never serve stale entries) |

`postgres` mode: ingest also reads `OPENLOG_SECRETS_KEY` and `OPENLOG_SECRETS_KEY_PREVIOUS` (see `openlog-alert`) to
decrypt integration setting passwords for agent sync ([releases-updates.md](releases-updates.md) §3). Use the same
value as on the api pods; without it settings with a password are not delivered (other syncs are unaffected), and an
invalid value stops the service at start-up.

License key is read from header `openlog-license-key`, then `x-api-key` (alias for senders configured for other OTLP backends), then `Authorization: Bearer <key>` (gRPC: metadata with the same names). Unknown/missing/revoked key → `401` / `UNAUTHENTICATED`.

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
| `OPENLOG_API_QUERY_TIMEOUT` | `30s` | Sets ClickHouse `max_execution_time` (per-tenant limits: see "ClickHouse read-only user and per-tenant query limits") |
| `OPENLOG_API_MAX_ROWS` | `10000` | Upper bound for list endpoints |
| `OPENLOG_API_UI_ENABLED` | `true` | Serve the embedded web UI at `/` (SPA fallback for non-`/api` paths) |
| `OPENLOG_COST_ENABLED` | `true` | Serve the infrastructure cost endpoints `/api/v1/costs/*` and the Costs view ([cost.md](cost.md), D-134). The numbers are estimates from a static price table, never billing data |
| `OPENLOG_COST_PRICES_FILE` | `` | JSON file merged over the built-in price table (instance prices, per-vCPU/per-GB fallback rates, region multipliers), so a price can be corrected without waiting for a release. Read once at start-up; a missing, unreadable or invalid file stops the api rather than pricing silently with the numbers the operator meant to replace. Format and an example: [cost.md](cost.md) §2 |
| `OPENLOG_INGEST_PUBLIC_URL` | `` | Public OTLP/HTTP base URL of ingest shown in the UI's **Add data** install commands (`GET /api/v1/onboarding`), e.g. `https://ingest.openlog.example.com:4318`; a path prefix is allowed, credentials/query/fragment/quotes/whitespace are not. Empty = scheme and host of `OPENLOG_PUBLIC_URL` with port `4318`, or without it the host the browser used. Set it when ingest has its own name or port (reverse proxy, load balancer). Only affects displayed commands |
| `OPENLOG_INGEST_PUBLIC_GRPC_URL` | `` | Public OTLP/gRPC endpoint for the same commands, e.g. `https://ingest.openlog.example.com:4317`. Empty = the host of `OPENLOG_INGEST_PUBLIC_URL` (or of its fallback) with port `4317` |
| `OPENLOG_SESSION_TTL` | `168h` | Absolute session lifetime (`postgres` mode) |
| `OPENLOG_SESSION_IDLE_TIMEOUT` | `24h` | A session unused for this long ends (`0` disables); activity is recorded at most once a minute |
| `OPENLOG_COOKIE_SECURE` | `true` | `Secure` attribute of the session cookie. Set `false` only for plain-HTTP development (the API logs a warning) |
| `OPENLOG_COOKIE_DOMAIN` | `` | Cookie `Domain`; empty = host-only cookie (recommended) |
| `OPENLOG_SIGNUP_ENABLED` | `false` | Enables `POST /api/v1/auth/signup` (self-service organization creation, SaaS) |
| `OPENLOG_LOGIN_MAX_FAILURES` | `10` | Failed password checks per (email, client IP) within the window before `429` |
| `OPENLOG_LOGIN_WINDOW` | `15m` | Sliding window for `OPENLOG_LOGIN_MAX_FAILURES` (shared by all api pods through PostgreSQL) |
| `OPENLOG_INVITATION_TTL` | `168h` | Invitation validity |
| `OPENLOG_API_TRUSTED_PROXIES` | `` | CIDRs/addresses of reverse proxies whose `X-Forwarded-For` is trusted for the client IP (rate limiting, sessions, audit log). Empty = use the TCP peer address |
| `OPENLOG_SIGNUP_REQUIRE_VERIFICATION` | automatic | Sign-ups must confirm their e-mail address before creating license/API keys or inviting (D-045). Unset = `true` when e-mail is configured (`OPENLOG_SMTP_HOST` and `OPENLOG_PUBLIC_URL`), else `false`. `true` without them is a configuration error |
| `OPENLOG_EMAIL_VERIFICATION_TTL` | `48h` | Validity of verification links |
| `OPENLOG_SIGNUP_MAX_PER_IP` | `10` | Sign-up attempts per client IP within the window before `429` (every attempt that passes input validation counts, successful or not) |
| `OPENLOG_SIGNUP_MAX_PER_EMAIL` | `5` | Sign-up attempts per e-mail address within the window |
| `OPENLOG_SIGNUP_RATE_WINDOW` | `1h` | Window of both sign-up limits (1s–24h; counters live in `login_failures`, shared by all api pods) |
| `OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS` | `` | Comma-separated e-mail domains refused at sign-up (subdomains included), e.g. disposable address providers |
| `OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS_FILE` | `` | File with one blocked domain per line (`#` comments), merged with the list above; read at start-up (e.g. a mounted disposable-domain list) |
| `OPENLOG_SIGNUP_CAPTCHA_PROVIDER` | `` | `turnstile` (Cloudflare) or `hcaptcha`: sign-up requires a CAPTCHA token verified server-side (`siteverify`, 10 s timeout; provider errors → `503`) |
| `OPENLOG_SIGNUP_CAPTCHA_SECRET` | `` | Provider secret key (required with a provider) |
| `OPENLOG_SIGNUP_CAPTCHA_SITE_KEY` | `` | Public site key, returned by `GET /api/v1/auth/config` for the UI widget (required with a provider) |
| `OPENLOG_SSO_ENABLED` | `true` | Single sign-on endpoints, session policy and SCIM (postgres mode; D-077). SSO also needs `OPENLOG_PUBLIC_URL` (redirect URI, SAML entity ID/ACS URL, SCIM base URL); without it the settings page reports SSO as unavailable |
| `OPENLOG_SSO_SECRET_KEY` | `` | ≥ 32 bytes. Encrypts OIDC client secrets and SAML SP private keys (AES-256-GCM). Empty = key derived from `OPENLOG_KEY_HASH_SECRET`; neither = stored unencrypted (warning). Same value on every api pod |
| `OPENLOG_SSO_SECRET_KEY_PREVIOUS` | `` | Previous key, still accepted for decryption during a rotation (requires `OPENLOG_SSO_SECRET_KEY`) |
| `OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS` | automatic | Identity provider URLs (OIDC issuer, SAML metadata) may use `http` and resolve to loopback/private/link-local addresses. Unset = `true` unless `OPENLOG_SIGNUP_ENABLED=true` (then organization admins are not operators: https and public addresses only, checked at connect time) |
| `OPENLOG_SSO_LOGIN_TTL` | `10m` | Time from "Sign in with SSO" to the callback (1m–1h) |
| `OPENLOG_SSO_CLOCK_SKEW` | `2m` | Tolerated IdP clock difference for ID token `exp`/`iat` and SAML time conditions (0–10m; process-wide) |
| `OPENLOG_SSO_HTTP_TIMEOUT` | `10s` | Timeout of requests to identity providers: discovery, JWKS, token, UserInfo, SAML metadata (1s–1m; bodies ≤ 2 MiB) |
| `OPENLOG_SCIM_ENABLED` | `true` | SCIM 2.0 provisioning at `/api/scim/v2` and SCIM token management (D-078) |

**E-mail (invitations, address verification).** The api sends transactional e-mail through the global SMTP server
of `openlog-alert` (`OPENLOG_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_FROM`, `_TLS`, `_INSECURE_SKIP_VERIFY`;
see below) with links to `OPENLOG_PUBLIC_URL`. Both must be set; otherwise invitations are shared as copyable links
(the UI shows the link) and sign-ups are not verified. Sending is synchronous (15 s timeout) and limited to 100
invitation e-mails per organization and 5 per invited address per hour, and 5 verification e-mails per user per hour.
E-mails are sent as plain text + HTML in English or Turkish, chosen from the request's `Accept-Language` (api.md
"Invitations" → "E-mail language"; templates in `internal/mail/templates`).

**APM** ([apm.md](apm.md)):

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_APM_DEFAULT_APDEX_T` | `500ms` | Apdex T of services without a setting (1ms–10m) |
| `OPENLOG_APM_LINK_ENABLED` | `true` | Run the edge-linking job (trace-linked service map edges). Runs on the api leader (PostgreSQL advisory lock); with `OPENLOG_AUTH_MODE=static` in every api process |
| `OPENLOG_APM_LINK_INTERVAL` | `1m` | Time between runs (≥ 10s) |
| `OPENLOG_APM_LINK_LOOKBACK` | `10m` | Every run recomputes the whole minutes of `[now − lookback, now − delay)`; later spans are linked by the catch-up and the late-span re-link (1m–24h) |
| `OPENLOG_APM_LINK_DELAY` | `1m` | Minutes younger than this are left for the next run (0–1h); keep it above the processor's ingest-to-queryable delay |
| `OPENLOG_APM_LINK_CATCHUP_ENABLED` | `true` | Daily catch-up pass that re-links the previous UTC day (spans later than the lookback), [apm.md](apm.md) §6 |
| `OPENLOG_APM_LINK_CATCHUP_AT` | `03:00` | `HH:MM` UTC when the pass starts (started up to 6 h later, e.g. after a leader change) |
| `OPENLOG_APM_LINK_CATCHUP_BATCH` | `1h` | Window of one catch-up step (5m–6h); steps run one at a time with a 10 s pause. Also the longest window of a re-link pass |
| `OPENLOG_APM_RELINK_ENABLED` | `true` | `openlog-processor`: enqueue the client minutes of late spans into `apm_relink_queue`; api leader: re-link them ([apm.md](apm.md) §6 "Late-span re-link", schema 0020) |
| `OPENLOG_APM_RELINK_AFTER` | `OPENLOG_APM_LINK_LOOKBACK` | `openlog-processor`: minutes older than this when their spans are processed are enqueued (1m – `OPENLOG_APM_LINK_LOOKBACK`; keep it ≤ the api's lookback) |
| `OPENLOG_APM_RELINK_MAX_AGE` | `168h` | Processor: late spans older than this are only counted; api: queued minutes older than this are ignored (1h – 30 days, ≤ `OPENLOG_APM_RETENTION_DAYS`) |
| `OPENLOG_APM_RELINK_INTERVAL` | `5m` | Api leader: time between re-link passes (10s–1h) |
| `OPENLOG_APM_RELINK_MAX_MINUTES` | `120` | Api leader: queued minutes re-linked per pass, oldest first; the rest waits for the next pass (1–1440) |
| `OPENLOG_APM_RETENTION_DAYS` | `30` | `openlog-migrate` / `openlog-allinone` (migrations): TTL of the APM tables (1–3650). A changed value is applied with `ALTER TABLE … ON CLUSTER … MODIFY TTL` after the migrations ([apm.md](apm.md) §8 "Retention"), together with the tiered storage moves of the APM tables (see "Tiered storage") |

The job connects to one replica per shard from `system.clusters` (like direct processor inserts, the replica
`host_name:port` must be reachable from the api; TLS settings apply). Metrics: `openlog_apm_link_runs_total{result}`,
`openlog_apm_link_rows_total`, `openlog_apm_link_duration_seconds`, `openlog_apm_link_lag_seconds`; late-span re-link:
`openlog_apm_relink_runs_total{result}`, `openlog_apm_relink_minutes_total`, `openlog_apm_relink_backlog_minutes`,
`openlog_apm_relink_late_calls_total`, `openlog_apm_relink_duration_seconds` (api), `openlog_apm_relink_enqueued_total`,
`openlog_apm_relink_too_old_total` (processor).

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

### Tiered storage (openlog-migrate, openlog-allinone migrations; D-066, D-067)

Moves old parts of the telemetry tables from the local disk to an optional warm disk and to S3
([tiered-storage.md](../operations/tiered-storage.md)). After the schema migrations, migrate owns the whole TTL of
the managed tables (retention + moves): it checks that every replica defines the storage policy, runs
`ALTER TABLE … ON CLUSTER … MODIFY SETTING storage_policy = '<policy>', materialize_ttl_recalculate_only = 1` for tables
that get a move and `MODIFY TTL … TO VOLUME 'warm'|'cold', … <retention>` for changed TTLs, and records each applied TTL
in `openlog.table_settings` (`ttl:<table>`). Idempotent; nothing runs while tiering was never enabled and
`OPENLOG_APM_RETENTION_DAYS` is unchanged. `-plan` prints the pending ALTERs.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_STORAGE_TIERING_ENABLED` | `false` | Add the moves. `false` after `true` removes the move clauses again; tables keep the policy and parts on S3 stay readable there. Migrate fails with `storage policy "<policy>" is not usable … <host>: …` when a replica lacks the policy or its volumes |
| `OPENLOG_STORAGE_POLICY` | `openlog_tiered` | ClickHouse storage policy: first volume named `default` with disk `default` (required to switch existing tables), `cold`, optional `warm` |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS` | `7` | Days after which parts move to volume `cold`: `metrics_local` (retention 30 d). `0` = no move; a value ≥ the retention is ignored (0–3650) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS_1M` | `30` | `metrics_1m_local` (retention 395 d) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS` | `3` | `logs_local` (14 d) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_TRACES` | `3` | `spans_local`, `trace_index_local` (7 d) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_APM` | `7` | The 8 `apm_*` rollup tables of `OPENLOG_APM_RETENTION_DAYS` |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_ALERTS` | `7` | `alert_evaluations_local` (30 d) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_RUM` | `7` | `rum_page_views_1m_local`, `rum_vitals_1m_local`, `rum_sessions_local` (30 d, D-136) |
| `OPENLOG_STORAGE_WARM_AFTER_DAYS_<CLASS>` | `0` | Same classes: days after which parts move to volume `warm` (must be below the cold age when both are set; requires a `warm` volume on every replica) |

**ClickHouse server variables** (not read by openlog; used by `deploy/compose/clickhouse/storage-tiered.xml` through
`from_env` in the ClickHouse container, and set by the Helm chart from `clickhouse.tieredStorage.s3` in operators mode):

| Variable | Compose default | Description |
|---|---|---|
| `OPENLOG_CLICKHOUSE_STORAGE_CONFIG` | `./clickhouse/storage-none.xml` | Compose only: file mounted as `config.d/openlog-storage.xml`; `./clickhouse/storage-tiered.xml` defines the policy |
| `OPENLOG_CLICKHOUSE_STORAGE_WARM_CONFIG` | `./clickhouse/storage-none.xml` | Compose only: `./clickhouse/storage-tiered-warm.xml` adds the warm volume (Docker volume `clickhouse-warm`) |
| `OPENLOG_CLICKHOUSE_S3_CREDENTIALS_FILE` | `./clickhouse/storage-none.xml` | Compose only: XML with the keys (`storage-tiered-credentials.xml.example`), overrides the two key variables |
| `OPENLOG_S3_ENDPOINT` | `http://minio:9000/openlog-cold/ch-1/` | Bucket URL with a trailing prefix, one prefix per replica (`{shard}`/`{replica}` macros are expanded) |
| `OPENLOG_S3_REGION` | `` | Region; empty = from the endpoint |
| `OPENLOG_S3_ACCESS_KEY_ID` / `OPENLOG_S3_SECRET_ACCESS_KEY` | MinIO profile keys | Static credentials; set both empty for IAM roles |
| `OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS` | `false` | `true`: AWS default credential chain (`AWS_*`, IRSA/web identity, instance profile) |
| `OPENLOG_S3_CACHE_MAX_SIZE` | `10Gi` | Filesystem cache for cold reads (local disk) |
| `OPENLOG_S3_CACHE_PATH` / `OPENLOG_WARM_PATH` | `/var/lib/clickhouse/openlog_s3_cache/` / `/var/lib/clickhouse-warm/` | Cache directory and warm disk path (one level below an existing directory: the image entrypoint creates missing parents as root) |

`COMPOSE_PROFILES=tiered` starts a local MinIO with bucket `openlog-cold` for testing.

## `openlog-allinone`

Runs ingest + processor + api in one process with all variables above (one `OPENLOG_SECRETS_KEY` serves the api,
ingest sync and alerting); one shared admin server.

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
| `storage status [--json]` | ClickHouse only (no PostgreSQL, uses `OPENLOG_CLICKHOUSE_*` writer credentials and `OPENLOG_STORAGE_*`): storage policy volumes, disks per replica, bytes/parts/rows per managed table per volume summed over the replicas, parts past their move TTL still on the hot volume, running moves, failed moves of the last 24 h (`system.part_log`), detached parts by reason and the TTL/policy ALTERs openlog-migrate would still apply ([tiered-storage.md](../operations/tiered-storage.md)) |

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_BOOTSTRAP_TENANT_ID` | `default` | Tenant id of the organization to ensure |
| `OPENLOG_BOOTSTRAP_ORG_NAME` | tenant id | Name used when the organization is created |
| `OPENLOG_BOOTSTRAP_OWNER_EMAIL` | `` | Owner to ensure (created if missing, added as owner if not a member) |
| `OPENLOG_BOOTSTRAP_OWNER_PASSWORD` | `` | Required only when the owner user does not exist yet (≥ 12 characters); an existing user's password is never changed |
| `OPENLOG_BOOTSTRAP_OWNER_NAME` | `` | Display name for a new owner |
| `OPENLOG_BOOTSTRAP_LICENSE_KEY` | `` | Plaintext ingest key to ensure: 8–256 characters of printable ASCII without spaces, quotes or backslashes (keys created in the UI/API with a custom value need ≥ 16). Prefer generated keys. Fails if it belongs to another organization or was revoked |
| `OPENLOG_BOOTSTRAP_API_KEY` | `` | Plaintext read-only API key to ensure (same rules) |

## `openlog-mcp`

Read-only Model Context Protocol server for AI tools ([mcp.md](../operations/mcp.md), D-126). It is a
client of the query API — every tool call is a request to `/api/v1` with an API key — so it reads none of the
common ClickHouse, Kafka or PostgreSQL variables and needs no access to them. `OPENLOG_ADMIN_ADDR` is used by
the `http` transport only. These variables are read by `internal/mcp`, not by `internal/config`, and the server
is not part of the Compose stack or the Helm chart: it runs next to the AI tool (stdio) or as its own
deployment (http).

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_MCP_TRANSPORT` | `stdio` | `stdio` (one local client, over stdin/stdout) or `http` (streamable HTTP at `/mcp`). `-transport` overrides it |
| `OPENLOG_MCP_HTTP_ADDR` | `:8092` | Listen address of the `http` transport (`-addr`). Serves plain HTTP; terminate TLS in front of it |
| `OPENLOG_MCP_API_URL` | `http://localhost:8080` | Base URL of `openlog-api`, e.g. `https://openlog.example.com` — **without** `/api/v1`. http(s) with a host, no credentials, query or fragment |
| `OPENLOG_MCP_API_KEY` | `` | API key (`ola_…`) tool calls act as. Required with `stdio`; with `http` it is only the fallback for requests that carry no `Authorization: Bearer` of their own (a request with neither → `401`). Ingest license keys (`olk_…`) are not API keys |
| `OPENLOG_MCP_ORG_ID` | `` | `X-Openlog-Org-Id` sent with every call, for keys of users in several organizations; empty = the key's organization |
| `OPENLOG_MCP_TIMEOUT` | `60s` | Bounds one API request (a tool call may make two) |
| `OPENLOG_MCP_MAX_ROWS` | `100` | Upper bound for the `limit` argument of the list tools (1–1000), so a model cannot pull an unbounded result into its context |

The organization's query limits, the tenant scope and the read-only ClickHouse user are enforced by
`openlog-api`, not here: the MCP server can reach no data its API key could not.

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
| `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` | `60s` | `poll_interval_seconds` for agents waiting for a later wave of an active rollout (1m–1h, at most `OPENLOG_FLEET_SYNC_INTERVAL`), so "Deploy now" reaches them quickly |
| `OPENLOG_FLEET_POLICY_CACHE_TTL` | `15s` | Ingest caches each organization's policy, overrides and current rollout this long (> 0, ≤ 30s) |
| `OPENLOG_FLEET_CONTROLLER_INTERVAL` | `30s` | Rollout controller period on the api leader (wave advance, halt, completion, auto-created rollouts) |
| `OPENLOG_FLEET_HOST_STALE_AFTER` | `24h` | Agents that did not sync for this long are ignored by rollouts and the summary counts |
| `OPENLOG_FLEET_REPORT_QUEUE_SIZE` | `10000` | Sync reports queued per ingest pod for PostgreSQL; when full the oldest is dropped |

Metrics: ingest `openlog_agent_sync_requests_total{code}`, `openlog_agent_sync_decisions_total{reason}`,
`openlog_release_mirror_requests_total{code}`, `openlog_agent_sync_integrations_config_total{result}`, `openlog_fleet_policy_cache_total{result}`,
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
`OPENLOG_UPDATER_REQUEST_POLL` (`10s`, UI update requests in `update_requests`; Compose only),
`OPENLOG_UPDATER_MAINTENANCE_WINDOW` (UTC, e.g. `sat,sun 02:00-05:00; mon-fri 03:00-04:00`; empty = any time),
`OPENLOG_UPDATER_HEALTH_TIMEOUT` (`5m`), `OPENLOG_UPDATER_IMAGE_REPOSITORY` (mirror repository, digest kept),
Compose: `OPENLOG_UPDATER_SERVICES` (`openlog`), `OPENLOG_UPDATER_HEALTH_URLS` (`http://openlog:9464/readyz`),
`OPENLOG_UPDATER_POSTGRES_SERVICE` (`postgres`), `OPENLOG_UPDATER_PGDUMP_USER` / `_DATABASE` (`openlog`),
`OPENLOG_UPDATER_BACKUP_DIR` (`/backups`), `OPENLOG_UPDATER_BACKUP_KEEP` (`5`), `OPENLOG_UPDATER_ENV_FILE`,
`OPENLOG_UPDATER_COMPOSE_DIR` (directory of the env file), `OPENLOG_UPDATER_COMPOSE_SYNC` (`auto` | `off`),
`OPENLOG_UPDATER_SELF_UPDATE` (`auto`: after an update install-server.sh installations replace the updater container
with the installed image; `on`: every Compose installation; `off`: notice only; D-120),
`OPENLOG_UPDATER_COMPOSE_PROJECT` (detected), `DOCKER_HOST` (`unix:///var/run/docker.sock`);
Kubernetes: `OPENLOG_UPDATER_K8S_DEPLOYMENTS`, `OPENLOG_UPDATER_K8S_MIGRATE_TEMPLATE`, `OPENLOG_UPDATER_VERSION_URL`,
`OPENLOG_UPDATER_ROLLOUT_TIMEOUT` (`15m`), `OPENLOG_UPDATER_MIGRATE_TIMEOUT` (`30m`), plus the release variables
above and `OPENLOG_POSTGRES_*` for its status and audit events. Details: [upgrading.md](../operations/upgrading.md).

## `openlog-alert`

Alert rule evaluation and notification delivery ([alerting.md](alerting.md)). Run any number of replicas (rules are
shared through PostgreSQL leases), or let `openlog-allinone` run it (`OPENLOG_ALERT_ENABLED`). Requires
`OPENLOG_AUTH_MODE=postgres`; uses the common ClickHouse and PostgreSQL variables. `openlog-api` reads the secrets,
public URL, SMTP and limit variables too (channel encryption, test sends, previews); `openlog-ingest` reads the secrets
variables (integration setting passwords in agent sync).

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_ALERT_ENABLED` | `true` | `openlog-allinone` only: run the evaluator and dispatcher in the process |
| `OPENLOG_SECRETS_KEY` | `` | Base64 32-byte AES-256-GCM key for channel secrets and integration setting passwords (`openssl rand -base64 32`). Without it channels cannot be saved, tested or delivered, and integration settings cannot store passwords. Same value on every api, alert and ingest pod |
| `OPENLOG_SECRETS_KEY_PREVIOUS` | `` | Comma-separated old keys still accepted for decryption during a rotation (then run `openlog-alert rotate-secrets`) |
| `OPENLOG_PUBLIC_URL` | `` | Web UI base URL for links in notifications (`https://openlog.example.com`); empty = no links |
| `OPENLOG_ALERT_LEASE_TTL` | `30s` | Rule lease lifetime (≥ 5s): a dead pod's rules move after this long |
| `OPENLOG_ALERT_LEASE_RENEW_INTERVAL` | `10s` | Renew/rebalance period (≤ TTL/2) |
| `OPENLOG_ALERT_EVALUATION_DELAY` | `15s` | Windows end this long before now (ingest-to-queryable delay) |
| `OPENLOG_ALERT_MAX_CONCURRENT_EVALUATIONS` | `16` | Per pod |
| `OPENLOG_ALERT_TENANT_MAX_CONCURRENT` | `4` | Per organization per pod |
| `OPENLOG_ALERT_TENANT_EVALUATIONS_PER_MINUTE` | `600` | Token bucket per organization per pod; over budget = `throttled`, retried |
| `OPENLOG_ALERT_MAX_SERIES_PER_RULE` | `1000` | Series per evaluation (more → evaluation error) |
| `OPENLOG_ALERT_QUERY_TIMEOUT` | `20s` | ClickHouse `max_execution_time` of evaluation and preview queries |
| `OPENLOG_ALERT_MAX_RULES_PER_ORG` | `1000` | api: rules per organization |
| `OPENLOG_ALERT_EVALUATION_HISTORY` | `true` | Write evaluation summaries to ClickHouse `alert_evaluations` (direct shard inserts, batched every 5 s; [alerting.md](alerting.md) §3.6). The replica `host_name:port` of `system.clusters` must be reachable, as for direct processor inserts |
| `OPENLOG_ALERT_DISPATCH_WORKERS` | `4` | Concurrent deliveries per pod |
| `OPENLOG_ALERT_DELIVERY_TIMEOUT` | `10s` | Per HTTP/SMTP delivery attempt |
| `OPENLOG_ALERT_DELIVERY_MAX_ATTEMPTS` | `10` | Then the notification is `failed` (also after 24 h) |
| `OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS` | `false` | Refuse webhook/Slack/Teams/SMTP connections to loopback, private, link-local and CGNAT addresses (checked after DNS resolution). **Set `true` for SaaS** |
| `OPENLOG_SMTP_HOST` / `OPENLOG_SMTP_PORT` | `` / `587` | Global SMTP server for e-mail channels without their own server |
| `OPENLOG_SMTP_USERNAME` / `OPENLOG_SMTP_PASSWORD` | `` | PLAIN auth, only over TLS |
| `OPENLOG_SMTP_FROM` | `` | Required with a host, e.g. `openlog <alerts@example.com>` |
| `OPENLOG_SMTP_TLS` | `starttls` | `starttls` (required), `tls` (implicit, port 465) or `none` (no credentials allowed) |
| `OPENLOG_SMTP_INSECURE_SKIP_VERIFY` | `false` | **Testing only** |

Commands: `openlog-alert rotate-secrets [-check]` (re-encrypt channel secrets with the current key; `-check` exits 3
while channels still use another key). Readiness checks: `postgres`, `clickhouse`. Metrics: alerting.md §8. On SIGTERM
running evaluations and deliveries finish, then the pod releases its leases (grace period ≥ query timeout + delivery
timeout). `openlog-alert` records itself in `component_heartbeats`.

## Tail sampling (`openlog-sampler`; flag also on ingest, processor, api, allinone; D-075)

Off by default; off means no behavior change. `OPENLOG_TAILSAMPLING_ENABLED` must have the same value on ingest (one traces record per trace id), processor (reads `<prefix>.otlp.traces.sampled.v1`), `openlog-sampler` and allinone (runs the sampler in-process). The api only reports it. Operations: docs/operations/tail-sampling.md.

| Variable | Default | Description |
|---|---|---|
| `OPENLOG_TAILSAMPLING_ENABLED` | `false` | Enables the tail sampling stage |
| `OPENLOG_TAILSAMPLING_GROUP` | `openlog-sampler` | Consumer group of the sampler |
| `OPENLOG_TAILSAMPLING_DECISION_WAIT` | `30s` | Time from a trace's first span to its decision (1s–10m) |
| `OPENLOG_TAILSAMPLING_MAX_TRACES` | `100000` | Buffered traces per instance; the oldest is decided early when full |
| `OPENLOG_TAILSAMPLING_MAX_SPANS_PER_TRACE` | `2000` | A trace reaching this is decided early; later spans follow the decision |
| `OPENLOG_TAILSAMPLING_MAX_BUFFERED_BYTES` | `536870912` | Protobuf span bytes buffered per instance (≥ 1 MiB); oldest traces decided early above it |
| `OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL` | `10m` | How long decisions are remembered for late spans (≥ decision wait, ≤ 24h) |
| `OPENLOG_TAILSAMPLING_DECISION_CACHE_SIZE` | `500000` | Remembered decisions per instance |
| `OPENLOG_TAILSAMPLING_POLICY_REFRESH` | `30s` | Reload interval of `tail_sampling_policies` (postgres auth mode; 1s–1h) |
| `OPENLOG_TAILSAMPLING_PRODUCE_TIMEOUT` | `10s` | Produce timeout per attempt to the sampled topic (retried) |
| `OPENLOG_TAILSAMPLING_DEFAULT_POLICY` | empty | JSON policy (apm.md §4.2) for tenants without a stored policy; empty keeps everything. Static auth mode uses only this |

## Usage, plans and billing

Usage metering is always on (schema `0050_usage`, processor accounting). Plans, quotas and billing: [usage.md](usage.md),
operator guide [docs/operations/saas.md](../operations/saas.md), D-079–D-081. Self-hosted defaults enforce nothing.

| Variable | Default | Services | Description |
|---|---|---|---|
| `OPENLOG_SAAS_MODE` | `false` | ingest, api, migrate, allinone | Hard enforcement: ingest `429` over the monthly quota and per-tenant rate limits; default of per-tenant retention. Requires `OPENLOG_AUTH_MODE=postgres` |
| `OPENLOG_PLANS` | empty | api, migrate, allinone | Plan catalog JSON (usage.md §4.1). Empty (and no file): one `unlimited` plan |
| `OPENLOG_PLANS_FILE` | empty | api, migrate, allinone | File with the plan catalog; mutually exclusive with `OPENLOG_PLANS` |
| `OPENLOG_DEFAULT_PLAN` | catalog `default`, else first plan | api, migrate | Plan of organizations without an assignment |
| `OPENLOG_SUPERADMIN_EMAILS` | empty | api | Comma-separated e-mail addresses of SaaS operators allowed to assign plans to any organization |
| `OPENLOG_USAGE_EVALUATION_INTERVAL` | `1m` | api | Quota evaluation interval of the leader (10s–1h) |
| `OPENLOG_USAGE_QUERY_COLLECTION_ENABLED` | `true` | api | Collect query compute from `system.query_log` into `usage_queries_1h` (leader, every 15m) |
| `OPENLOG_USAGE_NOTIFY_THRESHOLDS` | `80,100` | api | Percentages that e-mail owners (once per billing period); the lowest below 100 is the warning level |
| `OPENLOG_QUOTA_REFRESH_INTERVAL` | `30s` | ingest | Reload interval of `tenant_quota_status` (1s–10m) |
| `OPENLOG_QUOTA_INGEST_PODS` | `0` | ingest | Divides tenant rate limits among pods; `0` = live ingest/allinone instances from `component_heartbeats` |
| `OPENLOG_QUOTA_BLOCKED_RETRY_AFTER` | `5m` | ingest | `Retry-After` of requests over the monthly quota (1s–24h) |
| `OPENLOG_QUOTA_RETENTION_ENABLED` | `OPENLOG_SAAS_MODE` | api, migrate, allinone | Per-tenant retention: migrate widens raw table TTLs to the longest plan retention, the api leader deletes older data of shorter-retention tenants (D-081) |
| `OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS` | `20` | api | Per-tenant retention mutations submitted per hourly run (1–1000) |
| `OPENLOG_BILLING_PROVIDER` | `none` | api | `none` or `noop` (no real provider yet; saas.md §6) |
| `OPENLOG_BILLING_PUSH_AT` | `02:00` | api | Daily usage push time (HH:MM UTC) |

Owner e-mails use `OPENLOG_SMTP_*` and link to `OPENLOG_PUBLIC_URL`.

### SaaS operations (operator console, lifecycle, hard limits, abuse detection; D-105, D-106)

The operator console (`/operator`, `OPENLOG_SUPERADMIN_EMAILS`) works in every postgres-mode installation. Suspension
enforcement, hard host/user limits, trials and abuse detection only run with `OPENLOG_SAAS_MODE=true`
([saas.md](../operations/saas.md) §8–§11). Ingest reuses `OPENLOG_QUOTA_REFRESH_INTERVAL` for suspension and host limits.

| Variable | Default | Services | Description |
|---|---|---|---|
| `OPENLOG_SAAS_HOST_SYNC_INTERVAL` | `1m` | api | How often the leader writes host limits and active host ids for ingest (10s–1h) |
| `OPENLOG_SAAS_SIGNUP_TRIAL_PLAN` | empty | api | Plan (with `trial_days`) whose trial new sign-up organizations get; empty = no automatic trial |
| `OPENLOG_SAAS_TRIAL_NOTIFY_DAYS` | `7,3,1` | api | Days before a trial ends that e-mail the owners (1–90 each) |
| `OPENLOG_SAAS_LIFECYCLE_INTERVAL` | `5m` | api | Trial job interval: sign-up trials, reminder e-mails, fallback plan at the end (10s–1h) |
| `OPENLOG_SAAS_SUPPORT_SESSION_TTL` | `2h` | api | Maximum length of one operator support view (5m–24h; never beyond the owner's grant) |
| `OPENLOG_SAAS_AUTO_SUSPEND` | `false` | api | Suspend an organization automatically when the abuse detector raises a new flag |
| `OPENLOG_SAAS_ABUSE_INTERVAL` | `10m` | api | Abuse detector interval (1m–24h) |
| `OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER` | `10` | api | Flag ingest of one hour above this multiple of the plan's hourly share (`ingest_gb_month / 730`); `0` disables |
| `OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS` | `7` | api | Age below which an organization counts as new (1–365) |
| `OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS` | `50` | api | Flag new organizations with more active hosts; `0` disables |
| `OPENLOG_SAAS_ABUSE_SOURCE_IPS` | `200` | api | Flag tenants whose license keys were used from more distinct client addresses within an hour (per ingest instance); `0` disables |

### Data subject requests and status page (api, allinone; D-107, D-108)

Exports, account deletion and organization deletion ([saas.md](../operations/saas.md) §12) exist in every postgres-mode
installation; the api leader runs the export queue and the hard deletion job. E-mails use `OPENLOG_SMTP_*` and links
`OPENLOG_PUBLIC_URL`. With tiered storage enabled (`OPENLOG_STORAGE_TIERING_ENABLED=true`) and no
`OPENLOG_DATA_EXPORT_S3_URL`, the export reuses the bucket of `OPENLOG_S3_ENDPOINT` (prefix `openlog-exports/`) and,
when the export has no keys of its own, `OPENLOG_S3_REGION`, `OPENLOG_S3_ACCESS_KEY_ID` and
`OPENLOG_S3_SECRET_ACCESS_KEY`. Without static keys the export client uses IAM credentials (D-116): the standard
`AWS_*` variables of the process environment, EKS IRSA (`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`), ECS task roles
and EKS Pod Identity (`AWS_CONTAINER_CREDENTIALS_*`) and the EC2 instance profile (IMDSv2).

| Variable | Default | Services | Description |
|---|---|---|---|
| `OPENLOG_ORG_DELETION_GRACE` | `168h` | api | Grace period of an organization deletion in which an owner can cancel it (0–2160h); operators may delete immediately |
| `OPENLOG_DATA_EXPORT_ENABLED` | `true` | api | Offer organization and personal data exports |
| `OPENLOG_DATA_EXPORT_STORAGE` | `auto` | api | `auto` (S3 when an S3 URL is known, else local), `local` or `s3`. Use S3 with more than one api pod: the leader writes the archive, any pod serves the download |
| `OPENLOG_DATA_EXPORT_LOCAL_PATH` | `/tmp/openlog-exports` | api | Absolute directory of local archives and of the temporary archive file while an export is built (also with S3). Compose: `/var/lib/openlog/exports`, the volume `data-exports` (the image creates it owned by uid 10001); Helm: `/tmp/openlog-exports` on the pod's `emptyDir` (use S3 with more than one api replica) |
| `OPENLOG_DATA_EXPORT_S3_URL` | empty | api | Object base URL with bucket and prefix, ending with `/` (path-style `https://s3.example.com/bucket/exports/` or virtual-hosted `https://bucket.s3.eu-west-1.amazonaws.com/exports/`) |
| `OPENLOG_DATA_EXPORT_S3_REGION` | `OPENLOG_S3_REGION`, else `us-east-1` | api | Signing region (AWS Signature Version 4) |
| `OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID` / `OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY` | `OPENLOG_S3_ACCESS_KEY_ID` / `OPENLOG_S3_SECRET_ACCESS_KEY` | api | Static credentials (set together) of `OPENLOG_DATA_EXPORT_S3_CREDENTIALS=static` |
| `OPENLOG_DATA_EXPORT_S3_CREDENTIALS` | `static` when static keys are set (own or `OPENLOG_S3_*`), else `auto` | api | `static`: the keys above (required). `auto`: AWS credential chain without the SDK, first found wins: environment (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`) → web identity (`AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_ROLE_ARN`, `AWS_ROLE_SESSION_NAME`; STS endpoint from `AWS_REGION`/`AWS_DEFAULT_REGION`, else the signing region, and `AWS_STS_REGIONAL_ENDPOINTS`) → container (`AWS_CONTAINER_CREDENTIALS_RELATIVE_URI`, or `AWS_CONTAINER_CREDENTIALS_FULL_URI` — https or a loopback/ECS/EKS link-local host — with `AWS_CONTAINER_AUTHORIZATION_TOKEN(_FILE)`) → EC2 instance profile via IMDSv2 (`AWS_EC2_METADATA_DISABLED=true` skips it). Temporary credentials are refreshed 5 minutes before they expire and sent with `X-Amz-Security-Token`; when no source has credentials, requests are unsigned (as before). Explicit `auto` does not borrow `OPENLOG_S3_*` keys and rejects `OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID`/`_SECRET_ACCESS_KEY`. Shared config/credentials files, SSO and process credentials are not supported (D-116) |
| `OPENLOG_DATA_EXPORT_TTL` | `168h` | api | How long an archive and its e-mailed download link stay available (1h–720h); expired archives are deleted |
| `OPENLOG_DATA_EXPORT_MAX_BYTES` | `4294967296` | api | Archive size limit (1 MiB–5 GiB, one S3 PUT); telemetry stops (`truncated`) before it is reached |
| `OPENLOG_DATA_EXPORT_MAX_ROWS` | `50000000` | api | Telemetry rows per export (≥ 1000) |
| `OPENLOG_DATA_EXPORT_MAX_RANGE` | `744h` | api | Longest telemetry time range of one export (1h–9600h) |
| `OPENLOG_DATA_EXPORT_ROWS_PER_SECOND` | `200000` | api | Throttle of telemetry reads from ClickHouse; `0` = unthrottled (queries also run with `max_threads=2`) |
| `OPENLOG_STATUS_PAGE_ENABLED` | `OPENLOG_SAAS_MODE` | api | Public status page: `/status`, `GET /api/v1/status` and the leader's one-minute self-checks |

### Synthetic monitoring (api, allinone; D-132)

Scheduled outside-in HTTP checks ([api.md](api.md#synthetic-monitoring)). Definitions are per organization in
PostgreSQL (`0090_synthetics`); the api **leader** runs the due ones, so the limits below bound one process, not the
cluster. Every run is stored in ClickHouse (`synthetic_runs`, 30 days) and mirrored as the gauges
`synthetics.check.success` and `synthetics.check.duration`, which metric alert rules and dashboards use like any
other metric. Postgres auth mode only.

A check is a request the server makes on behalf of an organization **member**, so the address it resolves to is
checked on every connection (after DNS resolution, so a rebinding answer is caught too) and redirects are limited
and re-validated: without `OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS` only public addresses may be reached, which
keeps loopback, private ranges, CGNAT and the link-local cloud metadata endpoints out of reach. The default follows
`OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS`: a self-hosted installation may check its own internal services, a sign-up
installation may not.

| Variable | Default | Services | Description |
|---|---|---|---|
| `OPENLOG_SYNTHETICS_ENABLED` | `true` | api | Offer `/api/v1/synthetics/*` and run the scheduler on the leader |
| `OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS` | `true` unless `OPENLOG_SIGNUP_ENABLED=true` | api | Let checks reach private, loopback, CGNAT and link-local addresses (SSRF protection when unset) |
| `OPENLOG_SYNTHETICS_MAX_CONCURRENT` | `20` | api | Concurrent runs of all organizations together on the leader (1–1000) |
| `OPENLOG_SYNTHETICS_TENANT_MAX_CONCURRENT` | `5` | api | Concurrent runs of one organization (1–`OPENLOG_SYNTHETICS_MAX_CONCURRENT`), so one tenant cannot use the whole pool |
| `OPENLOG_SYNTHETICS_MAX_RESPONSE_BYTES` | `1048576` | api | Response body one run reads (1024–67108864); a larger response fails the run instead of being truncated |
| `OPENLOG_SYNTHETICS_MAX_REDIRECTS` | `5` | api | Redirects one run follows (0–10); each hop is re-validated |

## Cloud connections (api, allinone; D-135)

Metrics of managed cloud services ([api.md](api.md#cloud-connections)). Connections are stored per
organization in PostgreSQL (`0092_cloud_connections`); the api **leader** polls the due scopes, so the limits
below bound one process, not the cluster. Every collected data point is written to ClickHouse `metrics` like
an agent's, so nothing else has to be configured for alerting or dashboards. Postgres auth mode only.

Credentials are encrypted with **`OPENLOG_SECRETS_KEY`** (the same key as alert channel secrets and
integration settings). Without it a connection cannot be saved with credentials and nothing is polled; the
UI says so rather than failing silently.

These provider APIs are billed per request and rate-limit hard, so the cost controls are per connection
rather than global: `max_metrics_per_poll` and `max_api_calls_per_poll` are fields of the connection
(defaults 5000 and 200), and a poll that reaches one is reported as `partial`. The variables below bound how
much the leader does at once.

| Variable | Default | Services | Description |
|---|---|---|---|
| `OPENLOG_CLOUD_ENABLED` | `true` | api | Offer `/api/v1/cloud/*` and run the poller on the leader |
| `OPENLOG_CLOUD_MAX_CONCURRENT` | `10` | api | Concurrent scope polls of all organizations together on the leader (1–1000) |
| `OPENLOG_CLOUD_TENANT_MAX_CONCURRENT` | `4` | api | Concurrent polls of one organization (1–`OPENLOG_CLOUD_MAX_CONCURRENT`), so one tenant cannot use the whole pool |
| `OPENLOG_CLOUD_CONNECTION_MAX_CONCURRENT` | `2` | api | Concurrent polls of one connection (1–`OPENLOG_CLOUD_TENANT_MAX_CONCURRENT`), so a connection covering many regions does not burst requests at one cloud account and trip its rate limits |
| `OPENLOG_CLOUD_REQUEST_TIMEOUT` | `30s` | api | Bound for one provider HTTP request (1s–5m) |

## Report chart images (`openlog-renderer`, api, allinone; D-097)

Optional PNG widget images in scheduled report e-mails ([operations/reports.md](../operations/reports.md)). Without
`OPENLOG_RENDERER_URL` reports keep their HTML tables and no render endpoint exists. `openlog-renderer` ships as its own
image (`ghcr.io/onuragtas/openlog-renderer`, headless Chromium); it needs no Kafka, ClickHouse or PostgreSQL.

| Variable | Default | Service | Description |
|---|---|---|---|
| `OPENLOG_RENDERER_URL` | `` | api | Render API base URL, e.g. `http://openlog-renderer:8090` (`https://` with TLS). Requires `OPENLOG_RENDERER_TOKEN` and `OPENLOG_KEY_HASH_SECRET` or `OPENLOG_SECRETS_KEY` (render tokens are signed with a key derived from them; same value on every api pod) |
| `OPENLOG_RENDERER_TOKEN` | `` | api, renderer | Shared secret of the render API (≥ 32 characters, `openssl rand -hex 32`); required by `openlog-renderer` |
| `OPENLOG_RENDERER_TIMEOUT` | `2m` | api | Bound for rendering the images of one report (≥ 5s); on timeout or any renderer error the e-mail is sent with tables |
| `OPENLOG_RENDERER_TLS_CERT_FILE` / `OPENLOG_RENDERER_TLS_KEY_FILE` | `` | api, renderer | Optional PEM files: the renderer's server certificate on `openlog-renderer`, the api's client certificate on api (set together) |
| `OPENLOG_RENDERER_TLS_CA_FILE` | `` | api, renderer | On `openlog-renderer`: CA of accepted client certificates (client certificates become required: mTLS; needs the server certificate). On api: CA that verifies the renderer |
| `OPENLOG_RENDERER_ADDR` | `:8090` | renderer | Listen address of the render API |
| `OPENLOG_RENDERER_UI_ORIGIN` | `` | renderer | Required. Web UI origin the browser loads the print view from, e.g. `http://openlog-api:8080` (scheme, host, port only). Every request of the page to another origin is refused, DNS resolves only this host |
| `OPENLOG_RENDERER_MAX_CONCURRENCY` | `2` | renderer | Simultaneous renders (1–64); each runs its own Chromium process (≈ 200–400 MiB) |
| `OPENLOG_RENDERER_QUEUE_TIMEOUT` | `30s` | renderer | How long a request waits for a free slot before `429` |
| `OPENLOG_RENDERER_RENDER_TIMEOUT` | `60s` | renderer | Bound for one render: browser start, page load, captures (≥ 5s); then `502` and the browser is killed |
| `OPENLOG_RENDERER_MAX_IMAGE_BYTES` | `1048576` | renderer | Largest PNG per widget (16 KiB–16 MiB); larger captures are reported as element errors. The api additionally embeds ≤ 1 MiB per image and ≤ 10 MiB per e-mail |
| `OPENLOG_RENDERER_CHROMIUM_PATH` | `` | renderer | Chromium binary; empty = `chromium`, `chromium-browser`, `google-chrome` or `headless-shell` from `PATH` (the image sets `/usr/bin/chromium`) |
| `OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX` | `false` | renderer | Run Chromium with `--no-sandbox`. Only in a locked-down container (non-root, read-only root filesystem, no capabilities, no new privileges, egress to the UI origin only); the Compose profile and the Helm chart set `true` with those restrictions because default seccomp profiles refuse the sandbox's user namespaces |

`openlog-renderer` endpoints: `POST /v1/render` (render API, [operations/reports.md](../operations/reports.md)) and the
admin port (`OPENLOG_ADMIN_ADDR`: `/healthz`, `/readyz` with checks `listener` and `chromium`, `/metrics` with
`openlog_renderer_renders_total{result}` and `openlog_renderer_render_duration_seconds`).

## Ports summary

| Port | Service |
|---|---|
| 4317 | ingest OTLP/gRPC |
| 4318 | ingest OTLP/HTTP |
| 8080 | api |
| 8090 | openlog-renderer render API (internal only) |
| 9464 | admin (health, readiness, metrics) |

## Not yet specified

- Kafka `OAUTHBEARER`/`GSSAPI` SASL mechanisms and ClickHouse HTTPS (the services use the native protocol only).
