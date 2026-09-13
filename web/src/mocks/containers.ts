// MSW handlers for the container endpoints (internal/api/containers.go, docs/contracts/api.md "Containers"):
// the shop compose project on web-1, a finished migration job and a standalone container on db-1.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { ApmServiceContainer, ComposeProject, Container, ContainerDetail, ContainerService } from "@/api/containers";
import type { LogRecord } from "@/api/types";
import { authenticate } from "./account";
import { formatTs, HOST_IDS } from "./fixtures";

const API = "*/api/v1";

type Code = "invalid_argument" | "not_found";
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: code === "not_found" ? 404 : 400 });

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

export const CONTAINER_IDS = {
  frontend: "0c".repeat(32),
  orders: "0a".repeat(32),
  catalog: "0b".repeat(32),
  redis: "0d".repeat(32),
  migrate: "0e".repeat(32),
  backup: "0f".repeat(32),
} as const;

interface Spec {
  id: string;
  name: string;
  image: string;
  tag: string;
  host: string;
  hostName: string;
  project: string;
  service: string;
  state: string;
  health: string;
  cpu: number;
  memory: number;
  limit: number;
  restarts: number;
  apmService?: string;
}

const GiB = 1024 ** 3;
const SPECS: Spec[] = [
  { id: CONTAINER_IDS.frontend, name: "shop-frontend-1", image: "openlog-apmdemo/frontend", tag: "1", host: HOST_IDS.web, hostName: "web-1", project: "shop", service: "frontend", state: "running", health: "healthy", cpu: 0.031, memory: 96 * 1024 ** 2, limit: 8 * GiB, restarts: 0, apmService: "frontend" },
  { id: CONTAINER_IDS.orders, name: "shop-orders-1", image: "openlog-apmdemo/orders", tag: "1", host: HOST_IDS.web, hostName: "web-1", project: "shop", service: "orders", state: "running", health: "healthy", cpu: 0.012, memory: 18 * 1024 ** 2, limit: 512 * 1024 ** 2, restarts: 2, apmService: "orders" },
  { id: CONTAINER_IDS.catalog, name: "shop-catalog-1", image: "openlog-apmdemo/catalog", tag: "1", host: HOST_IDS.web, hostName: "web-1", project: "shop", service: "catalog", state: "running", health: "unhealthy", cpu: 0.024, memory: 64 * 1024 ** 2, limit: 8 * GiB, restarts: 0, apmService: "catalog" },
  { id: CONTAINER_IDS.redis, name: "shop-redis-1", image: "redis", tag: "7-alpine", host: HOST_IDS.web, hostName: "web-1", project: "shop", service: "redis", state: "running", health: "", cpu: 0.004, memory: 9 * 1024 ** 2, limit: 8 * GiB, restarts: 0 },
  { id: CONTAINER_IDS.migrate, name: "shop-migrate-1", image: "openlog-apmdemo/orders", tag: "1", host: HOST_IDS.web, hostName: "web-1", project: "shop", service: "migrate", state: "exited", health: "", cpu: 0, memory: 0, limit: 8 * GiB, restarts: 0 },
  { id: CONTAINER_IDS.backup, name: "pg-backup", image: "postgres", tag: "16-alpine", host: HOST_IDS.db, hostName: "db-1", project: "", service: "", state: "running", health: "", cpu: 0.002, memory: 22 * 1024 ** 2, limit: 16 * GiB, restarts: 0 },
];

function window(url: URL): { from: number; to: number } | Response {
  const now = Date.now();
  const f = url.searchParams.get("from");
  const t = url.searchParams.get("to");
  const from = f ? Number(f) : now - 3_600_000;
  const to = t ? Number(t) : now;
  if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) return fail("invalid_argument", "from must be before to");
  return { from, to };
}

function wave(spec: Spec, from: number, to: number, n: number, base: number): [number, number][] {
  if (spec.state !== "running") return [];
  const step = (to - from) / n;
  const seed = parseInt(spec.id.slice(0, 2), 16);
  return Array.from({ length: n }, (_, i) => [Math.round(from + i * step), base * (1 + 0.25 * Math.sin((i + seed) / 3))] as [number, number]);
}

