# openlog-infra-agent

The Linux host agent of [openlog](https://github.com/onuragtas/openlog), an open-source observability platform.
It finds out **what is on the machine it runs on**, monitors it and sends everything over OTLP/HTTP.
It contains no environment-specific code. Everything is recognised by generic collectors plus a
**data-driven YAML discovery rule catalog**.

Platforms: Linux amd64 and arm64. License: Apache-2.0.

## What it collects

**Metrics** (every `interval`, OTel `hostmetrics` names, see `openlog/docs/contracts/semantic-conventions.md` §2):
CPU time and utilization, logical CPU count, load average, memory and paging usage, filesystem usage and utilization,
disk I/O and operations, network I/O, packets, errors and drops, uptime, and process counts by status.
Filesystems exclude bind-mounted files (Docker's `/etc/hosts`, `/etc/hostname`, `/etc/resolv.conf`).

**Process metrics** (`process_metrics`): `process.cpu.utilization`, `process.memory.usage` (RSS), `process.memory.virtual`,
`process.threads` and `process.open_file_descriptors` for the union of the top 20 processes by CPU and the top 20 by memory, with
`process.pid`, `process.executable.name`, `process.executable.path`, `process.owner` and `openlog.discovery.id` (the rule id of the
discovered service the process belongs to). Only these processes produce series; they churn with PIDs.

**Container metrics** (`containers`, cgroup v2 only): `container.cpu.time`, `container.cpu.utilization`, `container.memory.usage`
(current − inactive_file), `container.memory.limit`, `container.blockio.io`, `container.blockio.operations` and `container.network.io`
(not for host-network containers), with `container.id`, `container.name`, `container.image.name`, `container.image.tags` and `container.runtime`.
Containers are found by walking `/sys/fs/cgroup` for 64-hex id directories (Docker, containerd/CRI, CRI-O, Podman); names and images come
from the Docker Engine API.
It also sends self-telemetry: `openlog.agent.export.items`, `openlog.agent.buffer.usage`,
`openlog.agent.collector.duration`, `openlog.agent.permission_denied` and `openlog.agent.collection.interval`
(the effective metrics interval in seconds, which rises when the CPU budget check backs off), plus
`openlog.agent.update.state{state}` and `openlog.agent.update.attempts{result}` (see [Self-update](#self-update)).

**Inventory** is sent as a full snapshot of OTLP log records (`event.name=openlog.inventory.item`, followed by
`openlog.inventory.snapshot`). Snapshots go out at start, every `inventory_interval`, and whenever a cheap change
fingerprint changes (package database mtime, systemd unit directories, listening port set).

| Category | Source |
|---|---|
| `os` | `/etc/os-release`, `/proc/sys/kernel/*`, `/proc/stat` |
| `hardware` (`cpu`, `memory`, `dmi`) | `/proc/cpuinfo`, `/proc/meminfo`, `/sys/class/dmi/id` (no serials) |
| `kernel_module` | `/proc/modules` |
| `package` | dpkg `/var/lib/dpkg/status`, apk `/lib/apk/db/installed`, rpm `rpmdb.sqlite` (read directly, see below) |
| `systemd_unit` | unit files in `/etc`, `/run`, `/usr/local/lib`, `/usr/lib`, `/lib`; enabled state from `*.wants` links; masks |
| `listening_port` | `/proc/net/{tcp,tcp6,udp,udp6}` + socket inode → PID via `/proc/<pid>/fd`; key `tcp:0.0.0.0:22`, IPv6 bracketed `tcp:[::]:22` |
| `process` | `/proc/<pid>/{stat,exe,cmdline,status,cgroup}`, grouped by executable; `command` = argv0 basename (e.g. `redis-server` for `/usr/bin/redis-check-rdb`) |
| `user` | `/etc/passwd` (name, uid, gid, home, shell) |
| `network_interface` | `/sys/class/net` + interface addresses |
| `mount` | `mountinfo` |
| `container` | Docker Engine API `GET /containers/json?all=1` over `containers.docker_socket` (id, name, image, image id, state, created, labels with values ≤ 256 bytes, ports) |
| `discovered_service` | discovery engine (below); includes `command` and the rule's `log_paths` |

Secrets are masked in command lines and `ExecStart`:
- `--password=***` and similar secret flags
- `TOKEN=***` for secret-looking keys, except keys ending in `file`, `path` or `dir` (`--keyfile=/etc/x.pem` stays readable)
- `scheme://user:***@`
- attached `-p***`, but only for MySQL/MariaDB clients (`mysql`, `mysqldump`, `mariadb*`, …), so `ssh -p22` and `docker run -p 8080:80` stay readable
Environment variables and file contents are never collected (except log files you configure, below).

rpm: `/var/lib/rpm/rpmdb.sqlite` (or `/usr/lib/sysimage/rpm/rpmdb.sqlite`) is read with a small built-in, read-only SQLite reader
(no cgo, no third-party code; committed WAL frames are included) and the rpm header blobs are parsed directly. That covers RHEL/Rocky/Alma 9+,
Fedora 33+ and CentOS Stream 9. Older Berkeley DB (`/var/lib/rpm/Packages`, RHEL 7/8) and ndb (SUSE) databases are read by running
`rpm -qa --queryformat …` when an `rpm` binary is available to the agent (with `--root` under `host.root_path`); the scratch container image has none,
so on those hosts the container image reports no rpm packages.

Docker: the Docker socket is usually `root:docker 0660`. The unprivileged `openlog-agent` user cannot open it with the default capabilities; the
failure is counted in `openlog.agent.permission_denied{collector="containers"}` and container metrics still come from cgroups (without names/images).
Adding the user to the `docker` group grants root-equivalent access to the host; decide deliberately.

## Integrations

Discovered services get metric integrations (contract: `semantic-conventions.md` §6; metric names are those of the OpenTelemetry Collector
receivers, so OTel Collector data fits the same panels):

| Integration | Rules | Source | Credentials | Metrics |
|---|---|---|---|---|
| `nginx` | nginx | `stub_status`, probed on discovered ports (`/nginx_status`, `/stub_status`, `/status`, `/basic_status`, `/server_status`; http, then https) | none | `nginx.*` |
| `redis` | redis | `INFO` over TCP, TLS or unix socket | optional `password` / ACL `username` | `redis.*` |
| `mysql` | mysql, mariadb | global status, `performance_schema` io waits (top-N), replica status | required | `mysql.*` |
| `postgresql` | postgresql | `pg_stat_database`, `pg_stat_bgwriter`/`pg_stat_checkpointer`, `pg_stat_replication`, `pg_locks`, top-N `pg_stat_user_tables`/`indexes` | required | `postgresql.*` |
| `docker` | docker | Docker Engine API reachability (no own metrics: `container.*` come from cgroups, OTel has no engine-level `docker.*` names) | socket access | — |

How it works: an integration instance starts when discovery finds a service whose rule has the integration id and stops when the service
disappears. Each instance runs on its own goroutine (`integrations.interval`, default 30 s; `integrations.timeout` 10 s; at most
`max_concurrent` at once; exponential backoff to 5 min after failures; panics are contained). Endpoints come from the service's listening ports
(wildcard → loopback), well-known unix sockets, published container ports and container IPs; the first endpoint that answers is used. Every
instance is its own OTLP resource: host resource + `openlog.discovery.id`, `openlog.discovery.instance`, `openlog.integration.id`,
`service.instance.id`, `server.address`, `server.port`. Self-telemetry: `openlog.agent.integration.{collections,errors,duration}{integration}`.

`discovered_service.integration` reports the real state: `enabled` (last collection ok, `endpoint` set; `error` for partial collections),
`needs_configuration` (credentials missing or rejected without a configured password, no stub_status, socket not accessible; `hint` holds a
`config.yaml` snippet), `error` (sanitized reason, e.g. `authentication failed: Access denied for user 'openlog'@'10.0.0.5' (using password: YES)`),
`not_available` (no implementation, disabled). A status change triggers a new inventory snapshot (at most once a minute after the first results).
`-once` runs every integration once, so `openlog-infra-agent -once | jq '.discovered_services[].integration'` shows what the service card will say.

Credentials: `password: env:NAME` (e.g. from a systemd `EnvironmentFile=`), `password: file:/etc/openlog-infra-agent/mysql.password` (mode 0600,
owned by the agent user) or a literal (a warning is logged). They are read on every connection, never logged, never part of inventory, statuses
or metrics, and passwords/DSN userinfo are masked in error messages.

```yaml
integrations:
  redis:
    password: env:OPENLOG_REDIS_PASSWORD          # only when requirepass/ACLs are used
  mysql:
    username: openlog
    password: file:/etc/openlog-infra-agent/mysql.password
    instances:                                     # overrides for matching services (port, endpoint, unit, container, instance)
      - match: { container: legacy-mariadb }
        username: monitor
        password: env:OPENLOG_LEGACY_MYSQL_PASSWORD
  postgresql:
    username: openlog
    password: env:OPENLOG_POSTGRESQL_PASSWORD
    exclude_databases: [rdsadmin]
  nginx:
    instances:
      - match: { port: 8080 }
        endpoint: http://127.0.0.1:8080/nginx_status
```

Least-privilege monitoring users:

```sql
-- MySQL 8 / MariaDB 10.5+ (MariaDB ≥ 10.5.9: use REPLICA MONITOR instead of REPLICATION CLIENT to read replica status)
CREATE USER 'openlog'@'localhost' IDENTIFIED BY '<password>' WITH MAX_USER_CONNECTIONS 3;
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'openlog'@'localhost';
GRANT SELECT ON performance_schema.* TO 'openlog'@'localhost';
```

```sql
-- PostgreSQL 10+
CREATE ROLE openlog WITH LOGIN PASSWORD '<password>' CONNECTION LIMIT 3;
GRANT pg_monitor TO openlog;          -- pg_stat_* incl. replication lag, pg_database_size; CONNECT on databases is granted to PUBLIC by default
```

```
# nginx: stub_status on loopback
server {
    listen 127.0.0.1:8080;
    location = /nginx_status { stub_status; allow 127.0.0.1; deny all; }
}
```

```
# Redis 6+ ACL user (only with requirepass/ACLs)
ACL SETUSER openlog on >'<password>' +info +ping
```

Dependencies: `github.com/go-sql-driver/mysql` (MPL-2.0, used unmodified as a library), `filippo.io/edwards25519` (BSD-3-Clause),
`github.com/jackc/pgx/v5`, `pgpassfile`, `pgservicefile` (MIT), `golang.org/x/text` (BSD-3-Clause).

## Logs

Logs are sent as plain OTLP LogRecords (never inventory events). See `semantic-conventions.md` §4 for fields and attributes.

**Files** (`logs.files`): absolute glob patterns (`filepath.Match` syntax; no `**`), optional `exclude`, `multiline_start` and `attributes`.
- Files are identified by device + inode plus a fingerprint of their first 1 KiB (guards against inode reuse). Globs are re-expanded every 10 s.
- Rotation by rename + create: the old file keeps being read until it has been idle for 5 s after the rename, and the new file is read from its start.
  copytruncate: a file that shrinks below the read offset is read again from 0 (lines written between the copy and our next poll can be lost; a
  truncation followed by writes beyond the old offset within one poll is not detectable).
- Offsets are persisted in `<state_dir>/logs-state.json` (temp file + fsync + rename, every 5 s and on shutdown) and only advance after the exporter
  settled the batch (sent, written to the disk buffer, or dropped), so delivery is at-least-once. A file rotated while the agent was down (known path,
  new inode) is read from the start. Files present at the first start follow `logs.start_at` (default `end`); files that appear later are read from the start.
- Lines: trailing `\r` removed, invalid UTF-8 replaced, cut at `max_line_bytes` (the rest of the line is skipped, `openlog.log.truncated=true`).
  A line without a newline is emitted after 5 s idle; a pending multiline record after 2 s idle.
- `rate_limit_lines` (per file, per second) and export backlog (disk buffer non-empty or memory queue half full) pause reading: data stays in the file,
  nothing is dropped, but a file rotated away and deleted while paused is lost. At most 512 files are tailed.

**journald** (`logs.journald`): runs `journalctl --output=export --follow` (with `--root` under `host.root_path`, `--unit`, `--priority`) and parses the
export format (text and binary fields). The cursor of the last settled entry is persisted and passed as `--after-cursor` on restart; the subprocess is
restarted with backoff. `PRIORITY` → severity, `_SYSTEMD_UNIT`, `_PID`, `_COMM`, `SYSLOG_IDENTIFIER` → attributes. `journalctl` must be available to
the agent (it is not in the scratch container image) and the agent user needs journal access (`systemd-journal`/`adm` group or `CAP_DAC_READ_SEARCH`).

**Discovery-driven**: rules may declare `logs: [{path: "/var/log/nginx/*.log"}]` (nginx, Apache, MySQL, MariaDB, PostgreSQL and Redis do). Discovered
services report them as `log_paths`; with `logs.auto_from_discovery: true` they are tailed with `openlog.discovery.id=<rule_id>`. A configured file whose
path matches a discovered glob also gets the discovery id.

**Masking**: log bodies are user data and are not masked unless `logs.mask_secrets: true` (same patterns as command lines, without the MySQL `-p` rule).

## Running

The module depends on `libs/release` in the same repository (`replace … => ../../libs/release`), so build from a full checkout.

```sh
make build-linux             # dist/openlog-infra-agent-linux-{amd64,arm64}
make test vet
make docker                  # build context is the repository root

# Version, commit, build date, install method and update capability.
openlog-infra-agent -version

# Debug: collect one round and print the OTLP payload as JSON; nothing is sent.
sudo openlog-infra-agent -once | jq '.discovered_services'

# Validate the embedded rules plus rules_dir.
openlog-infra-agent -validate-rules -config /etc/openlog-infra-agent/config.yaml

# Parse the config, run every collector once without sending, check state_dir is writable (exit 0/1).
openlog-infra-agent -self-test -config /etc/openlog-infra-agent/config.yaml

# Run as a service (tarball layout; deb/rpm packages install the same layout).
V=0.4.0
sudo useradd --system --no-create-home --shell /usr/sbin/nologin openlog-agent
sudo install -d -o openlog-agent -g openlog-agent /opt/openlog/infra-agent /opt/openlog/infra-agent/versions
sudo install -D -o openlog-agent -m 0755 dist/openlog-infra-agent-linux-amd64 /opt/openlog/infra-agent/versions/$V/openlog-infra-agent
sudo install -o openlog-agent -m 0644 manifest.json manifest.json.sig /opt/openlog/infra-agent/versions/$V/   # from the release
sudo ln -sfn versions/$V /opt/openlog/infra-agent/current
sudo install -D -m 0640 packaging/config.example.yaml /etc/openlog-infra-agent/config.yaml
sudo install -m 0644 packaging/systemd/openlog-infra-agent.service /etc/systemd/system/
sudo systemctl enable --now openlog-infra-agent
```

The systemd unit runs as `openlog-agent` with `CAP_DAC_READ_SEARCH` and `CAP_SYS_PTRACE`, which are needed to read other users'
`/proc/<pid>/exe` and fd links. Anything it cannot read is skipped and counted in `openlog.agent.permission_denied`.
It uses `Restart=always` (the agent exits with status 0 to restart into an update) and may write `/opt/openlog/infra-agent`.

## Self-update

Contract: `openlog/docs/contracts/releases-updates.md` §3. Layout:

```
/opt/openlog/infra-agent/
  versions/0.3.0/{openlog-infra-agent, manifest.json, manifest.json.sig, LICENSE, README.md, packaging/}
  versions/0.4.0/…
  current -> versions/0.4.0
/var/lib/openlog-infra-agent/update-state.json   # previous, candidate, attempts, staged_at, confirmed + reported state
```

- **Sync:** a separate goroutine posts `POST <endpoint>/v1/openlog/agent/sync` (same license key header) with version, commit, install method,
  `update_capable`, the last update state and a config hash. The first sync runs within 10 s of start, then every `poll_interval_seconds`
  from the server, clamped to [60 s, 3600 s] with ±10% jitter; state changes trigger an immediate sync. `404`/`501` disables sync until restart.
  Sync and downloads never block collection or export.
- **Install method:** `container` (`/.dockerenv`, `/run/.containerenv`, a container cgroup, or `OPENLOG_AGENT_CONTAINER=1`; `=0` turns detection
  off), `dev` (the running binary is not `install_root/versions/<v>/openlog-infra-agent`), `deb` (dpkg's `openlog-infra-agent.list` owns
  `install_root`), `rpm` (package `openlog-infra-agent` in rpmdb.sqlite), otherwise `tarball`. `update_capable` requires tarball/deb/rpm,
  `update.enabled`, trusted release keys, a `current` symlink and a writable install root. `-version` prints all of this, including the package version.
- **Verification** (in contract order; rule 8 is checked right after rules 1–5, before anything is downloaded):
  1. manifest signature from a trusted key (compiled-in keys + `release.trusted_keys_file`; none → `no trusted release keys`);
  2. `product`, `schema`, `version == target_version`;
  3. a `infra-agent` `linux/<arch>` `tar.gz` artifact;
  4. upgrade: newer than running and running ≥ `min_upgrade_from`;
  5. rollback: older than running and ≥ `rollback_floor` of the running version's kept `manifest.json` (missing → rejected);
  6. download to `versions/.<v>.partial` (≤ 256 MiB, 10 min timeout, size + streaming sha256 check), extraction that rejects absolute
     paths, `..`, symlinks, hard links, device files, entries outside `openlog-infra-agent_<v>_linux_<arch>/` and more than 512 MiB in total;
  7. `<new binary> -self-test -config <config>` exits 0 within 30 s;
  8. not update capable → `not update capable`.
  A download from the ingest host (backend mirror) carries the license key; other hosts never see it.
- **Stage/switch:** the verified tree plus `manifest.json`/`.sig` becomes `versions/<v>/`, the state `{previous, candidate, attempts: 0, staged_at}`
  is written, `current` is replaced atomically (temp symlink + rename) and the agent exits with status 0 after flushing its queue.
- **Startup check** (first thing in `main`, before the configuration is validated): an unconfirmed candidate counts a start attempt; on the
  4th start or 5 minutes after staging, `current` goes back to `previous`, the state becomes `rolled_back` and the process exits.
  A running candidate that does not confirm within 5 minutes rolls back the same way.
- **Confirm:** the first successful OTLP export of the candidate sets `confirmed`, reports `succeeded` and prunes `versions/` to current + previous
  (plus the version a deb/rpm package owns).
- A failed instruction is not retried for the same action/version/rollout for 1 hour; a rolled-back one never. `not_before` is waited for
  and instructions past `deadline` are ignored (a deadline passing during staging fails the attempt).

Build-time keys: `make build-linux OPENLOG_RELEASE_PUBLIC_KEYS=<b64>,<b64>`. The end-to-end scenario lives in [`test/update`](test/update).

In a container, mount the host root at `/host` and share the PID and network namespaces:

```sh
docker run -d --pid=host --network=host -v /:/host:ro \
  --cap-add SYS_PTRACE --cap-add DAC_READ_SEARCH \
  -e OPENLOG_LICENSE_KEY=... -e OPENLOG_ENDPOINT=https://ingest.example:4318 openlog/openlog-infra-agent
```

## Configuration

See [`packaging/config.example.yaml`](packaging/config.example.yaml). `OPENLOG_LICENSE_KEY` and `OPENLOG_ENDPOINT` override the file.
Unknown keys are rejected.

Reliability:
- Collection and export are decoupled. Collectors run on their own fixed schedule and only enqueue payloads, so a slow or unavailable ingest never delays or skips a sample.
- A bounded in-memory queue (64 payloads / 16 MiB) is drained by a single exporter goroutine.
- Payloads are gzip-compressed and posted to `<endpoint>/v1/metrics` and `/v1/logs` with the `openlog-license-key` header.
- On network errors, 429 or 503 the exporter retries with exponential backoff and jitter, honouring `Retry-After`.
- The memory queue spills to the on-disk buffer when it is full, or when a send keeps failing. The buffer holds at most `buffer.max_bytes`; when full, the oldest payloads are dropped and counted in `openlog.agent.export.items{outcome="dropped"}`.
- Every payload gets a sequence number when it is enqueued, and the exporter always sends the lowest pending one, whether it waits in memory or on disk. Delivery therefore stays FIFO across restarts and outages.
- Inventory snapshots are split into requests of at most `export.max_request_bytes`; the snapshot-complete record is always sent after all its item records.
- On SIGTERM/SIGINT the agent stops collecting and tries to flush the queue for up to 5s. Whatever is still pending, including a send cancelled at the deadline, is persisted to the disk buffer and replayed on the next start.
- If collection itself (not waiting on the network) uses more than 1% of a CPU over a sustained window, the metrics interval doubles, up to 60s, and is reported as `openlog.agent.collection.interval`.

## Discovery rules

Each rule is one YAML file. The catalog is embedded from [`rules/`](rules). Files in `discovery.rules_dir` add new rules, and a
rule there with the same `id` replaces the embedded one. **Supporting a new service needs only a new rule; no Go code.**

```yaml
id: redis                   # [a-z0-9][a-z0-9_.-]*, unique
name: Redis
category: database          # free-form lowercase: web, proxy, database, cache, search, queue, container, runtime, system, …
match:                      # any matcher makes the rule a candidate; each entry has exactly one kind
  - process: { exe_basename: ["redis-server"] }          # also exe_basename_regex, cmdline_regex (all given fields must match)
  - systemd_unit: { name_regex: "^redis(-server)?(@.*)?\\.service$" }
  - listening_port: { port: 6379, process_exe_basename: ["redis-server"] }  # optional protocol: tcp|udp
  - package: { name: ["redis-server", "redis"] }         # or name_regex
  - container: { image_regex: "(^|/)redis(:|$)" }        # evaluated once container inventory lands (M1)
version:                    # tried in order; binaries are never executed
  - package: ["redis-server", "redis"]                   # distro version normalised: 5:7.0.15-1build2 → 7.0.15
  - package_regex: "^redis[0-9]*$"
  - cmdline_regex: "redis-server.*v=(\\S+)"              # first capture group
endpoints:
  - from: listening_port
integration:                # omit → not_available
  id: redis
  auto_enable: true         # and no unmet requires → enabled
  requires: []              # e.g. ["credentials"]; unmet (always in M0) → needs_configuration
apm_hint: null              # or { language: php, agent: openlog-agent-php }
logs:                       # well-known log globs → log_paths; tailed with logs.auto_from_discovery
  - path: "/var/log/redis/*.log"
```

How matches become services (`discovered_service`, key `<rule_id>:<instance>`). The instance is the first available of:
executable path → systemd unit → container id → package key → port key.
1. Processes matching a `process` matcher are grouped by executable path: the resolved `exe` link, or an absolute argv[0] when the link is unreadable.
   Processes inside a container are grouped by container id instead (every redis container runs `/usr/local/bin/redis-server`).
   Children with an unreadable exe join their matching parent's instance.
2. A matching `listening_port` joins the owning process's instance.
3. A matching `systemd_unit` merges into the instances whose processes run inside it.
   A unit that runs nothing visible joins the existing instances; otherwise it becomes its own instance (template units `foo@.service` are ignored).
4. Containers match by image (Docker container inventory). A container whose processes already joined an instance merges into it; otherwise the container id is the instance.
5. Installed packages named by the rule's package matchers or version sources are linked. A matching server package with nothing else found yields a package-key instance.
6. Ports with no known owner join the existing instances; only when there are none does the port key become the instance.

To add a rule, write `rules/<id>.yaml` (or drop it in `rules_dir`). Add a case to `internal/discovery/testdata/cases/`,
then run `go test ./internal/discovery`. The test fails for any embedded rule without a positive case.

Starter catalog: nginx, Apache httpd, HAProxy, Caddy, MySQL, MariaDB, PostgreSQL, MongoDB, Redis, Memcached,
Elasticsearch, OpenSearch, ClickHouse, RabbitMQ, Kafka, Docker, containerd, kubelet, PHP-FPM, Node.js, JVM,
Gunicorn, uWSGI, Uvicorn, .NET, sshd, cron, chrony, ntpd.

## Layout

```
cmd/openlog-infra-agent   flags: -config -version -once -validate-rules -self-test
internal/config        YAML config, env overrides, validation
internal/version       build version, commit, date (ldflags)
internal/release       trusted release keys (ldflags + release.trusted_keys_file)
internal/update        sync client, install detection, manifest verification, download/extract, stage/confirm/rollback
internal/hostfs        root-path aware file access (+ statfs, linux only)
internal/procfs        pure procfs parsers
internal/resource      host.id chain and resource attributes
internal/metrics       host metric collectors + self-telemetry
internal/inventory     inventory collectors and snapshot encoding
internal/mask          secret masking
internal/discovery     rule schema, loader, engine, service index (process → rule id)
internal/containers    Docker Engine API client, cgroup v2 container lookup
internal/logs          file tailing, journald export reader, offsets/cursor state
internal/rpmdb         read-only SQLite reader + rpm header parser
internal/exporter      OTLP/HTTP client, retry, request splitting
internal/buffer        on-disk FIFO retry buffer
internal/agent         scheduler, change fingerprint, resource budget
rules/                 embedded discovery catalog
packaging/             systemd unit, example configs
test/update            self-update end-to-end scenario (Debian 12 container)
```
