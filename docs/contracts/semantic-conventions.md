# Contract: Semantic Conventions (v1)

Binding contract between agents and the backend. Where an OpenTelemetry semantic convention exists, openlog follows it; openlog-specific names use the `openlog.` prefix.

## 1. Resource attributes

### Infra agent (every OTLP resource it sends)

| Attribute | Required | Source / value |
|---|---|---|
| `host.id` | yes | `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid` → generated UUID persisted in the agent state dir |
| `host.name` | yes | kernel hostname |
| `host.arch` | yes | `amd64`, `arm64`, … (OTel values) |
| `os.type` | yes | `linux` |
| `os.name` | no | `/etc/os-release` `ID` |
| `os.version` | no | `/etc/os-release` `VERSION_ID` |
| `os.description` | no | `/etc/os-release` `PRETTY_NAME` |
| `openlog.os.kernel_release` | no | `uname -r` equivalent (`/proc/sys/kernel/osrelease`) |
| `openlog.entity.type` | yes | `host` |
| `openlog.agent.name` | yes | `openlog-infra-agent` |
| `openlog.agent.version` | yes | agent build version |
| *user extra attributes* | no | `host.extra_attributes` from config, sent as-is |

### Backend extraction

The processor extracts these columns from resource attributes; missing values become `''`:

| Column | Attribute |
|---|---|
| `host_id` | `host.id` |
| `host_name` | `host.name` |
| `service_name` | `service.name` |

`tenant_id` is **never** taken from attributes; it comes from the Kafka header set by ingest.

## 2. Host metrics (infra agent)

Counters are **cumulative**; utilization values are computed by the agent between two samples and sent as gauges. M0 sends CPU totals across all CPUs (no per-CPU series).

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `system.cpu.time` | Sum, cumulative, monotonic | `s` | `cpu.mode` = `user`,`nice`,`system`,`idle`,`iowait`,`interrupt`,`softirq`,`steal` |
| `system.cpu.utilization` | Gauge | `1` | `cpu.mode` (same values; 0..1) |
| `system.cpu.logical.count` | Sum, non-monotonic | `{cpu}` | — |
| `system.cpu.load_average.1m` / `.5m` / `.15m` | Gauge | `{thread}` | — |
| `system.memory.usage` | Sum, non-monotonic | `By` | `system.memory.state` = `used`,`free`,`cached`,`buffers` |
| `system.memory.limit` | Sum, non-monotonic | `By` | — |
| `system.memory.utilization` | Gauge | `1` | `system.memory.state` |
| `system.paging.usage` | Sum, non-monotonic | `By` | `system.paging.state` = `used`,`free` |
| `system.filesystem.usage` | Sum, non-monotonic | `By` | `system.device`, `system.filesystem.mountpoint`, `system.filesystem.type`, `system.filesystem.state` = `used`,`free`,`reserved` |
| `system.filesystem.utilization` | Gauge | `1` | `system.device`, `system.filesystem.mountpoint`, `system.filesystem.type` |
| `system.disk.io` | Sum, cumulative, monotonic | `By` | `system.device`, `disk.io.direction` = `read`,`write` |
| `system.disk.operations` | Sum, cumulative, monotonic | `{operation}` | `system.device`, `disk.io.direction` |
| `system.network.io` | Sum, cumulative, monotonic | `By` | `network.interface.name`, `network.io.direction` = `receive`,`transmit` |
| `system.network.packets` | Sum, cumulative, monotonic | `{packet}` | `network.interface.name`, `network.io.direction` |
| `system.network.errors` | Sum, cumulative, monotonic | `{error}` | `network.interface.name`, `network.io.direction` |
| `system.network.dropped` | Sum, cumulative, monotonic | `{packet}` | `network.interface.name`, `network.io.direction` |
| `system.uptime` | Gauge | `s` | — |
| `system.process.count` | Sum, non-monotonic | `{process}` | `process.status` = `running`,`sleeping`,`disk_sleep`,`stopped`,`zombie`,`idle`,`other` |

`system.memory.usage{used}` = `MemTotal − MemFree − Buffers − Cached − SReclaimable` (all from `/proc/meminfo`).
`SReclaimable` is counted in `cached` (`cached` = `Cached + SReclaimable`), so the four states sum to `MemTotal`.