function container(spec: Spec, from: number, to: number): Container {
  const now = Date.now();
  const running = spec.state === "running";
  const cpu = wave(spec, from, to, 30, spec.cpu);
  const mem = wave(spec, from, to, 30, spec.memory);
  return {
    container_id: spec.id,
    name: spec.name,
    image_name: spec.image,
    image_tags: [spec.tag],
    runtime: "docker",
    host_id: spec.host,
    host_name: spec.hostName,
    compose_project: spec.project,
    compose_service: spec.service,
    k8s_pod_name: "",
    k8s_namespace_name: "",
    k8s_container_name: "",
    state: spec.state,
    health: spec.health,
    started_at: formatTs(now - 5 * 3_600_000),
    restart_count: spec.restarts,
    first_seen: formatTs(now - 3 * 86_400_000),
    last_seen: formatTs(running ? now - 5_000 : now - 40 * 60_000),
    reporting: running,
    cpu_utilization: cpu.length ? cpu[cpu.length - 1]![1] : null,
    memory_usage: mem.length ? mem[mem.length - 1]![1] : null,
    memory_limit: running ? spec.limit : null,
    cpu_sparkline: cpu,
    memory_sparkline: mem,
  };
}

function list(url: URL, from: number, to: number): Container[] {
  const p = url.searchParams;
  const q = (p.get("q") ?? "").toLowerCase().split(/\s+/).filter(Boolean);
  return SPECS.map((s) => container(s, from, to)).filter((c) => {
    if (p.get("host_id") && c.host_id !== p.get("host_id")) return false;
    if (p.has("compose_project") && c.compose_project !== p.get("compose_project")) return false;
    if (p.has("compose_service") && c.compose_service !== p.get("compose_service")) return false;
    if (p.get("state") && (c.state || "unknown") !== p.get("state")) return false;
    const text = [c.container_id, c.name, c.image_name, ...c.image_tags, c.host_name, c.compose_project, c.compose_service].join(" ").toLowerCase();
    return q.every((term) => text.includes(term));
  });
}

function groups(cs: Container[]): ComposeProject[] {
  const out: ComposeProject[] = [];
  for (const c of [...cs].sort((a, b) => a.compose_project.localeCompare(b.compose_project) || a.compose_service.localeCompare(b.compose_service))) {
    let p = out.find((x) => x.compose_project === c.compose_project);
    if (!p) out.push((p = { compose_project: c.compose_project, host_ids: [], containers: 0, running: 0, services: [] }));
    let s = p.services.find((x) => x.compose_service === c.compose_service);
    if (!s) p.services.push((s = { compose_service: c.compose_service, containers: 0, running: 0, cpu_utilization: null, memory_usage: null }));
    if (!p.host_ids.includes(c.host_id)) p.host_ids.push(c.host_id);
    p.containers++;
    s.containers++;
    if (c.reporting && c.state === "running") {
      p.running++;
      s.running++;
      s.cpu_utilization = (s.cpu_utilization ?? 0) + (c.cpu_utilization ?? 0);
      s.memory_usage = (s.memory_usage ?? 0) + (c.memory_usage ?? 0);
    }
  }
  return out;
}

/** Container log records merged into GET /logs (resource attributes of semantic-conventions §4.1). */
export function containerLogs(now: number): LogRecord[] {
  const out: LogRecord[] = [];
  const lines: [string, string, string, number][] = [
    ["stdout", "GET /orders/42 200 3.1ms", "INFO", 9],
    ["stderr", "level=error msg=\"db timeout\" order=17", "ERROR", 17],
    ["stdout", "POST /orders 201 11.4ms", "INFO", 9],
  ];
  for (const spec of SPECS.filter((s) => s.state === "running")) {
    lines.forEach(([stream, body, sev, num], i) => {
      out.push({
        timestamp: formatTs(now - (i + 1) * 45_000 - parseInt(spec.id.slice(0, 2), 16) * 1000),
        severity_text: sev,
        severity_number: num,
        body: `${spec.service || spec.name}: ${body}`,
        host_id: spec.host,
        service_name: "",
        trace_id: "",
        span_id: "",
        attributes: { "openlog.log.source": "container", "log.iostream": stream },
        resource_attributes: {
          "host.id": spec.host,
          "host.name": spec.hostName,
          "container.id": spec.id,
          "container.name": spec.name,
          "container.image.name": spec.image,
          ...(spec.project ? { "docker.compose.project": spec.project, "docker.compose.service": spec.service } : {}),
        },
      });
    });
  }
  return out;
}

