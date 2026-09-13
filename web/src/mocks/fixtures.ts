// Realistic fixtures shaped exactly like the openlog-api responses
// (internal/api handlers; values modelled on cmd/openlog-loadgen).
import type { DiscoveredService, Host, InventoryItem, LogRecord, Span } from "@/api/types";

export const VALID_LICENSE_KEYS = ["dev-license-key", "demo"];

/** RFC3339 with 9 fractional digits, UTC (formatTime in internal/api). */
export function formatTs(ms: number, extraNs = 0): string {
  const iso = new Date(Math.floor(ms)).toISOString(); // 2026-09-13T10:00:00.123Z
  const subMs = Math.max(0, Math.min(999_999, Math.round((ms - Math.floor(ms)) * 1e6) + extraNs));
  return iso.replace(/\.(\d{3})Z$/, (_m, ms3: string) => `.${ms3}${String(subMs).padStart(6, "0")}Z`);
}

const HOST_BASE_ATTRS = (name: string, id: string, os: { id: string; version: string; pretty: string }, arch: string, agent: string) => ({
  "host.id": id,
  "host.name": name,
  "host.arch": arch,
  "os.type": "linux",
  "os.name": os.id,
  "os.version": os.version,
  "os.description": os.pretty,
  "openlog.os.kernel_release": "6.8.0-45-generic",
  "openlog.entity.type": "host",
  "openlog.agent.name": "openlog-infra-agent",
  "openlog.agent.version": agent,
});

export const HOST_IDS = {
  web: "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b",
  db: "1a2b3c4d5e6f47a8b9c0d1e2f3a4b5c6",
  worker: "c0ffee00c0ffee00c0ffee00c0ffee00",
} as const;

export function hosts(now: number): Host[] {
  const mk = (
    id: string,
    name: string,
    os: { id: string; version: string; pretty: string },
    arch: string,
    agent: string,
    lastSeenAgo: number,
    extra: Record<string, string>,
  ): Host => ({
    host_id: id,
    host_name: name,
    os_description: os.pretty,
    arch,
    agent_version: agent,
    last_seen: formatTs(now - lastSeenAgo),
    resource_attributes: { ...HOST_BASE_ATTRS(name, id, os, arch, agent), ...extra },
  });
  return [
    mk(HOST_IDS.db, "db-1", { id: "debian", version: "12", pretty: "Debian GNU/Linux 12 (bookworm)" }, "arm64", "0.1.0", 12_000, { env: "prod", team: "data" }),
    mk(HOST_IDS.web, "web-1", { id: "ubuntu", version: "24.04", pretty: "Ubuntu 24.04 LTS" }, "amd64", "0.1.0", 8_000, { env: "prod", team: "payments", region: "eu-central", rack: "r12", tier: "frontend" }),
    mk(HOST_IDS.worker, "worker-1", { id: "ubuntu", version: "22.04", pretty: "Ubuntu 22.04.4 LTS" }, "amd64", "0.1.0-rc.2", 2 * 3_600_000, {}),
  ].sort((a, b) => a.host_name.localeCompare(b.host_name) || a.host_id.localeCompare(b.host_id));
}

// ---- inventory ----

const PKG_NAMES = [
  "openssl", "libssl3t64", "nginx", "redis-server", "curl", "bash", "coreutils", "systemd", "openssh-server", "php8.3-fpm",
  "ca-certificates", "tzdata", "python3", "perl-base", "libc6", "zlib1g", "git", "vim", "less", "sudo",
];

function packages(host: string): InventoryItem[] {
  const out: InventoryItem[] = PKG_NAMES.map((name, i) => ({
    category: "package",
    key: `dpkg:${name}`,
    data: { manager: "dpkg", name, version: name === "openssl" ? (host === "db-1" ? "3.0.11-1~deb12u2" : "3.0.13-0ubuntu3.4") : `1.${i}.${(i * 7) % 10}-1`, arch: "amd64" },
  }));
  // Many library packages to exercise the virtualized table.
  for (let i = 0; i < 480; i++) {
    const name = `lib${["gcc", "x11", "gtk", "xml", "ssh", "curl", "db", "icu"][i % 8]}-${i}`;
    out.push({ category: "package", key: `dpkg:${name}`, data: { manager: "dpkg", name, version: `${(i % 5) + 1}.${i % 13}.0-1`, arch: i % 9 === 0 ? "all" : "amd64" } });
  }
  return out;
}

