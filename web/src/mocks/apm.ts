// MSW handlers for the APM endpoints (internal/api/apm.go, docs/contracts/api.md "APM"): a small
// shop (frontend → orders → postgresql, frontend → catalog → redis) with deterministic series.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type {
  ApmAgentStatus,
  ApmAgentUpgrade,
  ApmAgentVersion,
  ApmServiceAgent,
  ApmServiceAgents,
  ApmDbQuery,
  ApmDeployment,
  ApmErrorActivity,
  ApmErrorComment,
  ApmErrorGroup,
  ApmErrorStatus,
  ApmMapEdge,
  ApmMapNode,
  ApmPoint,
  ApmRed,
  ApmService,
  ApmSettings,
  ApmTraceResult,
  ApmTransaction,
} from "@/api/apm";
import { authenticate, MOCK_EMAIL, mockMembers, mockUser } from "./account";
import { formatTs, HOST_IDS, TRACE_ID } from "./fixtures";

const API = "*/api/v1";
const NS = "shop";
const ENV = "prod";

type Code = "invalid_argument" | "not_found" | "permission_denied";
const STATUS: Record<Code, number> = { invalid_argument: 400, not_found: 404, permission_denied: 403 };
const HOURS = 3_600_000;
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

type GroupSeed = Pick<ApmErrorGroup, "group_id" | "error_type" | "message" | "count" | "total_count" | "first_seen" | "last_seen" | "last_trace_id" | "last_span_name" | "sparkline"> & {
  stacktrace: string;
  last_message: string;
};

