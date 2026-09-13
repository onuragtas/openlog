// MSW handlers for the APM endpoints (internal/api/apm.go, docs/contracts/api.md "APM"): a small
// shop (frontend → orders → postgresql, frontend → catalog → redis) with deterministic series.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { ApmDbQuery, ApmErrorGroup, ApmMapEdge, ApmMapNode, ApmPoint, ApmRed, ApmService, ApmSettings, ApmTraceResult, ApmTransaction } from "@/api/apm";
import { authenticate } from "./account";
import { formatTs, HOST_IDS, TRACE_ID } from "./fixtures";

const API = "*/api/v1";
const NS = "shop";
const ENV = "prod";

type Code = "invalid_argument" | "not_found" | "permission_denied";
const STATUS: Record<Code, number> = { invalid_argument: 400, not_found: 404, permission_denied: 403 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

interface Profile {
  name: string;
  language: string;
  rpm: number;
  errorRate: number;
  avg: number;
  p50: number;
  p95: number;
  p99: number;
  apdex: number;
}

const PROFILES: Profile[] = [
  { name: "catalog", language: "php", rpm: 290, errorRate: 0.004, avg: 6.2, p50: 4.1, p95: 18.4, p99: 41.0, apdex: 0.99 },
  { name: "frontend", language: "nodejs", rpm: 480, errorRate: 0.021, avg: 38.4, p50: 22.0, p95: 140.2, p99: 910.0, apdex: 0.91 },
  { name: "orders", language: "go", rpm: 190, errorRate: 0.012, avg: 45.1, p50: 12.3, p95: 1480.0, p99: 1720.0, apdex: 0.82 },
];

const TRANSACTIONS: Record<string, { name: string; share: number; p95: number; errorRate: number }[]> = {
  frontend: [
    { name: "GET /api/products", share: 0.3, p95: 40, errorRate: 0 },
    { name: "GET /api/products/:id", share: 0.25, p95: 35, errorRate: 0.02 },
    { name: "GET /api/orders/:id", share: 0.15, p95: 60, errorRate: 0.06 },
    { name: "POST /api/orders", share: 0.15, p95: 55, errorRate: 0 },
    { name: "GET /api/flaky", share: 0.05, p95: 4, errorRate: 0.2 },
    { name: "GET /api/orders/report", share: 0.03, p95: 1750, errorRate: 0 },
  ],
  orders: [
    { name: "GET /orders/{id}", share: 0.4, p95: 14, errorRate: 0.06 },
    { name: "POST /orders", share: 0.4, p95: 11, errorRate: 0 },
    { name: "GET /orders/report", share: 0.05, p95: 1740, errorRate: 0 },
    { name: "GET /orders/recent", share: 0.15, p95: 9, errorRate: 0 },
  ],
  catalog: [
    { name: "GET /products", share: 0.5, p95: 12, errorRate: 0 },
    { name: "GET /products/{id}", share: 0.5, p95: 22, errorRate: 0.05 },
  ],
};

function window(url: URL): { from: number; to: number; step: number } | Response {
  const now = Date.now();
  const parse = (v: string | null, def: number) => (v === null ? def : /^\d+$/.test(v) ? Number(v) : Date.parse(v));
  const from = parse(url.searchParams.get("from"), now - 3_600_000);
  const to = parse(url.searchParams.get("to"), now);
  if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) return fail("invalid_argument", "from must be before to");
  const stepParam = url.searchParams.get("step");
  let step = Math.max(60, Math.ceil((to - from) / 1000 / 60));
  if (stepParam) {
    const m = /^(\d+)(s|m|h)$/.exec(stepParam);
    const secs = m ? Number(m[1]) * { s: 1, m: 60, h: 3600 }[m[2] as "s" | "m" | "h"] : NaN;
    if (!Number.isFinite(secs)) return fail("invalid_argument", `step: invalid duration "${stepParam}"`);
    if (secs < 60) return fail("invalid_argument", "step must be at least 60s");
    step = secs;
  }
  step = Math.ceil(step / 60) * 60;
  return { from, to, step };
}

