# openlog-infra-agent

The host agent of [openlog](https://github.com/onuragtas/openlog), an open-source observability platform.
It finds out **what is on the machine it runs on**, monitors it and sends everything over OTLP/HTTP.
It contains no environment-specific code. Everything is recognised by generic collectors plus a
**data-driven YAML discovery rule catalog**.

Platforms: Linux, macOS and Windows (amd64 and arm64; see [macOS and Windows](#macos-and-windows)). License: Apache-2.0.

## What it collects

**Metrics** (every `interval`, OTel `hostmetrics` names, see `openlog/docs/contracts/semantic-conventions.md` §2):
CPU time and utilization, logical CPU count, load average, memory and paging usage, filesystem usage and utilization,
disk I/O and operations, network I/O, packets, errors and drops, uptime, and process counts by status.
Filesystems exclude bind-mounted files (Docker's `/etc/hosts`, `/etc/hostname`, `/etc/resolv.conf`).

**Hardware sensors** (`sensors`, Linux): `system.hardware.temperature`, `.fan.speed`, `.voltage`, `.current` and `.power` from
`/sys/class/hwmon` — the same readings `sensors(1)` shows — with the kernel's high and critical thresholds as their own series.
A machine without hwmon reports nothing rather than an error; a fan at 0 rpm is reported, because a stopped fan is the reading
that matters most.

**Process metrics** (`process_metrics`): `process.cpu.utilization`, `process.memory.usage` (RSS), `process.memory.virtual`,
`process.threads` and `process.open_file_descriptors` for the union of the top 20 processes by CPU and the top 20 by memory, with
`process.pid`, `process.executable.name`, `process.executable.path`, `process.owner` and `openlog.discovery.id` (the rule id of the
discovered service the process belongs to). Only these processes produce series; they churn with PIDs.

**Container metrics** (`containers`, cgroup v2 only): `container.cpu.time`, `container.cpu.utilization`, `container.memory.usage`
(current − inactive_file), `container.memory.limit`, `container.blockio.io`, `container.blockio.operations` and `container.network.io`
(not for host-network containers), with `container.id`, `container.name`, `container.image.name`, `container.image.tags`, `container.runtime`
and, from container labels, `docker.compose.project`, `docker.compose.service` and `k8s.pod.name`/`k8s.namespace.name`/`k8s.container.name`.
Containers are found by walking `/sys/fs/cgroup` for 64-hex id directories (Docker, containerd/CRI, CRI-O, Podman); names, images and labels come
from the Docker Engine API and, for containerd/CRI-O (Kubernetes nodes), from the CRI (`containers.cri_sockets`). `openlog.container.status` (value 1, `openlog.container.state`, `openlog.container.health`,
`openlog.container.started_at`) and `container.restarts` report every Docker container that is running or stopped within the last 24 hours
(at most 500), so stopped containers stay visible in openlog's Containers page; start time, restart count and health come from
`GET /containers/{id}/json` (new containers, state changes, once a minute per running container).
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
| `systemd_unit` | unit files in `/etc`, `/run`, `/usr/local/lib`, `/usr/lib`, `/lib`; enabled state from `*.wants` links; masks. When the D-Bus system bus (`/run/dbus/system_bus_socket` under the host root) is reachable: active/sub state, active since, restarts, memory/CPU (with accounting), loaded template instances and transient services; otherwise file data only. No privileges or unit changes needed (systemd allows these reads for any user; `AF_UNIX` is allowed by the hardened unit) |
| `listening_port` | `/proc/net/{tcp,tcp6,udp,udp6}` + socket inode → PID via `/proc/<pid>/fd`; key `tcp:0.0.0.0:22`, IPv6 bracketed `tcp:[::]:22` |
| `process` | `/proc/<pid>/{stat,exe,cmdline,status,cgroup}`, grouped by executable; `command` = argv0 basename (e.g. `redis-server` for `/usr/bin/redis-check-rdb`) |
| `user` | `/etc/passwd` (name, uid, gid, home, shell) |
| `network_interface` | `/sys/class/net` + interface addresses |
| `mount` | `mountinfo` |
| `container` | Docker Engine API `GET /containers/json?all=1` over `containers.docker_socket` (id, name, image, image id, state, health, created, started/finished, restart count, exit code, labels with values ≤ 256 bytes, ports); containerd/CRI-O containers through the CRI `runtime.v1` API (`ListContainers`, `ContainerStatus`) on `containers.cri_sockets` (`runtime` `containerd`/`cri-o`, Kubernetes pod/namespace/container labels, restart count annotation, log path), merged with Docker's |
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

Docker: the Docker socket is usually `root:docker 0660`. The packages add `openlog-agent` to the `docker` group by default (see
[Docker access](#docker-access)). Without access the failure is counted in `openlog.agent.permission_denied{collector="containers"}`, container
metrics still come from cgroups (without names/images) and integrations derive container ports and addresses from `/proc/<pid>/net` and
`docker-proxy` instead.

## Docker access

The deb/rpm packages and `scripts/install.sh` add `openlog-agent` to the `docker` group when that group exists, so the agent can
read the Docker Engine API (container names, images, states, ports and IPs used to reach nginx, Redis, … in containers, and container log
streams when the json-file logs are not readable). Existing
members are left alone. A new membership only takes effect after a restart; the installers restart a running service once.

**Docker group membership is root-equivalent:** anyone who controls the `openlog-agent` user can control Docker and thus the host.

Opt out (any of these; the file also stops later upgrades and re-runs from adding it again):

```sh
curl -fsSL …/install.sh | sudo sh -s -- --no-docker-access …   # creates the opt-out file
sudo env OPENLOG_AGENT_DOCKER_ACCESS=0 apt-get install ./openlog-infra-agent.deb   # or dnf/rpm; also creates the file
sudo mkdir -p /etc/openlog-infra-agent && sudo touch /etc/openlog-infra-agent/no-docker-access   # before installing
```

Plain `sudo VAR=… apt-get …` only works when the sudoers policy allows setting variables; `sudo env VAR=… …` always does.

Revert on an installed host:

```sh
sudo gpasswd -d openlog-agent docker && sudo touch /etc/openlog-infra-agent/no-docker-access && sudo systemctl restart openlog-infra-agent
```

To re-enable later, delete the file and re-run the installer (or `usermod -aG docker openlog-agent` and restart). The systemd
unit's hardening does not block the socket (`AF_UNIX` is allowed; `ProtectSystem=strict` does not prevent connecting to a socket).
The container image (`packaging/container.yaml`) is not affected: it runs as root and reaches the socket through the host root
mounted at `/host`.

## Integrations

Discovered services get metric integrations (contract: `semantic-conventions.md` §6; metric names are those of the OpenTelemetry Collector
receivers, so OTel Collector data fits the same panels):

| Integration | Rules | Source | Credentials | Metrics |
|---|---|---|---|---|
| `nginx` | nginx | `stub_status`, probed on discovered ports (`/nginx_status`, `/stub_status`, `/basic_status`, `/status`, `/server_status`; http, then https — certificates unverified on loopback only) | none | `nginx.*` |
| `redis` | redis | `INFO` over TCP, TLS or unix socket | optional `password` / ACL `username` | `redis.*` |
| `mysql` | mysql, mariadb | global status, `performance_schema` io waits (top-N), replica status | required | `mysql.*` |
| `postgresql` | postgresql | `pg_stat_database`, `pg_stat_bgwriter`/`pg_stat_checkpointer`, `pg_stat_replication`, `pg_locks`, top-N `pg_stat_user_tables`/`indexes` | required | `postgresql.*` |
| `docker` | docker | Docker Engine API reachability (no own metrics: `container.*` come from cgroups, OTel has no engine-level `docker.*` names) | socket access | — |
| `mssql` | mssql | `sys.dm_os_performance_counters` (connections, batch requests, deadlocks, lock waits, buffer cache hit ratio, page life expectancy), `sys.master_files` sizes, top-N `sys.dm_os_wait_stats`; any OS, also remote servers via `endpoint` | required (SQL Server authentication) | `sqlserver.*` |
| `iis` | iis | Windows performance counters through WMI (Web Service per site: connections, requests per method, bytes and files sent/received, not-found errors; application pool state) | none (Windows only) | `iis.*` |
| `apache` | apache-httpd | `mod_status` in its machine-readable form, probed on discovered ports (`/server-status?auto`, `/status?auto`, `/apache-status?auto`, `/httpd-status?auto`) | none | `apache.*` |
| `memcached` | memcached | `stats` over the text protocol (SASL servers: `needs_configuration`) | none | `memcached.*` |
| `haproxy` | haproxy | CSV statistics: the stats page (`/;csv`, `/stats;csv`, …) or the runtime socket's `show stat`; one resource per frontend, backend and server (max 500) | none | `haproxy.*` |
| `php-fpm` | php-fpm | the pool status page over FastCGI on the pool's own socket or TCP port (`pm.status_path`: `/status`, `/fpm-status`, … are tried); a pool without the setting is `needs_configuration` | none | `phpfpm.*` |
| `rabbitmq` | rabbitmq | management API (`/api/overview`, `/api/nodes`, `/api/queues`, max 500 queues) on 15672 | required when the broker asks for them (HTTP 401) | `rabbitmq.*` |
| `elasticsearch` | elasticsearch, opensearch | REST API on 9200: `/_cluster/health` and the local node's `/_nodes/_local/stats` | required when security is on (HTTP 401) | `elasticsearch.*`, `jvm.*` |
| `jvm` | jvm | the `java.lang` MBeans of any JVM, read over Jolokia in one bulk request (heap, GC, threads, classes, CPU) | only if the Jolokia bridge asks for them | `jvm.*` |
| `kafka` | kafka | the broker's own MBeans over the same bridge: throughput, partitions, controller, request latency | only if the bridge asks for them | `kafka.*` |
| `mongodb` | mongodb | `serverStatus`, `listDatabases`, `dbStats` (max 32 databases) and `replSetGetStatus` on a replica set member; the connection is direct, so the numbers are this process's | required (`clusterMonitor`) | `mongodb.*` |

How it works: an integration instance starts when discovery finds a service whose rule has the integration id and stops when the service
disappears. Each instance runs on its own goroutine (`integrations.interval`, default 30 s; `integrations.timeout` 10 s; at most
`max_concurrent` at once; exponential backoff to 5 min after failures; panics are contained). Endpoints come from the service's listening ports
(wildcard → loopback), well-known unix sockets, published container ports and container IPs (from the Docker API, or — without socket
access — from the container's `/proc/<pid>/net/{tcp,tcp6,fib_trie}` and `docker-proxy` command lines), and as a last resort the default port on
`127.0.0.1`; the first endpoint that answers is used. Every
instance is its own OTLP resource: host resource + `openlog.discovery.id`, `openlog.discovery.instance`, `openlog.integration.id`,
`service.instance.id`, `server.address`, `server.port`. Self-telemetry: `openlog.agent.integration.{collections,errors,duration}{integration}`.

`discovered_service.integration` reports the real state: `enabled` (last collection ok, `endpoint` set; `error` for partial collections),
`needs_configuration` (credentials missing or rejected without a configured password, no stub_status, socket not accessible; `hint` holds a
`config.yaml` snippet), `error` (sanitized reason, e.g. `authentication failed: Access denied for user 'openlog'@'10.0.0.5' (using password: YES)`),
`not_available` (no implementation, disabled). A status change triggers a new inventory snapshot (at most once a minute after the first results).
`-once` runs every integration once, so `openlog-infra-agent -once | jq '.discovered_services[].integration'` shows what the service card will say.

**Configure from the UI (no SSH):** on a host's integration panel, `needs_configuration`/`error` statuses show a form (endpoint, username,
password, database, enabled) and a "disable on this host" switch. Settings are stored per organization (all hosts or one host; passwords
encrypted with `OPENLOG_SECRETS_KEY`, never shown again) and reach the agent with its next sync (≤ 5 min by default). The agent merges them over
`config.yaml` (remote wins per field), keeps the last received set in `/var/lib/openlog-infra-agent/integrations-remote.json` (0600) and applies
them without a restart; `journalctl -u openlog-infra-agent | grep "remote integration config"` shows the applied revision. Set
`integrations.remote_config: false` to manage integrations only through this file (the UI then shows remote config as disabled for the host).

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
  rabbitmq:
    username: openlog                              # rabbitmqctl set_user_tags openlog monitoring
    password: env:OPENLOG_RABBITMQ_PASSWORD
  elasticsearch:
    username: openlog                              # built-in role monitoring_user
    password: env:OPENLOG_ELASTICSEARCH_PASSWORD
    tls: { enabled: true, ca_file: /etc/elasticsearch/certs/http_ca.crt }
  jvm:
    # The JVM needs the Jolokia agent on its start command; openlog only reads it:
    #   java -javaagent:/opt/jolokia/jolokia-agent-jvm.jar=port=8778,host=127.0.0.1 -jar app.jar
    # endpoint: http://127.0.0.1:8778/jolokia   # only when it is not on the usual port or path
  kafka:
    # KAFKA_OPTS="-javaagent:/opt/jolokia/jolokia-agent-jvm.jar=port=8778,host=127.0.0.1"
    # endpoint: http://127.0.0.1:8778/jolokia
  mongodb:
    username: openlog                              # db.createUser(... roles: [{role: "clusterMonitor", db: "admin"}])
    password: env:OPENLOG_MONGODB_PASSWORD
    # database: admin                              # the authentication source, not a database to monitor
  haproxy:
    instances:
      - match: { unit: haproxy.service }
        endpoint: unix:/run/haproxy/admin.sock     # or http://127.0.0.1:8404/;csv
```

Least-privilege monitoring users:

```sql
-- MySQL 8 / MariaDB 10.5+ (MariaDB ≥ 10.5.9: use REPLICA MONITOR instead of REPLICATION CLIENT to read replica status)
-- For a server in a Docker container the agent connects through the Docker network: use 'openlog'@'%' (or the bridge subnet).
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
# nginx: stub_status (the agent finds it on any discovered port; it never edits nginx configuration)
location = /nginx_status {
    stub_status;
    allow 127.0.0.1;
    allow 172.16.0.0/12;   # Docker networks: published ports arrive from the bridge gateway
    deny all;
}
```

```
# Redis 6+ ACL user (only with requirepass/ACLs)
ACL SETUSER openlog on >'<password>' +info +ping
```

```sql
-- SQL Server 2016+ (mixed-mode authentication). SQL Server 2022: VIEW SERVER PERFORMANCE STATE is enough instead of VIEW SERVER STATE.
CREATE LOGIN openlog WITH PASSWORD = '<password>', CHECK_POLICY = ON;
GRANT VIEW SERVER STATE TO openlog;
GRANT VIEW ANY DEFINITION TO openlog;   -- database sizes (sys.master_files)
```

Dependencies: `github.com/go-sql-driver/mysql` (MPL-2.0, used unmodified as a library), `filippo.io/edwards25519` (BSD-3-Clause),
`github.com/jackc/pgx/v5`, `pgpassfile`, `pgservicefile` (MIT), `golang.org/x/text` (BSD-3-Clause), `github.com/microsoft/go-mssqldb`,
`github.com/golang-sql/civil`, `github.com/golang-sql/sqlexp` (BSD-3-Clause), `github.com/google/uuid`, `github.com/shopspring/decimal` (MIT),
`golang.org/x/crypto` (BSD-3-Clause), `github.com/yusufpapurcu/wmi`, `github.com/go-ole/go-ole` (MIT).

## Database query performance

Turned on per database integration, the agent also reports **what runs on the database server**: statement
statistics, the sessions and their waits, and execution plans (`docs/contracts/db-monitoring.md`). It appears under
**Databases** in openlog, and a statement links to the APM services that send it.

```yaml
# /etc/openlog-infra-agent/config.yaml
integrations:
  postgresql:
    username: openlog
    password: file:/etc/openlog-infra-agent/postgresql.password
    query_stats:
      enabled: true          # statements, sessions and plans
      top_n: 20              # statements sent per collection, by time spent in the interval
      sessions: true         # sample non-idle sessions every sample_interval
      sample_interval: 10s
      explain: true          # capture execution plans of the top statements
      explain_interval: 1h
```

The same `query_stats` block works for `mysql` and `mssql`. What each server needs:

| | Statements | Sessions and blocking | Plans |
|---|---|---|---|
| PostgreSQL | `pg_stat_statements` (`shared_preload_libraries`, `CREATE EXTENSION`), role with `pg_monitor` | `pg_stat_activity`, `pg_blocking_pids()` | `GRANT pg_read_all_data` (read statements only; PostgreSQL 16+ also plans parameterized ones) |
| MySQL / MariaDB | `performance_schema` on with statement digests | `performance_schema.threads`, `data_lock_waits` (MySQL 8) | `SELECT` on the schemas; read statements only |
| SQL Server | `VIEW SERVER STATE` | `sys.dm_exec_requests` | from the plan cache, no extra permission |

Statement text never leaves the host with its values: literals are replaced by `?` before sending, and the
execution plans are redacted too (MySQL conditions, SQL Server parameter values). The agent's own monitoring
statements are excluded. Sessions are sampled every 10 s by default — set `sessions: false` to turn that off,
`explain: false` for the plans.

## Prometheus and OpenMetrics endpoints

The agent scrapes any endpoint that speaks the Prometheus text format or OpenMetrics (node_exporter, the exporters of
databases and brokers, an application's own `/metrics`) and sends the samples as OTLP metrics, so they show up in the
Metrics Explorer, OQL, dashboards and alerts like every other metric (semantic-conventions §6.9). Three ways to add a target:

```yaml
# config.yaml: static targets
prometheus:
  targets:
    - url: http://127.0.0.1:9100/metrics
      job: node
```

```yaml
# docker-compose.yml: a container opts in with labels (the agent needs Docker access, see above)
services:
  api:
    labels:
      prometheus.io/scrape: "true"
      prometheus.io/port: "9090"        # optional when the container declares exactly one port
      prometheus.io/path: /metrics      # default
```

```yaml
# Kubernetes (node mode): the usual pod annotations
metadata:
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "8080"
```

Every target also gets `up`, `scrape_duration_seconds` and `scrape_samples_scraped`, so an exporter that stops answering
can be alerted on (`SELECT latest(up) FROM Metric FACET service.name`). A target exposing more than `sample_limit`
samples or a larger body than `body_limit_bytes` is rejected as a whole and reports `up = 0`; use `metrics.exclude` to
drop families you do not need (`go_*`, `process_*`). Counters keep their Prometheus names (`http_requests_total`),
histograms and summaries become OTLP histograms and summaries, OpenMetrics exemplars link a chart to its traces.

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

**Containers** (`logs.containers`, on by default with `containers.enabled`): stdout/stderr of Docker and containerd/CRI-O containers, one OTLP resource per container
(host attributes + `container.id`, `container.name`, image, `docker.compose.project/service`), records with `openlog.log.source=container`,
`log.iostream=stdout|stderr`, Docker's timestamp and, for JSON bodies with a `trace_id`/`traceId` field, the trace and span id (so the trace page
finds them). See `semantic-conventions.md` §4.1.
- `source: auto` (default) reads the json-file log (`/var/lib/docker/containers/<id>/<id>-json.log`, `root:root 0640`) when the agent can open
  it — the systemd unit grants `CAP_DAC_READ_SEARCH`, so packaged installs read the files — with the file offsets, rotation (`-json.log.1`) and
  16 KiB partial-message joining; otherwise it streams `GET /containers/{id}/logs?follow=1&timestamps=1` over the Docker socket (docker group),
  which also covers the `local` and `journald` log drivers and resumes after the last delivered record. `source: file` / `api` force one path.
- Running containers are read (running first, at most `max_containers`, default 100); stopped containers until their log was read to the end.
  `rate_limit_lines` is per container (default 1000/s, backpressure). Containers running at the first start follow `logs.start_at`.
- Select with `include` / `exclude` (`name`, `image`, `compose_project`, `compose_service`, `label` globs) or label a container `openlog.logs=false`.
- Drivers that cannot be read (`none`, remote drivers without dual logging) are retried with backoff and logged once.
- containerd/CRI-O containers: the CRI log file (`/var/log/pods/…/<n>.log`, CRI format, `P` partial lines joined) is tailed like a json-file
  log; there is no API stream for them.
- Stack traces without a pattern: `logs.join_continuations: true` joins indented / `at …` / `Caused by:` / `... N more` lines to the record
  before them. Off by default — it delays each record until the next line (up to ~1 s) and can reorder stdout against stderr.
- Multiline: label a container `openlog.logs.multiline='^\d{4}-\d{2}-\d{2}'` or set `multiline_start` on an `include` item (the label wins;
  add `- name: "*"` so the include list still selects every container). Lines are grouped per stream (stdout/stderr) like `logs.files`:
  flushed on the next start line or after 2 s idle, cut at `max_line_bytes`.

**Masking**: log bodies are user data and are not masked unless `logs.mask_secrets: true` (same patterns as command lines, without the MySQL `-p` rule).

## PHP forwarder

The openlog PHP extension (`openlog.so`, `agents/php`) never talks to the network: it writes one JSON datagram per request to
`/run/openlog-infra-agent/php.sock`, and the agent's `php_forwarder` module converts the spans to OTLP and sends them through the
same export queue, disk buffer and retries as metrics and logs (`/v1/traces`). A backend outage never affects PHP requests; spans are
buffered and delivered later. Contract: `openlog/docs/contracts/php-agent.md` §1, §2, §6.

- **Enabled** automatically while discovery finds PHP (a `php-fpm` service, Apache with mod_php, or a `php`/`phpX.Y` binary in
  `/usr/bin`, `/usr/local/bin`, `/bin`), re-evaluated with every inventory snapshot; `php_forwarder.enabled: true|false` wins.
  Automatic enabling needs `inventory.enabled` and `discovery.enabled`.
- **Socket**: directory `0755` (systemd `RuntimeDirectory=openlog-infra-agent`), socket `0660` with group `socket_group`
  (auto: **`openlog-php`**; only when that group does not exist, the first existing of `www-data`, `nginx`, `apache`, `php-fpm`).
  A stale socket from a previous run is replaced; the extension sends unconnected datagrams, so restarts and self-updates need no
  PHP reload. `socket_mode: "0666"` lets any local user send.
- **PHP socket access (`openlog-php`)**: on every service start (the unit's root pre-start step), package install/upgrade and
  `install.sh` run, `-reconcile` creates the system group `openlog-php`, adds `openlog-agent` (so it can set the socket's group)
  and adds every PHP-FPM pool user it finds (`user =` in `/etc/php/*/fpm/pool.d/*.conf`, `/etc/php-fpm.d/*.conf`, Remi, cPanel
  `ea-php*`, Plesk; per-site users of HestiaCP, cPanel, Plesk, ISPConfig) plus `www-data`/`apache`/`nginx` when Apache or nginx is
  installed. Root and non-local accounts are skipped; members are never removed. Only the PHP-FPM services (and Apache) whose users
  were added are reloaded gracefully (`systemctl reload php8.2-fpm`, …; never restarted) so new workers get the group. The installers
  print what changed. It is a separate group on purpose: the `openlog-agent` group can read `config.yaml` with the license key and
  must never be given to PHP users.
- **New sites** added later: the agent reports pools whose user cannot send (log warning, Fleet sync `php_access`, host → Services
  PHP-FPM card with the fix). Apply with `sudo systemctl restart openlog-infra-agent`, or manually:
  `sudo usermod -aG openlog-php <pool user> && sudo systemctl reload php<version>-fpm`.
- **Opt out** of automatic grants (the group and the agent membership are still set up): `php_forwarder.grant_pool_users: false`,
  `sudo touch /etc/openlog-infra-agent/no-php-access`, `install.sh --no-php-access` or `sudo env OPENLOG_AGENT_PHP_ACCESS=0 apt-get
  install …` (both create the file). Revert a grant: `sudo gpasswd -d <user> openlog-php`.
- **Group problems** are logged once: a missing agent membership of `openlog-php` (or of an explicit `socket_group`) as a warning
  counted in `openlog.agent.permission_denied{collector="php_forwarder"}`; a fallback group (no `openlog-php`, e.g. in a container or a
  dev build) at info level.
- **Containers** that cannot share the socket file: `udp_listen: 127.0.0.1:18127` and `openlog.transport=udp://127.0.0.1:18127`.
- **Input** is validated strictly (60 000-byte datagrams, lowercase hex IDs, ≤ 128 attributes, ≤ 4 KiB strings) and malformed
  messages are dropped. Split messages are reassembled per (pid, trace_id); after `reassembly_timeout` they are exported with
  `openlog.php.incomplete=true`. The extension cannot set `host.*`, `os.*` or `openlog.*` resource attributes: the forwarder adds the
  agent's own, so PHP services link to this host (`host.id`).
- **Status**: discovered `php-fpm` services report `apm_hint.status` `active` (spans received in the last 10 minutes) or
  `not_installed`. Self-telemetry: `openlog.agent.php.messages{result}`, `.spans`, `.reassembly_timeouts`, `.pending_traces`.

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

# Run as a service (tarball layout; deb/rpm packages install the same layout). The install root is root-owned.
V=0.4.0
sudo install -d -m 0755 /opt/openlog/infra-agent /opt/openlog/infra-agent/versions
sudo install -D -m 0755 dist/openlog-infra-agent-linux-amd64 /opt/openlog/infra-agent/versions/$V/openlog-infra-agent
sudo install -m 0644 manifest.json manifest.json.sig /opt/openlog/infra-agent/versions/$V/   # from the release
sudo ln -sfn versions/$V /opt/openlog/infra-agent/current
sudo install -D -m 0640 packaging/config.example.yaml /etc/openlog-infra-agent/config.yaml
# Creates openlog-agent, installs the systemd unit, sets ownership, docker group (see "Docker access").
sudo /opt/openlog/infra-agent/current/openlog-infra-agent -reconcile -config /etc/openlog-infra-agent/config.yaml
sudo systemctl daemon-reload && sudo systemctl enable --now openlog-infra-agent
```

The systemd unit runs as `openlog-agent` with `CAP_DAC_READ_SEARCH` and `CAP_SYS_PTRACE`, which are needed to read other users'
`/proc/<pid>/exe` and fd links. Anything it cannot read is skipped and counted in `openlog.agent.permission_denied`.
It uses `Restart=always` (the agent exits with status 0 to restart into an update). The agent can write only
`/var/lib/openlog-infra-agent`; the unit's privileged pre-start step (`ExecStartPre=-+… -apply`, root) installs updates.

## macOS and Windows

Since D-104 the agent also runs on macOS (amd64, arm64) and Windows (amd64, arm64). The same binary layout, configuration schema,
metric names and inventory bodies are used; collectors read native APIs instead of procfs. Contracts: `semantic-conventions.md`
(platform notes in §1–§4) and `releases-updates.md` §3 "macOS and Windows services".

| Feature | Linux | macOS | Windows |
|---|---|---|---|
| Host metrics (CPU, memory, paging, filesystem, disk, network, uptime, process counts) | ✅ procfs/sysfs | ✅ gopsutil | ✅ gopsutil (no load average) |
| Process metrics (top N) | ✅ | ✅ | ✅ (`open_file_descriptors` = handles) |
| Inventory: OS, hardware, packages, processes, users, interfaces, mounts, listening ports | ✅ | ✅ `sw_vers`/sysctl, pkgutil receipts + Homebrew + `/Applications`, `dscacheutil`, `lsof` | ✅ registry, Programs and Features, ProfileList, GetExtendedTcpTable |
| Service manager inventory | `systemd_unit` | `launchd_service` | `windows_service` |
| Kernel modules | ✅ | not available | not available |
| Discovery + integrations (nginx, Redis, MySQL/MariaDB, PostgreSQL, SQL Server) | ✅ | ✅ (Homebrew services and log paths) | ✅ (process/service names; plus IIS through performance counters) |
| Containers | ✅ Docker/CRI inventory + cgroup metrics + logs | Docker inventory over `docker.sock` only | not available |
| Logs: files | ✅ | ✅ | ✅ (opened with `FILE_SHARE_DELETE`) |
| Logs: system | journald | unified log (`logs.unified_log`) | Event Log (`logs.windows_event_log`) |
| PHP forwarder / PHP agent installation | ✅ | forwarder ✅ (`/var/run/openlog-infra-agent/php.sock`, group `_www`), installation not available | not available (disabled) |
| Kubernetes | ✅ | not available | not available |
| Service | systemd, `openlog-agent` user | LaunchDaemon, root | Windows service, LocalSystem |
| Self-update (signed manifests, self-test, rollback) | ✅ `ExecStartPre=+ -apply` | ✅ in the service process | ✅ in the service process |
| Packages | tar.gz, deb, rpm | tar.gz (`install.sh`), pkg | zip (`install.ps1`), MSI (amd64, arm64) |

### Permissions

- **macOS runs as root.** launchd has no privileged pre-start hook: the root process verifies and installs staged updates itself,
  and reading every process's executable/arguments, listening port owners (`lsof`) and the system launchd domain needs root anyway.
  The configuration is root 0600, the state directory root 0700. A dedicated `_openlog` account would need a second root helper daemon;
  it is not implemented.
- **Windows runs as LocalSystem.** Needed for the Security event log, full process and port information, the SCM and for installing
  updates under `C:\Program Files`. `LocalService` is not supported (updates and part of the inventory would fail). The service SID type is
  `unrestricted`; `C:\ProgramData\openlog\infra-agent` (configuration with the license key, state, logs) is restricted to SYSTEM and
  Administrators by `-configure`/`-reconcile`, the installers and the MSI.

### Install on macOS

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key KEY --endpoint https://ingest.example.com:4318

sudo launchctl print system/org.openlog.infra-agent     # status
tail -f /var/log/openlog-infra-agent.log                 # JSON log (rotated by newsyslog)
sudo openlog-infra-agent -once | jq '.discovered_services'
```

Or with the installer package (credentials in a root-owned file that the postinstall script reads and deletes; see
`docs/operations/releasing.md` "MSI and macOS pkg"):

```sh
sudo sh -c 'umask 077; printf "OPENLOG_LICENSE_KEY=KEY\nOPENLOG_ENDPOINT=https://ingest.example.com:4318\n" > /tmp/openlog-infra-agent.env'
sudo installer -pkg openlog-infra-agent_<v>_darwin_arm64.pkg -target /
```

Paths: `/opt/openlog/infra-agent/{versions/<v>,current}`, `/etc/openlog-infra-agent/config.yaml`, `/var/lib/openlog-infra-agent`,
`/Library/LaunchDaemons/org.openlog.infra-agent.plist`, `/usr/local/bin/openlog-infra-agent`.
Downloads by `curl` carry no quarantine attribute, so the unsigned binary runs; releases signed and notarized in CI need the Apple secrets
listed in `.github/workflows/release.yml`.

Uninstall:

```sh
sudo /opt/openlog/infra-agent/current/openlog-infra-agent -uninstall-service
sudo rm -rf /opt/openlog/infra-agent /usr/local/bin/openlog-infra-agent /etc/newsyslog.d/openlog-infra-agent.conf
sudo rm -rf /etc/openlog-infra-agent /var/lib/openlog-infra-agent /var/log/openlog-infra-agent.log*   # configuration and state
sudo pkgutil --forget org.openlog.infra-agent                                                       # pkg installs
```

### Install on Windows

PowerShell as Administrator:

```powershell
& ([scriptblock]::Create((Invoke-RestMethod https://github.com/onuragtas/openlog/releases/latest/download/install.ps1))) `
  -LicenseKey KEY -Endpoint https://ingest.example.com:4318

# or the MSI (amd64; arm64 for Windows on ARM)
msiexec /i openlog-infra-agent_<v>_windows_amd64.msi LICENSE_KEY="KEY" ENDPOINT="https://ingest.example.com:4318" /qn

Get-Service openlog-infra-agent
Get-Content C:\ProgramData\openlog\infra-agent\logs\openlog-infra-agent.log -Tail 20 -Wait
& 'C:\Program Files\openlog\infra-agent\current\openlog-infra-agent.exe' -once
```

Paths: `C:\Program Files\openlog\infra-agent\{versions\<v>,current}`, `C:\ProgramData\openlog\infra-agent\{config.yaml,state\,logs\,discovery.d\}`.
Uninstall: `install.ps1 -Uninstall` (add `-Purge` to remove configuration and state) or `msiexec /x` / Apps & features for MSI installs.
The MSI is unsigned unless the release workflow has the Authenticode secrets; SmartScreen may warn.

### Configuration differences

Defaults follow the OS (`config.DefaultFor`): Windows disables `containers`, `logs.containers` and `php_forwarder` and preconfigures
`logs.windows_event_log.channels` (System and Application, critical/error/warning; `enabled: false`); macOS uses no CRI sockets and puts
`php.sock` under `/var/run`. `-configure` writes a minimal file on macOS and Windows (only `license_key` and `endpoint`) so that the OS
defaults apply; `packaging/config.example.yaml` documents every key with Linux paths. Paths in the configuration may be POSIX or
Windows paths (`C:\logs\*.log`). Inputs that do not exist on an OS (`logs.journald` outside Linux, `logs.unified_log` outside macOS,
`logs.windows_event_log` outside Windows) are reported as configuration warnings.

```yaml
logs:
  unified_log:                 # macOS
    enabled: true
    level: default             # default | info | debug
    predicate: 'subsystem == "com.example.app" OR process == "nginx"'
  windows_event_log:           # Windows
    enabled: true
    channels:
      - { name: System, levels: [critical, error, warning] }
      - { name: Application, levels: [critical, error, warning] }
      - { name: Security, event_ids: [4624, 4625, 4740] }
      - { name: Microsoft-Windows-PowerShell/Operational, query: "*[System[Level<=3]]" }
```

### Self-update on macOS and Windows

The service process runs the privileged apply step itself before collecting (see `releases-updates.md` §3): it re-verifies the staged
release's signature with its compiled-in keys, copies and checks the archive (`tar.gz` on macOS, `zip` on Windows), self-tests the candidate,
switches `current` and exits once so launchd (`KeepAlive`) or the SCM (recovery actions, exit code 3) starts the new binary. An unconfirmed
candidate is rolled back after 3 starts or 5 minutes, like on Linux. The MSI and pkg install a signed manifest of their version next to
the binary (D-113), so a rollback ordered from openlog also works from a package-installed version. CI runs the whole cycle on both OSes
(`test/update/native.sh`, `test/update/native.ps1`: install the older release, upgrade through the fake backend, roll back).


## Kubernetes

Install with the `deploy/helm/openlog-agent` chart ([docs/operations/kubernetes.md](../../docs/operations/kubernetes.md)). Inside a pod
(`KUBERNETES_SERVICE_HOST`, `kubernetes.enabled: auto`) the agent runs in one of two modes (`internal/k8s`, semantic-conventions §7):

- **node** (DaemonSet): the host agent plus a watch of the node's pods, which adds `k8s.pod.uid`, owner workload, node, cluster and
  allowlisted pod labels to container metrics and container logs (`/var/log/pods`), and kubelet `/stats/summary` metrics
  (`k8s.node.*`, `k8s.pod.*`, `k8s.container.*`).
- **cluster** (`kubernetes.mode: cluster`, Deployment): no host collection, updates or agent sync; the `coordination.k8s.io` Lease
  holder lists nodes, pods, workloads, jobs, HPAs, namespaces and quotas every 30 s (kube-state metrics) and sends Kubernetes events
  as logs. `-once` prints one listing.

No `client-go`: a small typed REST client (list, watch, Lease) with the ServiceAccount token. Live test on kind with an OTLP capture
server: `agents/infra/test/k8s/run.sh`.

## Self-update

Contract: `openlog/docs/contracts/releases-updates.md` §3. A self-update is equivalent to installing the new
package or re-running `install.sh`: besides the binary, the new release's systemd unit, account, docker group
membership and file ownership are applied.

> **Upgrading from 0.1.x (once per host):** units installed before the privileged pre-start step only let the agent
> swap its binary. Run the package upgrade (`apt install --only-upgrade openlog-infra-agent`, `dnf upgrade
> openlog-infra-agent`) or re-run `install.sh` once. Until then the agent keeps updating binary-only (while
> `versions/` is still writable by `openlog-agent`) and reports `update_notice: "unit outdated: …"`.

Layout:

```
/opt/openlog/infra-agent/                        # root:root 0755; the agent cannot write here
  versions/0.3.0/{openlog-infra-agent, manifest.json, manifest.json.sig, LICENSE, README.md, packaging/}
  versions/0.4.0/…
  current -> versions/0.4.0
  apply-status.json                              # written by -apply (root), read by the agent
  reconcile-status.json                          # written by -reconcile (root), reported in sync as "reconcile"
/var/lib/openlog-infra-agent/update-state.json   # previous, candidate, attempts, staged_at, confirmed + reported state
/var/lib/openlog-infra-agent/updates/<v>/        # staged release: archive.tar.gz, manifest.json, manifest.json.sig
```

- **Staged mode** (unit with `ExecStartPre=-+/opt/openlog/infra-agent/current/openlog-infra-agent -apply`): the agent
  (unprivileged, sandboxed) verifies, downloads, extracts and self-tests the release, keeps only the archive, manifest and
  signature in `updates/<v>/`, records it in its state and exits 0. On the restart, `-apply` runs as root from the
  root-owned current binary and treats everything in the state directory as hostile: it verifies the signature again
  with the keys compiled into itself (rules 1–5), copies the archive into the install root while checking size and
  sha256 (no symlink escapes out of the state directory, no FIFOs), extracts it root-owned (rule 6), runs the
  candidate's `-self-test` as `openlog-agent` (rule 7), switches `current` and runs the new binary's `-reconcile`.
  The configuration and `release.trusted_keys_file` are ignored by `-apply` unless only root can change them.
- **Reconcile** (`-reconcile`, also run by the deb/rpm postinstall and `install.sh`): creates the account if missing;
  makes the install root root-owned (legacy trees written by an older agent: the current one is copied into a root-owned
  directory, others are removed); `/var/lib/openlog-infra-agent` `openlog-agent` 0750; `config.yaml` `root:openlog-agent`
  0640 (never rewritten); installs the unit embedded in the binary (deb/rpm: `/usr/lib/systemd/system`, not when a full
  override exists in `/etc/systemd/system`; tarball: `/etc/systemd/system`; drop-ins are never touched) and runs
  `systemctl daemon-reload`; docker group as in [Docker access](#docker-access). When the unit changed during `-apply`,
  the agent exits once right after starting so systemd starts it under the new unit (only once per unit content).
- **Legacy mode** (unit without the pre-start step): the old binary-only switch below, only while `versions/` is
  writable by the agent; reported as `update_mode: "legacy"` with the `unit outdated` notice.

- **Sync:** a separate goroutine posts `POST <endpoint>/v1/openlog/agent/sync` (same license key header) with version, commit, install method,
  `update_capable`, the last update state and a config hash. The first sync runs within 10 s of start, then every `poll_interval_seconds`
  from the server, clamped to [60 s, 3600 s] with ±10% jitter; state changes trigger an immediate sync. `404`/`501` disables sync until restart.
  Sync and downloads never block collection or export.
- **Install method:** `container` (`/.dockerenv`, `/run/.containerenv`, a container cgroup, or `OPENLOG_AGENT_CONTAINER=1`; `=0` turns detection
  off), `dev` (the running binary is not `install_root/versions/<v>/openlog-infra-agent`), `deb` (dpkg's `openlog-infra-agent.list` owns
  `install_root`), `rpm` (package `openlog-infra-agent` in rpmdb.sqlite), otherwise `tarball`. `update_capable` requires tarball/deb/rpm,
  `update.enabled`, trusted release keys, a `current` symlink and either `-apply` having run for this start (same systemd
  `$INVOCATION_ID`: staged mode) or a writable install root (legacy mode). `-version` prints all of this, including the package version.
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
- **Stage/switch (legacy mode):** the verified tree plus `manifest.json`/`.sig` becomes `versions/<v>/`, the state `{previous, candidate, attempts: 0, staged_at}`
  is written, `current` is replaced atomically (temp symlink + rename) and the agent exits with status 0 after flushing its queue.
  In staged mode `-apply` does this as root on the next start (see above).
- **Startup check:** staged mode: `-apply` counts the starts of an unconfirmed candidate in its root-owned status and on the 4th start,
  5 minutes after the switch, or when the running candidate's watchdog asks for it, switches back to the previous version *it* recorded
  (a root-owned directory; the agent's state file cannot choose the target); the agent reports `rolled_back`. Legacy mode: the same
  check runs first thing in `main` and switches `current` itself.
- **Confirm:** the first successful OTLP export of the candidate sets `confirmed`, reports `succeeded`; `versions/` is pruned to current + previous
  (plus the version a deb/rpm package owns), by `-apply` on the next start in staged mode.
- A failed instruction is not retried for the same action/version/rollout for 1 hour; a rolled-back one never. `not_before` is waited for
  and instructions past `deadline` are ignored (a deadline passing during staging fails the attempt).

Build-time keys: `make build-linux OPENLOG_RELEASE_PUBLIC_KEYS=<b64>,<b64>`. The end-to-end scenario lives in [`test/update`](test/update).

In a container, mount the host root at `/host` and share the PID and network namespaces:

```sh
docker run -d --pid=host --network=host -v /:/host:ro \
  --cap-add SYS_PTRACE --cap-add DAC_READ_SEARCH \
  -e OPENLOG_LICENSE_KEY=... -e OPENLOG_ENDPOINT=https://ingest.example:4318 openlog/openlog-infra-agent
```

## PHP agent installation

Contract: `openlog/docs/contracts/php-agent.md` §7.3. The agent reports the host's PHP runtimes (`php`, `php-cgi`,
`php-fpm` binaries: version, ABI, whether `openlog.so` is enabled and loaded) in every sync, and can install, upgrade
and remove the PHP agent (`/opt/openlog/php-agent`) — by default it only reports (`mode: manual`). The Fleet page sets
the mode for all hosts (update policy) or per host; `config.yaml` works without the backend:

```yaml
php_agent:
  mode: auto               # off | manual (default) | auto
  version: agent           # agent (= this agent's version, default) or e.g. 0.9.1
  reload: graceful         # none (default: PHP-FPM loads the module at its next reload) | graceful
  exclude_bins: [/usr/bin/php7.4]
  remote_config: true      # fleet settings replace these (default)
  health_check_after: 5m
```

- Installing needs the privileged pre-start step (`ExecStartPre=+… -apply`) of the current unit: the agent downloads and
  verifies the signed release into `/var/lib/openlog-infra-agent/php-agent/`, exits once, and `-apply` verifies it
  again, installs it root-owned, runs `openlog-php-install install` and writes `/opt/openlog/infra-agent/php-agent-status.json`.
  `health_check_after` later the agent checks that PHP loads the module, reloaded units are active and PHP services
  still send data; otherwise the next start switches back to the previous version (or removes it).
- Installations from the `openlog-php-agent` deb/rpm/apk package or by hand are reported (`managed_by: package` /
  `manual`) and never changed. The fleet marks its own installation with `/opt/openlog/php-agent/.managed-by-openlog-infra-agent`.
- `mode: off` removes a fleet installation (ini files and `/opt/openlog/php-agent`).
- Self-telemetry: `openlog.agent.php_agent.operations{operation, result}`; the host carries `openlog.php_agent.version`.
- Spans of PHP-FPM workers reach the agent through `php.sock` (see "PHP forwarder" for its group and mode).

## Java agent updates

Contract: `openlog/docs/contracts/java-agent.md` §2 (D-123). The agent reports the JVMs that load the openlog Java agent
(`-javaagent:` on the command line or in `JAVA_TOOL_OPTIONS`/`JDK_JAVA_OPTIONS`: pid, agent path, the version the JVM
actually loaded, whether it must be restarted) and can keep one stable jar path current. Configure the application
once, e.g. in the PM2 ecosystem file or the systemd unit:

```sh
JAVA_TOOL_OPTIONS=-javaagent:/opt/openlog/openlog-javaagent.jar
```

```yaml
java_agent:
  mode: auto                # off | manual (default: only report JVMs) | auto
  version: agent            # agent (= this agent's version, default) or e.g. 0.9.1
  remote_config: true       # the Fleet page's mode/version replace these (default)
  health_check_after: 2m
  install_root: /opt/openlog/java-agent          # versions/<v>/openlog-javaagent.jar, current
  link_path: /opt/openlog/openlog-javaagent.jar  # Windows: C:\Program Files\openlog\openlog-javaagent.jar
```

- The jar is downloaded and verified like a self-update (signed manifest, sha256), installed root-owned into
  `versions/<v>/` and `link_path` is switched atomically to it (a new symlink renamed over the old one). A jar is never
  changed in place: running JVMs keep the version they started with and are reported `restart_pending` — restart the
  application to load the new version. The agent never restarts or signals a JVM.
- Linux: installing needs the privileged pre-start step (`-apply`), like the PHP agent (the agent exits once). macOS and
  Windows: the privileged service process applies it directly.
- A `link_path` the agent did not create (e.g. a jar copied there by hand) is never touched: the report says
  `unmanaged`. Adopt it by pointing `-javaagent` at `/opt/openlog/java-agent/current/openlog-javaagent.jar`, or move
  the file away (`sudo mv /opt/openlog/openlog-javaagent.jar /opt/openlog/openlog-javaagent.jar.manual`; running JVMs
  are unaffected) so that the agent creates the link within 5 minutes (or at `systemctl restart openlog-infra-agent`).
- Windows locks jars that a JVM has open, so the copy at `link_path` switches only once those applications stopped
  (retried every 30 s); `-javaagent:C:\Program Files\openlog\java-agent\current\openlog-javaagent.jar` updates without
  that wait.
- `mode: off` removes the managed jar and link once no running JVM uses them. Applications still configured with the
  path will not start afterwards.
- Rollback: automatic when the installed jar fails verification; otherwise pin `java_agent.version` (Fleet policy) to
  the previous version.

## Configuration

See [`packaging/config.example.yaml`](packaging/config.example.yaml). `OPENLOG_LICENSE_KEY` and `OPENLOG_ENDPOINT` override the file.
Unknown keys are rejected.

Reliability:
- Collection and export are decoupled. Collectors run on their own fixed schedule and only enqueue payloads, so a slow or unavailable ingest never delays or skips a sample.
- A bounded in-memory queue (64 payloads / 16 MiB) is drained by a single exporter goroutine.
- Payloads are gzip-compressed and posted to `<endpoint>/v1/metrics`, `/v1/logs` and (PHP forwarder) `/v1/traces` with the `openlog-license-key` header.
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
cmd/openlog-infra-agent   flags: -config -version -once -validate-rules -self-test -apply -reconcile [-reconcile-context]
                          -configure [-license-key -endpoint] -verify-release <manifest> [-artifact] -uninstall-service
                          (Windows: runs as a service when started by the SCM)
internal/config        YAML config, env overrides, validation
internal/version       build version, commit, date (ldflags)
internal/release       trusted release keys (ldflags + release.trusted_keys_file)
internal/update        sync client, install detection, manifest verification, download/extract, stage/confirm/rollback,
                       privileged apply (-apply) and reconcile (-reconcile)
internal/hostfs        root-path aware file access (+ statfs, linux only)
internal/osutil        open flags, file identity, delete-sharing opens (Windows)
internal/osinfo        macOS/Windows OS version (sw_vers, sysctl, registry)
internal/plist         binary and XML property list decoder (macOS receipts, bundles, launchd)
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
packaging/             systemd unit and launchd plist (embedded by packaging.go), example config, windows/ (WiX MSI, build and install test scripts)
test/update            self-update end-to-end scenario (Debian 12 container)
```