const GROUPS: Record<string, GroupSeed[]> = {
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
    return { id: svcId(p.name), type: "service", name: p.name, service_namespace: NS, environment: ENV, requests: r.requests, throughput: r.throughput, error_rate: r.error_rate, avg_ms: r.avg_ms, p95_ms: r.p95_ms, apdex: r.apdex, host_count: p.name === "frontend" ? 2 : 1, container_count: p.name === "frontend" ? 3 : 1 };
  };
  const dep = (type: ApmMapNode["type"], name: string, rpm: number, p95: number): ApmMapNode => ({
    id: `${type}:${name}`, type, name, service_namespace: "", environment: "", requests: rpm * mins, throughput: rpm, error_rate: 0, avg_ms: p95 / 3, p95_ms: p95, apdex: null, host_count: 0, container_count: 0,
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

// ---- error workflow (in memory; reset with resetMockApm) ----

type Workflow = Pick<ApmErrorGroup, "status" | "assignee" | "resolved_at" | "resolved_in_version" | "resolved_by_email" | "regressed_at" | "regression_count" | "updated_at" | "updated_by_email">;

const workflow = new Map<string, Workflow>();
const comments = new Map<string, ApmErrorComment[]>();
const activity = new Map<string, ApmErrorActivity[]>();

const defaultWorkflow = (): Workflow => ({ status: "unresolved", assignee: null, resolved_at: null, resolved_in_version: "", resolved_by_email: "", regressed_at: null, regression_count: 0, updated_at: null, updated_by_email: "" });

function seedWorkflow(): void {
  workflow.clear();
  comments.clear();
  activity.clear();
  // UpstreamError (frontend) was resolved in 1.4.1 and regressed after the 1.4.2 deployment.
  const now = Date.now();
  workflow.set("0b7d3e5f9a1c2d44", { ...defaultWorkflow(), regressed_at: formatTs(now - 30 * 60_000), regression_count: 1, updated_at: formatTs(now - 30 * 60_000) });
  activity.set("0b7d3e5f9a1c2d44", [
    { action: "apm.error_group.update", actor_email: MOCK_EMAIL, details: { status: { from: "unresolved", to: "resolved" }, resolved_in_version: "1.4.1" }, created_at: formatTs(now - 5 * HOURS) },
    { action: "apm.error_group.regressed", actor_email: "openlog-apm", details: { version: "1.4.2" }, created_at: formatTs(now - 30 * 60_000) },
  ]);
}
seedWorkflow();

function serviceOfGroup(id: string): string | undefined {
  return Object.keys(GROUPS).find((svc) => GROUPS[svc]!.some((g) => g.group_id === id));
}

function inboxGroups(p: Profile, w: { from: number; to: number; step: number }): ApmErrorGroup[] {
  return groupsFor(p.name, w).map(({ stacktrace: _s, last_message: _m, ...g }) => ({
    ...g,
    service_name: p.name,
    service_namespace: NS,
    environment: ENV,
    ...(workflow.get(g.group_id) ?? defaultWorkflow()),
    comment_count: comments.get(g.group_id)?.length ?? 0,
  }));
}

function inbox(url: URL, services: Profile[], w: { from: number; to: number; step: number }): Response {
  const p = url.searchParams;
  const statusParam = p.get("status") ?? "all";
  const statuses = statusParam === "all" ? null : statusParam.split(",");
  if (statuses && !statuses.every((st) => ["unresolved", "resolved", "ignored"].includes(st))) return fail("invalid_argument", "status must be all or a comma-separated list of unresolved, resolved, ignored");
  let assignee = p.get("assignee") ?? "any";
  if (assignee === "me") assignee = mockUser().id;
  const q = (p.get("q") ?? "").toLowerCase();
  const sort = p.get("sort") ?? "count";
  let all = services.flatMap((s) => inboxGroups(s, w));
  if (assignee === "none") all = all.filter((g) => !g.assignee);
  else if (assignee !== "any") all = all.filter((g) => g.assignee?.user_id === assignee);
  if (q) all = all.filter((g) => [g.error_type, g.message, g.service_name, g.last_span_name].join(" ").toLowerCase().includes(q));
  const counts = { unresolved: 0, resolved: 0, ignored: 0 };
  for (const g of all) counts[g.status]++;
  let groups = statuses ? all.filter((g) => statuses.includes(g.status)) : all;
  const key = sort === "last_seen" ? (g: ApmErrorGroup) => Date.parse(g.last_seen ?? "") : sort === "first_seen" ? (g: ApmErrorGroup) => Date.parse(g.first_seen ?? "") : (g: ApmErrorGroup) => g.count;
  groups = [...groups].sort((a, b) => key(b) - key(a));
  return HttpResponse.json({ groups: groups.slice(0, Number(p.get("limit") ?? 50)), step: `${w.step}s`, counts, truncated: false, workflow: true });
}

/** Trace ids of a transaction (logs mock: GET /logs?transaction=&transaction_service=). */
export function transactionTraceIds(service: string, transaction: string): string[] {
  return (TRANSACTIONS[service] ?? []).some((t) => t.name === transaction) ? [TRACE_ID] : [];
}

type WriteCtx = { role: string; kind: string };

function writer(request: Request): WriteCtx | Response {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (ctx.role === "viewer") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return ctx;
}

function addActivity(id: string, a: Omit<ApmErrorActivity, "created_at" | "actor_email">): void {
  const list = activity.get(id) ?? [];
  list.push({ ...a, actor_email: MOCK_EMAIL, created_at: formatTs(Date.now()) });
  activity.set(id, list);
}

// ---- deployments ----

function deploymentsFor(service: string, now: number): ApmDeployment[] {
  const at = (ms: number) => Math.floor((now - ms) / 60_000) * 60_000;
  const d = (t: number, version: string, previous: string, initial = false, rollback = false): ApmDeployment => ({ timestamp: formatTs(t), t, service_namespace: NS, environment: ENV, version, previous_version: previous, initial, rollback });
  switch (service) {
    case "orders":
      return [d(at(40 * 60_000), "1.4.2", "1.4.1")];
    case "frontend":
      return [d(at(6 * HOURS), "1.4.1", "1.4.0"), d(at(50 * 60_000), "1.4.2", "1.4.1")];
    default:
      return [];
  }
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

  http.get(`${API}/apm/services/:service/errors`, withService((p, url, w) => inbox(url, [p], w))),

  http.get(`${API}/apm/errors`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const env = url.searchParams.get("environment");
    const ns = url.searchParams.get("namespace");
    const svc = url.searchParams.get("service");
    const services = (env !== null && env !== ENV) || (ns !== null && ns !== NS) ? [] : PROFILES.filter((p) => !svc || p.name === svc);
    return inbox(url, services, w);
  })),

  http.patch(`${API}/apm/errors/groups`, async ({ request }) => {
    const ctx = writer(request);
    if (ctx instanceof Response) return ctx;
    const body = (await request.json().catch(() => ({}))) as { group_ids?: unknown; status?: unknown; assignee_user_id?: unknown; resolved_in_version?: unknown };
    const ids = Array.isArray(body.group_ids) ? body.group_ids.map((v) => String(v).toLowerCase()) : [];
    if (ids.length === 0 || ids.length > 500 || !ids.every((id) => /^[0-9a-f]{16}$/.test(id))) return fail("invalid_argument", "group_ids must list 1-500 error groups");
    const status = body.status as ApmErrorStatus | undefined;
    if (status !== undefined && !["unresolved", "resolved", "ignored"].includes(status)) return fail("invalid_argument", "invalid error group change: status must be unresolved, resolved or ignored");
    const version = typeof body.resolved_in_version === "string" ? body.resolved_in_version.trim() : undefined;
    if (version && status !== "resolved") return fail("invalid_argument", "invalid error group change: resolved_in_version needs status resolved");
    const assigneeId = typeof body.assignee_user_id === "string" ? body.assignee_user_id : undefined;
    if (status === undefined && assigneeId === undefined && version === undefined) return fail("invalid_argument", "invalid error group change: nothing to change");
    const member = assigneeId ? mockMembers().find((m) => m.user_id === assigneeId) : undefined;
    if (assigneeId && !member) return fail("invalid_argument", "assignee is not a member of the organization");
    const missing = ids.filter((id) => !serviceOfGroup(id));
    if (missing.length) return fail("not_found", `unknown error groups: ${missing.join(", ")}`);
    const now = formatTs(Date.now());
    const groups = ids.map((id) => {
      const cur = workflow.get(id) ?? defaultWorkflow();
      const next: Workflow = { ...cur, updated_at: now, updated_by_email: MOCK_EMAIL };
      const details: Record<string, unknown> = {};
      if (status !== undefined) {
        if (status !== cur.status) details.status = { from: cur.status, to: status };
        next.status = status;
        next.resolved_at = status === "resolved" ? now : null;
        next.resolved_in_version = status === "resolved" ? (version ?? "") : "";
        next.resolved_by_email = status === "resolved" ? MOCK_EMAIL : "";
        if (version) details.resolved_in_version = version;
      }
      if (assigneeId !== undefined) {
        if ((cur.assignee?.user_id ?? "") !== assigneeId) details.assignee_user_id = { from: cur.assignee?.user_id ?? "", to: assigneeId };
        next.assignee = member ? { user_id: member.user_id, email: member.email, name: member.name } : null;
      }
      workflow.set(id, next);
      if (Object.keys(details).length) addActivity(id, { action: "apm.error_group.update", details });
      return { group_id: id, service_name: serviceOfGroup(id)!, service_namespace: NS, environment: ENV, ...next, comment_count: comments.get(id)?.length ?? 0 };
    });
    return HttpResponse.json({ groups });
  }),

  http.get(`${API}/apm/errors/groups/:groupId/comments`, authed(({ params }) => HttpResponse.json({ comments: comments.get(String(params.groupId).toLowerCase()) ?? [] }))),

  http.post(`${API}/apm/errors/groups/:groupId/comments`, async ({ request, params }) => {
    const ctx = writer(request);
    if (ctx instanceof Response) return ctx;
    const id = String(params.groupId).toLowerCase();
    if (!serviceOfGroup(id)) return fail("not_found", "error group not found");
    const body = (await request.json().catch(() => ({}))) as { body?: unknown };
    const text = typeof body.body === "string" ? body.body.trim() : "";
    if (!text || new TextEncoder().encode(text).length > 4000) return fail("invalid_argument", "invalid error group change: body must be 1-4000 bytes");
    const user = mockUser();
    const c: ApmErrorComment = { id: crypto.randomUUID(), author_user_id: user.id, author_email: user.email, author_name: user.name, body: text, created_at: formatTs(Date.now()) };
    comments.set(id, [...(comments.get(id) ?? []), c]);
    addActivity(id, { action: "apm.error_group.comment", details: { comment_id: c.id } });
    return HttpResponse.json(c, { status: 201 });
  }),

  http.delete(`${API}/apm/errors/groups/:groupId/comments/:commentId`, ({ request, params }) => {
    const ctx = writer(request);
    if (ctx instanceof Response) return ctx;
    const id = String(params.groupId).toLowerCase();
    const list = comments.get(id) ?? [];
    const c = list.find((x) => x.id === params.commentId);
    if (!c || (c.author_user_id !== mockUser().id && ctx.role !== "admin" && ctx.role !== "owner")) return fail("not_found", "comment not found");
    comments.set(id, list.filter((x) => x !== c));
    addActivity(id, { action: "apm.error_group.comment_delete", details: { comment_id: c.id } });
    return new HttpResponse(null, { status: 204 });
  }),

  http.get(`${API}/apm/services/:service/deployments`, withService((p, _url, w) =>
    HttpResponse.json({ deployments: deploymentsFor(p.name, Date.now()).filter((d) => d.t >= w.from && d.t <= w.to), gap_seconds: 1800 }),
  )),

  http.get(`${API}/apm/services/:service/deployments/compare`, withService((p, url) => {
    const atParam = url.searchParams.get("at");
    if (!atParam) return fail("invalid_argument", "at is required");
    const at = /^\d+$/.test(atParam) ? Number(atParam) : Date.parse(atParam);
    if (!Number.isFinite(at) || at >= Date.now()) return fail("invalid_argument", "at must be in the past");
    const win = url.searchParams.get("window") ?? "30m";
    const m = /^(\d+)(m|h)$/.exec(win);
    const secs = m ? Number(m[1]) * (m[2] === "h" ? 3600 : 60) : NaN;
    if (!(secs >= 300 && secs <= 86400)) return fail("invalid_argument", "window must be a duration between 5m0s and 24h0m0s");
    const minute = Math.floor(at / 60_000) * 60_000;
    const mins = secs / 60;
    const worse = p.name === "orders";
    const before = red(p, mins);
    const after = red({ ...p, p95: p.p95 * (worse ? 1.35 : 0.9), p99: p.p99 * (worse ? 1.3 : 0.9), errorRate: p.errorRate * (worse ? 2 : 0.8), apdex: Math.max(0, p.apdex - (worse ? 0.07 : -0.02)) }, mins);
    const iso = (t: number) => formatTs(t);
    const first = GROUPS[p.name]?.[0];
    return HttpResponse.json({
      at: iso(minute),
      window_seconds: secs,
      apdex_t_ms: settingsFor(p.name).apdex_t_ms,
      before: { from: iso(minute - secs * 1000), to: iso(minute), ...before },
      after: { from: iso(minute), to: iso(Math.min(minute + secs * 1000, Date.now())), ...after },
      new_error_groups: worse && first ? [{ group_id: first.group_id, error_type: first.error_type, message: first.message, first_seen: iso(minute + 4 * 60_000), total_count: 57 }] : [],
    });
  })),

  http.get(`${API}/apm/services/:service/errors/:groupId`, withService((p, _url, w, params) => {
    const id = String(params.groupId).toLowerCase();
    if (!/^[0-9a-f]{16}$/.test(id)) return fail("invalid_argument", "group_id must be 16 hex characters");
    const g = groupsFor(p.name, w).find((x) => x.group_id === id);
    if (!g) return fail("not_found", "error group not found");
    const { sparkline, ...rest } = g;
    const now = Date.now();
    const aff = (value: string, count: number, name = "") => ({ value, name, count, first_seen: formatTs(now - 3 * 86_400_000), last_seen: formatTs(now - 20_000) });
    return HttpResponse.json({
      ...rest,
      service_name: p.name,
      service_namespace: NS,
      environment: ENV,
      ...(workflow.get(id) ?? defaultWorkflow()),
      comment_count: comments.get(id)?.length ?? 0,
      first_seen: g.first_seen ?? formatTs(0),
      last_seen: g.last_seen ?? formatTs(0),
      last_span_id: "eee19b7ec3c1b174",
      step: `${w.step}s`,
      series: sparkline,
      samples: Array.from({ length: 6 }, (_, i) => ({ trace_id: i === 0 ? TRACE_ID : `${(i + 7).toString(16).padStart(8, "0")}${"e".repeat(24)}`, span_id: `${i}${"f".repeat(15)}`, timestamp: formatTs(w.to - i * 61_000), span_name: g.last_span_name, transaction_name: g.last_span_name, duration_ms: 3.2 + i, message: g.last_message, version: i < 4 ? "1.4.2" : "1.4.1", host_id: HOST_IDS.web })),
      affected: {
        versions: [aff("1.4.2", Math.round(g.total_count * 0.7)), aff("1.4.1", Math.round(g.total_count * 0.3))],
        hosts: [aff(HOST_IDS.web, g.total_count, "web-1")],
        containers: [aff("c0ffee".padEnd(64, "0"), g.total_count, `${p.name}-1`)],
        transactions: [aff(g.last_span_name, g.total_count)],
      },
      comments: comments.get(id) ?? [],
      activity: [...(activity.get(id) ?? [])].reverse(),
      workflow: true,
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
    const service = url.searchParams.get("service");
    const env = url.searchParams.get("environment");
    const ns = url.searchParams.get("namespace");
    if (!service && ((env !== null && env !== ENV) || (ns !== null && ns !== NS))) return HttpResponse.json({ nodes: [], edges: [] });
    return HttpResponse.json(mapData(w, service ?? undefined));
  })),

  http.get(`${API}/apm/map/path`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const service = url.searchParams.get("service");
    const txn = url.searchParams.get("transaction");
    if (!service || !txn) return fail("invalid_argument", "service and transaction are required");
    if (service === "frontend" && txn.includes("orders")) {
      return HttpResponse.json({ trace_count: 12, nodes: ["db:postgresql/orders", svcId("frontend"), svcId("orders")], edges: [`${svcId("frontend")}->${svcId("orders")}`, `${svcId("orders")}->db:postgresql/orders`] });
    }
    if (service === "catalog" || (service === "frontend" && txn.includes("products"))) {
      return HttpResponse.json({ trace_count: 8, nodes: ["db:redis", svcId("catalog"), svcId("frontend")], edges: [`${svcId("frontend")}->${svcId("catalog")}`, `${svcId("catalog")}->db:redis`] });
    }
    const known = (TRANSACTIONS[service] ?? []).some((t) => t.name === txn);
    return HttpResponse.json({ trace_count: known ? 5 : 0, nodes: known ? [svcId(service)] : [], edges: [] });
  })),

  http.get(`${API}/apm/traces`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const min = url.searchParams.get("min_duration_ms");
    if (min !== null && !(Number(min) >= 0)) return fail("invalid_argument", "min_duration_ms must be a non-negative number");
    return HttpResponse.json({ traces: traces(w, url) });
  })),

  // Language agent versions (D-124): catalog unsupported (PHP), frontend outdated (Node.js), orders ok (Go),
  // legacy-billing a plain OpenTelemetry SDK.
  http.get(`${API}/apm/agents`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const p = url.searchParams;
    const svc = p.get("service");
    const ns = p.get("namespace");
    const env = p.get("environment");
    const services = mockAgentServices()
      .filter((s) => (!svc || s.service_name === svc) && (ns === null || s.service_namespace === ns) && (env === null || s.environment === env))
      .map((s) => (p.get("upgrade") === "false" ? { ...s, agents: s.agents.map((a) => ({ ...a, upgrade: null })) } : s));
    return HttpResponse.json({
      release: { catalog: "ok", channel: "stable", latest: AGENT_LATEST, oldest_supported: "0.1.20", notes_url: `https://github.com/onuragtas/openlog/releases/tag/v${AGENT_LATEST}` },
      services,
    });
  })),
];