const byId = (id: string) => SPECS.find((s) => s.id === id);

function idParam(raw: unknown): string | Response {
  const id = String(raw).toLowerCase();
  return /^[0-9a-f]{64}$/.test(id) ? id : fail("invalid_argument", "container_id must be 64 hex characters");
}

export const containerHandlers = [
  http.get(`${API}/containers`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const state = url.searchParams.get("state");
    if (state && !["running", "paused", "restarting", "exited", "created", "dead", "removing", "unknown"].includes(state)) return fail("invalid_argument", "state must be one of …");
    const all = list(url, w.from, w.to);
    const limit = Number(url.searchParams.get("limit") ?? 100);
    return HttpResponse.json({ containers: all.slice(0, limit), total: all.length, step: "120s" });
  })),

  http.get(`${API}/containers/groups`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    return HttpResponse.json({ projects: groups(list(url, w.from, w.to)) });
  })),

  http.get(`${API}/containers/:id`, authed(({ request, params }) => {
    const id = idParam(params.id);
    if (id instanceof Response) return id;
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const spec = byId(id);
    if (!spec) return fail("not_found", "container not found");
    const detail: ContainerDetail = {
      ...container(spec, w.from, w.to),
      attributes: {
        "container.id": spec.id,
        "container.name": spec.name,
        "container.image.name": spec.image,
        "container.runtime": "docker",
        "openlog.container.state": spec.state,
        ...(spec.project ? { "docker.compose.project": spec.project, "docker.compose.service": spec.service } : {}),
      },
    };
    return HttpResponse.json(detail);
  })),

  http.get(`${API}/containers/:id/timeseries`, authed(({ request, params }) => {
    const id = idParam(params.id);
    if (id instanceof Response) return id;
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const spec = byId(id);
    if (!spec) return fail("not_found", "container not found");
    const n = 120;
    return HttpResponse.json({
      container_id: id,
      host_id: spec.host,
      step: "30s",
      from: w.from,
      to: w.to,
      series: {
        cpu_utilization: wave(spec, w.from, w.to, n, spec.cpu),
        memory_usage: wave(spec, w.from, w.to, n, spec.memory),
        memory_limit: wave(spec, w.from, w.to, n, spec.limit).map(([t]) => [t, spec.limit]),
        network_receive: wave(spec, w.from, w.to, n, 4200),
        network_transmit: wave(spec, w.from, w.to, n, 2100),
        blockio_read: wave(spec, w.from, w.to, n, 512),
        blockio_write: wave(spec, w.from, w.to, n, 8192),
      },
    });
  })),

  http.get(`${API}/containers/:id/services`, authed(({ params }) => {
    const id = idParam(params.id);
    if (id instanceof Response) return id;
    const spec = byId(id);
    const services: ContainerService[] = spec?.apmService
      ? [{
          service_name: spec.apmService, service_namespace: "shop", environment: "prod", first_seen: formatTs(Date.now() - 86_400_000),
          last_seen: formatTs(Date.now() - 2_000), apdex_t_ms: 500, requests: 11_400, throughput: 190, errors: 137, error_rate: 0.012,
          avg_ms: 45.1, p50_ms: 12.3, p95_ms: 1480, p99_ms: 1720, apdex: 0.82,
        }]
      : [];
    return HttpResponse.json({ services });
  })),

  http.get(`${API}/apm/services/:service/containers`, authed(({ request, params }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const containers: ApmServiceContainer[] = SPECS.filter((s) => s.apmService === params.service).map((s) => {
      const c = container(s, w.from, w.to);
      return {
        container_id: s.id, name: s.name, host_id: s.host, host_name: s.hostName, first_seen: c.first_seen, last_seen: c.last_seen,
        known: true, state: s.state, reporting: c.reporting, cpu_utilization: c.cpu_utilization, memory_usage: c.memory_usage, memory_limit: c.memory_limit,
      };
    });
    if (params.service === "orders") {
      containers.push({ container_id: "1f".repeat(32), name: "orders-canary", host_id: "", host_name: "", first_seen: formatTs(Date.now() - 600_000), last_seen: formatTs(Date.now() - 60_000),
        known: false, state: "", reporting: false, cpu_utilization: null, memory_usage: null, memory_limit: null });
    }
    return HttpResponse.json({ containers });
  })),
];