const minutes = (w: { from: number; to: number }) => (w.to - Math.floor(w.from / 60_000) * 60_000) / 60_000;

function red(p: { rpm: number; errorRate: number; avg: number; p50: number; p95: number; p99: number; apdex: number }, mins: number, factor = 1): ApmRed {
  const requests = Math.round(p.rpm * mins * factor);
  return {
    requests,
    throughput: requests / mins,
    errors: Math.round(requests * p.errorRate),
    error_rate: p.errorRate,
    avg_ms: requests ? p.avg : null,
    p50_ms: requests ? p.p50 : null,
    p95_ms: requests ? p.p95 : null,
    p99_ms: requests ? p.p99 : null,
    apdex: requests ? p.apdex : null,
  };
}

function series(p: Profile, w: { from: number; to: number; step: number }, factor = 1): ApmPoint[] {
  const out: ApmPoint[] = [];
  const stepMs = w.step * 1000;
  for (let t = Math.ceil(w.from / stepMs) * stepMs; t < w.to; t += stepMs) {
    const wave = 1 + 0.25 * Math.sin(t / 600_000 + p.name.length);
    const spike = Math.sin(t / 900_000) > 0.92 ? 3 : 1;
    const base = red({ ...p, rpm: p.rpm * wave, p95: p.p95 * spike, p99: p.p99 * spike, errorRate: p.errorRate * spike, apdex: Math.max(0.4, p.apdex - (spike - 1) * 0.1) }, w.step / 60, factor);
    out.push({ t, ...base });
  }
  return out;
}

function profile(name: string): Profile | undefined {
  return PROFILES.find((p) => p.name === name);
}

function service(p: Profile, w: { from: number; to: number; step: number }): ApmService {
  const tMs = settingsFor(p.name).apdex_t_ms;
  return {
    service_name: p.name,
    service_namespace: NS,
    environment: ENV,
    language: p.language,
    version: "1.4.2",
    last_seen: formatTs(Date.now() - 4_000),
    apdex_t_ms: tMs,
    ...red(p, minutes(w)),
    sparkline: series(p, w).map((pt): [number, number] => [pt.t, pt.throughput]),
  };
}

function transactions(p: Profile, w: { from: number; to: number; step: number }): ApmTransaction[] {
  const mins = minutes(w);
  const total = p.avg * p.rpm * mins;
  return (TRANSACTIONS[p.name] ?? []).map((tx) => {
    const r = red({ ...p, p95: tx.p95, p99: tx.p95 * 1.4, p50: tx.p95 / 3, avg: tx.p95 / 2, errorRate: tx.errorRate }, mins, tx.share);
    const consumed = total * tx.share * (tx.p95 > 1000 ? 3 : 1);
    return { transaction_type: "web", transaction_name: tx.name, time_consumed_ms: consumed, time_share: 0, max_ms: tx.p95 * 1.6, ...r };
  });
}

// ---- settings (in memory; reset per page load) ----

const settings = new Map<string, ApmSettings>();

function settingsFor(name: string, namespace = "", environment = ""): ApmSettings {
  const key = `${name}|${namespace}|${environment}`;
  return (
    settings.get(key) ??
    settings.get(`${name}||`) ?? { service_name: name, service_namespace: namespace, environment, apdex_t_ms: 500, is_default: true, updated_at: null, updated_by_email: "" }
  );
}

// ---- errors ----