function services(host: string): InventoryItem[] {
  const svc = (s: DiscoveredService): InventoryItem => ({ category: "discovered_service", key: `${s.rule_id}:${s.instance}`, data: s });
  const list: InventoryItem[] = [
    svc({
      // Real agents report the resolved executable (a redis-check-rdb symlink target) as instance.
      rule_id: "redis", name: "Redis", category: "database", instance: "/usr/bin/redis-check-rdb", command: "redis-server", version: "7.0.15",
      matched_by: ["process", "systemd_unit", "listening_port"], pids: [812],
      ports: [{ protocol: "tcp", address: "127.0.0.1", port: 6379 }], systemd_units: ["redis-server.service"],
      packages: ["dpkg:redis-server"], container_ids: [], integration: { id: "redis", status: "needs_configuration" }, apm_hint: null,
    }),
    svc({
      rule_id: "sshd", name: "OpenSSH", category: "system", instance: "/usr/sbin/sshd", version: "9.6p1",
      matched_by: ["process", "listening_port"], pids: [640], ports: [{ protocol: "tcp", address: "::", port: 22 }],
      integration: { status: "not_available" }, apm_hint: null,
    }),
  ];
  if (host === "web-1") {
    list.push(
      svc({
        rule_id: "nginx", name: "NGINX", category: "web_server", instance: "/usr/sbin/nginx", command: "nginx", version: "1.24.0",
        matched_by: ["process", "listening_port"], pids: [901, 902, 903],
        ports: [{ protocol: "tcp", address: "0.0.0.0", port: 443 }, { protocol: "tcp", address: "0.0.0.0", port: 80 }, { protocol: "tcp", address: "::", port: 80 }],
        integration: { id: "nginx", status: "enabled" }, apm_hint: null,
      }),
      svc({
        rule_id: "php-fpm", name: "PHP-FPM", category: "runtime", instance: "/usr/sbin/php-fpm8.3", version: "8.3.6",
        matched_by: ["process", "systemd_unit"], pids: [1201, 1202], systemd_units: ["php8.3-fpm.service"], packages: ["dpkg:php8.3-fpm"],
        integration: { status: "not_available" }, apm_hint: { language: "php", agent: "openlog-agent-php" },
      }),
    );
  }
  return list;
}

export function inventory(host: Host): InventoryItem[] {
  const name = host.host_name;
  const items: InventoryItem[] = [
    { category: "os", key: "os", data: { id: host.resource_attributes["os.name"], name: host.os_description.split(" ")[0], version_id: host.resource_attributes["os.version"], pretty_name: host.os_description, kernel_release: "6.8.0-45-generic", arch: host.arch, hostname: name, boot_time: "2026-08-30T04:12:00Z" } },
    { category: "hardware", key: "cpu", data: { vendor: "GenuineIntel", model: "Intel(R) Xeon(R) Platinum 8375C", logical_cores: 4, physical_cores: 2, sockets: 1, mhz: 2900 } },
    { category: "hardware", key: "memory", data: { total_bytes: 17179869184, swap_total_bytes: 0 } },
    { category: "hardware", key: "dmi", data: { sys_vendor: "Amazon EC2", product_name: "m6i.xlarge", bios_vendor: "Amazon EC2", bios_version: "1.0" } },
    ...packages(name),
    { category: "systemd_unit", key: "redis-server.service", data: { name: "redis-server.service", type: "service", path: "/lib/systemd/system/redis-server.service", enabled_state: "enabled", description: "Advanced key-value store", exec_start: "/usr/bin/redis-server /etc/redis/redis.conf --requirepass ***" } },
    { category: "systemd_unit", key: "ssh.service", data: { name: "ssh.service", type: "service", enabled_state: "enabled", description: "OpenBSD Secure Shell server", exec_start: "/usr/sbin/sshd -D" } },
    { category: "systemd_unit", key: "apt-daily.timer", data: { name: "apt-daily.timer", type: "timer", enabled_state: "enabled", description: "Daily apt download activities" } },
    { category: "listening_port", key: "tcp:127.0.0.1:6379", data: { protocol: "tcp", family: 4, address: "127.0.0.1", port: 6379, pid: 812, process_name: "redis-server", process_exe: "/usr/bin/redis-server" } },
    { category: "listening_port", key: "tcp:[::]:22", data: { protocol: "tcp", family: 6, address: "::", port: 22, pid: 640, process_name: "sshd", process_exe: "/usr/sbin/sshd" } },
    { category: "process", key: "/usr/bin/redis-server", data: { exe: "/usr/bin/redis-server", name: "redis-server", cmdline: "/usr/bin/redis-server 127.0.0.1:6379", count: 1, pids: [812], uids: [110], start_time: "2026-08-30T04:12:09Z", systemd_unit: "redis-server.service", container_id: "" } },
    { category: "process", key: "/usr/sbin/sshd", data: { exe: "/usr/sbin/sshd", name: "sshd", cmdline: "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", count: 1, pids: [640], uids: [0], start_time: "2026-08-30T04:12:05Z", systemd_unit: "ssh.service" } },
    { category: "kernel_module", key: "overlay", data: { name: "overlay", size_bytes: 151552, state: "Live" } },
    { category: "kernel_module", key: "nf_conntrack", data: { name: "nf_conntrack", size_bytes: 175616, state: "Live" } },
    { category: "user", key: "root", data: { name: "root", uid: 0, gid: 0, home: "/root", shell: "/bin/bash" } },
    { category: "user", key: "redis", data: { name: "redis", uid: 110, gid: 118, home: "/var/lib/redis", shell: "/usr/sbin/nologin" } },
    { category: "network_interface", key: "eth0", data: { name: "eth0", mac: "0a:1b:2c:3d:4e:5f", mtu: 9001, operstate: "up", addresses: ["10.0.1.23/24", "fe80::81b:2cff:fe3d:4e5f/64"] } },
    { category: "mount", key: "/", data: { mountpoint: "/", device: "/dev/nvme0n1p1", fs_type: "ext4", options: "rw,relatime,discard" } },
    ...services(name),
  ];
  if (name === "web-1") {
    items.push({ category: "listening_port", key: "tcp:0.0.0.0:443", data: { protocol: "tcp", family: 4, address: "0.0.0.0", port: 443, pid: 901, process_name: "nginx", process_exe: "/usr/sbin/nginx" } });
    items.push({ category: "listening_port", key: "tcp:[::]:80", data: { protocol: "tcp", family: 6, address: "::", port: 80, pid: 901, process_name: "nginx", process_exe: "/usr/sbin/nginx" } });
  }
  // The API orders by category, item_key.
  return items.sort((a, b) => a.category.localeCompare(b.category) || a.key.localeCompare(b.key));
}

