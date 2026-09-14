# openlog — `single` profile (Docker Compose)

One Kafka broker (KRaft), one ClickHouse node (embedded Keeper, cluster `openlog` = 1 shard × 1 replica),
PostgreSQL 16 (organizations, users, keys, sessions), a one-shot `bootstrap` container and `openlog-allinone`
(ingest + processor + api in one process). The schema and code are identical to the `cluster` profile;
ClickHouse still uses `ON CLUSTER` DDL and Replicated/Distributed tables.

## Quick start

```sh
cd deploy/compose
cp .env.example .env          # optional: edit the bootstrap owner, keys, credentials, ports
docker compose up -d --build --wait      # first build ~3-5 min (UI + Go), later starts ~20 s
docker compose ps                        # postgres, kafka, clickhouse, openlog healthy; bootstrap exited (0)
curl -s localhost:9464/readyz            # {"clickhouse":"ok",...,"version":"0.0.0-dev"}
```

Open <http://localhost:8080> and sign in as `admin@openlog.local` / `openlog-dev-password`. Send data with the
agent (`install.sh`, [agents/infra](../../agents/infra/README.md)) using endpoint `http://<this host>:4318` and
license key `dev-license-key`, or with the load generator below.

Local agents and demo apps (infra agent hosts, Node/Go/PHP APM services) that send to this stack without running
their own backend: [`test/localagents`](../../test/localagents/docker-compose.yml)
(`docker compose -p openlog-agents -f test/localagents/docker-compose.yml up -d --build`).

> **Project name.** This file declares `name: openlog`, so every `docker compose` command that uses it without
> `-p` acts on the shared `openlog` stack — including `down -v`, which deletes its data. Test suites, demos and
> scripts that reuse this file must always pass their own `-p` (e.g. `-p openlog-e2e`).

`--build` builds `OPENLOG_IMAGE` (`openlog:dev`) from the working tree as version `0.0.0-dev` **without** release
signing keys, so the release check reports `failed` ("no trusted release keys") and fleet updates are disabled.
Released images (`OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:<version>`, no `--build`) have the keys compiled in.
The whole system with signed local releases, agents on Debian hosts, a fleet update and a backend self-update is
scripted in [docs/operations/local-stack-demo.md](../../docs/operations/local-stack-demo.md) (`make stack-demo`).

`./releases` (`OPENLOG_RELEASE_HOST_DIR`) is mounted read-only at `/releases` in `openlog` and `openlog-updater`:
put an air-gapped release mirror (`OPENLOG_RELEASE_MIRROR_DIR=/releases`) or extra trusted keys
(`OPENLOG_RELEASE_TRUSTED_KEYS_FILE=/releases/…`) there. Docker creates the empty directory on first start.

`bootstrap` (`openlog-admin bootstrap`) applies the PostgreSQL migrations and creates — idempotently, on every
start — the organization `OPENLOG_BOOTSTRAP_TENANT_ID` (default `default`), its owner
(`admin@openlog.local` / `openlog-dev-password`) and the ingest license key `dev-license-key`
(plus `OPENLOG_BOOTSTRAP_API_KEY` if set). An existing owner's password is never changed. These defaults are
for local development only: set your own values in `.env`, or leave `OPENLOG_BOOTSTRAP_OWNER_EMAIL` empty and run
`docker compose run --rm --entrypoint openlog-admin bootstrap create-owner --email you@example.com --org "My org"`
(prints a generated password and ingest key once).

Open <http://localhost:8080> and sign in with the owner's email and password. Under **Settings** you can create
ingest license keys (shown once), read-only API keys, invite members and manage your sessions. The session cookie
is not `Secure` in this profile (`OPENLOG_COOKIE_SECURE=false`, plain HTTP); set it to `true` behind TLS.

`openlog-allinone` applies the ClickHouse migrations and creates the Kafka topics on start
(`OPENLOG_MIGRATE_ON_START=true`), then becomes ready (`/readyz`). Topics are created with
`OPENLOG_KAFKA_PARTITIONS`, `OPENLOG_KAFKA_MIN_INSYNC_REPLICAS`, `OPENLOG_KAFKA_RETENTION_MS` and
`OPENLOG_KAFKA_MAX_MESSAGE_BYTES` from `.env`; topics that already exist are never modified
(run `docker compose down -v` to recreate them with new settings).

If Kafka is unavailable, ingest answers `503` with `Retry-After` (OTLP/gRPC: `UNAVAILABLE` with `RetryInfo`).