const GO_STACK = [
  "goroutine 81 [running]:",
  "runtime/debug.Stack()",
  "\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e",
  "go.opentelemetry.io/otel/sdk/trace.recordStackTrace()",
  "\t/go/pkg/mod/go.opentelemetry.io/otel/sdk@v1.46.0/trace/span.go:596 +0x1c",
  "main.loadInventory({0x1d4e2a0, 0xc000312f00}, 0x22)",
  "\t/src/main.go:319 +0x1b8",
  "main.(*server).getOrder(0xc0001a2a50, {0x1d4d5e0, 0xc0002c80e0}, 0xc000316500)",
  "\t/src/main.go:292 +0x9d",
  "net/http.HandlerFunc.ServeHTTP(0xc000130510, {0x1d4d5e0, 0xc0002c80e0}, 0xc000316500)",
  "\t/usr/local/go/src/net/http/server.go:2322 +0x29",
].join("\n");

const NODE_STACK = [
  "TypeError: Cannot read properties of undefined (reading 'price')",
  "    at priceOf (/app/server.js:137:20)",
  "    at /app/server.js:143:23",
  "    at Layer.handleRequest (/app/node_modules/router/lib/layer.js:152:17)",
  "    at next (/app/node_modules/router/lib/route.js:157:13)",
].join("\n");

const PHP_STACK = [
  "RuntimeException: Product 13 is discontinued",
  "#0 /app/public/index.php(46): App\\Catalog->find(13)",
  "#1 /app/vendor/slim/slim/Slim/Handlers/Strategies/RequestResponse.php(38): {closure}(Object(Slim\\Psr7\\Request), Object(Slim\\Psr7\\Response), Array)",
  "#2 {main}",
].join("\n");

const GROUPS: Record<string, (ApmErrorGroup & { stacktrace: string; last_message: string })[]> = {
  orders: [
    { group_id: "5a1f0c9e3b2d4e71", error_type: "*errors.errorString", message: "order <n>: inventory shard <n> unavailable", last_message: "order 68: inventory shard 0 unavailable", count: 0, total_count: 412, first_seen: null, last_seen: null, last_trace_id: TRACE_ID, last_span_name: "GET /orders/{id}", sparkline: [], stacktrace: GO_STACK },
  ],
  frontend: [
    { group_id: "9c2e7d1a4f6b3801", error_type: "TypeError", message: "Cannot read properties of undefined (reading '?')", last_message: "Cannot read properties of undefined (reading 'price')", count: 0, total_count: 820, first_seen: null, last_seen: null, last_trace_id: TRACE_ID, last_span_name: "GET /api/flaky", sparkline: [], stacktrace: NODE_STACK },
    { group_id: "0b7d3e5f9a1c2d44", error_type: "UpstreamError", message: "orders responded with HTTP <n>", last_message: "orders responded with HTTP 500", count: 0, total_count: 405, first_seen: null, last_seen: null, last_trace_id: TRACE_ID, last_span_name: "GET /api/orders/:id", sparkline: [], stacktrace: "" },
  ],
  catalog: [
    { group_id: "e4d3c2b1a0f98765", error_type: "RuntimeException", message: "Product <n> is discontinued", last_message: "Product 13 is discontinued", count: 0, total_count: 300, first_seen: null, last_seen: null, last_trace_id: TRACE_ID, last_span_name: "GET /products/{id}", sparkline: [], stacktrace: PHP_STACK },
  ],
};

function groupsFor(name: string, w: { from: number; to: number; step: number }) {
  const now = Date.now();
  return (GROUPS[name] ?? []).map((g, i) => {
    const spark = series(profile(name)!, w).map((p): [number, number] => [p.t, Math.max(0, Math.round(p.errors / (i + 1)))]);
    return { ...g, count: spark.reduce((s, p) => s + p[1], 0), first_seen: formatTs(now - 3 * 86_400_000), last_seen: formatTs(now - 20_000), sparkline: spark };
  });
}

// ---- databases ----

