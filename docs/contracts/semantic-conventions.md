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

Data point attributes: `container.id` (always), `container.name`, `container.image.name`, `container.image.tags` (string array; e.g. `["1.25"]`)
when Docker metadata is available, `container.runtime` (`docker`, `containerd`, `cri-o`, `podman`) when known.
Cumulative container counters carry no start time.

### Agent self-telemetry

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `openlog.agent.export.items` | Sum, cumulative, monotonic | `{item}` | `signal` = `metrics`,`logs`; `outcome` = `sent`,`buffered`,`dropped` |
| `openlog.agent.buffer.usage` | Sum, non-monotonic | `By` | — |
| `openlog.agent.collector.duration` | Gauge | `s` | `collector` |
| `openlog.agent.permission_denied` | Sum, cumulative, monotonic | `{error}` | `collector` |
| `openlog.agent.collection.interval` | Gauge | `s` | — (current metrics interval; rises above the configured value when the CPU budget check backs off) |
| `openlog.agent.update.state` | Gauge | `1` | `state` = `idle`,`downloading`,`verifying`,`staged`,`restarting`,`confirming`,`succeeded`,`failed`,`rolled_back` (one point, value 1, attribute = current self-update state; see releases-updates.md §3) |
| `openlog.agent.update.attempts` | Sum, cumulative, monotonic | `{attempt}` | `result` = `succeeded`,`failed`,`rolled_back` (since agent start; a rollback performed at startup is counted by the next process) |

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
| `container` | container id (64 hex) | `id`, `name`, `runtime` (`docker`), `image` (as reported, e.g. `nginx:1.25` or `sha256:…`), `image_id`, `state` (`running`,`exited`,`paused`,`created`,…), `created` (RFC3339), `labels` (object; at most 100 keys, values truncated to 256 bytes), `ports` (array of `{ip, private_port, public_port, protocol}`) |
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

`integration.status` ∈ `enabled`, `needs_configuration`, `not_available`:
- rule has no integration → `{"status": "not_available"}` (no `id`)
- integration with `auto_enable: true` and no unmet `requires` → `enabled`. In M0 this means "would be enabled"; integrations run from M2.
- otherwise (`auto_enable: false`, or `requires` not satisfied) → `needs_configuration`

`apm_hint`, when present: `{"language": "php", "agent": "openlog-agent-php"}`.

`command` is the argv0 basename of the service's main process (lowest PID whose parent is not part of the service; a setproctitle suffix `:` is removed),
e.g. `redis-server` on Debian where the executable resolves to `redis-check-rdb`. It is a display name only; `instance` keeps the resolved executable. Omitted when the service has no process.

`log_paths` lists the log file globs declared by the rule (`logs:`); always present, possibly empty. They are tailed when `logs.auto_from_discovery` is true.

`instance` groups matches, first available of: the service's executable path (the resolved `/proc/<pid>/exe` target; if unreadable, an absolute argv0), systemd unit name, container id, package key (`dpkg:redis-server`), listening port key.
Exception: processes running inside a container (container id from `/proc/<pid>/cgroup`) are grouped by container, and the container id is the instance, because containers share executable paths; a matching `container` inventory item joins that instance.
Processes whose exe is unreadable but whose parent belongs to an instance join that instance.

### 3.5 Masking rules (agent-side, mandatory)

In `cmdline` and `exec_start`, values are replaced with `***` for:
- arguments matching `(?i)--?(password|passwd|pwd|secret|token|api[-_]?key|auth|credentials?)(=|\s+)\S+`
- `-p<value>` directly attached — **only** when the executable basename matches `^(mysql|mysqldump|mysqladmin|mysqlimport|mysqlcheck|mysqlpump|mariadb.*)$` (so `ssh -p22` and `docker -p 8080:80` stay readable)
- `KEY=value` pairs whose key matches `(?i)(pass|secret|token|key|auth)`, **except** keys ending in `file`, `path` or `dir` (e.g. `--keyfile=/etc/x.pem` stays readable)
- URL userinfo: `scheme://user:pass@` → `scheme://user:***@`

Environment variables and file contents are never collected (log files are collected only when configured, see §4).

## 4. Log records (infra agent)

Log lines are sent as ordinary OTLP LogRecords on the logs signal in the host resource of §1. They never carry `event.name` or `openlog.inventory.*` attributes.

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