| Port | Service |
|---|---|
| 4317 | OTLP/gRPC |
| 4318 | OTLP/HTTP |
| 8080 | Query API (`/api/v1/...`) |
| 9464 | Admin: `/healthz`, `/readyz`, `/metrics` |
| 127.0.0.1:9092 | Kafka (for running services from the host) |
| 127.0.0.1:8123 / 9000 | ClickHouse HTTP / native |
| 127.0.0.1:5432 | PostgreSQL (user `openlog`, `POSTGRES_HOST_PORT`) |

## Send data

```sh
# from the repository root
go run ./cmd/openlog-loadgen -endpoint http://localhost:4318 -license-key dev-license-key -hosts 10 -duration 1m
# or inside compose
docker compose --profile loadgen up -d loadgen
```

Any OpenTelemetry SDK/Collector works too: point the OTLP exporter at `http://localhost:4318`
and add the header `openlog-license-key: dev-license-key` (or `Authorization: Bearer dev-license-key`).
A revoked license key is still accepted for up to `OPENLOG_AUTH_CACHE_TTL` (60 s).

## Query

The Query API takes a session cookie (the UI) or a read-only API key — not ingest license keys. Create an API key
under **Settings → API keys** (or set `OPENLOG_BOOTSTRAP_API_KEY`):

```sh
KEY='Authorization: Bearer ola_...'
curl -s -H "$KEY" localhost:8080/api/v1/hosts
curl -s -H "$KEY" "localhost:8080/api/v1/hosts/<host_id>/metrics?name=system.cpu.utilization&group_by=cpu.mode"
curl -s -H "$KEY" localhost:8080/api/v1/hosts/<host_id>/inventory?category=package
curl -s -H "$KEY" localhost:8080/api/v1/hosts/<host_id>/services
curl -s -H "$KEY" "localhost:8080/api/v1/logs?severity_min=WARN&limit=20"
curl -s -H "$KEY" localhost:8080/api/v1/traces/<trace_id>
```

## Updates

Admins see a banner in the UI when a newer signed release exists (`OPENLOG_UPDATE_CHECK`, once a day). To let
openlog update itself, start the optional updater:

```sh
# .env: OPENLOG_UPDATER_MODE=auto (default notify), optional OPENLOG_UPDATER_MAINTENANCE_WINDOW="sat,sun 02:00-05:00"
docker compose --profile updater up -d openlog-updater
```

In `auto` mode it backs up PostgreSQL to `./backups` (`pg_dump -Fc`, newest 5 kept), pulls the image digest from the
signed release manifest, runs `openlog-migrate` with it, recreates `openlog` through the Docker Engine API (same
config, only the image changes), waits for `/readyz` to report the new version and otherwise restores the previous
container. `OPENLOG_IMAGE` in `.env` follows the running version. Status: `GET /api/v1/version` (`updater`) and the
audit log. It mounts `/var/run/docker.sock` and runs as root. Manual upgrades, restore and troubleshooting:
[docs/operations/upgrading.md](../../docs/operations/upgrading.md).

## Tiered storage (S3)

Old parts can move from the ClickHouse volume to S3 while staying queryable ([docs/operations/tiered-storage.md](../../docs/operations/tiered-storage.md)):

```sh
# .env
OPENLOG_CLICKHOUSE_STORAGE_CONFIG=./clickhouse/storage-tiered.xml
OPENLOG_STORAGE_TIERING_ENABLED=true
OPENLOG_S3_ENDPOINT=https://my-bucket.s3.eu-central-1.amazonaws.com/openlog/ch-1/
OPENLOG_S3_ACCESS_KEY_ID=...        # or empty + OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS=true
OPENLOG_S3_SECRET_ACCESS_KEY=...
# local try-out instead of a bucket: COMPOSE_PROFILES=tiered (MinIO, keep the S3 defaults)

docker compose up -d --wait
docker compose exec openlog openlog-admin storage status
```

Move ages per signal: `OPENLOG_STORAGE_COLD_AFTER_DAYS_{METRICS,METRICS_1M,LOGS,TRACES,APM,ALERTS}` (`.env.example`).
Back up ClickHouse with `BACKUP … TO S3` once tiering is on: a copy of the `clickhouse-data` volume only references the
parts on S3.

## Stop

```sh
docker compose down        # keep data
docker compose down -v     # delete Kafka, ClickHouse and PostgreSQL volumes
```

`OPENLOG_AUTH_MODE=static` (with `OPENLOG_LICENSE_KEYS`) skips users entirely: the API then accepts the license
keys directly and the UI cannot sign in. It exists for tests and headless development only.