const QUERIES: Record<string, Omit<ApmDbQuery, "throughput" | "time_share" | "error_rate">[]> = {
  orders: [
    { db_system: "postgresql", db_name: "orders", db_operation: "SELECT", statement: "SELECT pg_sleep(? + random() * ?), count(*) FROM orders", calls: 90, errors: 0, avg_ms: 1498, p95_ms: 1760, max_ms: 1799, time_consumed_ms: 134_820 },
    { db_system: "postgresql", db_name: "orders", db_operation: "INSERT", statement: "INSERT INTO orders (customer_id, product_id, quantity, status) VALUES (?, ?, ?, ?) RETURNING id", calls: 1400, errors: 0, avg_ms: 2.1, p95_ms: 4.8, max_ms: 31, time_consumed_ms: 2940 },
    { db_system: "postgresql", db_name: "orders", db_operation: "SELECT", statement: "SELECT id, customer_id, product_id, quantity, status FROM orders WHERE id = ?", calls: 1300, errors: 0, avg_ms: 0.9, p95_ms: 1.9, max_ms: 12, time_consumed_ms: 1170 },
    { db_system: "postgresql", db_name: "orders", db_operation: "SELECT", statement: "SELECT id, status FROM orders WHERE customer_id = ? AND status IN (?) ORDER BY created_at DESC LIMIT ?", calls: 450, errors: 0, avg_ms: 1.4, p95_ms: 2.6, max_ms: 9, time_consumed_ms: 630 },
  ],
  catalog: [
    { db_system: "redis", db_name: "", db_operation: "GET", statement: "GET ?", calls: 5100, errors: 0, avg_ms: 0.3, p95_ms: 0.7, max_ms: 5, time_consumed_ms: 1530 },
    { db_system: "redis", db_name: "", db_operation: "SETEX", statement: "SETEX ?", calls: 310, errors: 0, avg_ms: 0.4, p95_ms: 0.9, max_ms: 4, time_consumed_ms: 124 },
  ],
};

// ---- map ----

const svcId = (name: string) => `service:${name}|${NS}|${ENV}`;

function mapData(w: { from: number; to: number; step: number }, focus?: string): { nodes: ApmMapNode[]; edges: ApmMapEdge[] } {
  const mins = minutes(w);
  const node = (p: Profile): ApmMapNode => {
    const r = red(p, mins);
    return { id: svcId(p.name), type: "service", name: p.name, service_namespace: NS, environment: ENV, requests: r.requests, throughput: r.throughput, error_rate: r.error_rate, avg_ms: r.avg_ms, p95_ms: r.p95_ms, apdex: r.apdex };
  };
  const dep = (type: ApmMapNode["type"], name: string, rpm: number, p95: number): ApmMapNode => ({
    id: `${type}:${name}`, type, name, service_namespace: "", environment: "", requests: rpm * mins, throughput: rpm, error_rate: 0, avg_ms: p95 / 3, p95_ms: p95, apdex: null,
  });
  const edge = (source: string, target: string, type: ApmMapEdge["target_type"], rpm: number, errorRate: number, p95: number): ApmMapEdge => ({
    id: `${source}->${target}`, source, target, target_type: type, calls: rpm * mins, throughput: rpm, errors: rpm * mins * errorRate, error_rate: errorRate, avg_ms: p95 / 3, p95_ms: p95,
  });
  const nodes = [...PROFILES.map(node), dep("db", "postgresql/orders", 260, 3.1), dep("db", "redis", 540, 0.7), dep("external", "api.stripe.com", 12, 320)];
  const edges = [
    edge(svcId("frontend"), svcId("orders"), "service", 190, 0.012, 1500),
    edge(svcId("frontend"), svcId("catalog"), "service", 290, 0.004, 21),
    edge(svcId("orders"), "db:postgresql/orders", "db", 260, 0, 3.1),
    edge(svcId("catalog"), "db:redis", "db", 540, 0, 0.7),
    edge(svcId("frontend"), "external:api.stripe.com", "external", 12, 0.08, 320),
  ];
  if (!focus) return { nodes, edges };
  const kept = edges.filter((e) => e.source === svcId(focus) || e.target === svcId(focus));
  const ids = new Set([svcId(focus), ...kept.flatMap((e) => [e.source, e.target])]);
  return { nodes: nodes.filter((n) => ids.has(n.id)), edges: kept };
}