/** worker-1 has not sent a complete snapshot yet. */
export function hasSnapshot(hostId: string): boolean {
  return hostId !== HOST_IDS.worker;
}

// ---- traces ----

export const TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";

export function trace(now: number): Span[] {
  const t0 = now - 90_000;
  const sp = (
    id: string,
    parent: string,
    name: string,
    service: string,
    kind: Span["kind"],
    startMs: number,
    durMs: number,
    extra: Partial<Span> = {},
  ): Span => ({
    span_id: id,
    parent_span_id: parent,
    name,
    kind,
    service_name: service,
    start: formatTs(t0 + startMs, 123),
    duration_ns: Math.round(durMs * 1e6),
    status_code: "unset",
    status_message: "",
    attributes: {},
    resource_attributes: { "service.name": service, "host.id": HOST_IDS.web, "host.name": "web-1" },
    events: [],
    ...extra,
  });
  return [
    sp("a1a1a1a1a1a1a1a1", "", "GET /api/cart", "frontend", "server", 0, 312.4, { status_code: "ok", attributes: { "http.request.method": "GET", "http.route": "/api/cart", "http.response.status_code": "200" } }),
    sp("b2b2b2b2b2b2b2b2", "a1a1a1a1a1a1a1a1", "checkout.getCart", "checkout", "internal", 4.1, 295.2),
    sp("c3c3c3c3c3c3c3c3", "b2b2b2b2b2b2b2b2", "SELECT cart_items", "checkout", "client", 8.3, 41.7, {
      attributes: { "db.system": "postgresql", "db.statement": "SELECT * FROM cart_items WHERE cart_id = $1" },
      events: [{ timestamp: formatTs(t0 + 8.3), name: "query.start", attributes: { "db.system": "postgresql" } }],
    }),
    sp("d4d4d4d4d4d4d4d4", "b2b2b2b2b2b2b2b2", "GET redis cart:*", "checkout", "client", 52.0, 2.3, { attributes: { "db.system": "redis" } }),
    sp("e5e5e5e5e5e5e5e5", "b2b2b2b2b2b2b2b2", "POST /pricing", "checkout", "client", 56.5, 210.8, { attributes: { "http.request.method": "POST", "server.address": "pricing" } }),
    sp("f6f6f6f6f6f6f6f6", "e5e5e5e5e5e5e5e5", "POST /pricing", "pricing", "server", 58.0, 206.1, { status_code: "error", status_message: "upstream returned 503" }),
    sp("0707070707070707", "f6f6f6f6f6f6f6f6", "rules.evaluate", "pricing", "internal", 60.2, 120.5),
    sp("0808080808080808", "f6f6f6f6f6f6f6f6", "GET currency-api /rates", "pricing", "client", 182.0, 80.4, { status_code: "error", status_message: "503 Service Unavailable" }),
    sp("0909090909090909", "b2b2b2b2b2b2b2b2", "publish cart.viewed", "checkout", "producer", 270.1, 3.2, { attributes: { "messaging.system": "kafka", "messaging.destination.name": "cart.viewed" } }),
    sp("1010101010101010", "0909090909090909", "consume cart.viewed", "recommendations", "consumer", 281.0, 25.9),
    sp("1111111111111111", "a1a1a1a1a1a1a1a1", "render cart", "frontend", "internal", 300.0, 11.9),
  ];
}