const AGENT_LATEST = "0.1.31";

function mockUpgrade(kind: "node" | "go" | "php"): ApmAgentUpgrade {
  const docs = "https://github.com/onuragtas/openlog/blob/master/";
  const base = { version: AGENT_LATEST, lang: "sh", registry: "" as const, registry_url: "", release_asset_url: "", docs_url: "", docs_section: "", notes: [] as ApmAgentUpgrade["notes"] };
  switch (kind) {
    case "node":
      return { ...base, package: "openlog-node", command: `npm install openlog-node@${AGENT_LATEST}`, registry: "available", registry_url: `https://www.npmjs.com/package/openlog-node/v/${AGENT_LATEST}`, docs_url: `${docs}agents/node/README.md` };
    case "go":
      return {
        ...base,
        package: "github.com/onuragtas/openlog/agents/go",
        command: `go get github.com/onuragtas/openlog/agents/go@v${AGENT_LATEST} github.com/onuragtas/openlog/agents/go/instrumentation/gin@v${AGENT_LATEST}\ngo mod tidy`,
        docs_url: `${docs}agents/go/README.md`,
        notes: ["go_modules"],
      };
    case "php": {
      const file = `openlog-php-agent_${AGENT_LATEST}_linux_amd64.deb`;
      const asset = `https://github.com/onuragtas/openlog/releases/download/v${AGENT_LATEST}/${file}`;
      return { ...base, package: "openlog-php-agent", command: `curl -fsSLO ${asset}\nsudo apt-get install ./${file}`, release_asset_url: asset, docs_url: `${docs}docs/contracts/php-agent.md`, notes: ["php_fleet_auto"] };
    }
  }
}