// ---- traces ----

function traces(w: { from: number; to: number }, url: URL): ApmTraceResult[] {
  const service = url.searchParams.get("service") ?? "frontend";
  const txn = url.searchParams.get("transaction");
  const minMs = Number(url.searchParams.get("min_duration_ms") ?? 0);
  const errorsOnly = url.searchParams.get("error") === "true";
  const names = (TRANSACTIONS[service] ?? []).map((t) => t.name);
  const out: ApmTraceResult[] = [];
  for (let i = 0; i < 40; i++) {
    const name = names[i % names.length] ?? "GET /";
    const dur = name.includes("report") ? 1400 + i * 9 : 3 + ((i * 37) % 180);
    const isError = i % 7 === 3;
    if (txn && name !== txn) continue;
    if (dur < minMs || (errorsOnly && !isError)) continue;
    out.push({
      trace_id: i === 0 ? TRACE_ID : `${i.toString(16).padStart(8, "0")}${"a".repeat(24)}`,
      span_id: `${i.toString(16).padStart(4, "0")}${"b".repeat(12)}`,
      timestamp: formatTs(w.to - i * 47_000),
      duration_ms: dur,
      is_error: isError,
      http_status_code: isError ? 500 : 200,
      service_name: service,
      service_namespace: NS,
      environment: ENV,
      transaction_type: "web",
      transaction_name: name,
    });
  }
  if (url.searchParams.get("sort") === "duration") out.sort((a, b) => b.duration_ms - a.duration_ms);
  return out.slice(0, Number(url.searchParams.get("limit") ?? 50));
}

function withService(fn: (p: Profile, url: URL, w: { from: number; to: number; step: number }, params: Record<string, string>) => Response): HttpResponseResolver {
  return authed(({ request, params }) => {
    const url = new URL(request.url);
    const p = profile(String(params.service));
    const w = window(url);
    if (w instanceof Response) return w;
    if (!p) return fail("not_found", "service not found");
    return fn(p, url, w, params as Record<string, string>);
  });
}