/** A large trace (default 12k spans, depth ≤ 3) to exercise the virtualized waterfall. */
export const BIG_TRACE_ID = "b16b16b16b16b16b16b16b16b16b16b1";

export function bigTrace(now: number, count = 12_000): Span[] {
  const t0 = now - 120_000;
  const totalMs = 5_000;
  const services = ["frontend", "checkout", "pricing", "inventory", "search", "recommendations"];
  const id = (n: number) => n.toString(16).padStart(16, "0");
  const groups = 40;
  const perGroup = Math.max(1, Math.ceil((count - 1 - groups) / groups));
  const out: Span[] = [];
  const mk = (n: number, parent: string, name: string, service: string, startMs: number, durMs: number, extra: Partial<Span> = {}): Span => ({
    span_id: id(n),
    parent_span_id: parent,
    name,
    kind: parent ? "internal" : "server",
    service_name: service,
    start: formatTs(t0 + startMs),
    duration_ns: Math.round(durMs * 1e6),
    status_code: "unset",
    status_message: "",
    attributes: {},
    resource_attributes: { "service.name": service },
    events: [],
    ...extra,
  });
  out.push(mk(1, "", "POST /api/batch-import", "frontend", 0, totalMs));
  let n = 2;
  for (let g = 0; g < groups && n <= count; g++) {
    const gStart = (g / groups) * totalMs;
    const gDur = totalMs / groups;
    const groupId = n;
    out.push(mk(n++, id(1), `import.chunk ${g}`, services[g % services.length]!, gStart, gDur * 0.95));
    for (let k = 0; k < perGroup && n <= count; k++) {
      const s = gStart + (k / perGroup) * gDur * 0.9;
      out.push(
        mk(n++, id(groupId), k % 3 === 0 ? "INSERT items" : "cache.set", services[(g + k) % services.length]!, s, 0.2 + ((k * 13) % 17) / 10, k % 997 === 0 ? { status_code: "error", status_message: "deadlock detected" } : {}),
      );
    }
  }
  return out;
}

// ---- logs ----

const LOG_TEMPLATES: { sev: number; text: string; body: string; service: string }[] = [
  { sev: 9, text: "INFO", body: "GET /api/cart 200 12ms", service: "checkout" },
  { sev: 9, text: "INFO", body: "user session refreshed", service: "checkout" },
  { sev: 5, text: "DEBUG", body: "cache hit for key product:1234", service: "checkout" },
  { sev: 13, text: "WARN", body: "slow query detected: 1.2s SELECT * FROM orders", service: "checkout" },
  { sev: 17, text: "ERROR", body: "payment gateway timeout after 30s", service: "checkout" },
  { sev: 9, text: "INFO", body: '10.0.1.7 - - "GET /healthz HTTP/1.1" 200 2', service: "nginx" },
  { sev: 17, text: "ERROR", body: "pricing: upstream returned 503 (currency-api)", service: "pricing" },
];

export function logs(now: number, count = 400): LogRecord[] {
  const hs = hosts(now);
  const out: LogRecord[] = [];
  for (let i = 0; i < count; i++) {
    const tpl = LOG_TEMPLATES[(i * 7 + (i % 3)) % LOG_TEMPLATES.length]!;
    const host = hs[i % 2]!; // db-1, web-1
    const ts = now - 5_000 - i * 45_000; // spans ~5h
    const traced = tpl.sev >= 13;
    out.push({
      timestamp: formatTs(ts, (i * 997) % 1000),
      severity_text: tpl.text,
      severity_number: tpl.sev,
      body: tpl.body,
      host_id: host.host_id,
      service_name: tpl.service,
      trace_id: traced ? (tpl.service === "pricing" || i % 5 === 0 ? TRACE_ID : (i.toString(16).padStart(8, "0") + "e".repeat(24))) : "",
      span_id: traced ? "f6f6f6f6f6f6f6f6" : "",
      attributes:
        tpl.service === "nginx"
          ? { "openlog.log.source": "file", "log.file.path": "/var/log/nginx/access.log", "log.file.name": "access.log", "openlog.discovery.id": "nginx" }
          : { "openlog.log.source": "file", "log.file.path": `/var/log/app/${tpl.service}.log` },
      resource_attributes: { ...host.resource_attributes, "service.name": tpl.service },
    });
  }
  return out;
}

