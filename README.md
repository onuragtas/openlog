# openlog

Open-source observability platform — infrastructure monitoring, APM, logs and alerts — built from scratch.
Runs as a hosted service or self-hosted, from a single machine up to a horizontally scaled cluster.

> Status: **v1.0 development complete** (M0–M4: infrastructure, logs, APM with Go/Node.js/Python/Java/.NET/PHP
> agents, alerting, OQL dashboards, Kubernetes, SSO/SCIM, quotas, tiered storage). Long-running validation and the
> operational release steps are listed in [the roadmap](docs/plan/06-roadmap.md#v10--tamamlanma-kriterleri).

## Install on a server (Docker Compose, single machine)

This is the `single` profile: PostgreSQL, Kafka, ClickHouse and `openlog-allinone` (ingest + processor + API +
web UI + alert evaluator) on one host. For Kubernetes see [deploy/helm/openlog](deploy/helm/openlog/README.md).

### Quick install (one command)

On a Linux server with Docker Engine and the Compose v2 plugin (or add `--install-docker`):

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --email you@example.com
```

It runs the signed release image `ghcr.io/onuragtas/openlog:<version>` with **automatic updates enabled**
(`openlog-updater` in `auto` mode: PostgreSQL backup, migrations, health check, rollback on failure). Steps:

1. checks Docker, Compose v2, RAM, disk and free ports (warnings only for RAM, disk and ports);
2. resolves the latest stable release (or `--version`) and downloads its compose files `openlog-compose-<version>.tar.gz`
   (sha256 checked against the release manifest; older releases: `deploy/compose` of the tag's source archive; no git);
3. creates `/opt/openlog-server/.env` (mode 0600) with random PostgreSQL/ClickHouse passwords,
   `OPENLOG_SECRETS_KEY`, an `olk_…` license key and your owner login;
4. starts the stack and the updater (`docker compose -p openlog …`) and waits until `/readyz` answers;
5. prints the UI URL, the license key, the agent install command and backup instructions.

Asked on the terminal when not given: the owner email and password (empty = generate one, printed once).
Re-running is safe: `.env` and all secrets are kept, missing settings are added, the compose files are replaced by the
requested/latest release and the stack is updated. The updater keeps the compose files at the running version (it
installs each release's verified compose bundle; changes it cannot apply, such as new volumes, are shown in
Settings → Organization → Version and updates with "re-run install-server.sh"). Put every setting in
`/opt/openlog-server/.env`: a `docker-compose.override.yml` is not used by the installer or the updater.

Running a stack from a git clone? [Migrate it to this layout](docs/operations/upgrading.md#migrating-from-a-git-clone-installation)
(same project `openlog`, volumes and data are kept).

| Flag | Default | Meaning |
|---|---|---|
| `--email EMAIL` | prompt | owner (admin) email; required without a terminal |
| `--password PASS` | prompt, else generated | owner password, 8–256 characters (env `OPENLOG_OWNER_PASSWORD` keeps it out of `ps`) |
| `--version X.Y.Z` | latest on the channel | release to install; an older version than the running one is refused |
| `--channel stable\|beta` | `stable` | release channel |
| `--dir DIR` | `/opt/openlog-server` | installation directory (`docker-compose.yml`, `.env`, `backups/`, `releases/`, `.bundle-version`) |
| `--updater auto\|notify\|off` | `auto` | `notify` only reports new releases in the UI |
| `--domain HOST` | – | public address behind your TLS reverse proxy: sets `OPENLOG_PUBLIC_URL=https://HOST` and `OPENLOG_COOKIE_SECURE=true` and prints a Caddy example (`http://HOST` keeps plain HTTP) |
| `--cors-origins LIST` | – | browser OTLP origins for ingest `:4318` |
| `--project NAME` | `openlog` | compose project name |
| `--index-url URL` | GitHub `index.json` | release index (mirror) |
| `--bundle-url URL` | the release's compose bundle | `tar.gz` with `openlog-compose-<v>/` or `deploy/compose/` (mirror) |
| `--install-docker` | off | install Docker with `get.docker.com` when missing |
| `--no-start` | off | only write the files |

Then continue with [4. Install the infra agent](#4-install-the-infra-agent-on-your-hosts) (the installer prints the
command) and [5. Applications, HTTPS and firewall](#5-applications-https-and-firewall). Manage the stack with
`docker compose -p openlog -f /opt/openlog-server/docker-compose.yml --env-file /opt/openlog-server/.env …`.

### Manual install

The same result step by step, or from source.

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

Generate the random values in a terminal and paste the **output** into the file (`.env` does not run commands):

```sh
echo "olk_$(openssl rand -hex 24)"   # OPENLOG_BOOTSTRAP_LICENSE_KEY
openssl rand -base64 32              # OPENLOG_SECRETS_KEY
openssl rand -hex 16                 # OPENLOG_POSTGRES_PASSWORD (run again for OPENLOG_CLICKHOUSE_PASSWORD)
```

Edit `deploy/compose/.env`. **Change every development value** before exposing the server:

| Variable | Set to |
|---|---|
| `OPENLOG_BOOTSTRAP_OWNER_EMAIL` / `OPENLOG_BOOTSTRAP_OWNER_PASSWORD` | your admin login (password: 8–256 characters) |
| `OPENLOG_BOOTSTRAP_LICENSE_KEY` | ingest key for agents (generated above) |
| `OPENLOG_SECRETS_KEY` | generated above; encrypts alert channel secrets — keep it with your backups |
| `OPENLOG_POSTGRES_PASSWORD`, `OPENLOG_CLICKHOUSE_PASSWORD` | generated above. Set them **before the first start**: PostgreSQL and ClickHouse only apply them when their volumes are created |
| `OPENLOG_COOKIE_SECURE` | `true` once the UI is served over HTTPS (see step 5), `false` for plain HTTP |
| `OPENLOG_UPDATE_CHECK` | `enabled` (checks GitHub releases for new versions; see step 6) |
| `OPENLOG_RELEASE_MIRROR_DIR`, `OPENLOG_RELEASE_SERVE_MIRROR` | empty / `false` (only for air-gapped release mirrors) |

The bootstrap values are applied **on first start only**; change the password later in the UI
(Settings → Security) and create more ingest keys under Settings → License keys.

Moving from another backend? Under Settings → License keys, turn on **Use my own key value** to import the key
your applications already send (in the `openlog-license-key`, `x-api-key` or `Authorization: Bearer` header), so
they keep working without being redeployed. Only a hash of the value is stored.

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
minute, with metrics, inventory and discovered services. Integrations (nginx, Redis, MySQL, PostgreSQL, SQL Server, IIS) and log
collection are configured in the same file — see [agents/infra/README.md](agents/infra/README.md).
A source-built agent has no release keys compiled in, so it does not update itself; release installs do.

**Guided install in the UI:** open **Add data** (top bar, or `http://<server>:8080/add-data`). It builds the install
commands for Linux hosts (install.sh/deb/rpm/tarball), the Docker container agent, Kubernetes (Helm), the APM agents
(Go, Node.js, Python, Java, .NET, PHP), logs, OpenTelemetry SDKs/Collector and integrations with this server's
endpoint prefilled, can create a license key for the install (admins; shown once) and waits until the data arrives.
Behind a reverse proxy or with a separate ingest name, set `OPENLOG_INGEST_PUBLIC_URL` (docs/contracts/config.md).

### 5. Applications, HTTPS and firewall

- **Application traces:** any OpenTelemetry SDK works — `OTEL_EXPORTER_OTLP_ENDPOINT=http://<server>:4318`,
  `OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=<key>`. Go: [agents/go](agents/go/README.md), Node.js:
  [agents/node](agents/node/README.md), Java: [agents/java](agents/java/README.md), .NET: [agents/dotnet](agents/dotnet/README.md), Python: [agents/python](agents/python/README.md).
- **HTTPS:** put a reverse proxy (Caddy, nginx, Traefik) in front of `8080` (UI/API) and `4318` (OTLP/HTTP), then set
  `OPENLOG_COOKIE_SECURE=true` and use `https://` endpoints for agents.
- **Firewall:** expose `8080` to users and `4317`/`4318` to monitored hosts; keep `9464` internal
  (Prometheus scraping / health checks only).

### 6. Update, stop, back up

**Automatic updates (recommended):** run the signed release image instead of a source build, and start the updater.
In `deploy/compose/.env` set `OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:<latest version>`,
`OPENLOG_UPDATE_CHECK=enabled` and `OPENLOG_UPDATER_MODE=auto` (`notify` only reports new versions), then:

```sh
docker compose -p openlog -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env up -d --wait
docker compose -p openlog -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env --profile updater up -d openlog-updater
```

The updater backs up PostgreSQL, runs migrations, recreates the containers on the new image and rolls back if the
health check fails. It never changes the files of a git clone: when `deploy/compose` is older than the running version
the Version page says so — `git checkout v<version>` and run `docker compose … up -d` again, or
[migrate to install-server.sh](docs/operations/upgrading.md#migrating-from-a-git-clone-installation), which keeps the
compose files in sync automatically. Agents installed from a release update themselves following the fleet policy (Fleet page).
A source build (`--build`) also trusts the official release key (`release-public-keys.txt`), so version checks and
agent updates work, and the updater moves it to the release image on the next version.

**Manual update from source:**

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
| [`agents/node`](agents/node) | Node.js APM agent `openlog-node` (OpenTelemetry distribution, Apache-2.0) |
| [`agents/java`](agents/java) | Java APM agent `openlog-javaagent.jar` (OpenTelemetry Java agent distribution, Apache-2.0) |
| [`agents/dotnet`](agents/dotnet) | .NET APM agent, NuGet `OpenLog.Agent` (OpenTelemetry .NET distribution, Apache-2.0) |
| [`agents/python`](agents/python) | Python APM agent, PyPI `openlog-agent` (OpenTelemetry Python distribution, Apache-2.0) |
| [`agents/php`](agents/php) | PHP APM agent (C extension, in development, Apache-2.0) |
| `libs/release` | Release manifests and signatures shared by agents and backend (Apache-2.0) |

## Releases

Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`: multi-arch image `ghcr.io/onuragtas/openlog:<v>`, agent
tarballs and deb/rpm, Helm chart, signed `manifest.json`/`index.json`, `install.sh` and `install-server.sh` on a GitHub Release.
One-time repository setup (signing key, secret, variable, permissions): [docs/operations/releasing.md](docs/operations/releasing.md).

## Documentation

- Plan (Turkish): [docs/plan](docs/plan/README.md) — vision, decisions, architecture, cluster, agents, roadmap, releases, M2
- Contracts: [docs/contracts](docs/contracts) — semantic conventions, Kafka, configuration, API, APM, alerting, releases, PHP agent
- Operations: [docs/operations](docs/operations) — releasing, upgrading, scaling, e2e, local stack demo

## License

Backend and UI: [AGPL-3.0](LICENSE). Agents and SDKs: Apache-2.0.