function mockAgentServices(): ApmServiceAgents[] {
  const seen = formatTs(Date.now() - 4_000);
  const version = (v: string, status: ApmAgentStatus, instances: number): ApmAgentVersion => ({ version: v, status, instances, spans: instances * 1200, last_seen: seen });
  const agent = (kind: ApmServiceAgent["kind"], sdk_language: string, status: ApmAgentStatus, versions: ApmAgentVersion[], extra: Partial<ApmServiceAgent> = {}): ApmServiceAgent => ({
    kind,
    distro_name: kind === "php" ? "openlog-php" : kind === "third_party" ? "" : "openlog",
    sdk_name: kind === "php" ? "" : "opentelemetry",
    sdk_language,
    status,
    instances: versions.reduce((n, v) => n + v.instances, 0),
    last_seen: seen,
    versions,
    versions_truncated: false,
    instrumentation_modules: [],
    upgrade: null,
    ...extra,
  });
  return [
    { service_name: "catalog", service_namespace: NS, environment: ENV, status: "unsupported", agents: [agent("php", "php", "unsupported", [version("0.1.9", "unsupported", 2)], { upgrade: mockUpgrade("php") })] },
    {
      service_name: "frontend",
      service_namespace: NS,
      environment: ENV,
      status: "outdated",
      agents: [agent("node", "nodejs", "outdated", [version("0.1.31", "ok", 1), version("0.1.28", "outdated", 3)], { upgrade: mockUpgrade("node") })],
    },
    { service_name: "legacy-billing", service_namespace: "", environment: ENV, status: "third_party", agents: [agent("third_party", "python", "third_party", [version("1.27.0", "third_party", 1)])] },
    { service_name: "orders", service_namespace: NS, environment: ENV, status: "ok", agents: [agent("go", "go", "ok", [version("0.1.31", "ok", 2)], { instrumentation_modules: ["gin"], upgrade: mockUpgrade("go") })] },
  ];
}

/** Test helper: forget stored Apdex settings. */
export function resetMockApm(): void {
  settings.clear();
  seedWorkflow();
}