// ---- metrics ----

export interface MetricDef {
  type: "gauge" | "sum";
  unit: string;
  monotonic?: boolean;
  series: { attributes: Record<string, string>; value: (tSec: number, rate: boolean) => number }[];
}

const wave = (t: number, period: number, phase = 0) => (Math.sin((t / period) * Math.PI * 2 + phase) + 1) / 2;
const noise = (t: number, seed: number) => {
  const x = Math.sin(t * 12.9898 + seed * 78.233) * 43758.5453;
  return x - Math.floor(x);
};

export const METRICS: Record<string, MetricDef> = {
  "system.cpu.utilization": {
    type: "gauge",
    unit: "1",
    series: (() => {
      const busy = (t: number) => 0.1 + 0.45 * wave(t, 3600) + 0.05 * noise(Math.floor(t / 60), 1);
      const shares: Record<string, number> = { user: 0.6, system: 0.28, iowait: 0.05, softirq: 0.04, nice: 0.01, interrupt: 0.01, steal: 0.01 };
      const modes = Object.keys(shares).map((mode) => ({ attributes: { "cpu.mode": mode }, value: (t: number) => busy(t) * shares[mode]! }));
      return [...modes, { attributes: { "cpu.mode": "idle" }, value: (t: number) => 1 - busy(t) }];
    })(),
  },
  "system.cpu.load_average.1m": { type: "gauge", unit: "{thread}", series: [{ attributes: {}, value: (t) => 0.4 + 2.2 * wave(t, 3600) + 0.4 * noise(Math.floor(t / 60), 2) }] },
  "system.cpu.load_average.5m": { type: "gauge", unit: "{thread}", series: [{ attributes: {}, value: (t) => 0.5 + 1.9 * wave(t, 3600, -0.3) }] },
  "system.cpu.load_average.15m": { type: "gauge", unit: "{thread}", series: [{ attributes: {}, value: (t) => 0.6 + 1.6 * wave(t, 3600, -0.6) }] },
  "system.memory.usage": {
    type: "sum",
    unit: "By",
    series: (() => {
      const total = 16 * 2 ** 30;
      const used = (t: number) => total * (0.35 + 0.25 * wave(t, 7200));
      return [
        { attributes: { "system.memory.state": "used" }, value: used },
        { attributes: { "system.memory.state": "cached" }, value: () => total * 0.2 },
        { attributes: { "system.memory.state": "buffers" }, value: () => total * 0.02 },
        { attributes: { "system.memory.state": "free" }, value: (t: number) => total - used(t) - total * 0.22 },
      ];
    })(),
  },
  "system.filesystem.utilization": {
    type: "gauge",
    unit: "1",
    series: [
      { attributes: { "system.device": "/dev/nvme0n1p1", "system.filesystem.mountpoint": "/", "system.filesystem.type": "ext4" }, value: (t) => 0.42 + 0.02 * wave(t, 86400) },
      { attributes: { "system.device": "/dev/nvme1n1", "system.filesystem.mountpoint": "/var/lib/redis", "system.filesystem.type": "xfs" }, value: (t) => 0.71 + 0.05 * wave(t, 20000) },
    ],
  },
  "system.disk.io": {
    type: "sum",
    unit: "By",
    monotonic: true,
    series: ["nvme0n1", "nvme1n1"].flatMap((dev, d) =>
      ["read", "write"].map((dir, k) => ({
        attributes: { "system.device": dev, "disk.io.direction": dir },
        value: (t: number) => (k === 0 ? 4e5 : 1.2e6) * (0.5 + wave(t, 1800, d + k)) * (0.8 + 0.4 * noise(Math.floor(t / 30), d * 2 + k)),
      })),
    ),
  },
  "system.network.io": {
    type: "sum",
    unit: "By",
    monotonic: true,
    series: ["receive", "transmit"].map((dir, k) => ({
      attributes: { "network.interface.name": "eth0", "network.io.direction": dir },
      value: (t: number) => (k === 0 ? 2.5e5 : 9e4) * (0.6 + wave(t, 2400, k)) * (0.8 + 0.4 * noise(Math.floor(t / 30), 7 + k)),
    })),
  },
};