Filesystems with these types are excluded by default: `proc, sysfs, devtmpfs, devpts, tmpfs, cgroup, cgroup2, overlay, squashfs, securityfs, debugfs, tracefs, pstore, bpf, mqueue, hugetlbfs, configfs, fusectl, autofs, binfmt_misc, nsfs, rpc_pipefs, ramfs`.
Mount points that are not directories are excluded (bind-mounted files such as Docker's `/etc/hosts`, `/etc/hostname`, `/etc/resolv.conf`).
A mount on the agent's own `state_dir` or `buffer.dir` is excluded when another reported mount has the same device (`system.device` and major:minor); other volumes on the same device (e.g. `/data`) are kept.
Disk devices excluded by default: `loop*`, `ram*`, and partitions are included. Network interfaces excluded by default: `lo`.

### Process metrics (infra agent, `process_metrics`)

Per-process series for the union of the top `process_metrics.top_n_cpu` processes by CPU utilization (default 20, only processes with utilization > 0)
and the top `process_metrics.top_n_memory` processes by RSS (default 20). Kernel threads are skipped. Only these processes produce series; a restarted
process gets a new `process.pid`, so series churn with PIDs (bounded by the top-N union per sample).

| Metric | Type | Unit | Notes |
|---|---|---|---|
| `process.cpu.utilization` | Gauge | `1` | (utime+stime delta) / elapsed / logical CPUs; 0..1 of the whole host; not sent on a process's first sample |
| `process.memory.usage` | Sum, non-monotonic | `By` | RSS |
| `process.memory.virtual` | Sum, non-monotonic | `By` | VSZ |
| `process.threads` | Sum, non-monotonic | `{thread}` | |
| `process.open_file_descriptors` | Sum, non-monotonic | `{count}` | omitted when `/proc/<pid>/fd` is unreadable |

Data point attributes (the resource stays the host resource of §1):

| Attribute | Value |
|---|---|
| `process.pid` | int |
| `process.executable.name` | `comm` (`/proc/<pid>/stat`) |
| `process.executable.path` | `/proc/<pid>/exe` target; omitted when unreadable |
| `process.owner` | user name of the real UID (`/etc/passwd`), else the numeric UID as string |
| `openlog.discovery.id` | `rule_id` of the discovered service the process belongs to (by PID, executable instance, then container id; a non-`runtime` service wins over a `runtime` one); omitted when none |

### Container metrics (infra agent, `containers`)

Read from cgroup v2 for every directory under `/sys/fs/cgroup` named after a 64-hex container id (`docker-<id>.scope`, `docker/<id>`,
`cri-containerd-<id>.scope`, `crio-<id>.scope`, `libpod-<id>.scope`). cgroup v1 hosts send no container metrics.

| Metric | Type | Unit | Source / attributes |
|---|---|---|---|
| `container.cpu.time` | Sum, cumulative, monotonic | `s` | `cpu.stat` `usage_usec` |
| `container.cpu.utilization` | Gauge | `1` | usage delta / elapsed / host logical CPUs (0..1 of the host) |
| `container.memory.usage` | Sum, non-monotonic | `By` | `memory.current − inactive_file` (like `docker stats`) |
| `container.memory.limit` | Sum, non-monotonic | `By` | `memory.max`; host `MemTotal` when unlimited |
| `container.blockio.io` | Sum, cumulative, monotonic | `By` | `io.stat` rbytes/wbytes summed over devices; `disk.io.direction` = `read`,`write` |
| `container.blockio.operations` | Sum, cumulative, monotonic | `{operation}` | `io.stat` rios/wios; `disk.io.direction` |
| `container.network.io` | Sum, cumulative, monotonic | `By` | `/proc/<pid>/net/dev` of a container process, non-`lo` interfaces summed; `network.io.direction` = `receive`,`transmit`; omitted for containers in the host network namespace |
| `container.restarts` | Sum, cumulative, monotonic | `{restart}` | Docker `RestartCount` (inspect); only with Docker metadata |
| `openlog.container.status` | Gauge | `1` | value 1 per container; `openlog.container.state` = Docker state (`running`,`paused`,`restarting`,`exited`,`created`,`dead`); `openlog.container.health` = `healthy`,`unhealthy`,`starting` (only with a healthcheck); `openlog.container.started_at` = RFC3339 start time (inspect) |

Data point attributes of every container metric: `container.id` (always), `container.name`, `container.image.name`, `container.image.tags`
(string array; e.g. `["1.25"]`) when Docker metadata is available, `container.runtime` (`docker`, `containerd`, `cri-o`, `podman`) when known,
and from container labels (Docker metadata only): `docker.compose.project` (`com.docker.compose.project`), `docker.compose.service`
(`com.docker.compose.service`), `k8s.pod.name` (`io.kubernetes.pod.name`), `k8s.namespace.name` (`io.kubernetes.pod.namespace`),
`k8s.container.name` (`io.kubernetes.container.name`; Kubernetes on Docker via cri-dockerd — containerd/CRI-O hosts have no names, see below).
Cumulative container counters carry no start time.

`openlog.container.status` is sent every metrics interval for every container listed by the Docker Engine API that is running, paused or
restarting, or that stopped, started or was created within the last 24 hours, and for every container cgroup without Docker metadata (state
`running`, no health); at most 500 containers per sample, running ones first. Stopped containers therefore stay visible (with their state)
until 24 hours after they stopped. `GET /containers/{id}/json` (start time, restart count, health, log source) runs for new containers, on
state changes and at most once a minute per running container, at most 64 per listing. Without the Docker socket (or permission) the agent
still sends cgroup metrics and status for running containers, keyed by `container.id` only; containerd, CRI-O and Podman containers are
never enriched (no names, images or labels).

**Container entity (backend).** The materialized view `containers_mv` (schema 0009) keeps one row per (tenant, `host.id`, `container.id`)
from `openlog.container.status`, `container.restarts` and `container.cpu.time` points: first/last seen, host name, the identity attributes
above, and state, health and start time of the latest status point (API: [api.md](api.md) "Containers"). 30 days after the last point.

### Agent self-telemetry

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `openlog.agent.export.items` | Sum, cumulative, monotonic | `{item}` | `signal` = `metrics`,`logs`,`traces` (`traces` only once the PHP forwarder has started; items = spans); `outcome` = `sent`,`buffered`,`dropped` |
| `openlog.agent.buffer.usage` | Sum, non-monotonic | `By` | — |
| `openlog.agent.collector.duration` | Gauge | `s` | `collector` |
| `openlog.agent.permission_denied` | Sum, cumulative, monotonic | `{error}` | `collector` |
| `openlog.agent.collection.interval` | Gauge | `s` | — (current metrics interval; rises above the configured value when the CPU budget check backs off) |
| `openlog.agent.update.state` | Gauge | `1` | `state` = `idle`,`downloading`,`verifying`,`staged`,`restarting`,`confirming`,`succeeded`,`failed`,`rolled_back` (one point, value 1, attribute = current self-update state; see releases-updates.md §3) |
| `openlog.agent.update.attempts` | Sum, cumulative, monotonic | `{attempt}` | `result` = `succeeded`,`failed`,`rolled_back` (since agent start; a rollback performed at startup is counted by the next process) |
| `openlog.agent.integration.collections` | Sum, cumulative, monotonic | `{collection}` | `integration` = integration id (§6); every collection attempt of every instance |
| `openlog.agent.integration.errors` | Sum, cumulative, monotonic | `{error}` | `integration`; failed collections (status `error` or `needs_configuration`; partial collections are not counted) |
| `openlog.agent.integration.duration` | Gauge | `s` | `integration`; duration of the latest collection of any instance of the integration |
| `openlog.agent.php.messages` | Sum, cumulative, monotonic | `{message}` | `result` = `accepted`,`malformed`,`unsupported_version`,`dropped` (PHP forwarder datagrams, php-agent.md §6; emitted once the forwarder has started) |
| `openlog.agent.php.spans` | Sum, cumulative, monotonic | `{span}` | — (spans handed to the export pipeline) |
| `openlog.agent.php.reassembly_timeouts` | Sum, cumulative, monotonic | `{trace}` | — (split traces exported incomplete after `reassembly_timeout`) |
| `openlog.agent.php.pending_traces` | Gauge | `{trace}` | — (traces waiting for missing parts) |

## 3. Inventory and discovery (OTLP logs)

Inventory and discovery results are sent as OTLP **LogRecords** on the logs signal, in the same resource as host metrics.

### 3.1 Item record

| Field | Value |
|---|---|
| `time_unix_nano` | snapshot time (same for every record of a snapshot) |
| attribute `event.name` | `openlog.inventory.item` |
| attribute `openlog.inventory.snapshot_id` | opaque unique string per snapshot (e.g. UUIDv7) |
| attribute `openlog.inventory.category` | see table below |
| attribute `openlog.inventory.key` | unique within (host, snapshot, category) |
| `body` | **string** containing a JSON object with the category's fields |

### 3.2 Snapshot-complete record

Sent **after** all items of a snapshot (items may span multiple OTLP requests). The backend only considers a snapshot current once this record arrives.

| Field | Value |
|---|---|
| attribute `event.name` | `openlog.inventory.snapshot` |
| attribute `openlog.inventory.snapshot_id` | same id |
| attribute `openlog.inventory.item_count` | int, number of item records |
| `body` | empty |

### 3.3 Categories and JSON body fields

Unknown fields must be ignored by consumers; missing fields are allowed.

| Category | Key | Body fields |
|---|---|---|
| `os` | `os` | `id`, `name`, `version_id`, `pretty_name`, `kernel_release`, `kernel_version`, `arch`, `hostname`, `boot_time` (RFC3339) |
| `hardware` | `cpu` | `vendor`, `model`, `logical_cores`, `physical_cores`, `sockets`, `mhz` |
| `hardware` | `memory` | `total_bytes`, `swap_total_bytes` |
| `hardware` | `dmi` | `sys_vendor`, `product_name`, `product_version`, `bios_vendor`, `bios_version` (no serial numbers) |
| `kernel_module` | module name | `name`, `size_bytes`, `state` |
| `package` | `<manager>:<name>` | `manager` (`dpkg`,`rpm`,`apk`), `name`, `version`, `arch` |
| `systemd_unit` | unit name | `name`, `type` (`service`,`socket`,`timer`,…), `path`, `enabled_state` (`enabled`,`disabled`,`static`,`masked`,`unknown`), `description`, `exec_start` (masked) |
| `listening_port` | `<proto>:<address>:<port>`; IPv6 addresses in brackets (`tcp:[::]:22`, `tcp:0.0.0.0:22`) | `protocol` (`tcp`,`udp`), `family` (4,6), `address`, `port`, `pid`, `process_name`, `process_exe` |
| `process` | exe path (or `comm:<name>` if exe unreadable) | `exe`, `name`, `command` (argv0 basename of the first instance, e.g. `redis-server` when exe is `/usr/bin/redis-check-rdb`), `cmdline` (masked, ≤1024 chars, first instance), `count`, `pids` (≤20), `uids` (distinct), `start_time` (earliest, RFC3339), `systemd_unit`, `container_id` |
| `user` | user name | `name`, `uid`, `gid`, `home`, `shell` |
| `network_interface` | interface name | `name`, `mac`, `mtu`, `operstate`, `addresses` |
| `mount` | mount point | `mountpoint`, `device`, `fs_type`, `options` |
| `container` | container id (64 hex) | `id`, `name`, `runtime` (`docker`), `image` (as reported, e.g. `nginx:1.25` or `sha256:…`), `image_id`, `state` (`running`,`exited`,`paused`,`created`,…), `health` (`healthy`,`unhealthy`,`starting`; omitted without healthcheck), `created` (RFC3339), `started_at` / `finished_at` (RFC3339; omitted until inspected or when never started/stopped), `restart_count`, `exit_code` (stopped containers, omitted when 0), `labels` (object; at most 100 keys, values truncated to 256 bytes), `ports` (array of `{ip, private_port, public_port, protocol}`) |
| `discovered_service` | `<rule_id>:<instance>` | see 3.4 |

### 3.4 `discovered_service` body

```json
{
  "rule_id": "redis",
  "name": "Redis",
  "category": "database",
  "instance": "/usr/bin/redis-server",
  "command": "redis-server",
  "version": "7.2.4",
  "matched_by": ["process", "systemd_unit", "listening_port"],
  "pids": [812],
  "ports": [{"protocol": "tcp", "address": "127.0.0.1", "port": 6379}],
  "systemd_units": ["redis-server.service"],
  "packages": ["dpkg:redis-server"],
  "container_ids": [],
  "integration": {"id": "redis", "status": "enabled"},
  "apm_hint": null,
  "log_paths": ["/var/log/redis/*.log"]
}
```

`integration` object (M2, reflects the running integration, §6):

| Field | Presence | Meaning |
|---|---|---|
| `id` | when the rule declares an integration | integration id (`nginx`, `redis`, `mysql`, `postgresql`, `docker`, …) |
| `status` | always | `enabled`, `needs_configuration`, `error`, `not_available` |
| `error` | optional | sanitized reason, ≤ 256 bytes, never contains credentials (see below) |
| `hint` | optional, with `needs_configuration` | multi-line `config.yaml` snippet (YAML, `#` comments) that configures the instance; UI shows it verbatim |
| `endpoint` | optional | endpoint of the last successful collection: `host:port`, `unix:<path>` or the nginx status URL; never contains credentials |

Status values:
- `not_available`: the rule has no integration (`{"status": "not_available"}`, no `id`); the agent has no implementation of `id` (`hint` explains);
  integrations are disabled (`integrations.enabled` / `integrations.<id>.enabled` false or the instance override `enabled: false`; `error` says which);
  or `integrations.max_instances` is reached.
- `needs_configuration`: the rule `requires: ["credentials"]` and neither `username` nor `password` is configured (no connection is attempted);
  the server demands authentication that is not configured (Redis `NOAUTH`, MySQL `1045` without password, PostgreSQL `28P01`/`28000` without password);
  a configured `env:`/`file:` secret is empty or missing; no endpoint can be derived; nginx has no `stub_status` page on any discovered port
  (or `auto_enable: false`); the Docker socket is not accessible. `error` describes the reason; `hint` is set.
- `error`: the last collection failed for another reason: wrong credentials (`authentication failed: …`), unreachable endpoints
  (`no endpoint reachable (127.0.0.1:6379): …`), missing privileges on the base query, protocol errors, timeouts.
- `enabled`: the last collection succeeded. `error` may still be present for a **partial** collection (e.g. `replica status: permission denied: …`):
  metrics were sent, some optional query failed. Before the first collection of a new instance (the first snapshot after start) the status is
  `enabled` without `endpoint`; a new snapshot is sent as soon as every instance has a first result, and afterwards at most once per minute
  when a status, error or endpoint changes (in addition to the regular snapshot triggers).

Sanitizing of `error`: configured secret values are replaced by `***`, URL userinfo (`scheme://user:***@`), DSN userinfo (`user:***@tcp(`),
`password=…`/`password: …` and the §3.5 patterns are masked, whitespace is collapsed, the text is cut at 256 bytes. User names, hosts and
server messages (e.g. `Access denied for user 'openlog'@'172.18.0.1' (using password: YES)`) are kept.

`-once` runs one collection of every integration before printing, so its `discovered_services` show real statuses.

`apm_hint`, when present: `{"language": "php", "agent": "openlog-agent-php", "status": "not_installed"}`. `status` (M2, set by the agent for
`agent` = `openlog-agent-php`): `active` when the agent's PHP forwarder received valid spans in the last 10 minutes, else `not_installed`
(php-agent.md §6). A status change triggers an inventory snapshot. Rules cannot set `status`.

`command` is the argv0 basename of the service's main process (lowest PID whose parent is not part of the service; a setproctitle suffix `:` is removed),
e.g. `redis-server` on Debian where the executable resolves to `redis-check-rdb`. It is a display name only; `instance` keeps the resolved executable. Omitted when the service has no process.

`log_paths` lists the log file globs declared by the rule (`logs:`); always present, possibly empty. They are tailed when `logs.auto_from_discovery` is true.

`instance` groups matches, first available of: the service's executable path (the resolved `/proc/<pid>/exe` target; if unreadable, an absolute argv0), systemd unit name, container id, package key (`dpkg:redis-server`), listening port key.
Exception: processes running inside a container (container id from `/proc/<pid>/cgroup`) are grouped by container, and the container id is the instance, because containers share executable paths; a matching `container` inventory item joins that instance.
Processes whose exe is unreadable but whose parent belongs to an instance join that instance.

### 3.5 Masking rules (agent-side, mandatory)

In `cmdline` and `exec_start`, values are replaced with `***` for:
- arguments matching `(?i)--?[A-Za-z0-9_.\-]*?(password|passwd|pass|pwd|secret|token|api[-_]?key|auth|credentials?)(=|\s+)\S+` — the flag name must **end** in a secret word, so `--requirepass x`, `--masterauth x` (Redis), `--db-password x` and `-keypass x` are masked while `--passive yes`, `--auth-mode md5` and `--token-file /path` stay readable
- `-p<value>` directly attached — **only** when the executable basename matches `^(mysql|mysqldump|mysqladmin|mysqlimport|mysqlcheck|mysqlpump|mariadb.*)$` (so `ssh -p22` and `docker -p 8080:80` stay readable)
- `KEY=value` pairs whose key matches `(?i)(pass|secret|token|key|auth)`, **except** keys ending in `file`, `path` or `dir` (e.g. `--keyfile=/etc/x.pem` stays readable)
- URL userinfo: `scheme://user:pass@` → `scheme://user:***@`

Environment variables and file contents are never collected (log files are collected only when configured, see §4).

## 4. Log records (infra agent)

Log lines are sent as ordinary OTLP LogRecords on the logs signal in the host resource of §1 (container logs: see §4.1). They never carry `event.name` or `openlog.inventory.*` attributes.

| Field | Files | journald |
|---|---|---|
| `time_unix_nano` | not set (0) | `__REALTIME_TIMESTAMP` |
| `observed_time_unix_nano` | time read | time read |
| `severity_number` / `severity_text` | heuristic (below) when `logs.parse_severity`; otherwise unset | from `PRIORITY` |
| `body` | string: one line, or one multiline record joined with `\n`; trailing `\r` removed; invalid UTF-8 replaced by U+FFFD; at most `logs.max_line_bytes` | string `MESSAGE`, same limits |

Attributes:

| Attribute | Files | journald |
|---|---|---|
| `openlog.log.source` | `file` | `journald` |
| `log.file.path` | host path (without `host.root_path`) | — |
| `log.file.name` | basename | — |
| `openlog.discovery.id` | `rule_id` when the path matches a `log_paths` glob of a discovered service | — |
| `openlog.systemd.unit` | — | `_SYSTEMD_UNIT` |
| `process.pid` | — | `_PID` (int) |
| `process.command` | — | `_COMM` |
| `openlog.syslog.identifier` | — | `SYSLOG_IDENTIFIER` |
| `openlog.log.truncated` | `true` (bool) when the body was cut at `logs.max_line_bytes` | same |
| *user attributes* | `logs.files[].attributes` | — |

`severity_text` uses canonical names `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`, `FATAL`.

journald `PRIORITY` → `severity_number`: 0 emerg → 24 (FATAL4), 1 alert → 23 (FATAL3), 2 crit → 21 (FATAL), 3 err → 17 (ERROR), 4 warning → 13 (WARN), 5 notice → 10 (INFO2), 6 info → 9 (INFO), 7 debug → 5 (DEBUG).

File severity heuristic: the first level word in the first 128 bytes, delimited by whitespace, `[ ] " ' = : ( < | ,` (not `/`, so URL paths do not match):
`emerg`/`emergency` → 24, `alert` → 23, `panic` → 22, `fatal`/`crit`/`critical` → 21, `err`/`error` → 17, `warn`/`warning` → 13, `notice` → 10, `info` → 9, `debug` → 5, `trace` → 1 (case-insensitive).
Examples: nginx `[error]`, Apache `[core:warn]`, PostgreSQL `FATAL:`, MySQL `[Warning]`, logfmt `level=info`, JSON `"level":"debug"`.

Masking: log bodies are user data and are **not** masked by default. With `logs.mask_secrets: true` the §3.5 patterns (secret flags, secret `KEY=value` tokens, URL userinfo) are applied to bodies; the MySQL `-p<value>` rule is not (the program is unknown). JSON-style `"password": "x"` is not covered.

Delivery is at-least-once: file offsets and the journal cursor are committed after the batch was sent or written to the disk buffer, so a crash can resend up to a few seconds of records.

### 4.1 Container logs (`logs.containers`)

stdout/stderr of Docker containers, collected automatically (default on; requires `containers.enabled`). Each container's records are a
separate `ResourceLogs` whose resource is the host resource of §1 plus the container identity attributes of §2 "Container metrics"
(`container.id`, `container.name`, `container.image.name`, `container.image.tags`, `container.runtime`, `docker.compose.project`,
`docker.compose.service`, `k8s.*`).

| Field | Value |
|---|---|
| `time_unix_nano` | Docker's time of the message |
| `observed_time_unix_nano` | time read |
| `body` | the message without its trailing newline; messages Docker split into 16 KiB parts are joined; same limits, severity heuristic and masking as files |
| `trace_id` / `span_id` | from a JSON object body (≤ 16 KiB) with a 32-hex `trace_id`, `traceId`, `traceID`, `trace.id`, `otelTraceID` or `TraceId` field (not all zeros), and a 16-hex `span_id`/`spanId`/`span.id`/`otelSpanID` field |
| attribute `openlog.log.source` | `container` |
| attribute `log.iostream` | `stdout` or `stderr` (absent for lines that are not json-file entries) |
| attribute `openlog.log.truncated` | as for files |

Sources, per container (`logs.containers.source: auto`): the json-file log (`LogPath` from `GET /containers/{id}/json`, e.g.
`/var/lib/docker/containers/<id>/<id>-json.log`) when the agent can open it — the files are `root:root 0640` in a `0710` directory, so this
needs `CAP_DAC_READ_SEARCH` (granted by the systemd unit) or root —, otherwise the Docker Engine API
`GET /containers/{id}/logs?follow=1&stdout=1&stderr=1&timestamps=1&since=…` (docker group). The API also serves the `local` and `journald`
drivers and, with Docker ≥ 20.10 dual logging, most remote drivers; drivers that cannot be read (`none`, or remote drivers with dual logging
disabled) are retried with backoff (30 s … 5 min) and logged once. `source: file` / `api` force one path. Without API access the agent lists
`logs.containers.docker_containers_dir/<id>/` and reads `config.v2.json` (name, image, labels, state) when readable.

Selection: running, paused and restarting containers, plus stopped containers until their log was read to the end (they stopped after the
agent started, or a saved position exists); running containers first, at most `max_containers`. A container label `openlog.logs=false`
disables a container; `exclude` then `include` (empty = all) match `name`, `image` (reference, name without tag or last path segment),
`compose_project`, `compose_service` and `label` (`key` or `key=value`) with `filepath.Match` globs, all given fields of an item must match.

Positions: json-file logs use the file offsets of §4 (rotation `<id>-json.log` → `.1` is followed; when the agent was down during a rotation
the rest of `<id>-json.log.1` is read before the new file). Containers running at the first listing start at `logs.start_at`; containers
seen later are read from their beginning, skipping messages older than the agent start (`start_at: end`) or already read. API streams
store the Docker time of the last delivered record per container in the state file and resume after it. Per-container rate limit
`rate_limit_lines` (backpressure: the file keeps unread data, the stream blocks). Multiline grouping is not applied to container logs.

## 5. Infra agent configuration keys (M1 additions)

| Key | Default | Meaning |
|---|---|---|
| `process_metrics.enabled` | `true` | per-process metrics (§2) |
| `process_metrics.top_n_cpu` / `top_n_memory` | `20` / `20` | top-N sizes; the union is reported |
| `containers.enabled` | `true` | container inventory and metrics |
| `containers.docker_socket` | `/var/run/docker.sock` | host path under `host.root_path`; `/run/docker.sock` is tried as a fallback |
| `logs.enabled` | `true` | master switch; nothing is read unless files, journald or auto discovery is configured |
| `logs.files[]` | `[]` | `path` (absolute glob, `filepath.Match` syntax, no `**`), `exclude` (globs matched against the path and the basename), `multiline_start` (regex of a record's first line), `attributes` (map) |
| `logs.auto_from_discovery` | `false` | tail `log_paths` of discovered services |
| `logs.journald.enabled` | `false` | run `journalctl -o export -f` |
| `logs.journald.journalctl_path` / `units` / `priority` | `journalctl` / `[]` / `""` | binary, `--unit` filters, `--priority` (emerg..debug or 0..7) |
| `logs.start_at` | `end` | where files first seen at startup (and journald without a saved cursor) start: `end` or `beginning`; files appearing later are always read from the beginning |
| `logs.max_line_bytes` | `65536` | longer lines/records are truncated, the rest of the line is skipped |
| `logs.rate_limit_lines` | `2000` | lines per second per file (0 = unlimited); excess stays in the file (backpressure, no drops) |
| `logs.parse_severity` | `true` | file severity heuristic |
| `logs.mask_secrets` | `false` | apply §3.5 masking to log bodies |
| `logs.poll_interval` | `1s` | file poll and batch flush interval |
| `logs.containers.enabled` | `true` | container logs (§4.1); needs `containers.enabled` |
| `logs.containers.source` | `auto` | `auto`, `file` (json-file logs only) or `api` (Docker Engine API only) |
| `logs.containers.max_containers` | `100` | containers read at once (1–1000) |
| `logs.containers.rate_limit_lines` | `1000` | lines per second per container (0 = unlimited) |
| `logs.containers.include[]` / `exclude[]` | `[]` | `{name, image, compose_project, compose_service, label}` globs; label `openlog.logs=false` always excludes |
| `logs.containers.docker_containers_dir` | `/var/lib/docker/containers` | json-file logs without Docker API access (host path under `host.root_path`) |

## 6. Integrations (infra agent, M2, D-031)

Integrations collect service metrics for discovered services (§3.4). An instance exists per `discovered_service` whose rule declares an
implemented `integration.id`; it starts when the service is discovered and stops when it disappears (inventory snapshot cadence, host change
fingerprint). Each instance collects on its own schedule (`integrations.interval`, default 30 s, first collection within 2 s), with a per-collection
timeout (`integrations.timeout`, 10 s), at most `integrations.max_concurrent` collections at once and exponential backoff after failures
(interval · 2ⁿ, max 5 min). The latest sample of every instance is sent with the next metrics payload. Metric names, types, units and attribute keys
are those of the OpenTelemetry Collector contrib receivers (`nginxreceiver`, `redisreceiver`, `mysqlreceiver`, `postgresqlreceiver`, checked
against their `metadata.yaml` on `main`, 2026-09-13), so data collected by an OTel Collector with these receivers matches the same panels.
Metrics that are disabled by default in the receiver but emitted by openlog are marked *(opt-in in OTel)*.

### 6.1 Resource

Every instance (and every entity of §6.5 PostgreSQL) is a separate OTLP `ResourceMetrics`. Its attributes are all host attributes of §1, then:

| Attribute | Value |
|---|---|
| `openlog.discovery.id` | `rule_id` of the discovered service (e.g. `mariadb`) |
| `openlog.discovery.instance` | `instance` of the discovered service; `<openlog.discovery.id>:<openlog.discovery.instance>` is the `discovered_service` key (§3.4) |
| `openlog.integration.id` | `nginx`, `redis`, `mysql`, `postgresql` |
| `service.instance.id` | `host:port` of the endpoint; loopback hosts are replaced by `host.name` (`web-1:6379`); unix sockets: `<host.name>:<socket path>`. MySQL overrides it with the receiver's UUIDv5 (§6.4) |
| `server.address` | endpoint host (IP as dialed, e.g. `127.0.0.1`, `172.18.0.5`), or the socket path for unix sockets |
| `server.port` | int, endpoint port (absent for unix sockets) |
| integration attributes | see the integration sections (`redis.version`, `mysql.instance.endpoint`, `postgresql.database.name`, …) |

Instrumentation scope: `name` = `openlog-infra-agent/integrations/<id>`, `version` = agent version.
Cumulative sums carry `start_time_unix_nano` = server start (now − uptime) for Redis and MySQL; nginx and PostgreSQL sums carry no start time.
The resource does **not** set `service.name` (the backend `service_name` column stays empty for integration metrics).
Join for UI panels: `host.id` + `openlog.discovery.id` + `openlog.discovery.instance` (service card) or `openlog.integration.id` (panel type).

Endpoint derivation (in order, at most 8 candidates, the first that answers is used and preferred afterwards):
1. `endpoint` from configuration (only candidate);
2. TCP listening ports of the service (integration default port first; `0.0.0.0` → `127.0.0.1`, `::` → `::1`; non-protocol ports skipped: MySQL 33060/33062, Redis 16379);
3. well-known unix sockets that exist under `host.root_path` (Redis `/run/redis/redis-server.sock`, …; MySQL `/run/mysqld/mysqld.sock`, `/var/lib/mysql/mysql.sock`, …; PostgreSQL `/var/run/postgresql/.s.PGSQL.5432`, …);
4. container services: published ports on `127.0.0.1:<public port>`, then container IP addresses (Docker `NetworkSettings`) with the container's private ports and the default port.
   Without the Docker Engine API (no socket, or `permission denied`) the agent derives the same data from the host: for every container id found in
   process cgroups it reads the container's network namespace through `/proc/<pid>/net/tcp{,6}` (listening TCP ports held by the container's
   processes; loopback-bound sockets are skipped) and `/proc/<pid>/net/fib_trie` (local non-loopback IPv4 addresses), and maps published ports from
   `docker-proxy -host-ip … -host-port … -container-ip … -container-port …` command lines. Containers in the host network namespace are skipped
   (their ports are host listening ports, step 2);
5. only when no candidate was found: the integration default port on `127.0.0.1` (nginx 80, Redis 6379, MySQL 3306, PostgreSQL 5432).

### 6.2 Configuration keys

| Key | Default | Meaning |
|---|---|---|
| `integrations.enabled` | `true` | master switch (requires `discovery.enabled`) |
| `integrations.interval` / `timeout` | `30s` / `10s` | default collection interval (≥ 5 s) and per-collection timeout (≤ interval) |
| `integrations.max_concurrent` / `max_instances` | `4` / `32` | concurrency limit; instance limit (later instances: `not_available`) |
| `integrations.<id>.enabled` | `true` | per integration (`nginx`, `redis`, `mysql`, `postgresql`, `docker`) |
| `integrations.<id>.interval` | — | overrides the default interval |
| `integrations.<id>.endpoint` | — | `host:port`, `unix:/path` (redis, mysql, postgresql) or the `http(s)://…` stub_status URL (nginx) |
| `integrations.<id>.username` / `password` | — | redis, mysql, postgresql. `password`: `env:NAME`, `file:/abs/path` (trailing newline removed; re-read on every connection) or a literal (startup warning) |
| `integrations.<id>.tls` | — | `{enabled, insecure_skip_verify, ca_file, server_name}`; PostgreSQL without `tls` uses `sslmode=prefer` over TCP |
| `integrations.postgresql.database` | `postgres` | initial database |
| `integrations.postgresql.databases` / `exclude_databases` | `[]` | database allow/deny lists (default: every non-template database with `datallowconn`, max 32) |
| `integrations.{mysql,postgresql}.top_n_tables` | `50` / `20` | cardinality guard: largest tables/indexes (PostgreSQL, per database) or tables/indexes with most io wait time (MySQL) |
| `integrations.<id>.instances[]` | `[]` | overrides for services matching **all** given `match` fields: `port` (listening, private or published), `endpoint` (derived candidate), `unit`, `container` (name or ≥ 12-char id prefix), `instance`; plus any setting above and `enabled` |
| `integrations.remote_config` | `true` | apply integration settings configured in the openlog UI (delivered by agent sync, [releases-updates.md](releases-updates.md) §3, D-039). `false`: ignored; the agent reports revision `disabled` |

Unknown keys and settings an integration does not support (e.g. `integrations.nginx.password`) are configuration errors. Credentials are never
logged, never included in inventory, statuses or metrics, and never sent to the backend by the agent.

**Remote integration config** (D-039): items of the sync response's `integrations_config` are merged over `config.yaml` in order, remote values
winning per non-empty field. An item without `match` changes the integration defaults (`integrations.<id>.*`, including `enabled`); an item with
`match` (`port`, `container`, `endpoint`, `instance`) updates the `config.yaml` instance with an identical match or is added before the configured
instances. Remote passwords are literal values (never `env:`/`file:` references). Invalid items (e.g. an nginx endpoint that is not a URL) are skipped
with a warning. The last received config is stored in `<state_dir>/integrations-remote.json` (mode 0600, it contains passwords) and applied at the
next start before the backend answers. Changes apply without a restart: instances whose endpoint, credentials, database settings or enabled state
changed are restarted. A hard cap of 20 000 data points per collection applies;
dropped points turn the collection partial (`error`: `cardinality guard: N data points dropped`).

### 6.3 nginx (`stub_status`)

Source: `ngx_http_stub_status_module` page. With `auto_enable: true` (nginx rule) and no configured `endpoint`, the paths `/nginx_status`,
`/stub_status`, `/basic_status`, `/status`, `/server_status` are probed on every candidate endpoint over `http`, then `https` (certificates are
not verified only for loopback endpoints); the first `200` page that parses as `stub_status` is used. The found URL is remembered per instance and
tried first when the instance re-probes after a failure (with the instance backoff). When no candidate serves the page the status is
`needs_configuration` with a hint saying that stub_status is not enabled and showing the nginx `location` snippet; the agent never edits the nginx
configuration. No credentials. Resource: §6.1 only.

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `nginx.requests` | Sum, cumulative, monotonic, int | `{requests}` | — |
| `nginx.connections_accepted` | Sum, cumulative, monotonic, int | `{connections}` | — |
| `nginx.connections_handled` | Sum, cumulative, monotonic, int | `{connections}` | — |
| `nginx.connections_current` | Sum, cumulative, non-monotonic, int | `{connections}` | `state` = `active`, `reading`, `writing`, `waiting` |

### 6.4 Redis (`INFO`) and MySQL/MariaDB

**Redis** — `INFO` (default sections) over TCP, TLS or a unix socket; `AUTH <password>` or `AUTH <username> <password>` when configured.
Required: nothing without `requirepass`; with ACLs the user needs `+info` (e.g. `ACL SETUSER openlog on >… +info +ping`).
Resource attribute: `redis.version` (`redis_version`, `unknown` when missing).

| Metric | Type | Unit | Attributes | INFO field |
|---|---|---|---|---|
| `redis.clients.connected` | Sum, non-monotonic, int | `{client}` | — | `connected_clients` |
| `redis.clients.blocked` | Sum, non-monotonic, int | `{client}` | — | `blocked_clients` |
| `redis.clients.max_input_buffer` | Gauge, int | `By` | — | `client_recent_max_input_buffer` |
| `redis.clients.max_output_buffer` | Gauge, int | `By` | — | `client_recent_max_output_buffer` |
| `redis.commands` | Gauge, int | `{ops}/s` | — | `instantaneous_ops_per_sec` |
| `redis.commands.processed` | Sum, monotonic, int | `{command}` | — | `total_commands_processed` |
| `redis.connections.received` | Sum, monotonic, int | `{connection}` | — | `total_connections_received` |
| `redis.connections.rejected` | Sum, monotonic, int | `{connection}` | — | `rejected_connections` |
| `redis.cpu.time` | Sum, monotonic, double | `s` | `state` = `sys`, `sys_children`, `sys_main_thread`, `user`, `user_children`, `user_main_thread` | `used_cpu_*` |
| `redis.db.keys` | Gauge, int | `{key}` | `db` (`"0"`, `"1"`, …) | `db<N>: keys` |
| `redis.db.expires` | Gauge, int | `{key}` | `db` | `db<N>: expires` |
| `redis.db.avg_ttl` | Gauge, int | `ms` | `db` | `db<N>: avg_ttl` |
| `redis.keys.evicted` | Sum, monotonic, int | `{key}` | — | `evicted_keys` |
| `redis.keys.expired` | Sum, monotonic, int | `{event}` | — | `expired_keys` |
| `redis.keyspace.hits` | Sum, monotonic, int | `{hit}` | — | `keyspace_hits` |
| `redis.keyspace.misses` | Sum, monotonic, int | `{miss}` | — | `keyspace_misses` |
| `redis.latest_fork` | Gauge, int | `us` | — | `latest_fork_usec` |
| `redis.memory.used` / `.peak` / `.rss` / `.lua` | Gauge, int | `By` | — | `used_memory`, `used_memory_peak`, `used_memory_rss`, `used_memory_lua` |
| `redis.memory.fragmentation_ratio` | Gauge, double | `1` | — | `mem_fragmentation_ratio` |
| `redis.maxmemory` *(opt-in in OTel)* | Gauge, int | `By` | — | `maxmemory` |
| `redis.net.input` / `redis.net.output` | Sum, monotonic, int | `By` | — | `total_net_input_bytes`, `total_net_output_bytes` |
| `redis.rdb.changes_since_last_save` | Sum, non-monotonic, int | `{change}` | — | `rdb_changes_since_last_save` |
| `redis.replication.offset` | Gauge, int | `By` | — | `master_repl_offset` |
| `redis.replication.backlog_first_byte_offset` | Gauge, int | `By` | — | `repl_backlog_first_byte_offset` |
| `redis.replication.replica_offset` *(opt-in in OTel)* | Gauge, int | `By` | — | `slave_repl_offset` (replicas) |
| `redis.role` *(opt-in in OTel)* | Sum, non-monotonic, int | `{role}` | `role` = `primary`, `replica` (value 1) | `role` |
| `redis.slaves.connected` | Sum, non-monotonic, int | `{replica}` | — | `connected_slaves` |
| `redis.uptime` | Sum, monotonic, int | `s` | — | `uptime_in_seconds` |

**MySQL / MariaDB** (rules `mysql` and `mariadb`, integration id `mysql`) — native protocol, `mysql_native_password` and
`caching_sha2_password` (RSA key exchange without TLS). Queries: `SHOW GLOBAL STATUS`, `SELECT @@innodb_buffer_pool_size`,
top-N `performance_schema.table_io_waits_summary_by_table` / `…_by_index_usage` (ordered by `SUM_TIMER_WAIT`), `SHOW REPLICA STATUS`
(`SHOW SLAVE STATUS` on older servers). Required privileges: `PROCESS`, `REPLICATION CLIENT` (MariaDB ≥ 10.5.9: `REPLICA MONITOR` or `SLAVE MONITOR`
for replica status), `SELECT ON performance_schema.*`. Missing performance_schema/replica privileges make a collection partial.
Resource attributes: `mysql.instance.endpoint` (endpoint display form, e.g. `127.0.0.1:3306`, `unix:/run/mysqld/mysqld.sock`);
`service.instance.id` = UUIDv5 (namespace `4d63009a-8d0f-11ee-aad7-4c796ed8e320`) of the §6.1 `host:port` value, as mysqlreceiver.

All MySQL metrics are int. Attribute keys are the receiver's `name_override` values.

| Metric | Type | Unit | Attributes (values) |
|---|---|---|---|
| `mysql.buffer_pool.data_pages` | Sum, non-monotonic | `1` | `status` = `dirty`, `clean` (pages_data − pages_dirty) |
| `mysql.buffer_pool.limit` | Sum, non-monotonic | `By` | — |
| `mysql.buffer_pool.operations` | Sum, monotonic | `1` | `operation` = `read_ahead_rnd`, `read_ahead`, `read_ahead_evicted`, `read_requests`, `reads`, `wait_free`, `write_requests` |
| `mysql.buffer_pool.page_flushes` | Sum, monotonic | `1` | — |
| `mysql.buffer_pool.pages` | Sum, non-monotonic | `1` | `kind` = `data`, `free`, `misc`, `total` (`misc` skipped when out of range) |
| `mysql.buffer_pool.usage` | Sum, non-monotonic | `By` | `status` = `dirty`, `clean` |
| `mysql.commands` *(opt-in in OTel)* | Sum, monotonic | `1` | `command` = `alter_table`, `create_index`, `create_table`, `delete`, `delete_multi`, `insert`, `optimize`, `select`, `update`, `update_multi` |
| `mysql.connection.count` *(opt-in in OTel)* | Sum, monotonic | `1` | — (`Connections`) |
| `mysql.connection.errors` *(opt-in in OTel)* | Sum, monotonic | `1` | `error` = `accept`, `internal`, `max_connections`, `peer_address`, `select`, `tcpwrap`, `aborted`, `aborted_clients`, `locked` |
| `mysql.double_writes` | Sum, monotonic | `1` | `kind` = `pages_written`, `writes` |
| `mysql.handlers` | Sum, monotonic | `1` | `kind` = `commit`, `delete`, `discover`, `external_lock`, `mrr_init`, `prepare`, `read_first`, `read_key`, `read_last`, `read_next`, `read_prev`, `read_rnd`, `read_rnd_next`, `rollback`, `savepoint`, `savepoint_rollback`, `update`, `write` |
| `mysql.index.io.wait.count` | Sum, monotonic | `1` | `operation` = `delete`, `fetch`, `insert`, `update`; `table`, `schema`, `index` (`NONE` for no index) |
| `mysql.index.io.wait.time` | Sum, monotonic | `ns` | same |
| `mysql.locks` | Sum, monotonic | `1` | `kind` = `immediate`, `waited` |
| `mysql.log_operations` | Sum, monotonic | `1` | `operation` = `waits`, `write_requests`, `writes`, `fsyncs` |
| `mysql.max_used_connections` *(opt-in in OTel)* | Sum, non-monotonic | `1` | — |
| `mysql.mysqlx_connections` | Sum, monotonic | `1` | `status` = `accepted`, `closed`, `rejected` (MySQL only) |
| `mysql.opened_resources` | Sum, monotonic | `1` | `kind` = `file`, `table`, `table_definition` |
| `mysql.operations` | Sum, monotonic | `1` | `operation` = `fsyncs`, `reads`, `writes` |
| `mysql.page_operations` | Sum, monotonic | `1` | `operation` = `created`, `read`, `written` |
| `mysql.prepared_statements` | Sum, monotonic | `1` | `command` = `execute`, `close`, `fetch`, `prepare`, `reset`, `send_long_data` |
| `mysql.query.count` / `mysql.query.client.count` / `mysql.query.slow.count` *(opt-in in OTel)* | Sum, monotonic | `1` | — (`Queries`, `Questions`, `Slow_queries`) |
| `mysql.replica.time_behind_source` *(opt-in in OTel)* | Sum, non-monotonic | `s` | — (replicas only; absent while `NULL`) |
| `mysql.replica.sql_delay` *(opt-in in OTel)* | Sum, non-monotonic | `s` | — (replicas only) |
| `mysql.row_locks` | Sum, monotonic | `1` | `kind` = `waits`, `time` (`Innodb_row_lock_time`, ms as reported) |
| `mysql.row_operations` | Sum, monotonic | `1` | `operation` = `deleted`, `inserted`, `read`, `updated` |
| `mysql.sorts` | Sum, monotonic | `1` | `kind` = `merge_passes`, `range`, `rows`, `scan` |
| `mysql.table.io.wait.count` | Sum, monotonic | `1` | `operation`, `table`, `schema` |
| `mysql.table.io.wait.time` | Sum, monotonic | `ns` | `operation`, `table`, `schema` |
| `mysql.threads` | Sum, non-monotonic | `1` | `kind` = `cached`, `connected`, `created`, `running` |
| `mysql.tmp_resources` | Sum, monotonic | `1` | `resource` = `disk_tables`, `files`, `tables` |
| `mysql.uptime` | Sum, monotonic | `s` | — |

### 6.5 PostgreSQL (`pg_stat_*`)

pgx protocol client (`sslmode=prefer`, simple query protocol, `application_name=openlog-infra-agent`). One connection to `database` is kept;
per-database connections are opened for each collection and closed. Required: `GRANT pg_monitor TO openlog` (or `SELECT` on the
`pg_stat_*` views plus `pg_read_all_stats` for replication lag) and `CONNECT` on the collected databases. Failing per-database queries make the
collection partial. Queries follow postgresqlreceiver (relations holding a granted `AccessExclusiveLock` are skipped). Resources follow the receiver's
default mode (feature gates `separateSchemaAttr` and `useOTelSemconv` off): server metrics on the instance resource (§6.1 only), then one resource
per database, per table and per index with these additional attributes:

| Resource | Extra attributes |
|---|---|
| instance | — |
| database | `postgresql.database.name` |
| table | `postgresql.database.name`, `postgresql.table.name` = `<schema>.<table>` |
| index | `postgresql.database.name`, `postgresql.table.name` = `<table>` (no schema, as the receiver), `postgresql.index.name` |

| Metric | Resource | Type | Unit | Attributes |
|---|---|---|---|---|
| `postgresql.bgwriter.buffers.allocated` | instance | Sum, monotonic, int | `{buffers}` | — |
| `postgresql.bgwriter.buffers.writes` | instance | Sum, monotonic, int | `{buffers}` | `source` = `bgwriter`, `checkpoints`, `backend`, `backend_fsync` (the last two only before PostgreSQL 17) |
| `postgresql.bgwriter.checkpoint.count` | instance | Sum, monotonic, int | `{checkpoints}` | `type` = `requested`, `scheduled` |
| `postgresql.bgwriter.duration` | instance | Sum, monotonic, double | `ms` | `type` = `write`, `sync` |
| `postgresql.bgwriter.maxwritten` | instance | Sum, monotonic, int | `1` | — |
| `postgresql.connection.max` | instance | Gauge, int | `{connections}` | — |
| `postgresql.database.count` | instance | Sum, non-monotonic, int | `{databases}` | — (collected databases) |
| `postgresql.replication.data_delay` | instance | Gauge, int | `By` | `replication_client` (client address or `unix`) |
| `postgresql.wal.lag` | instance | Gauge, int | `s` | `operation` = `write`, `flush`, `replay`; `replication_client` (omitted while unknown) |
| `postgresql.wal.age` | instance | Gauge, int | `s` | — (only when WAL archiving has archived a segment) |
| `postgresql.backends` | database | Sum, non-monotonic, int | `1` | — |
| `postgresql.commits` / `postgresql.rollbacks` | database | Sum, monotonic, int | `1` | — |
| `postgresql.db_size` | database | Sum, non-monotonic, int | `By` | — |
| `postgresql.deadlocks` *(opt-in in OTel)* | database | Sum, monotonic, int | `{deadlock}` | — |
| `postgresql.database.locks` *(opt-in in OTel)* | database | Gauge, int | `{lock}` | `relation` (`''` for non-relation locks), `mode`, `lock_type`; top 100 rows by count |
| `postgresql.table.count` | database | Sum, non-monotonic, int | `{table}` | — |
| `postgresql.rows` | table | Sum, non-monotonic, int | `1` | `state` = `live`, `dead` |
| `postgresql.operations` | table | Sum, monotonic, int | `1` | `operation` = `ins`, `upd`, `del`, `hot_upd` |
| `postgresql.table.size` | table | Sum, non-monotonic, int | `By` | — |
| `postgresql.table.vacuum.count` | table | Sum, monotonic, int | `{vacuum}` | — |
| `postgresql.blocks_read` | table | Sum, monotonic, int | `1` | `source` = `heap_read`, `heap_hit`, `idx_read`, `idx_hit`, `toast_read`, `toast_hit`, `tidx_read`, `tidx_hit` |
| `postgresql.index.scans` | index | Sum, monotonic, int | `{scans}` | — |
| `postgresql.index.size` | index | Gauge, int | `By` | — |

Cardinality: at most 32 databases, `top_n_tables` (default 20) largest tables and indexes per database, 100 lock rows per database.

### 6.6 Docker Engine

Integration id `docker` (rule `docker`) sends **no metrics**: the OTel `docker_stats` receiver defines only `container.*` metrics, which the agent
already sends from cgroup v2 (§2 container metrics), and there are no OTel engine-level `docker.*` names. The integration reports the Docker Engine
API state for the service card: `enabled` when `GET /containers/json` succeeds on `containers.docker_socket`, `needs_configuration` on permission
denied (hint: docker group membership, which is root-equivalent), `error` when the socket is missing or the API fails, `not_available` when
`containers.enabled` is false.
