# Contract: Semantic Conventions (v1)

Binding contract between agents and the backend. Where an OpenTelemetry semantic convention exists, openlog follows it; openlog-specific names use the `openlog.` prefix.

## 1. Resource attributes

### Infra agent (every OTLP resource it sends)

| Attribute | Required | Source / value |
|---|---|---|
| `host.id` | yes | `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid` → generated UUID persisted in the agent state dir. The running agent publishes the resolved id in `/run/openlog-infra-agent/host-id` (`<id>\n`, mode 0644, only when the directory exists, e.g. systemd `RuntimeDirectory`); APM agents read it first, also from containers that mount the directory read-only. macOS: `IOPlatformUUID`; Windows: `MachineGuid` (`HKLM\SOFTWARE\Microsoft\Cryptography`; images cloned without sysprep share it); both fall back to the UUID persisted in the state dir; the published `host-id` file is Linux only (D-104) |
| `host.name` | yes | kernel hostname |
| `host.arch` | yes | `amd64`, `arm64`, … (OTel values) |
| `os.type` | yes | `linux`, `darwin` (macOS), `windows` (D-104) |
| `os.name` | no | `/etc/os-release` `ID`; `macos` / `windows` |
| `os.version` | no | `/etc/os-release` `VERSION_ID`; macOS `sw_vers -productVersion`; Windows `<major>.<minor>.<build>` from the registry |
| `os.description` | no | `/etc/os-release` `PRETTY_NAME`; e.g. `macOS 15.5 (24F74)`, `Windows Server 2022 Datacenter 21H2 (build 20348.2340)` |
| `openlog.os.kernel_release` | no | `uname -r` equivalent (`/proc/sys/kernel/osrelease`) |
| `cloud.provider` | no | `aws`, `gcp`, `azure` — the instance metadata service that answered (see "Cloud instance facts" below) |
| `cloud.platform` | no | `aws_ec2`, `gcp_compute_engine`, `azure_vm` |
| `cloud.region` | no | `eu-central-1`, `europe-west1`, `westeurope`; derived from the zone where the provider reports only a zone |
| `cloud.availability_zone` | no | `eu-central-1a`, `europe-west1-b`, `westeurope-2` (Azure: `<location>-<zone>`) |
| `cloud.account.id` | no | AWS account id, GCP project id, Azure subscription id |
| `host.type` | no | instance type: `m5.large`, `n2-standard-4`, `Standard_D4s_v5` |
| `openlog.host.lifecycle` | no | `on-demand`, `spot` or `preemptible` (GCP's legacy preemptible VMs) |
| `openlog.entity.type` | yes | `host` |
| `openlog.agent.name` | yes | `openlog-infra-agent` |
| `openlog.agent.version` | yes | agent build version |
| *user extra attributes* | no | `host.extra_attributes` from config, sent as-is |

**Cloud instance facts.** The agent asks the instance metadata service **once at start-up** (`host.cloud_metadata: auto`,
§5) and attaches the answer to every payload; the backend prices the machine from it ([cost.md](cost.md)). The probe is
bounded to 1.5 s in total and 500 ms per request, uses the link-local address `169.254.169.254` (never DNS, never an
HTTP proxy), and asks the provider suggested by the DMI system vendor first (AWS IMDSv2 with an IMDSv1 fallback, GCP
`computeMetadata/v1`, Azure `metadata/instance`). A machine that is **not** in a cloud gets a refused connection, or at
worst hits the deadline behind a firewall that drops the packets: it simply carries none of these attributes, the agent
logs nothing at start-up and everything else proceeds unchanged. Hosts without them are priced per vCPU/GB or reported
as unpriced — never as free. The facts are not re-read while the agent runs; an instance type changes only across a stop
and start, which restarts the agent anyway.

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

### Platform notes (macOS and Windows, D-104)

The same metric names, units and attributes are sent from every OS; values come from native APIs (gopsutil v4) when the agent runs on macOS or Windows with `host.root_path: /`. States a platform does not have are reported as 0 so that every host has the same series:

| Metric | macOS | Windows |
|---|---|---|
| `system.cpu.time` / `utilization` | `host_statistics` modes; `iowait`, `softirq`, `steal` = 0 | `GetSystemTimes`; `nice`, `iowait`, `softirq`, `steal` = 0 |
| `system.cpu.load_average.*` | `getloadavg` | not sent (no load average) |
| `system.memory.usage` | `free` = free pages, `cached` = inactive, `used` = the rest (active + wired + compressed), `buffers` = 0 | `free` = available (includes the standby list), `used` = total − available, `cached` = `buffers` = 0 |
| `system.filesystem.*` | mounted volumes; `devfs`/`autofs`/`nullfs` and the sealed system volume's helper mounts below `/System/Volumes` (except `/System/Volumes/Data`) are skipped; `system.device` = `/dev/diskNsM` | drive letters; `system.device` = `C:` |
| `system.disk.*` | IOKit per whole disk | `IOCTL_DISK_PERFORMANCE` per volume (may be absent while disk performance counters are off) |
| `system.network.*` | `lo0` skipped | loopback pseudo-interface skipped; `network.interface.name` is the adapter name |
| `system.process.count` | kernel `p_stat` (macOS reports most processes as `running`) | every process as `running` (Windows has no run state) |
| `process.*` | `process.owner` = user name | `process.owner` = `DOMAIN\user`; `process.open_file_descriptors` = handle count |

Container metrics (cgroups) exist on Linux only.


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
| `container.restarts` | Sum, cumulative, monotonic | `{restart}` | Docker `RestartCount` (inspect), CRI annotation `io.kubernetes.container.restartCount`; only with container metadata |
| `openlog.container.status` | Gauge | `1` | value 1 per container; `openlog.container.state` = Docker state (`running`,`paused`,`restarting`,`exited`,`created`,`dead`); `openlog.container.health` = `healthy`,`unhealthy`,`starting` (only with a healthcheck); `openlog.container.started_at` = RFC3339 start time (inspect) |

Data point attributes of every container metric: `container.id` (always), `container.name`, `container.image.name`, `container.image.tags`
(string array; e.g. `["1.25"]`) when Docker or CRI metadata is available, `container.runtime` (`docker`, `containerd`, `cri-o`, `podman`) when known,
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
| `package` | `<manager>:<name>` | `manager` (`dpkg`,`rpm`,`apk`,`pkgutil`,`homebrew`,`app`,`windows`), `name`, `version`, `arch` |
| `systemd_unit` | unit name | `name`, `type` (`service`,`socket`,`timer`,…), `path`, `enabled_state` (`enabled`,`disabled`,`static`,`masked`,`unknown`; `generated`,`transient` for units known only over D-Bus), `description`, `exec_start` (masked). Runtime state over D-Bus (`org.freedesktop.systemd1`, system bus socket `/run/dbus/system_bus_socket` under the host root; all omitted when the bus is unreachable or the unit is not loaded): `load_state`, `active_state` (`active`,`inactive`,`failed`,`activating`,…), `sub_state` (`running`,`dead`,`exited`,…), `active_since` (RFC3339, `ActiveEnterTimestamp`; not for `inactive`/`failed`), services only: `restarts` (`NRestarts`, may be 0), `memory_bytes` (`MemoryCurrent`), `cpu_usage_ns` (`CPUUsageNSec`; memory/CPU omitted when accounting is off). Loaded services without a unit file of their own (template instances `foo@bar.service` — `path`/`exec_start` from the template —, generated and transient services) are items too; devices, scopes and other file-less units are not. State is as of the snapshot (no extra snapshot on state changes) |
| `launchd_service` | job label | macOS (D-104): `label`, `pid` (running jobs), `last_exit_status`, `program` (from the plist's `Program`/`ProgramArguments[0]`), `path` (plist), `domain` (`system` when the agent runs as root, else `user`). Loaded jobs from `launchctl list` plus not-loaded daemons in `/Library/LaunchDaemons`; per-launch `application.*` GUI jobs are skipped |
| `windows_service` | service name | Windows (D-104): `name`, `display_name`, `state` (`running`,`stopped`,`start_pending`,`stop_pending`,`paused`,…), `start_type` (`automatic`,`automatic_delayed`,`manual`,`disabled`,`boot`,`system`), `pid`, `binary_path` (masked), `account` |

| `listening_port` | `<proto>:<address>:<port>`; IPv6 addresses in brackets (`tcp:[::]:22`, `tcp:0.0.0.0:22`) | `protocol` (`tcp`,`udp`), `family` (4,6), `address`, `port`, `pid`, `process_name`, `process_exe` |
| `process` | exe path (or `comm:<name>` if exe unreadable) | `exe`, `name`, `command` (argv0 basename of the first instance, e.g. `redis-server` when exe is `/usr/bin/redis-check-rdb`), `cmdline` (masked, ≤1024 chars, first instance), `count`, `pids` (≤20), `uids` (distinct), `start_time` (earliest, RFC3339), `systemd_unit`, `container_id` |
| `user` | user name | `name`, `uid`, `gid`, `home`, `shell` |
| `network_interface` | interface name | `name`, `mac`, `mtu`, `operstate`, `addresses` |
| `mount` | mount point | `mountpoint`, `device`, `fs_type`, `options` |
| `container` | container id (64 hex) | `id`, `name`, `runtime` (`docker`; `containerd`, `cri-o` for containers listed through the CRI, where `name` is the Kubernetes container name, `ports` is empty and `restart_count` comes from the kubelet annotation `io.kubernetes.container.restartCount`), `image` (as reported, e.g. `nginx:1.25` or `sha256:…`), `image_id`, `state` (`running`,`exited`,`paused`,`created`,…), `health` (`healthy`,`unhealthy`,`starting`; omitted without healthcheck), `created` (RFC3339), `started_at` / `finished_at` (RFC3339; omitted until inspected or when never started/stopped), `restart_count`, `exit_code` (stopped containers, omitted when 0), `labels` (object; at most 100 keys, values truncated to 256 bytes), `ports` (array of `{ip, private_port, public_port, protocol}`) |
| `discovered_service` | `<rule_id>:<instance>` | see 3.4 |

Platform notes (D-104): macOS and Windows hosts send no `kernel_module`, `systemd_unit` or (Windows) `container` items. `package` managers there are `pkgutil` (installer receipts), `homebrew` (formulae and casks), `app` (bundles in `/Applications`) and `windows` (Programs and Features: the 64-bit and 32-bit Uninstall registry keys, system components and updates skipped; `arch` `amd64`/`arm64`/`386`). `os`: `id` `macos`/`windows`, `version_id` e.g. `15.5` / `10.0.20348`, `pretty_name` e.g. `macOS 15.5 (24F74)` / `Windows Server 2022 Datacenter 21H2 (build 20348.2340)`. `user` on Windows lists accounts with a profile (`uid` = relative identifier, `gid` 0, `home` = profile path); `process.uids` is empty on Windows. `discovered_service` bodies may carry `services` (launchd labels or Windows service names) and `matched_by` may contain `service`; discovery rules match a Windows process name without `.exe`.

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

`display_instance` is the user-facing instance for multi-call or symlinked executables: the main process's absolute argv0 (`/usr/bin/redis-server`) when
`instance` is an executable path, its basename differs from the executable's (`/usr/bin/redis-check-rdb`) and `comm` equals it (first 15 bytes; so it is the
executed name, not a setproctitle title). Omitted otherwise (container instances, relative argv0, same name). UIs show `command` as the name and
`display_instance`, else `instance`, as the path. It is display only: `instance`, the item key, `openlog.discovery.instance` (§6) and integration
joins keep the resolved executable, so existing entities (e.g. `redis:/usr/bin/redis-check-rdb` on Debian) keep their keys and metric series after an
agent upgrade; older agents simply omit the field. Different invoked names of one binary matched by the same rule still share one instance, and
`process` items stay keyed by the resolved executable.

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

### 4.2 macOS unified log and Windows Event Log (D-104)

| Field / attribute | `logs.unified_log` (macOS) | `logs.windows_event_log` (Windows) |
|---|---|---|
| source | `log stream --style ndjson --level <level> [--predicate <p>]`, restarted with backoff; live only (no backfill after downtime) | `EvtSubscribe` per channel with the XPath query from `event_ids`/`levels` (or `query`); resumes after the bookmark committed with the delivered batch, otherwise from `logs.start_at` (`end` = future events, `beginning` = oldest record) |
| `time_unix_nano` | `timestamp` | `System/TimeCreated/@SystemTime` |
| `severity_number` / `severity_text` | `messageType`: `Fault` → 21 FATAL, `Error` → 17 ERROR, `Default` → 10 INFO, `Info` → 9 INFO, `Debug` → 5 DEBUG | `Level`: 1 critical → 21 FATAL, 2 error → 17 ERROR, 3 warning → 13 WARN, 4 information and 0 → 9 INFO, 5 verbose → 5 DEBUG |
| `body` | `eventMessage` (same limits and masking as journald) | rendered message (`EvtFormatMessage`); without publisher metadata the event data as `name=value` pairs |
| `openlog.log.source` | `unified_log` | `windows_event_log` |
| other attributes | `openlog.macos.subsystem`, `openlog.macos.category`, `process.pid`, `process.executable.path`, `process.command` | `openlog.windows.event_log.channel`, `openlog.windows.event.id` (int), `openlog.windows.event.provider`, `openlog.windows.event.record_id` (int), `process.pid` |

Only `logEvent` entries of the unified log are sent (no activities or signposts). Reading the `Security` channel needs LocalSystem or the Event Log Readers group (the service runs as LocalSystem). Log files are tailed on every OS; on Windows they are opened with `FILE_SHARE_DELETE` so rotation still works, and file identity is the volume serial number plus the file index.


### 4.1 Container logs (`logs.containers`)

stdout/stderr of Docker and CRI (containerd, CRI-O) containers, collected automatically (default on; requires `containers.enabled`). Each container's records are a
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
`rate_limit_lines` (backpressure: the file keeps unread data, the stream blocks).

CRI containers (`container.runtime` `containerd` / `cri-o`, listed through `containers.cri_sockets`): the log file is the absolute
`log_path` of `ContainerStatus` (kubelet: `/var/log/pods/<ns>_<pod>_<uid>/<container>/<restart>.log`, needs `CAP_DAC_READ_SEARCH` or root)
in the CRI log format `<RFC3339Nano> <stdout|stderr> <F|P> <message>`; `P` (partial) lines are joined with the following lines of the
same stream up to the `F` line, the time is that of the first part. There is no API fallback (`source: api` reads nothing for them);
lines that are not in the format are sent as they are without `log.iostream`. A rotation while the agent was down is not followed
(kubelet's `<n>.log.<timestamp>` files are not read).

Multiline grouping: a container's lines are grouped into multiline records when a start pattern is set — the container label
`openlog.logs.multiline=<regex>` (wins), else `multiline_start` of the first matching `include` item. As for files, a line matching the
pattern starts a record and following non-matching lines are appended with `\n` (lines before the first start line are sent alone).
Grouping is per stream (stdout and stderr separately), after Docker/CRI partial messages were joined; a record is sent when the next
start line of its stream arrives, after 2 s without a new line of its stream, or when the log ends; at most `logs.max_line_bytes`
(`openlog.log.truncated=true`). `time_unix_nano` is the time of the first line; the saved position (file offset / stream time) never
passes a record that was not sent. An invalid label pattern disables grouping for that container (logged once).

## 5. Infra agent configuration keys (M1 additions)

| Key | Default | Meaning |
|---|---|---|
| `process_metrics.enabled` | `true` | per-process metrics (§2) |
| `process_metrics.top_n_cpu` / `top_n_memory` | `20` / `20` | top-N sizes; the union is reported |
| `host.cloud_metadata` | `auto` | `auto` = ask the instance metadata service once at start-up for the cloud instance facts of §1; `off` = never probe |
| `containers.enabled` | `true` | container inventory and metrics |
| `containers.docker_socket` | `/var/run/docker.sock` | host path under `host.root_path`; `/run/docker.sock` is tried as a fallback |
| `containers.cri_sockets` | `[/run/containerd/containerd.sock, /run/k3s/containerd/containerd.sock, /var/run/crio/crio.sock]` | CRI (`runtime.v1`) sockets of containerd / CRI-O, host paths under `host.root_path` (`/var/run/…` also tried as `/run/…`); containers of every answering socket are added to Docker's (union by id, Docker wins); CRI errors are ignored while Docker works; a socket without the CRI service is not asked again for 5 min; `[]` disables |
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
| `logs.containers.include[]` / `exclude[]` | `[]` | `{name, image, compose_project, compose_service, label}` globs; label `openlog.logs=false` always excludes; `include` items may set `multiline_start` (regex; label `openlog.logs.multiline` wins, §4.1) — an include list selects containers, so add `{name: "*"}` to keep collecting the others |
| `logs.containers.docker_containers_dir` | `/var/lib/docker/containers` | json-file logs without Docker API access (host path under `host.root_path`) |

## 6. Integrations (infra agent, M2, D-031)

Integrations collect service metrics for discovered services (§3.4). An instance exists per `discovered_service` whose rule declares an
implemented `integration.id`; it starts when the service is discovered and stops when it disappears (inventory snapshot cadence, host change
fingerprint). Each instance collects on its own schedule (`integrations.interval`, default 30 s, first collection within 2 s), with a per-collection
timeout (`integrations.timeout`, 10 s), at most `integrations.max_concurrent` collections at once and exponential backoff after failures
(interval · 2ⁿ, max 5 min). The latest sample of every instance is sent with the next metrics payload. Metric names, types, units and attribute keys
are those of the OpenTelemetry Collector contrib receivers (`nginxreceiver`, `redisreceiver`, `mysqlreceiver`, `postgresqlreceiver`, checked
against their `metadata.yaml` on `main`, 2026-09-13; `sqlserverreceiver` and `iisreceiver` on 2026-09-15), so data collected by an OTel Collector
with these receivers matches the same panels.
Metrics that are disabled by default in the receiver but emitted by openlog are marked *(opt-in in OTel)*.

### 6.1 Resource

Every instance (and every entity of §6.5 PostgreSQL) is a separate OTLP `ResourceMetrics`. Its attributes are all host attributes of §1, then:

| Attribute | Value |
|---|---|
| `openlog.discovery.id` | `rule_id` of the discovered service (e.g. `mariadb`) |
| `openlog.discovery.instance` | `instance` of the discovered service; `<openlog.discovery.id>:<openlog.discovery.instance>` is the `discovered_service` key (§3.4) |
| `openlog.integration.id` | `nginx`, `redis`, `mysql`, `postgresql`, `mssql`, `iis` |
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
| `integrations.<id>.enabled` | `true` | per integration (`nginx`, `apache`, `redis`, `memcached`, `mysql`, `postgresql`, `mongodb`, `docker`, `mssql`, `iis`, `haproxy`, `rabbitmq`, `elasticsearch`, `jvm`, `kafka`) |
| `integrations.<id>.interval` | — | overrides the default interval |
| `integrations.<id>.endpoint` | — | `host:port`, `unix:/path` (redis, mysql, postgresql, memcached; haproxy: its runtime socket) or an `http(s)://…` URL (nginx: stub_status page, NGINX Plus API `/api/` or `/api/<n>`, or VTS JSON page, the format is detected, §6.3; apache: mod_status page §6.10; haproxy: stats page §6.12; rabbitmq: management API §6.13; elasticsearch: REST API §6.14) |
| `integrations.<id>.username` / `password` | — | redis, mysql, postgresql, mssql (SQL Server authentication; Windows authentication is not supported), rabbitmq, elasticsearch (HTTP basic auth). `password`: `env:NAME`, `file:/abs/path` (trailing newline removed; re-read on every connection) or a literal (startup warning) |
| `integrations.<id>.tls` | — | `{enabled, insecure_skip_verify, ca_file, server_name}`; PostgreSQL without `tls` uses `sslmode=prefer` over TCP |
| `integrations.postgresql.database` | `postgres` | initial database |
| `integrations.mongodb.database` | `admin` | the authentication source, not a database to monitor (§6.15) |
| `integrations.mongodb.databases` / `exclude_databases` | `[]` | which databases get `dbStats` (max 32) |
| `integrations.postgresql.databases` / `exclude_databases` | `[]` | database allow/deny lists (default: every non-template database with `datallowconn`, max 32) |
| `integrations.{mysql,postgresql}.top_n_tables` | `50` / `20` | cardinality guard: largest tables/indexes (PostgreSQL, per database) or tables/indexes with most io wait time (MySQL) |
| `integrations.mssql.top_n_tables` | `10` | wait types with the most total wait time in `sqlserver.os.wait.duration` (§6.7) |
| `integrations.postgresql.query_stats` | `{enabled: false, top_n: 20, min_calls: 0}` | opt-in `pg_stat_statements` top statements (§6.5): `top_n` 0..100 (0 = 20) by total execution time, `min_calls` skips statements with fewer calls. `config.yaml` only (not part of remote integration config) |
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

### 6.3 nginx (`stub_status`, NGINX Plus API, VTS)

Sources: the `ngx_http_stub_status_module` page, the NGINX Plus API (`api` directive) or the nginx-module-vts JSON page. With `auto_enable: true`
(nginx rule) and no configured `endpoint`, these paths are probed on every candidate endpoint over `http`, then `https` (certificates are not
verified only for loopback endpoints), in this order: `/api/` (Plus), `/status/format/json`, `/vts/format/json`, `/vts_status/format/json` (VTS),
`/nginx_status`, `/stub_status`, `/basic_status`, `/status`, `/server_status` (stub_status). The first `200` page whose body is detected as one of the
formats is used: a JSON array of API versions (Plus root, the highest version is used) or of endpoint names containing `connections` and `http`
(versioned Plus root, e.g. a configured `…/api/9`) — accepted only when `<api>/connections` answers; a JSON object with `connections` and
`serverZones` (VTS); a `stub_status` text page. A configured `endpoint` URL is detected the same way. The found URL is remembered per instance and
tried first when the instance re-probes after a failure (with the instance backoff). When no candidate serves a page the status is
`needs_configuration` with a hint saying that stub_status is not enabled and showing the nginx `location` snippets (stub_status, Plus `api`, VTS);
the agent never edits the nginx configuration. No credentials.

Resource attribute (openlog extension): `nginx.status.source` = `stub_status`, `plus` or `vts`.

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `nginx.requests` | Sum, cumulative, monotonic, int | `{requests}` | — |
| `nginx.connections_accepted` | Sum, cumulative, monotonic, int | `{connections}` | — |
| `nginx.connections_handled` | Sum, cumulative, monotonic, int | `{connections}` | — |
| `nginx.connections_current` | Sum, cumulative, non-monotonic, int | `{connections}` | `state` = `active`, `reading`, `writing`, `waiting` |

Core metrics by source: stub_status and VTS (`connections` object) provide all of the above. Plus: `nginx.requests` = `http/requests.total`,
`connections_accepted` = `connections.accepted`, `connections_handled` = `accepted − dropped`, `connections_current` only `state` = `active`
(`active + idle`, as stub_status counts idle keep-alive connections) and `waiting` (`idle`); Plus has no reading/writing states.

Zone and upstream metrics (Plus and VTS only; openlog extensions named after the NGINX agent's `nginxplusreceiver`, `metadata.yaml` on `v3`,
2026-09-14, including its units). Plus reads `/api/<n>/connections`, `/http/requests` (required), `/http/server_zones` and `/http/upstreams`
(a failure of the last two makes the collection partial). VTS: `serverZones` without the `*` aggregate zone, `upstreamZones` (including
`::nogroups`). Cardinality: the 50 server zones and the 100 upstream peers with the most requests.

| Metric | Type | Unit | Attributes | Sources |
|---|---|---|---|---|
| `nginx.http.requests` | Sum, cumulative, monotonic, int | `requests` | `nginx.zone.name`, `nginx.zone.type` = `SERVER` | Plus (`requests`), VTS (`requestCounter`) |
| `nginx.http.response.status` | Sum, cumulative, monotonic, int | `responses` | `nginx.status_range` = `1xx`…`5xx`, `nginx.zone.name`, `nginx.zone.type` | Plus, VTS (`responses`) |
| `nginx.http.upstream.peer.requests` | Sum, cumulative, monotonic, int | `requests` | `nginx.peer.address`, `nginx.peer.name`, `nginx.upstream.name`, `nginx.zone.name` (Plus only) | Plus, VTS (`server` is name and address) |
| `nginx.http.upstream.peer.responses` | Sum, cumulative, monotonic, int | `responses` | peer attributes + `nginx.status_range` | Plus, VTS |
| `nginx.http.upstream.peer.fails` | Sum, cumulative, monotonic, int | `attempts` | peer attributes | Plus |
| `nginx.http.upstream.peer.state` | Gauge, int | `is_deployed` | peer attributes + `nginx.peer.state` = `UP`, `DOWN`, `DRAINING`, `UNAVAILABLE`, `UNHEALTHY`, `CHECKING` (value 1 for the current state) | Plus |

5xx rate alert: `rate(nginx.http.response.status{nginx.status_range="5xx"})` / `rate(sum by zone of nginx.http.response.status)`.

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

**Redis Cluster** — when `INFO` reports `cluster_enabled:1` the agent also runs `CLUSTER INFO` and `CLUSTER NODES` (ACL: `+cluster|info +cluster|nodes`;
without them the INFO metrics are kept and the collection is partial with a hint). Every cluster node is its own instance, so the INFO metrics above
are per node. Resource attribute: `redis.cluster.node.id` (id of the `myself` line of `CLUSTER NODES`). Names, types and units are those of
redisreceiver's `metadata.yaml` (all *opt-in in OTel*; the receiver itself only fills `redis.cluster.cluster_enabled`). Not emitted:
`redis.cluster.uptime` / `redis.cluster.node.uptime` (defined with unit `s` but meaning the cluster/node epoch).

| Metric | Type | Unit | Attributes | Field |
|---|---|---|---|---|
| `redis.cluster.cluster_enabled` | Gauge, int | `1` | — | INFO `cluster_enabled` (every server, `0` or `1`) |
| `redis.cluster.state` | Gauge, int | `{state}` | `cluster_state` = `ok` (value 1), `fail` (value 0) | `cluster_state` |
| `redis.cluster.slots_assigned` / `slots_ok` / `slots_pfail` / `slots_fail` | Gauge, int | `{slot}` | — | `cluster_slots_assigned`, `_ok`, `_pfail`, `_fail` |
| `redis.cluster.known_nodes` | Gauge, int | `{node}` | — | `cluster_known_nodes` |
| `redis.cluster.node.count` | Gauge, int | `{node}` | — | `cluster_size` (masters serving slots) |
| `redis.cluster.stats_messages_sent` / `stats_messages_received` | Sum, monotonic, int | `{message}` | — | `cluster_stats_messages_sent`, `_received` |
| `redis.cluster.links_buffer_limit_exceeded.count` | Sum, monotonic, int | `{count}` | — | `total_cluster_links_buffer_limit_exceeded` |

**MySQL / MariaDB** (rules `mysql` and `mariadb`, integration id `mysql`) — native protocol, `mysql_native_password` and
`caching_sha2_password` (RSA key exchange without TLS). Queries: `SHOW GLOBAL STATUS`, `SELECT @@innodb_buffer_pool_size`,
top-N `performance_schema.table_io_waits_summary_by_table` / `…_by_index_usage` (ordered by `SUM_TIMER_WAIT`), `SHOW REPLICA STATUS`
(`SHOW SLAVE STATUS` on older servers). Required privileges: `PROCESS`, `REPLICATION CLIENT` (MariaDB ≥ 10.5.9: `REPLICA MONITOR` or `SLAVE MONITOR`
for replica status), `SELECT ON performance_schema.*`. Missing performance_schema/replica privileges make a collection partial, as does
`performance_schema = OFF` (the MariaDB default; the io wait tables exist but stay empty) with a hint to enable it. io waits: schemas `mysql`,
`performance_schema`, `information_schema`, `sys` excluded; ties ordered by schema, table (, index); counts from `COUNT_*`, times
`FLOOR(SUM_TIMER_*/1000)` (picoseconds → ns) per operation. mysqlreceiver's index query selects `FETCH, INSERT, UPDATE, DELETE` but scans them as
`delete, fetch, insert, update`; openlog emits the correct operation labels.
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
| query (opt-in `query_stats`) | `postgresql.database.name`, `postgresql.queryid`, `postgresql.rolname`, `db.query.text` |

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

**Top statements** (`integrations.postgresql.query_stats.enabled: true`, openlog extension). postgresqlreceiver reports `pg_stat_statements` only as
`db.server.top_query` log events; openlog emits metrics and reuses that event's attribute keys. Queried on the `database` connection: the
extension must be installed there (`SELECT extversion FROM pg_extension`), the server needs `shared_preload_libraries = 'pg_stat_statements'`, and
statements of other roles need `pg_read_all_stats` (included in `pg_monitor`). The top `top_n` (default 20, max 100) rows of
`pg_stat_statements` by `total_exec_time` (`total_time` for extension versions < 1.8, PostgreSQL < 13) with `calls ≥ max(1, min_calls)` of the
collected databases; one resource per `queryid` + database + role (a duplicate from `pg_stat_statements.track = all` keeps the first row).
`db.query.text` is the statement as normalized by PostgreSQL (`$1` placeholders) with whitespace collapsed, string literals (`'…'`, `E'…'`,
`$$…$$`) replaced by `'?'`/`$$?$$` (utility statements such as `CREATE ROLE … PASSWORD '…'` are not normalized by PostgreSQL) and truncated to
1024 bytes. A missing extension, `shared_preload_libraries` error (SQLSTATE 55000), permission error or rows shown as `<insufficient privilege>`
make the collection partial with a hint (`CREATE EXTENSION pg_stat_statements`, `shared_preload_libraries`, `pg_read_all_stats`); other metrics are
unaffected.

| Metric | Resource | Type | Unit | Attributes | Column |
|---|---|---|---|---|---|
| `postgresql.query.calls` | query | Sum, monotonic, int | `{call}` | — | `calls` |
| `postgresql.query.rows` | query | Sum, monotonic, int | `{row}` | — | `rows` |
| `postgresql.query.total_exec_time` | query | Sum, monotonic, double | `ms` | — | `total_exec_time` (mean = Δtotal_exec_time / Δcalls) |
| `postgresql.query.shared_blocks` | query | Sum, monotonic, int | `{block}` | `source` = `hit`, `read` | `shared_blks_hit`, `shared_blks_read` |

### 6.6 Docker Engine

Integration id `docker` (rule `docker`) sends **no metrics**: the OTel `docker_stats` receiver defines only `container.*` metrics, which the agent
already sends from cgroup v2 (§2 container metrics), and there are no OTel engine-level `docker.*` names. The integration reports the Docker Engine
API state for the service card: `enabled` when `GET /containers/json` succeeds on `containers.docker_socket`, `needs_configuration` on permission
denied (hint: docker group membership, which is root-equivalent), `error` when the socket is missing or the API fails, `not_available` when
`containers.enabled` is false. The state reflects the Docker Engine API only: on hosts where only a CRI runtime (containerd, CRI-O) answers,
container listing succeeds but the integration reports `error` (`docker socket not found`).

### 6.7 Microsoft SQL Server (`mssql`, D-113)

Integration id `mssql` (rule `mssql`: Windows service `MSSQLSERVER`/`MSSQL$<instance>`, `sqlservr`, port 1433, `mssql/server` containers, the
`mssql-server` package on Linux). TDS over TCP with `github.com/microsoft/go-mssqldb` (pinned in `agents/infra/go.mod`) on every OS: a remote server
(Azure SQL Managed Instance, a Linux host without the agent) is monitored from any agent with `integrations.mssql.instances[].endpoint`. Unix
sockets are skipped (no TDS endpoint). SQL Server authentication only (`username`/`password`, remote config D-039: endpoint, username, password);
without credentials the status is `needs_configuration` (the rule requires `credentials`), a failed login (18456) without a password
`needs_configuration`, with a password `error: authentication failed`. Connection: database `master`, `app name=openlog-infra-agent`,
`encrypt=false` (login packet only, the SQL Server default) or, with `tls.enabled`, `encrypt=true` with `TrustServerCertificate` =
`tls.insecure_skip_verify`, `certificate` = `tls.ca_file`, `hostNameInCertificate` = `tls.server_name`. Required permissions: `VIEW SERVER STATE`
(SQL Server 2022: `VIEW SERVER PERFORMANCE STATE`) and, for database sizes, `VIEW ANY DEFINITION`; a missing permission for sizes or waits makes
the collection partial.

Resource attributes: `sqlserver.version` (`SERVERPROPERTY('ProductVersion')`), `sqlserver.instance.name` (`@@SERVICENAME`). Cumulative sums
carry `start_time_unix_nano` = `sqlserver_start_time`. Sources: `sys.dm_os_performance_counters` (object prefix `SQLServer:` / `MSSQL$<instance>:`
removed; per-instance counters use `_Total`), `sys.master_files`, `sys.dm_os_wait_stats`. `.rate` gauges are per-second deltas of the cumulative
`/sec` counters between two collections of the same connection (none on the first collection or after a counter reset).

| Metric | Type | Unit | Attributes | Source |
|---|---|---|---|---|
| `sqlserver.user.connection.count` | Gauge, int | `{connections}` | — | General Statistics `User Connections` |
| `sqlserver.processes.blocked` *(opt-in in OTel)* | Gauge, int | `{processes}` | — | General Statistics `Processes blocked` |
| `sqlserver.batch.request.rate` | Gauge, double | `{requests}/s` | — | SQL Statistics `Batch Requests/sec` |
| `sqlserver.batch.sql_compilation.rate` | Gauge, double | `{compilations}/s` | — | SQL Statistics `SQL Compilations/sec` |
| `sqlserver.batch.sql_recompilation.rate` | Gauge, double | `{compilations}/s` | — | SQL Statistics `SQL Re-Compilations/sec` |
| `sqlserver.deadlock.rate` *(opt-in in OTel)* | Gauge, double | `{deadlocks}/s` | — | Locks `Number of Deadlocks/sec` (`_Total`) |
| `sqlserver.deadlock.count` *(openlog, not in OTel)* | Sum, monotonic, int | `{deadlocks}` | — | same counter, cumulative (no deadlock is lost between collections) |
| `sqlserver.lock.wait.rate` | Gauge, double | `{requests}/s` | — | Locks `Lock Waits/sec` (`_Total`) |
| `sqlserver.transaction.rate` | Gauge, double | `{transactions}/s` | — | Databases `Transactions/sec` (`_Total`) |
| `sqlserver.page.split.rate` | Gauge, double | `{pages}/s` | — | Access Methods `Page Splits/sec` |
| `sqlserver.page.buffer_cache.hit_ratio` | Gauge, double | `%` | — | Buffer Manager `Buffer cache hit ratio` / `… base` × 100 |
| `sqlserver.page.life_expectancy` | Gauge, int | `s` | `performance_counter.object_name` = `Buffer Manager` | Buffer Manager `Page life expectancy` |
| `sqlserver.database.size` *(openlog, not in OTel)* | Sum, non-monotonic, int | `By` | `sqlserver.database.name`, `file_type` (`rows`, `log`, `filestream`, `fulltext`) | `sys.master_files` pages × 8192 |
| `sqlserver.os.wait.duration` *(opt-in in OTel)* | Sum, monotonic, double | `s` | `wait.type`, `wait.category` | `sys.dm_os_wait_stats.wait_time_ms` / 1000, top `top_n_tables` (10) wait types by total wait time, idle/background waits excluded; category as in Query Store (`Lock`, `Buffer IO`, `Buffer Latch`, `Latch`, `Tran Log IO`, `Network IO`, `Parallelism`, `CPU`, `Memory`, `Preemptive`, `Other Disk IO`, `Replication`, `Other`) |

### 6.8 Microsoft IIS (`iis`, D-113)

Integration id `iis` (rule `iis`: Windows service `W3SVC`, `w3wp` worker processes), Windows only; on other OSes the status is `not_available`.
No endpoint, no credentials (`auto_enable`). The service process (LocalSystem) reads the raw performance counter classes through WMI
(`SELECT * FROM Win32_PerfRawData_W3SVC_WebService` and `Win32_PerfRawData_APPPOOLCountersProvider_APPPOOLWAS`); raw values are the cumulative
counts behind the `…/sec` counters. `_Total` instances are skipped. Status: `error` when the Web Service class cannot be read or has no site instance
(W3SVC stopped, counters not registered: `lodctr /R`); missing application pool counters or properties unknown to the Windows version make the
collection partial.

Resources: one per site (`iis.site`) and one per application pool (`iis.application_pool`), each with the instance attributes of §6.1.

| Metric | Type | Unit | Attributes | Counter (per site unless noted) |
|---|---|---|---|---|
| `iis.connection.active` | Sum, non-monotonic, int | `{connections}` | — | `CurrentConnections` |
| `iis.connection.anonymous` | Sum, monotonic, int | `{connections}` | — | `TotalAnonymousUsers` |
| `iis.connection.attempt.count` | Sum, monotonic, int | `{attempts}` | — | `TotalConnectionAttemptsallinstances` |
| `iis.network.blocked` | Sum, monotonic, int | `By` | — | `TotalBlockedBandwidthBytes` |
| `iis.network.io` | Sum, monotonic, int | `By` | `direction` = `sent`, `received` | `TotalBytesSent`, `TotalBytesReceived` |
| `iis.network.file.count` | Sum, monotonic, int | `{files}` | `direction` = `sent`, `received` | `TotalFilesSent`, `TotalFilesReceived` |
| `iis.request.count` | Sum, monotonic, int | `{requests}` | `request` = `delete`, `get`, `head`, `options`, `post`, `put`, `trace` | `Total<Method>Requests` |
| `iis.request.not_found.count` *(openlog, not in OTel)* | Sum, monotonic, int | `{requests}` | — | `TotalNotFoundErrors` |
| `iis.application_pool.state` | Gauge, int | `{state}` | — (resource `iis.application_pool`) | `CurrentApplicationPoolState`: 1 Uninitialized, 2 Initialized, 3 Running, 4 Disabling, 5 Disabled, 6 Shutdown Pending, 7 Delete Pending |

Not collected: `iis.uptime`, `iis.application_pool.uptime` (elapsed-time counters need the performance counter timebase), `iis.request.queue.*`,
`iis.request.rejected`, `iis.thread.active` (HTTP Service Request Queues / W3SVC_W3WP classes).

### 6.8b Database query performance (`query_stats`, D-138)

Turned on per database integration (`integrations.<postgresql|mysql|mssql>.query_stats.enabled`), the agent sends
statement statistics, session samples and execution plans as OTLP **log records**, which the processor routes to
its own tables rather than to `logs`. The records, their attributes, what each server needs and the API are the
contract of [db-monitoring.md](db-monitoring.md); the server metrics of §6.4–6.7 are unchanged by it.

### 6.9 Prometheus and OpenMetrics endpoints (`prometheus`, D-137)

Not bound to discovery: the infra agent scrapes (a) the static `prometheus.targets[]` of `config.yaml`, (b) running containers
labelled `prometheus.io/scrape: "true"` (`prometheus.containers`, default on) and (c) in Kubernetes node mode the `Running` pods of the node
annotated `prometheus.io/scrape: "true"` (`prometheus.kubernetes_pods`, default on; containers of a pod are then not scraped through their
labels, so a pod is never scraped twice). Opt-in keys, the same on labels and annotations: `prometheus.io/port` (default: the only declared TCP
port; none or several declared is a skipped target with a warning), `prometheus.io/path` (default `/metrics`, may carry a query),
`prometheus.io/scheme` (`http` default, `https`), `prometheus.io/job`. Address: the pod IP, the container's first network address, or
`127.0.0.1:<published port>` for a container without an address of its own. Targets are refreshed every `prometheus.interval`; static targets
come first and at most `prometheus.max_targets` (64) are scraped.

Scrape: `GET` with `Accept: application/openmetrics-text;version=1.0.0;q=0.75,text/plain;version=0.0.4;q=0.5,*/*;q=0.1`,
`X-Prometheus-Scrape-Timeout-Seconds`, gzip, same-host redirects only, bearer token or basic auth and TLS (`ca_file`, `server_name`,
`insecure_skip_verify`) for static targets. The whole scrape is rejected (no partial families) when the body exceeds `body_limit_bytes`
(32 MiB), the target exposes more than `sample_limit` (20 000) samples or the exposition does not parse. Text format 0.0.4 and OpenMetrics
1.0 (including UTF-8 metric names in quotes) are parsed by the agent itself (`internal/promscrape`); `prometheus.metrics.include`/`exclude`
(globs on family names) drop families before conversion.

Conversion (the OTel Collector `prometheus` receiver's mapping):

| Exposition | OTLP | Notes |
|---|---|---|
| counter | Sum, cumulative, monotonic, double | name = sample name (`http_requests_total`) |
| gauge, unknown/untyped, info (`x_info` = 1), stateset (one point per state) | Gauge, double | gaugehistogram: `x_bucket`, `x_gsum`, `x_gcount` gauges |
| histogram | Histogram, cumulative | cumulative `le` buckets become per-bucket counts, the last one `(max, +Inf]`; `count` from `_count` (else the `+Inf` bucket) |
| summary | Summary | `quantile` label → quantile values |

Every label becomes a data point attribute; `le` and `quantile` are consumed. Sample timestamps are kept (text: ms, OpenMetrics: s). Start
time of cumulative points: `_created` when exposed, else the first scrape that saw the series; a value lower than the previous one is a reset
and starts a new run at the previous scrape. OpenMetrics exemplars become OTLP exemplars (`trace_id`/`span_id` labels → trace and span id, other
labels filtered attributes), stored like every exemplar (§8, D-130). UNIT `seconds`/`bytes`/`ratio`/… is mapped to UCUM (`s`, `By`, `1`).

Resource (one per scrape): the host resource of §1, then the target's `labels` (static) or container attributes (`container.id`,
`container.name`, `container.image.name`, Kubernetes pod attributes of §7.2) or pod attributes (§7.2), then `service.name` = job (static: `job`
or the URL host; container: `prometheus.io/job`, the Compose service or the container name; pod: `prometheus.io/job` or
`<namespace>/<workload>`), `service.instance.id` = `host:port`, `server.address`, `server.port`, `url.scheme`, `openlog.integration.id` =
`prometheus`, `openlog.scrape.source` = `static`/`container`/`pod`. Every scrape, successful or not, adds the Prometheus health gauges `up`
(1/0), `scrape_duration_seconds` and `scrape_samples_scraped`, so an unreachable exporter is visible and alertable (`up == 0`). Agent
self-telemetry counts scrapes as `openlog.agent.integration.*{integration="prometheus"}`.

### 6.10 Apache HTTP Server (`apache`, D-139)

Source: the `mod_status` page in its machine-readable form (`?auto`). Integration id `apache` (rule `apache-httpd`), `auto_enable: true`: with no
configured `endpoint` the paths `/server-status?auto`, `/status?auto`, `/apache-status?auto`, `/httpd-status?auto` are probed on every candidate
endpoint over `http`, then `https` (certificates are not verified only for loopback endpoints). The first page carrying `Total Accesses` and `Uptime`
is the instance's page and is remembered across collector restarts. A port that answers HTTP without such a page, or a page `mod_status` does not
serve, is `needs_configuration` with the hint; a port that does not answer is the next candidate. No credentials: a page behind authentication is
configured with `integrations.apache.endpoint`. `ExtendedStatus On` (the default when `mod_status` is loaded on most distributions) is what adds
request counts, traffic, CPU and the scoreboard; without it only workers and connections are reported.

Resource: one per instance (§6.1) plus `apache.server.version` (e.g. `Apache/2.4.58 (Unix)`). Counters are cumulative from the server's start, which
`Uptime` gives as the start time.

| Metric | Type | Unit | Attributes | mod_status field |
|---|---|---|---|---|
| `apache.uptime` | Sum, monotonic, int | `s` | — | `Uptime` |
| `apache.requests` | Sum, monotonic, int | `{requests}` | — | `Total Accesses` |
| `apache.traffic` | Sum, monotonic, int | `By` | — | `Total kBytes` × 1024 |
| `apache.request.time` | Sum, monotonic, int | `ms` | — | `Total Duration` (2.4.37+) |
| `apache.workers` | Sum, non-monotonic, int | `{workers}` | `state` = `busy`, `idle` | `BusyWorkers`, `IdleWorkers` |
| `apache.current_connections` | Sum, non-monotonic, int | `{connections}` | — | `ConnsTotal` (event/worker MPM) |
| `apache.connections.async` | Sum, non-monotonic, int | `{connections}` | `state` = `writing`, `keep-alive`, `closing` | `ConnsAsync*` |
| `apache.scoreboard` | Sum, non-monotonic, int | `{workers}` | `state` = `waiting`, `starting`, `reading`, `sending`, `keepalive`, `dnslookup`, `closing`, `logging`, `finishing`, `idle_cleanup`, `open`, `unknown` | `Scoreboard`, one character per worker slot |
| `apache.cpu.load` | Gauge, double | `%` | — | `CPULoad` |
| `apache.cpu.time` | Sum, monotonic, double | `s` | `level` = `self`, `children`; `mode` = `user`, `system` | `CPUUser`, `CPUSystem`, `CPUChildren*` |
| `apache.load.1` / `.5` / `.15` | Gauge, double | `%` | — | `Load1`, `Load5`, `Load15` (2.4.31+) |

### 6.11 Memcached (`memcached`, D-139)

Source: the `stats` command of the text protocol on the discovered port (11211), `auto_enable: true`, no credentials. A server with SASL
authentication answers `CLIENT_ERROR` and is reported `needs_configuration`: the agent does not authenticate to Memcached. Counters are cumulative
from the server's start (`uptime` gives the start time).

| Metric | Type | Unit | Attributes | `stats` field |
|---|---|---|---|---|
| `memcached.uptime` | Sum, monotonic, int | `s` | — | `uptime` |
| `memcached.bytes` | Sum, non-monotonic, int | `By` | — | `bytes` |
| `memcached.connections.current` | Sum, non-monotonic, int | `{connections}` | — | `curr_connections` |
| `memcached.connections.total` | Sum, monotonic, int | `{connections}` | — | `total_connections` |
| `memcached.current_items` | Sum, non-monotonic, int | `{items}` | — | `curr_items` |
| `memcached.evictions` | Sum, monotonic, int | `{evictions}` | — | `evictions` |
| `memcached.threads` | Sum, non-monotonic, int | `{threads}` | — | `threads` |
| `memcached.network` | Sum, monotonic, int | `By` | `direction` = `sent`, `received` | `bytes_written`, `bytes_read` |
| `memcached.commands` | Sum, monotonic, int | `{commands}` | `command` = `get`, `set`, `flush`, `touch` | `cmd_*` |
| `memcached.operations` | Sum, monotonic, int | `{operations}` | `operation` = `get`, `increment`, `decrement`, `delete`; `type` = `hit`, `miss` | `*_hits`, `*_misses` |
| `memcached.operation_hit_ratio` | Gauge, double | `%` | `operation` | derived: hits / (hits + misses) |
| `memcached.cpu.usage` | Sum, monotonic, double | `s` | `state` = `user`, `system` | `rusage_user`, `rusage_system` |

### 6.12 HAProxy (`haproxy`, D-139)

Source: the CSV statistics, read either from the stats page over HTTP (`;csv`) or from the runtime API socket (`show stat`). `auto_enable: true`:
the paths `/;csv`, `/stats;csv`, `/haproxy?stats;csv`, `/haproxy_stats;csv` are probed on the candidate endpoints (default port 8404) and the
well-known runtime sockets (`/run/haproxy/admin.sock`, `/var/run/haproxy/admin.sock`, `/run/haproxy.sock`, `/var/lib/haproxy/stats`) are tried as
endpoints; the source that answered is remembered. The socket needs at least level `operator` and a group the agent's user is in. A configured
`endpoint` is either the `http(s)://…` stats URL or `unix:/path`. No credentials.

Resources: one per CSV row — a frontend, a backend or one of its servers — with `haproxy.proxy.name` (`pxname`), `haproxy.service.name` (`svname`)
and `haproxy.proxy.type` = `frontend`, `backend`, `server`, `listener`. At most 500 rows are stored per collection (`MaxProxies`); beyond that the
collection is partial, so the cardinality of a busy load balancer is a property of its configuration, not a surprise on the backend.

| Metric | Type | Unit | Attributes | CSV column |
|---|---|---|---|---|
| `haproxy.status` | Gauge, int (1) | `{status}` | `state` = `up`, `down`, `open`, `maint`, `drain`, `nolb`, … | `status` (first word; `UP 2/3` → `up`) |
| `haproxy.sessions.count` | Sum, monotonic, int | `{sessions}` | — | `stot` |
| `haproxy.sessions.current` / `.limit` | Sum, non-monotonic, int | `{sessions}` | — | `scur`, `slim` |
| `haproxy.sessions.rate` | Gauge, int | `{sessions}/s` | — | `rate` |
| `haproxy.connections.total` | Sum, monotonic, int | `{connections}` | — | `conn_tot` |
| `haproxy.connections.rate` | Gauge, int | `{connections}/s` | — | `conn_rate` |
| `haproxy.connections.denied` / `.errors` | Sum, monotonic, int | `{connections}` | — | `dcon`, `econ` |
| `haproxy.requests.total` | Sum, monotonic, int | `{requests}` | — | `req_tot` |
| `haproxy.requests.rate` | Gauge, int | `{requests}/s` | — | `req_rate` |
| `haproxy.requests.denied` / `.errors` | Sum, monotonic, int | `{requests}` | — | `dreq`, `ereq` |
| `haproxy.responses.count` | Sum, monotonic, int | `{responses}` | `status_code` = `1xx`…`5xx`, `other` | `hrsp_*` |
| `haproxy.responses.errors` | Sum, monotonic, int | `{responses}` | — | `eresp` |
| `haproxy.bytes` | Sum, monotonic, int | `By` | `direction` = `received`, `sent` | `bin`, `bout` |
| `haproxy.server.retries` / `.redispatches` | Sum, monotonic, int | `{retries}` / `{redispatches}` | — | `wretr`, `wredis` |
| `haproxy.health_check.failures` | Sum, monotonic, int | `{checks}` | — | `chkfail` |
| `haproxy.queue.current` | Gauge, int | `{requests}` | — | `qcur` |
| `haproxy.queue.time`, `.connect.time`, `.response.time`, `.session.time` | Gauge, int | `ms` | — | `qtime`, `ctime`, `rtime`, `ttime` (last 1024 requests) |
| `haproxy.servers` | Gauge, int | `{servers}` | `state` = `active`, `backup` | `act`, `bck` |

An empty column emits nothing: a frontend has no queue or backend timings, and absence must not read as zero.

### 6.13 RabbitMQ (`rabbitmq`, D-139)

Source: the management plugin's HTTP API (`/api/overview`, `/api/nodes`, `/api/queues`) on port 15672 — discovery finds the AMQP port (5672), so the
integration moves to the management port unless an `endpoint` says otherwise. `auto_enable: true` with `requires: []`: the broker itself answers
whether credentials are needed (HTTP 401 → `needs_configuration` with the hint), so an anonymous or differently secured broker is not assumed to need
a login. The recommended user is `monitoring`-tagged with read-only permissions. The queue listing asks for the columns it uses and at most 500
queues (`MaxQueues`); more is a partial collection, as is a broker whose node or queue listing fails while the overview succeeded.

Resources: the instance resource (§6.1) with `rabbitmq.version` and `rabbitmq.cluster.name`; one per node (`rabbitmq.node.name`); one per queue
(`rabbitmq.queue.name`, `rabbitmq.vhost.name`, `rabbitmq.node.name`). Message and consumer metrics are the queue's, as in the OTel
`rabbitmqreceiver`: the broker's own totals are **not** emitted under the same names, because summing an instance would then count every message
twice — once in its queue and once in the total.

| Metric | Type | Unit | Attributes | Resource / API field |
|---|---|---|---|---|
| `rabbitmq.connection.count`, `.channel.count`, `.exchange.count`, `.queue.count` | Sum, non-monotonic, int | `{connections}`, `{channels}`, `{exchanges}`, `{queues}` | — | instance, `/api/overview` `object_totals` |
| `rabbitmq.message.current` | Sum, non-monotonic, int | `{messages}` | `state` = `ready`, `unacknowledged` | queue |
| `rabbitmq.message.published`, `.delivered`, `.acknowledged`, `.redelivered`, `.dropped` | Sum, monotonic, int | `{messages}` | — | queue, `message_stats` (`delivered` = `deliver` + `deliver_get`, `dropped` = unroutable) |
| `rabbitmq.consumer.count` | Sum, non-monotonic, int | `{consumers}` | — | queue |
| `rabbitmq.queue.state` | Gauge, int (1) | `{status}` | `state` = `running`, `idle`, `flow`, … | queue |
| `rabbitmq.node.up` | Gauge, int | `{status}` | `type` = `disc`, `ram` | node, `running` |
| `rabbitmq.node.memory.used` / `.limit` | Sum, non-monotonic, int | `By` | — | node |
| `rabbitmq.node.disk.free` / `.free_limit` | Sum, non-monotonic, int | `By` | — | node |
| `rabbitmq.node.file_descriptors.used` / `.limit`, `.sockets.used` / `.limit`, `.processes.used` / `.limit` | Sum, non-monotonic, int | `{file_descriptors}`, `{sockets}`, `{processes}` | — | node |
| `rabbitmq.node.run_queue` | Gauge, int | `{processes}` | — | node |
| `rabbitmq.node.uptime` | Sum, monotonic, int | `s` | — | node (ms / 1000) |
| `rabbitmq.node.alarm` | Gauge, int (0/1) | `{status}` | `kind` = `memory`, `disk` | node; an alarm is why the broker stops accepting publishes |
| `rabbitmq.node.partitions` | Gauge, int | `{partitions}` | — | node, network partitions seen |

### 6.14 Elasticsearch and OpenSearch (`elasticsearch`, D-139)

Source: the REST API on port 9200 — `/` (version and distribution), `/_cluster/health` and `/_nodes/_local/stats`. Only the local node's statistics
are read: an agent runs on every node, and asking each node about all the others would multiply the same series by the size of the cluster. The
transport port (9300) is never offered as an endpoint. `auto_enable: true` with `requires: []`: a cluster with security disabled needs no
credentials, and a secured one answers HTTP 401, which becomes `needs_configuration` with the hint (`monitoring_user` is the built-in read-only
role). The OpenSearch discovery rule uses this integration; `elasticsearch.distribution` tells the two apart. Reachable cluster, unreadable node
statistics: partial, with the cluster health kept.

Resources: the instance resource (§6.1) with `elasticsearch.cluster.name`, `elasticsearch.version` and `elasticsearch.distribution`
(`elasticsearch`, `opensearch`); one per node (`elasticsearch.node.name`).

| Metric | Type | Unit | Attributes | API field |
|---|---|---|---|---|
| `elasticsearch.cluster.health` | Gauge, int | `{status}` | `status` = `green`, `yellow`, `red` | `status`, as 0, 1, 2 so a chart and an alert can use it |
| `elasticsearch.cluster.nodes` / `.data_nodes` | Sum, non-monotonic, int | `{nodes}` | — | `number_of_nodes`, `number_of_data_nodes` |
| `elasticsearch.cluster.shards` | Sum, non-monotonic, int | `{shards}` | `state` = `active`, `active_primary`, `relocating`, `initializing`, `unassigned`, `delayed_unassigned` | health shard counts |
| `elasticsearch.cluster.pending_tasks` | Sum, non-monotonic, int | `{tasks}` | — | `number_of_pending_tasks` |
| `elasticsearch.cluster.in_flight_fetch` | Gauge, int | `ms` | — | `task_max_waiting_in_queue_millis` |
| `elasticsearch.node.documents` | Sum, non-monotonic, int | `{documents}` | `state` = `active`, `deleted` | `indices.docs` |
| `elasticsearch.node.disk.usage` | Sum, non-monotonic, int | `By` | — | `indices.store.size_in_bytes` |
| `elasticsearch.node.operations.completed` / `.time` | Sum, monotonic, int | `{operations}` / `ms` | `operation` = `index`, `query`, `fetch`, `merge` | `indices.indexing`, `.search`, `.merges` |
| `elasticsearch.node.operations.failed` | Sum, monotonic, int | `{operations}` | `operation` = `index` | `indices.indexing.index_failed` |
| `elasticsearch.node.translog.operations` / `.size` | Sum, non-monotonic, int | `{operations}` / `By` | — | `indices.translog` |
| `elasticsearch.node.cache.memory.usage`, `.evictions` | Sum (non-monotonic / monotonic), int | `By` / `{evictions}` | `cache_name` = `query`, `fielddata` | `indices.query_cache`, `.fielddata` |
| `elasticsearch.node.cache.count` | Sum, monotonic, int | `{hits}` | `type` = `hit`, `miss` | `indices.query_cache` |
| `elasticsearch.node.thread_pool.threads`, `.threads.active`, `.tasks.queued`, `.tasks.rejected`, `.tasks.finished` | Sum (monotonic for rejected and finished), int | `{threads}` / `{tasks}` | `thread_pool_name` | `thread_pool.*`, at most 32 pools |
| `elasticsearch.node.open_files` | Sum, non-monotonic, int | `{files}` | — | `process.open_file_descriptors` |
| `elasticsearch.node.cpu.usage` | Gauge, int | `%` | — | `process.cpu.percent` |
| `elasticsearch.node.fs.disk.free` / `.total` | Sum, non-monotonic, int | `By` | — | `fs.total` |
| `elasticsearch.breaker.tripped` | Sum, monotonic, int | `{breaks}` | `circuit_breaker_name` | `breakers.*.tripped` |
| `elasticsearch.breaker.memory.estimated` / `.limit` | Sum, non-monotonic, int | `By` | `circuit_breaker_name` | `breakers.*` |
| `jvm.memory.heap.used` / `.max`, `.nonheap.used` | Sum, non-monotonic, int | `By` | — | `jvm.mem` |
| `jvm.memory.heap.utilization` | Gauge, int | `%` | — | `jvm.mem.heap_used_percent` |
| `jvm.threads.count` | Sum, non-monotonic, int | `{threads}` | — | `jvm.threads.count` |
| `jvm.gc.collections.count` / `.elapsed` | Sum, monotonic, int | `{collections}` / `ms` | `name` = `young`, `old` | `jvm.gc.collectors` |

### 6.15 MongoDB (`mongodb`, D-143)

Sources: `serverStatus` (the server), `listDatabases` and `dbStats` (per database) and, on a replica set
member, `replSetGetStatus`. Integration id `mongodb` (rule `mongodb`), default port 27017, credentials
required: MongoDB answers nothing useful to an unauthenticated client on a secured deployment, and the
monitoring user is the built-in `clusterMonitor` role.

The connection is **direct** (`directConnection`): the agent runs next to this `mongod` and is asking about
*this* process, so following the topology to a primary elsewhere would report another machine's numbers
under this host's name. `integrations.mongodb.database` is the authentication source (default `admin`), not
a database to monitor; `databases` / `exclude_databases` bound which ones get `dbStats`, at most 32
(`MaxDatabases`), because a database is a set of series and an installation with one per tenant has
thousands.

Resources: one per instance (§6.1) with `db.version`, `mongodb.process` (`mongod` or `mongos` — a router
reports no storage numbers, and saying which it is explains why) and, on a replica set,
`mongodb.replica_set.name`; one per database (`db.namespace`). Counters are cumulative from the server's
start, which `uptime` gives as the start time.

| Metric | Type | Unit | Attributes | serverStatus / dbStats field |
|---|---|---|---|---|
| `mongodb.uptime` | Sum, monotonic, int | `s` | — | `uptime` |
| `mongodb.connection.count` | Sum, non-monotonic, int | `{connections}` | `type` = `current`, `available`, `active` | `connections.*` |
| `mongodb.memory.usage` | Sum, non-monotonic, int | `By` | `type` = `resident`, `virtual` | `mem.*` (MiB → bytes) |
| `mongodb.operation.count` | Sum, monotonic, int | `{operations}` | `operation` = `insert`, `query`, `update`, `delete`, `getmore`, `command` | `opcounters.*` |
| `mongodb.document.operation.count` | Sum, monotonic, int | `{documents}` | `operation` = `insert`, `query`, `update`, `delete` | `metrics.document.*` |
| `mongodb.operation.latency.time` | Sum, monotonic, int | `us` | `operation` = `read`, `write`, `command` | `opLatencies.*.latency` |
| `mongodb.network.io.receive` / `.transmit` | Sum, monotonic, int | `By` | — | `network.bytesIn`, `network.bytesOut` |
| `mongodb.network.request.count` | Sum, monotonic, int | `{requests}` | — | `network.numRequests` |
| `mongodb.global_lock.time` | Sum, monotonic, int | `ms` | — | `globalLock.totalTime` (µs → ms) |
| `mongodb.cursor.count` | Sum, non-monotonic, int | `{cursors}` | `type` = `open`, `no_timeout` | `metrics.cursor.open.*` |
| `mongodb.cursor.timeout.count` | Sum, monotonic, int | `{cursors}` | — | `metrics.cursor.timedOut` |
| `mongodb.cache.operations` | Sum, monotonic, int | `{operations}` | `type` = `hit`, `miss` | WiredTiger pages requested minus pages read in, and pages read in |
| `mongodb.session.count` | Sum, non-monotonic, int | `{sessions}` | — | `logicalSessionRecordCache.activeSessionsCount` |
| `mongodb.asserts` | Sum, monotonic, int | `{asserts}` | `type` = `regular`, `warning` | `asserts.*` |
| `mongodb.database.count` | Sum, non-monotonic, int | `{databases}` | — | `listDatabases` |
| `mongodb.collection.count`, `.index.count`, `.object.count`, `.view.count` | Sum, non-monotonic, int | `{collections}`, `{indexes}`, `{objects}`, `{views}` | — (resource `db.namespace`) | `dbStats` |
| `mongodb.data.size`, `.storage.size`, `.index.size` | Sum, non-monotonic, int | `By` | — (resource `db.namespace`) | `dbStats` |
| `mongodb.replica_set.member` | Gauge, int (1) | `{status}` | `state` = `primary`, `secondary`, … | `replSetGetStatus` |
| `mongodb.replica_set.members` | Sum, non-monotonic, int | `{members}` | — | `replSetGetStatus` |
| `mongodb.replica_set.lag` | Gauge, int | `s` | — | the primary's `optimeDate` minus this member's |

`replSetGetStatus` fails on a standalone server (`not running with --replSet`), which is not reported as a
problem: most MongoDB servers people monitor are standalone. The lag is emitted **only** when the answer
names a primary — without one there is nothing to be behind, and reporting 0 would be a lie.

### 6.16 Java runtimes (`jvm`, D-144)

Source: the `java.lang` MBeans of any JVM that exposes Jolokia, read as **one bulk POST** per collection
(`internal/integrations/jolokia`). Integration id `jvm` (rule `jvm`: any `java` process), `auto_enable: true`
with `requires: []`: the JVM itself answers whether the bridge is there. The paths `/jolokia`,
`/actuator/jolokia`, `/jmx` and `/api/jolokia` are probed on the candidate endpoints and on port 8778 (the
standalone Jolokia agent's own port); the one that answers `/version` is remembered. No Jolokia, or a
bridge whose access policy hides `java.lang`, is `needs_configuration` with the hint.

Why Jolokia rather than JMX directly: reaching MBeans over JMX means Java RMI plus object serialization —
thousands of lines with no mature Go implementation, breaking on a Java release. Jolokia turns the same
MBeans into one HTTP request, and the cost moves to a `-javaagent` line the operator adds once, where it is
visible and revertible (D-144).

Resource: one per instance (§6.1) plus `jvm.version` (`Runtime.VmVersion`). Counters are cumulative from
the JVM's start, which `Runtime.Uptime` gives as the start time.

| Metric | Type | Unit | Attributes | MBean attribute |
|---|---|---|---|---|
| `jvm.uptime` | Sum, monotonic, int | `ms` | — | `Runtime.Uptime` |
| `jvm.memory.used` / `.committed` / `.limit` | Sum, non-monotonic, int | `By` | `jvm.memory.type` = `heap`, `non_heap` | `Memory.{Heap,NonHeap}MemoryUsage` |
| `jvm.memory.pool.used` / `.limit` | Sum, non-monotonic, int | `By` | `jvm.memory.pool.name` | `MemoryPool.Usage` (pattern read) |
| `jvm.thread.count` | Sum, non-monotonic, int | `{threads}` | `jvm.thread.daemon` = `true`, `false` | `Threading.{Thread,DaemonThread}Count` |
| `jvm.thread.peak` | Sum, non-monotonic, int | `{threads}` | — | `Threading.PeakThreadCount` |
| `jvm.class.count` | Sum, non-monotonic, int | `{classes}` | — | `ClassLoading.LoadedClassCount` |
| `jvm.class.unloaded` | Sum, monotonic, int | `{classes}` | — | `ClassLoading.UnloadedClassCount` |
| `jvm.gc.collections` | Sum, monotonic, int | `{collections}` | `jvm.gc.name` | `GarbageCollector.CollectionCount` (pattern read) |
| `jvm.gc.duration` | Sum, monotonic, int | `ms` | `jvm.gc.name` | `GarbageCollector.CollectionTime` |
| `jvm.cpu.recent_utilization` | Gauge, double | `1` | — | `OperatingSystem.ProcessCpuLoad` |
| `jvm.system.cpu.utilization` | Gauge, double | `1` | — | `OperatingSystem.SystemCpuLoad` |
| `jvm.file_descriptor.count` / `.limit` | Sum, non-monotonic, int | `{file_descriptors}` | — | `OperatingSystem.{Open,Max}FileDescriptorCount` |

Three modelling rules: a maximum of `-1` (a pool with no limit) is **not** emitted as a limit; a CPU share
of `-1` (the MXBean before its second sample) is not emitted at all; and the thread count is partitioned
into daemon and non-daemon rather than emitted as a total plus a subset, so summing the series gives the
total instead of counting the daemons twice.

### 6.17 Apache Kafka (`kafka`, D-144)

Source: the broker's own MBeans over the same Jolokia bridge, in one bulk read. Integration id `kafka`
(rule `kafka`), `auto_enable: true`. Discovery finds the broker's listener port (9092/9093/9094); the
bridge is elsewhere, so the endpoint moves to Jolokia's 8778 unless an `endpoint` says otherwise.

The JVM metrics of the same process are **not** repeated here: a broker is a JVM, `jvm` reads `java.lang`,
and both bind to the same endpoint. What this adds is what makes the process a broker.

| Metric | Type | Unit | Attributes | MBean |
|---|---|---|---|---|
| `kafka.messages.in` | Sum, monotonic, int | `{messages}` | — | `BrokerTopicMetrics.MessagesInPerSec.Count` |
| `kafka.network.io` | Sum, monotonic, int | `By` | `direction` = `in`, `out` | `BrokerTopicMetrics.Bytes{In,Out}PerSec.Count` |
| `kafka.request.count` / `.failed` | Sum, monotonic, int | `{requests}` | `type` = `fetch`, `produce` | `BrokerTopicMetrics.Total*/Failed*RequestsPerSec` |
| `kafka.request.total` | Sum, monotonic, int | `{requests}` | `type` (request name) | `RequestMetrics.RequestsPerSec` (pattern read) |
| `kafka.request.time.avg` | Gauge, double | `ms` | `type` (request name) | `RequestMetrics.TotalTimeMs.Mean` |
| `kafka.request.queue` / `kafka.response.queue` | Sum, non-monotonic, int | `{requests}` / `{responses}` | — | `RequestChannel.{Request,Response}QueueSize` |
| `kafka.request.handler.busy` | Gauge, double | `1` | — | `1 −` `KafkaRequestHandlerPool.RequestHandlerAvgIdlePercent` |
| `kafka.network.processor.busy` | Gauge, double | `1` | — | `1 −` `SocketServer.NetworkProcessorAvgIdlePercent` |
| `kafka.partition.count` / `.global` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.PartitionCount`, `KafkaController.GlobalPartitionCount` |
| `kafka.partition.under_replicated` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.UnderReplicatedPartitions` |
| `kafka.partition.under_min_isr` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.UnderMinIsrPartitionCount` |
| `kafka.partition.offline` | Sum, non-monotonic, int | `{partitions}` | — | `KafkaController.OfflinePartitionsCount` |
| `kafka.leader.count` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.LeaderCount` |
| `kafka.leader.election.count` / `.unclean` | Sum, monotonic, int | `{elections}` | — | `ControllerStats.*` |
| `kafka.isr.operation.count` | Sum, monotonic, int | `{operations}` | `operation` = `shrink`, `expand` | `ReplicaManager.Isr{Shrinks,Expands}PerSec` |
| `kafka.controller.active.count` | Sum, non-monotonic, int | `{controllers}` | — | `KafkaController.ActiveControllerCount` |
| `kafka.topic.count` | Sum, non-monotonic, int | `{topics}` | — | `KafkaController.GlobalTopicCount` |
| `kafka.purgatory.size` | Sum, non-monotonic, int | `{operations}` | `operation` = `produce`, `fetch` | `DelayedOperationPurgatory.PurgatorySize` |
| `kafka.logs.flush.count` | Sum, monotonic, int | `{flushes}` | — | `LogFlushStats.LogFlushRateAndTimeMs.Count` |
| `kafka.broker.state` | Gauge, int | `{state}` | — | `KafkaServer.BrokerState` (3 = running) |

The `Count` attribute is read rather than the `*Rate` ones: a rate the broker computed over its own window
cannot be re-aggregated, while a monotonic total can. `kafka.request.handler.busy` inverts the broker's own
idle share, because "how busy is it" is the question an operator asks.

## 7. Kubernetes (infra agent, M4, D-070, D-071)

The infra agent runs in Kubernetes from the `deploy/helm/openlog-agent` chart in two modes ([operations/kubernetes.md](../operations/kubernetes.md)):
**node** (DaemonSet, one per node: host, container, kubelet metrics and container logs) and **cluster** (one leader-elected
Deployment replica: kube-state metrics and Kubernetes events). Object identity is sent as **data point / log record attributes**
on one resource per agent, like container metrics (§2), so every Kubernetes series of a node lands on the node's shard and alert
rules filter it with `attr.*`. Names follow the OTel `kubeletstats` and `k8s_cluster` receivers; openlog-only names use `openlog.k8s.`.

### 7.1 Detection and resource

Kubernetes mode is on when `kubernetes.enabled` is `true`, or `auto` (default) and `KUBERNETES_SERVICE_HOST` is set. The API server is
reached in-cluster (`https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT`, ServiceAccount token and CA).

| Attribute | Node mode (host resource, §1) | Cluster mode resource |
|---|---|---|
| `k8s.cluster.name` | `kubernetes.cluster_name` (chart `clusterName`) | same |
| `k8s.cluster.uid` | uid of the `kube-system` namespace (omitted when it cannot be read) | same (required: the cluster agent does not start without it) |
| `k8s.node.name` | `kubernetes.node_name` (downward API `spec.nodeName`) | — |
| `openlog.entity.type` | `host` | `k8s_cluster` |
| `openlog.agent.mode` | `node` | `cluster` |
| `host.id`, `host.name`, OS attributes | as §1 | **not sent** (`host_id` = `''`; the cluster agent writes no host row) |
| `openlog.agent.name`, `openlog.agent.version` | as §1 | same |

### 7.2 Pod metadata on container metrics and logs (node mode)

The node agent watches the pods of its node (`GET /api/v1/pods?fieldSelector=spec.nodeName=<node>`, list + watch, relist every
`kubernetes.pod_resync` (5m) and after `410 Gone`). Containers listed from the CRI (`io.kubernetes.*` labels) or Docker are matched by
`container.id` (pod `status.containerStatuses[].containerID`) and else by pod uid + container name. These attributes are added to every
container metric data point (§2) and to the resource of container log records (§4.1), when known:

| Attribute | Source |
|---|---|
| `k8s.pod.name`, `k8s.namespace.name`, `k8s.container.name` | CRI labels `io.kubernetes.pod.name`, `io.kubernetes.pod.namespace`, `io.kubernetes.container.name` (now for containerd and CRI-O too), else the pod |
| `k8s.pod.uid` | CRI label `io.kubernetes.pod.uid`, else the pod |
| `k8s.node.name`, `k8s.cluster.name` | agent configuration |
| `k8s.replicaset.name`, `k8s.deployment.name` | controller owner `ReplicaSet`; the deployment is the ReplicaSet name without the `-<pod-template-hash>` suffix |
| `k8s.statefulset.name`, `k8s.daemonset.name`, `k8s.job.name` | controller owner of that kind |
| `k8s.cronjob.name` | owner `Job` whose name is `<cronjob>-<digits>` (CronJob-created jobs) |
| `openlog.k8s.workload.kind`, `openlog.k8s.workload.name` | top-level owner: `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob`, `Job`, `ReplicaSet` (no deployment), or `Pod` (bare pod, name = pod name) |
| `k8s.pod.label.<key>` | pod labels in `kubernetes.label_allowlist` (default `app`, `app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/version`, `app.kubernetes.io/component`) |

Log files of CRI containers are `/var/log/pods/<namespace>_<pod>_<pod-uid>/<container>/<restart>.log` (CRI log format, §4.1); when the
runtime metadata is missing the agent takes namespace, pod name, pod uid and container name from this path.

### 7.3 Kubelet metrics (node mode)

`GET https://<kubernetes.kubelet.endpoint>/stats/summary` (default `https://$OPENLOG_K8S_HOST_IP:10250`, ServiceAccount bearer token,
RBAC `nodes/stats get`) every metrics interval. Attributes of pod metrics: `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`,
`k8s.node.name` plus the owner attributes of §7.2; container metrics add `k8s.container.name` and `container.id` (from the pod watch);
node metrics carry `k8s.node.name`. Cumulative counters carry no start time (like container metrics, §2).

| Metric | Type | Unit | Summary field |
|---|---|---|---|
| `k8s.node.cpu.usage` | Gauge | `{cpu}` | `node.cpu.usageNanoCores` / 1e9 |
| `k8s.node.cpu.time` | Sum, cumulative, monotonic | `s` | `node.cpu.usageCoreNanoSeconds` / 1e9 |
| `k8s.node.memory.usage` · `k8s.node.memory.working_set` · `k8s.node.memory.rss` · `k8s.node.memory.available` | Gauge | `By` | `node.memory.*` |
| `k8s.node.network.io` | Sum, cumulative, monotonic | `By` | `node.network.interfaces[]` rx/tx summed; `network.io.direction` |
| `k8s.node.filesystem.usage` · `.capacity` · `.available` | Gauge | `By` | `node.fs` |
| `k8s.pod.cpu.usage` | Gauge | `{cpu}` | `pods[].cpu.usageNanoCores` / 1e9 |
| `k8s.pod.cpu.time` | Sum, cumulative, monotonic | `s` | `pods[].cpu.usageCoreNanoSeconds` / 1e9 |
| `k8s.pod.memory.usage` · `k8s.pod.memory.working_set` · `k8s.pod.memory.rss` | Gauge | `By` | `pods[].memory.*` |
| `k8s.pod.network.io` | Sum, cumulative, monotonic | `By` | `pods[].network.interfaces[]` summed; `network.io.direction` = `receive`,`transmit` |
| `k8s.pod.network.errors` | Sum, cumulative, monotonic | `{error}` | rx/tx errors; `network.io.direction` |
| `k8s.pod.filesystem.usage` · `.capacity` · `.available` | Gauge | `By` | `pods[].ephemeral-storage` |
| `k8s.container.cpu.usage` | Gauge | `{cpu}` | `pods[].containers[].cpu.usageNanoCores` / 1e9 |
| `k8s.container.cpu.time` | Sum, cumulative, monotonic | `s` | `containers[].cpu.usageCoreNanoSeconds` / 1e9 |
| `k8s.container.memory.usage` · `k8s.container.memory.working_set` · `k8s.container.memory.rss` | Gauge | `By` | `containers[].memory.*` |
| `k8s.container.filesystem.usage` | Gauge | `By` | `containers[].rootfs.usedBytes` + `logs.usedBytes` |

The OTel `kubeletstats` receiver names the container metrics `container.*`; openlog uses `k8s.container.*` because `container.*` names are
already the cgroup metrics of §2 (both are sent; they differ slightly: cgroup `container.memory.usage` excludes `inactive_file`, kubelet
`working_set` excludes it too but `usage` does not).

### 7.4 Cluster metrics (cluster mode)

Listed every `kubernetes.cluster.interval` (30s) with `resourceVersion=0` (served from the API server watch cache), only by the Lease holder
(`coordination.k8s.io` Lease `kubernetes.cluster.lease_name` in the agent namespace, 15s lease, renew every 5s). Attribute sets:
*pod* = `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, `k8s.node.name` + owner attributes (§7.2, resolved exactly through ReplicaSets
and Jobs); *container* = *pod* + `k8s.container.name`, `container.id` (runtime prefix removed; empty before the container started),
`container.image.name`, `container.image.tags`; *workload* = `k8s.namespace.name`, `openlog.k8s.workload.kind`, `openlog.k8s.workload.name`,
`openlog.k8s.workload.uid` + the kind's name/uid attribute (`k8s.deployment.name`/`k8s.deployment.uid`, …).

| Metric | Type | Unit | Attributes / value |
|---|---|---|---|
| `openlog.k8s.cluster.status` | Gauge | `1` | value 1; `openlog.k8s.cluster.version` (API server `gitVersion`), `openlog.k8s.cluster.nodes`, `openlog.k8s.cluster.namespaces` (counts as strings) |
| `openlog.k8s.node.status` | Gauge | `1` | value 1; `k8s.node.name`, `k8s.node.uid`, `openlog.k8s.node.ready` (`true`,`false`,`unknown`), `openlog.k8s.node.unschedulable` (`true`,`false`), `openlog.k8s.node.roles` (comma-separated `node-role.kubernetes.io/*`), `openlog.k8s.node.kubelet_version`, `openlog.k8s.node.os_image`, `openlog.k8s.node.container_runtime`, `openlog.k8s.node.internal_ip`, `openlog.k8s.node.created_at` (RFC3339) |
| `k8s.node.condition` | Gauge | `1` | `k8s.node.name`, `condition` (`Ready`, `MemoryPressure`, `DiskPressure`, `PIDPressure`, `NetworkUnavailable`); 1 true, 0 false, -1 unknown |
| `k8s.node.allocatable_cpu` · `k8s.node.allocatable_memory` · `k8s.node.allocatable_pods` · `k8s.node.allocatable_ephemeral_storage` | Gauge | `{cpu}` · `By` · `{pod}` · `By` | `k8s.node.name` |
| `openlog.k8s.pod.status` | Gauge | `1` | value 1; *pod* + `openlog.k8s.pod.phase` (`Pending`,`Running`,`Succeeded`,`Failed`,`Unknown`), `openlog.k8s.pod.ready` (`true`,`false`), `openlog.k8s.pod.reason` (first of: pod `status.reason` e.g. `Evicted`, a container waiting reason e.g. `CrashLoopBackOff`/`ImagePullBackOff`, a terminated reason e.g. `OOMKilled`/`Error`; else empty), `openlog.k8s.pod.restarts` (sum over containers), `openlog.k8s.pod.ip`, `openlog.k8s.pod.qos_class`, `openlog.k8s.pod.created_at`, `openlog.k8s.pod.started_at`, `openlog.k8s.pod.containers` (JSON array `[{"name","container_id","image","ready","restarts","state","reason"}]`, init containers excluded, at most 4 KiB) |
| `k8s.pod.phase` | Gauge | `1` | *pod*; 1 Pending, 2 Running, 3 Succeeded, 4 Failed, 5 Unknown |
| `k8s.container.restarts` | Gauge | `{restart}` | *container*; `restartCount` |
| `k8s.container.ready` | Gauge | `1` | *container*; 1 ready, 0 not |
| `k8s.container.cpu_request` · `k8s.container.cpu_limit` | Gauge | `{cpu}` | *container*; only when set |
| `k8s.container.memory_request` · `k8s.container.memory_limit` | Gauge | `By` | *container*; only when set |
| `openlog.k8s.workload.status` | Gauge | `1` | value 1; *workload* + `openlog.k8s.workload.desired`, `.ready`, `.available`, `.updated` (integers as strings; see kinds below), `openlog.k8s.workload.created_at` |
| `openlog.k8s.workload.unavailable` | Gauge | `{pod}` | *workload*; max(desired − available, 0) (Jobs: failed pods; CronJobs: 0) |
| `k8s.deployment.desired` · `k8s.deployment.available` | Gauge | `{pod}` | *workload* (Deployment) |
| `k8s.statefulset.desired_pods` · `.current_pods` · `.ready_pods` · `.updated_pods` | Gauge | `{pod}` | *workload* (StatefulSet) |
| `k8s.daemonset.desired_scheduled_nodes` · `.current_scheduled_nodes` · `.ready_nodes` · `.misscheduled_nodes` | Gauge | `{node}` | *workload* (DaemonSet) |
| `k8s.job.active_pods` · `.failed_pods` · `.successful_pods` · `.desired_successful_pods` · `.max_parallel_pods` | Gauge | `{pod}` | *workload* (Job; desired/max only when set) |
| `k8s.cronjob.active_jobs` | Gauge | `{job}` | *workload* (CronJob) |
| `k8s.hpa.current_replicas` · `.desired_replicas` · `.min_replicas` · `.max_replicas` | Gauge | `{pod}` | `k8s.namespace.name`, `k8s.hpa.name`, `k8s.hpa.uid`, `k8s.hpa.scaletargetref.kind`, `k8s.hpa.scaletargetref.name` (`autoscaling/v2`) |
| `k8s.namespace.phase` | Gauge | `1` | `k8s.namespace.name`; 1 Active, 0 Terminating |
| `k8s.resource_quota.hard_limit` · `k8s.resource_quota.used` | Gauge | `{resource}` | `k8s.namespace.name`, `k8s.resource_quota.name`, `k8s.resource_quota.uid`, `resource` (e.g. `limits.cpu`; quantities in cores / bytes / counts) |

Workload status values: Deployment desired = `spec.replicas`, ready = `status.readyReplicas`, available = `status.availableReplicas`,
updated = `status.updatedReplicas`; StatefulSet desired = `spec.replicas`, ready = `readyReplicas`, available = `availableReplicas`,
updated = `updatedReplicas`; DaemonSet desired = `desiredNumberScheduled`, ready = `numberReady`, available = `numberAvailable`,
updated = `updatedNumberScheduled`; Job desired = `spec.completions` (1 when unset), ready = `status.ready`, available = `succeeded`,
updated = `failed`; CronJob desired = 0, ready = active jobs, available = 0, updated = 0 (`openlog.k8s.workload.suspended` = `true`/`false`).
ReplicaSets owned by a Deployment and Jobs owned by a CronJob are not reported as workloads.

At most `kubernetes.cluster.max_pods` (10000) pods and 5000 workloads per interval are reported; the agent logs a warning when it truncates.

### 7.5 Kubernetes events (cluster mode, OTLP logs)

The leader watches `GET /api/v1/events` (core/v1, all namespaces; relist on `410 Gone`) and sends each new or updated event
(by uid and `count`/`series.count`) once. Events older than 5 minutes at startup are skipped. Log record: `event_name` = `k8s.event`,
`body` = `message`, timestamp = `series.lastObservedTime` → `lastTimestamp` → `eventTime` → `firstTimestamp`, severity `WARN` (13) for
type `Warning`, else `INFO` (9). Resource = the cluster resource of §7.1.

| Log attribute | Source |
|---|---|
| `k8s.event.name`, `k8s.event.uid` | event metadata |
| `k8s.event.reason`, `k8s.event.type` (`Normal`,`Warning`), `k8s.event.action`, `k8s.event.count` (int) | event |
| `k8s.event.source` | `source.component` / `reportingController` |
| `k8s.namespace.name` | `involvedObject.namespace` (else event namespace) |
| `k8s.object.kind`, `k8s.object.name`, `k8s.object.uid`, `k8s.object.fieldpath` | `involvedObject` |
| `k8s.pod.name`, `k8s.pod.uid` | only for `Pod` objects |
| `k8s.node.name` | `Node` objects; for `Pod` objects the `source.host` when set |

### 7.6 Backend entities

Materialized views on `metrics_local` (schema 0040–0044, like `containers_mv`) keep entity rows per tenant: `k8s_clusters` (from
`openlog.k8s.cluster.status`, key `k8s.cluster.uid`), `k8s_nodes` (`openlog.k8s.node.status`), `k8s_workloads`
(`openlog.k8s.workload.status`) and `k8s_pods` (`openlog.k8s.pod.status`), 30 days after the last point, plus bloom filter skip indexes
on `logs_local` for `resource_attributes['k8s.pod.uid']` and `attributes['k8s.object.uid']`. A node links to its host through the host
resource attributes `k8s.cluster.name` + `k8s.node.name`; a pod links to containers (`containers.container_id`) through
`openlog.k8s.pod.containers[].container_id` and to APM services through `apm_service_containers` (API: [api.md](api.md) "Kubernetes").

## 8. Metric data points (storage, all resources)

Every OTLP metric is stored in `metrics` (one row per data point) whatever its resource — infra agent, APM agents,
OpenTelemetry SDKs or collectors; `service_name`, `host_id` and `host_name` are copied from the resource attributes
`service.name`, `host.id` and `host.name` when present (empty otherwise). Per type (processor `AddMetrics`, D-119):

| OTLP type | `metric_type` | Stored |
|---|---|---|
| Gauge | `gauge` | `value` |
| Sum | `sum` | `value`, `temporality` (`delta`/`cumulative`), `is_monotonic` |
| Histogram | `histogram` | `count`, `sum`, `value` = mean, `explicit_bounds`, `bucket_counts` (len(bounds)+1), `temporality` |
| ExponentialHistogram | `exponential_histogram` | `count`, `sum`, `value` = mean, `temporality`; buckets converted to `explicit_bounds`/`bucket_counts`: index i of each sign covers magnitudes (base^i, base^(i+1)] with base = 2^(2^-scale), the zero bucket [-zero_threshold, zero_threshold]; gaps become empty buckets; scales outside -10..20 or more than 1024 buckets store no buckets |
| Summary | `summary` | `count`, `sum`, `value` = mean, `temporality` = `cumulative`, `quantiles` + `quantile_values` (0081) |

Data points with `FLAG_NO_RECORDED_VALUE` are dropped (reason `no_recorded_value`). Rows written before D-119 have
exponential histogram `bucket_counts` of the positive buckets only (no bounds) and summaries without quantiles.
`metrics_1m` rolls up gauges and sums only. The hourly key index `attribute_keys` (0080, D-118) is filled from the
attribute and resource attribute maps of `logs_local`, `spans_local` and `metrics_local` (per metric name).

## 9. Cloud service metrics (cloud connections)

Data points the backend collects from cloud provider APIs for a **cloud connection**
([api.md](api.md#cloud-connections), D-135). They are written to `metrics` exactly like an agent's OTLP data
points — same columns, same `series_id` computation — so the Metrics Explorer, metric alert rules and
dashboards treat a managed database like any other source. No agent is involved and nothing is sent over
OTLP; `internal/cloudconnect` writes the rows directly.

**Metric name:** `cloud.<provider>.<service>.<metric>`, where `<provider>` is `aws`, `azure` or `gcp`,
`<service>` is the connection's service id (`rds`, `s3`, `lambda`, `azure_sql`, `cloud_sql`, …) and
`<metric>` is the provider's own metric name converted to snake_case: separators become `_` and camel and
acronym boundaries are split, so `CPUUtilization` → `cpu_utilization`, `HTTPCode_Target_5XX_Count` →
`http_code_target_5xx_count`, `Percentage CPU` → `percentage_cpu`, `cpu/utilization` → `cpu_utilization`.
The provider's original spelling is kept on the data point as `cloud.metric.name`, so nothing is lost by the
conversion.

Every point is a **gauge** with `temporality` `unspecified`: the provider already aggregated the window, so
what openlog stores is the value at that timestamp and the aggregation travels as an attribute rather than as
an OTLP temporality openlog would have to invent. Gauges are rolled up by `metrics_1m`, so cloud metrics are
kept for 395 days like every other metric ([apm.md](apm.md) §8).

### Resource attributes

| Attribute | Required | Value |
|---|---|---|
| `cloud.provider` | yes | `aws`, `gcp`, `azure` (§1 uses the same values) |
| `cloud.platform` | yes | `aws_rds`, `aws_s3`, `aws_lambda`, `azure_sql`, `gcp_cloud_sql`, … — one per catalog service |
| `cloud.region` | no | `eu-central-1`; for Azure the resource's location, for GCP the resource's `location`/`zone` label |
| `cloud.account.id` | no | AWS account id (read once per poll from STS), Azure subscription id, GCP project id |
| `cloud.resource.id` | no | The provider's unique id: an Azure resource id or a GCP resource name. CloudWatch does not return one, so AWS points carry only the name |
| `cloud.resource.name` | no | The individual resource: a DB instance identifier, a bucket, a function, a queue |
| `openlog.entity.type` | yes | `cloud_resource` |
| `openlog.cloud.connection.id` | yes | The connection the point was collected by |
| `openlog.cloud.connection.name` | yes | Its display name, so a chart can be grouped by account without a join |
| `openlog.cloud.service` | yes | The catalog service id (`rds`, `azure_sql`, `cloud_sql`, …) |

`service_name`, `host_id` and `host_name` are empty: a managed service is not a host and is not an APM
service. Grouping in the UI is by the `cloud.*` resource attributes above.

### Data point attributes

| Attribute | Required | Value |
|---|---|---|
| `cloud.metric.name` | yes | The provider's own metric name (`CPUUtilization`, `cpu_percent`, `cpu/utilization`) |
| `cloud.metric.stat` | no | The aggregation the value represents: `Average`, `Sum`, `Maximum`, `Total` |
| `cloud.aws.dimension.<name>` | no | Every remaining CloudWatch dimension, its name converted like a metric name |
| `cloud.azure.dimension.<name>` | no | Every Azure Monitor metadata value |
| `cloud.gcp.resource.<label>` | no | Every remaining monitored-resource label (`project_id`, `location` and `zone` already became resource attributes) |
| `cloud.gcp.metric.<label>` | no | Every Cloud Monitoring metric label |

Adding a service is a code change in `internal/cloudconnect` with a name under this contract, not
configuration: the catalog is what fixes the metric names an organization can build alerts and dashboards on.