export const apmHandlers = [
  http.get(`${API}/apm/services`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const env = url.searchParams.get("environment");
    const list = env !== null && env !== ENV ? [] : PROFILES.map((p) => service(p, w));
    return HttpResponse.json({ services: list, step: `${w.step}s` });
  })),

  http.get(`${API}/apm/services/:service`, withService((p) => {
    const s = settingsFor(p.name);
    return HttpResponse.json({
      service_name: p.name,
      apdex_t_ms: s.apdex_t_ms,
      apdex_t_default: s.is_default,
      instances: [{ service_namespace: NS, environment: ENV, first_seen: formatTs(Date.now() - 7 * 86_400_000), last_seen: formatTs(Date.now() - 4000), version: "1.4.2", language: p.language, sdk_name: "opentelemetry", resource_attributes: { "service.name": p.name, "host.id": HOST_IDS.web } }],
      hosts: [
        { host_id: HOST_IDS.web, host_name: "web-1", first_seen: formatTs(Date.now() - 7 * 86_400_000), last_seen: formatTs(Date.now() - 4000), known: true },
        { host_id: "d0c4e7000000000000000000000000ff", host_name: "k8s-node-7", first_seen: formatTs(Date.now() - 86_400_000), last_seen: formatTs(Date.now() - 60_000), known: false },
      ],
    });
  })),

  http.get(`${API}/apm/services/:service/overview`, withService((p, url, w) => {
    const pts = series(p, w);
    return HttpResponse.json({ step: `${w.step}s`, apdex_t_ms: settingsFor(p.name).apdex_t_ms, totals: red(p, minutes(w)), series: url.searchParams.get("transaction") ? series(p, w, 0.2) : pts });
  })),

  http.get(`${API}/apm/services/:service/transactions`, withService((p, url, w) => {
    const sort = url.searchParams.get("sort") ?? "time";
    if (!["time", "throughput", "slowest", "errors"].includes(sort)) return fail("invalid_argument", "sort must be time, throughput, slowest or errors");
    const list = transactions(p, w);
    const total = list.reduce((s, t) => s + t.time_consumed_ms, 0);
    for (const t of list) t.time_share = total ? t.time_consumed_ms / total : 0;
    const key = { time: (t: ApmTransaction) => t.time_consumed_ms, throughput: (t: ApmTransaction) => t.requests, slowest: (t: ApmTransaction) => t.p95_ms ?? -1, errors: (t: ApmTransaction) => t.errors }[sort as "time"];
    list.sort((a, b) => key(b) - key(a));
    return HttpResponse.json({ transactions: list.slice(0, Number(url.searchParams.get("limit") ?? 100)), apdex_t_ms: settingsFor(p.name).apdex_t_ms });
  })),

  http.get(`${API}/apm/services/:service/transaction`, withService((p, url, w) => {
    const name = url.searchParams.get("name");
    if (!name) return fail("invalid_argument", "name is required");
    const tx = transactions(p, w).find((t) => t.transaction_name === name);
    const totals = tx ?? red({ ...p, rpm: 0 }, minutes(w));
    const center = Math.round(8 * Math.log2(Math.max(0.5, (tx?.p95_ms ?? 10) / 2.5)));
    const histogram = Array.from({ length: 24 }, (_, i) => {
      const b = center - 10 + i;
      const count = Math.max(0, Math.round(400 * Math.exp(-((i - 10) ** 2) / 18) + (i === 21 ? 25 : 0)));
      return { from_ms: 2 ** ((b - 1) / 8), to_ms: 2 ** (b / 8), count };
    }).filter((b) => b.count > 0);
    return HttpResponse.json({
      transaction_name: name,
      transaction_type: "",
      step: `${w.step}s`,
      apdex_t_ms: settingsFor(p.name).apdex_t_ms,
      totals,
      max_ms: tx?.max_ms ?? 0,
      series: tx ? series(p, w, 0.25) : [],
      histogram: tx ? histogram : [],
      slowest: tx
        ? Array.from({ length: 5 }, (_, i) => ({ trace_id: i === 0 ? TRACE_ID : `${(i + 1).toString(16).padStart(8, "0")}${"c".repeat(24)}`, span_id: `${i}${"d".repeat(15)}`, timestamp: formatTs(w.to - i * 91_000), duration_ms: (tx.max_ms ?? 100) - i * 7, is_error: i === 2, http_status_code: i === 2 ? 500 : 200, service_name: p.name, transaction_name: name }))
        : [],
    });
  })),

  http.get(`${API}/apm/services/:service/errors`, withService((p, _url, w) => HttpResponse.json({ groups: groupsFor(p.name, w).map(({ stacktrace: _s, last_message: _m, ...g }) => g), step: `${w.step}s` }))),

  http.get(`${API}/apm/services/:service/errors/:groupId`, withService((p, _url, w, params) => {
    const id = String(params.groupId).toLowerCase();
    if (!/^[0-9a-f]{16}$/.test(id)) return fail("invalid_argument", "group_id must be 16 hex characters");
    const g = groupsFor(p.name, w).find((x) => x.group_id === id);
    if (!g) return fail("not_found", "error group not found");
    const { sparkline, ...rest } = g;
    return HttpResponse.json({
      ...rest,
      first_seen: g.first_seen ?? formatTs(0),
      last_seen: g.last_seen ?? formatTs(0),
      last_span_id: "eee19b7ec3c1b174",
      step: `${w.step}s`,
      series: sparkline,
      samples: Array.from({ length: 6 }, (_, i) => ({ trace_id: i === 0 ? TRACE_ID : `${(i + 7).toString(16).padStart(8, "0")}${"e".repeat(24)}`, span_id: `${i}${"f".repeat(15)}`, timestamp: formatTs(w.to - i * 61_000), span_name: g.last_span_name, transaction_name: g.last_span_name, duration_ms: 3.2 + i, message: g.last_message })),
    });
  })),

  http.get(`${API}/apm/services/:service/databases`, withService((p, url, w) => {
    const mins = minutes(w);
    const list = (QUERIES[p.name] ?? []).map((q) => ({ ...q, throughput: q.calls / mins, error_rate: q.calls ? q.errors / q.calls : 0, time_share: 0 }));
    const total = list.reduce((s, q) => s + q.time_consumed_ms, 0);
    for (const q of list) q.time_share = total ? q.time_consumed_ms / total : 0;
    const sort = url.searchParams.get("sort") ?? "time";
    const key = { time: (q: ApmDbQuery) => q.time_consumed_ms, calls: (q: ApmDbQuery) => q.calls, slowest: (q: ApmDbQuery) => q.avg_ms ?? 0, errors: (q: ApmDbQuery) => q.errors }[sort as "time"];
    if (!key) return fail("invalid_argument", "sort must be time, calls, slowest or errors");
    list.sort((a, b) => key(b) - key(a));
    return HttpResponse.json({ queries: list });
  })),

  http.get(`${API}/apm/services/:service/hosts`, withService(() => HttpResponse.json({ hosts: [{ host_id: HOST_IDS.web, host_name: "web-1", first_seen: formatTs(Date.now() - 86_400_000), last_seen: formatTs(Date.now()), known: true }] }))),

  http.get(`${API}/apm/services/:service/settings`, authed(({ request, params }) => {
    const url = new URL(request.url);
    return HttpResponse.json(settingsFor(String(params.service), url.searchParams.get("namespace") ?? "", url.searchParams.get("environment") ?? ""));
  })),

  http.put(`${API}/apm/services/:service/settings`, async ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
    if (ctx.role !== "admin" && ctx.role !== "owner") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    const body = (await request.json().catch(() => ({}))) as { apdex_t_ms?: unknown };
    const v = body.apdex_t_ms;
    if (typeof v !== "number" || !Number.isInteger(v) || v < 1 || v > 600000) return fail("invalid_argument", "apdex_t_ms must be an integer between 1 and 600000");
    const url = new URL(request.url);
    const s: ApmSettings = {
      service_name: String(params.service),
      service_namespace: url.searchParams.get("namespace") ?? "",
      environment: url.searchParams.get("environment") ?? "",
      apdex_t_ms: v,
      is_default: false,
      updated_at: formatTs(Date.now()),
      updated_by_email: "admin@openlog.local",
    };
    settings.set(`${s.service_name}||`, s);
    settings.set(`${s.service_name}|${s.service_namespace}|${s.environment}`, s);
    return HttpResponse.json(s);
  }),

  http.get(`${API}/apm/hosts/:hostId/services`, authed(({ params }) => {
    const services = params.hostId === HOST_IDS.web ? PROFILES.map((p) => ({ service_name: p.name, service_namespace: NS, environment: ENV, first_seen: formatTs(Date.now() - 86_400_000), last_seen: formatTs(Date.now() - 5000) })) : [];
    return HttpResponse.json({ services });
  })),

  http.get(`${API}/apm/map`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    return HttpResponse.json(mapData(w, url.searchParams.get("service") ?? undefined));
  })),

  http.get(`${API}/apm/traces`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const min = url.searchParams.get("min_duration_ms");
    if (min !== null && !(Number(min) >= 0)) return fail("invalid_argument", "min_duration_ms must be a non-negative number");
    return HttpResponse.json({ traces: traces(w, url) });
  })),
];

/** Test helper: forget stored Apdex settings. */
export function resetMockApm(): void {
  settings.clear();
}
