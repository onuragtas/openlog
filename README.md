# openlog

Open-source observability platform — infrastructure monitoring, APM, logs and alerts — built from scratch.
Runs as a hosted service or self-hosted, from a single machine up to a horizontally scaled cluster.

> Status: **M1 done, M2 in progress** (alerting, integrations, APM, Go and PHP agents). Not production ready yet.

## Install on a server (Docker Compose, single machine)

This is the `single` profile: PostgreSQL, Kafka, ClickHouse and `openlog-allinone` (ingest + processor + API +
web UI + alert evaluator) on one host. For Kubernetes see [deploy/helm/openlog](deploy/helm/openlog/README.md).

### 1. Requirements

- Linux server with **Docker Engine 24+** and the **Docker Compose v2 plugin** (`docker compose version`)
- **4 vCPU, 8 GB RAM, 50 GB disk** as a starting point (ClickHouse and Kafka are the main consumers)
- Git; no Go or Node needed on the server (the image builds the UI and binaries)
- Free ports **4317** (OTLP/gRPC), **4318** (OTLP/HTTP), **8080** (UI + API) and **9464** (health/metrics).
  PostgreSQL, Kafka and ClickHouse are published on `127.0.0.1` only.

### 2. Get the code and create the configuration

```sh
git clone https://github.com/onuragtas/openlog.git
cd openlog
cp deploy/compose/.env.example deploy/compose/.env
```

Edit `deploy/compose/.env`. **Change every development value** before exposing the server:

| Variable | Set to |
|---|---|
| `OPENLOG_BOOTSTRAP_OWNER_EMAIL` / `OPENLOG_BOOTSTRAP_OWNER_PASSWORD` | your admin login (password ≥ 8 characters) |
| `OPENLOG_BOOTSTRAP_LICENSE_KEY` | ingest key for agents, e.g. `olk_$(openssl rand -hex 24)` |
| `OPENLOG_SECRETS_KEY` | `openssl rand -base64 32` (encrypts alert channel secrets; keep it with your backups) |
| `OPENLOG_POSTGRES_PASSWORD`, `OPENLOG_CLICKHOUSE_PASSWORD` | `openssl rand -hex 16` each |
| `OPENLOG_COOKIE_SECURE` | `true` once the UI is served over HTTPS (see step 5), `false` for plain HTTP |
| `OPENLOG_UPDATE_CHECK` | `disabled` until the first signed GitHub release exists |
| `OPENLOG_RELEASE_MIRROR_DIR`, `OPENLOG_RELEASE_SERVE_MIRROR` | empty / `false` (only for air-gapped release mirrors) |

The bootstrap values are applied **on first start only**; change the password later in the UI
(Settings → Security) and create more ingest keys under Settings → License keys.

### 3. Start

```sh
docker compose -p openlog -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env up -d --build --wait
docker compose -p openlog -f deploy/compose/docker-compose.yml ps     # postgres, kafka, clickhouse, openlog healthy; bootstrap exited (0)
curl -s http://127.0.0.1:9464/readyz                                    # {"clickhouse":"ok",...,"postgres":"ok",...}
```

The first build takes a few minutes. Always pass `-p openlog`: the compose file declares project `openlog`, and a
`docker compose … down -v` without the right project deletes that project's data.

Open `http://<server>:8080` and sign in with the owner email and password.

### 4. Install the infra agent on your hosts

**From a GitHub release** (once releases are published):

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key <OPENLOG_BOOTSTRAP_LICENSE_KEY> --endpoint http://<server>:4318
```

**From source** (until the first release; build on any machine with Go 1.26):

```sh
make -C agents/infra build-linux        # dist/openlog-infra-agent-linux-amd64 and -arm64
```

Copy the binary for the host's architecture to the host, then on the host (from a checkout of this repo):

```sh
sudo useradd --system --home /var/lib/openlog-infra-agent --shell /usr/sbin/nologin openlog-agent
sudo install -d -o openlog-agent -g openlog-agent /opt/openlog/infra-agent/versions/dev /var/lib/openlog-infra-agent
sudo install -m 0755 openlog-infra-agent-linux-amd64 /opt/openlog/infra-agent/versions/dev/openlog-infra-agent
sudo ln -sfn versions/dev /opt/openlog/infra-agent/current
sudo install -d /etc/openlog-infra-agent
sudo install -m 0640 -g openlog-agent agents/infra/packaging/config.example.yaml /etc/openlog-infra-agent/config.yaml
sudoedit /etc/openlog-infra-agent/config.yaml     # license_key: <key>   endpoint: http://<server>:4318
sudo install -m 0644 agents/infra/packaging/systemd/openlog-infra-agent.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now openlog-infra-agent
journalctl -u openlog-infra-agent -f
```

On the openlog server itself use `endpoint: http://127.0.0.1:4318`. The host appears under **Hosts** within about a
minute, with metrics, inventory and discovered services. Integrations (nginx, Redis, MySQL, PostgreSQL) and log
collection are configured in the same file — see [agents/infra/README.md](agents/infra/README.md).
A source-built agent has no release keys compiled in, so it does not update itself; release installs do.

### 5. Applications, HTTPS and firewall

- **Application traces:** any OpenTelemetry SDK works — `OTEL_EXPORTER_OTLP_ENDPOINT=http://<server>:4318`,
  `OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=<key>`. Go: [agents/go](agents/go/README.md).
- **HTTPS:** put a reverse proxy (Caddy, nginx, Traefik) in front of `8080` (UI/API) and `4318` (OTLP/HTTP), then set
  `OPENLOG_COOKIE_SECURE=true` and use `https://` endpoints for agents.
- **Firewall:** expose `8080` to users and `4317`/`4318` to monitored hosts; keep `9464` internal
  (Prometheus scraping / health checks only).

### 6. Update, stop, back up

```sh
git pull
docker compose -p openlog -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env up -d --build --wait
docker compose -p openlog -f deploy/compose/docker-compose.yml down        # stop, keep data
```

Data lives in the Docker volumes `openlog_postgres-data`, `openlog_clickhouse-data` and `openlog_kafka-data`.
Back up PostgreSQL with `docker compose -p openlog -f deploy/compose/docker-compose.yml exec -T postgres pg_dump -U openlog -d openlog -Fc > openlog.dump`.
Upgrade paths, the automatic updater and rollbacks: [docs/operations/upgrading.md](docs/operations/upgrading.md).
More options: [deploy/compose/README.md](deploy/compose/README.md).

## Components

| Path | What |
|---|---|
| `cmd/openlog-ingest` | OTLP gateway (HTTP :4318, gRPC :4317) → Kafka; agent sync and release mirror |
| `cmd/openlog-processor` | Kafka → ClickHouse (direct shard inserts) |
| `cmd/openlog-api` | Query and management API + embedded web UI (:8080) |
| `cmd/openlog-alert` | Alert evaluator and notification dispatcher |
| `cmd/openlog-allinone` | ingest + processor + api + alert in one process (`single` profile) |
| `cmd/openlog-migrate`, `cmd/openlog-admin` | PostgreSQL/ClickHouse migrations and Kafka topics; bootstrap and user admin |
| `cmd/openlog-updater` | Automatic backend updates (Compose / Kubernetes) |
| `cmd/openlog-release`, `cmd/openlog-loadgen` | Signed release tooling; OTLP load generator |
| `schema/clickhouse`, `migrations/postgres` | Database schemas |
| `web/` | Web UI (React + TypeScript) |
| `deploy/compose`, `deploy/helm/openlog` | `single` and `cluster` deployment profiles |
| [`agents/infra`](agents/infra) | Linux host agent: metrics, inventory, discovery, logs, integrations, PHP forwarder (Apache-2.0) |
| [`agents/go`](agents/go) | Go APM agent (OpenTelemetry distribution, Apache-2.0) |
| [`agents/php`](agents/php) | PHP APM agent (C extension, in development, Apache-2.0) |
| `libs/release` | Release manifests and signatures shared by agents and backend (Apache-2.0) |

## Releases

Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`: multi-arch image `ghcr.io/onuragtas/openlog:<v>`, agent
tarballs and deb/rpm, Helm chart, signed `manifest.json`/`index.json` and `install.sh` on a GitHub Release.
One-time repository setup (signing key, secret, variable, permissions): [docs/operations/releasing.md](docs/operations/releasing.md).

## Documentation

- Plan (Turkish): [docs/plan](docs/plan/README.md) — vision, decisions, architecture, cluster, agents, roadmap, releases, M2
- Contracts: [docs/contracts](docs/contracts) — semantic conventions, Kafka, configuration, API, APM, alerting, releases, PHP agent
- Operations: [docs/operations](docs/operations) — releasing, upgrading, scaling, e2e, local stack demo

## License

Backend and UI: [AGPL-3.0](LICENSE). Agents and SDKs: Apache-2.0.
