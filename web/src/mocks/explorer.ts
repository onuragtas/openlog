// MSW handlers for the explorer API (api.md "Fields", D-118, D-119): field keys and values, structured log queries and
// volume, the metrics explorer and in-memory saved views. Logs are the host, container and Kubernetes fixtures plus
// application logs with JSON bodies and many attributes; metrics are the host metrics per host plus application
// gauges, sums (cumulative and delta), a histogram and a summary.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { FieldKey, FieldType, LogQueryRow, MetricAggregation, MetricDetail, MetricInfo, QueryFilter, SavedView, SavedViewInput, SpanQueryRow } from "@/api/explorer";
import type { LogRecord, MetricSeries } from "@/api/types";
import { parseGoDuration } from "@/lib/logs-explorer";
import { authenticate } from "./account";
import { containerLogs } from "./containers";
import * as fx from "./fixtures";
import { kubernetesLogs } from "./kubernetes";

const API = "*/api/v1";
type Code = "invalid_argument" | "permission_denied" | "not_found";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

type Ctx = Exclude<ReturnType<typeof authenticate>, Response>;
type Info = Parameters<HttpResponseResolver>[0];

function authed(fn: (ctx: Ctx, info: Info) => Response | Promise<Response>): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    return ctx instanceof Response ? ctx : fn(ctx, info);
  };
}

const parseTime = (v: unknown): number | null => {
  if (typeof v === "number") return Number.isFinite(v) ? v : null;
  if (typeof v !== "string" || v === "") return null;
  if (/^\d+$/.test(v)) return Number(v);
  const t = Date.parse(v);
  return Number.isNaN(t) ? null : t;
};

function timeWindow(fromRaw: unknown, toRaw: unknown): { from: number; to: number } | Response {
  const now = Date.now();
  const from = fromRaw === undefined || fromRaw === null || fromRaw === "" ? now - 3_600_000 : parseTime(fromRaw);
  const to = toRaw === undefined || toRaw === null || toRaw === "" ? now : parseTime(toRaw);
  if (from === null || to === null) return fail("invalid_argument", "from/to: invalid time (use RFC3339 or unix milliseconds)");
  if (from >= to) return fail("invalid_argument", "from must be before to");
  return { from, to };
}

// ---- filters ----------------------------------------------------------------------------------------------------------

const OPS = new Set(["=", "!=", "in", "not_in", "contains", "not_contains", "like", "not_like", "regex", "not_regex", "exists", "not_exists", ">", ">=", "<", "<="]);

function validateFilter(f: unknown): string | null {
  if (!f || typeof f !== "object") return "filter must be an object";
  const { key, op, value, values } = f as QueryFilter;
  if (typeof key !== "string" || key === "" || key.length > 256) return "filter key must be 1-256 characters";
  if (!OPS.has(op)) return `unsupported operator "${String(op)}"`;
  if (op === "in" || op === "not_in") {
    if (!Array.isArray(values) || values.length === 0 || values.length > 100) return `${key}: ${op} takes 1-100 values`;
  } else if (op !== "exists" && op !== "not_exists") {
    if (value === undefined || value === null) return `${key}: ${op} takes a value`;
    if (String(value).length > 1024) return `${key}: value longer than 1024 bytes`;
    if (op === "regex" || op === "not_regex") {
      try {
        new RegExp(String(value));
      } catch {
        return `${key}: invalid regular expression`;
      }
    }
  }
  return null;
}

interface FilterBody {
  filters?: QueryFilter[];
  groups?: QueryFilter[][];
  q?: string;
  transaction?: string;
  transaction_service?: string;
}

function validateFilterBody(b: FilterBody): string | null {
  const groups = b.groups ?? [];
  if (!Array.isArray(b.filters ?? []) || !Array.isArray(groups)) return "filters and groups must be arrays";
  if (groups.length > 10) return "at most 10 groups";
  const all = [...(b.filters ?? []), ...groups.flat()];
  if (all.length > 50) return "at most 50 conditions";
  for (const f of all) {
    const err = validateFilter(f);
    if (err) return err;
  }
  if (b.q !== undefined && (typeof b.q !== "string" || b.q.length > 1024)) return "q must be at most 1024 characters";
  return null;
}

/** Trace ids of an APM transaction context (entry spans of the mock traces with that name and service), or null. */
function transactionTraceIds(b: { transaction?: string; transaction_service?: string }): Set<string> | null | Response {
  if (!b.transaction && !b.transaction_service) return null;
  if (!b.transaction || !b.transaction_service) return fail("invalid_argument", "transaction and transaction_service must be given together");
  const ids = spanRecords(Date.now())
    .filter((r) => r.row.is_entry && r.row.transaction_name === b.transaction && r.row.service_name === b.transaction_service)
    .map((r) => r.row.trace_id);
  // APM mock transactions have no spans here: fall back to the mock trace so the page shows correlated logs.
  return new Set(ids.length ? ids : [fx.TRACE_ID]);
}

const likeRegex = (pattern: string) => new RegExp(`^${pattern.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/%/g, ".*").replace(/_/g, ".")}$`, "s");

function matchValue(v: string | undefined, f: QueryFilter): boolean {
  const present = v !== undefined && v !== "";
  if (f.op === "exists") return present;
  if (f.op === "not_exists") return !present;
  if (v === undefined) return f.op === "!=" || f.op === "not_in" || f.op === "not_contains" || f.op === "not_like" || f.op === "not_regex";
  const want = String(f.value ?? "");
  switch (f.op) {
    case "=":
      return v === want;
    case "!=":
      return v !== want;
    case "in":
      return (f.values ?? []).map(String).includes(v);
    case "not_in":
      return !(f.values ?? []).map(String).includes(v);
    case "contains":
      return v.toLowerCase().includes(want.toLowerCase());
    case "not_contains":
      return !v.toLowerCase().includes(want.toLowerCase());
    case "like":
      return likeRegex(want).test(v);
    case "not_like":
      return !likeRegex(want).test(v);
    case "regex":
      return new RegExp(want).test(v);
    case "not_regex":
      return !new RegExp(want).test(v);
    default: {
      const a = Number(v);
      const b = Number(f.value);
      if (v.trim() === "" || !Number.isFinite(a) || !Number.isFinite(b)) return false;
      return f.op === ">" ? a > b : f.op === ">=" ? a >= b : f.op === "<" ? a < b : a <= b;
    }
  }
}

function matchesAll(get: (key: string) => string | undefined, b: FilterBody, skipKey?: string): boolean {
  const ok = (f: QueryFilter) => f.key === skipKey || matchValue(get(f.key), f);
  if (!(b.filters ?? []).every(ok)) return false;
  const groups = (b.groups ?? []).filter((g) => g.length > 0);
  return groups.length === 0 || groups.some((g) => g.every(ok));
}

const typeOf = (values: string[]): FieldType =>
  values.length > 0 && values.every((v) => v === "true" || v === "false") ? "bool" : values.length > 0 && values.every((v) => v.trim() !== "" && Number.isFinite(Number(v))) ? "number" : "string";

// ---- logs -------------------------------------------------------------------------------------------------------------

interface Rec {
  ts: number;
  log: LogRecord;
  hostName: string;
  json: Record<string, unknown> | null;
}

const ROUTES = ["/api/cart", "/api/checkout", "/api/orders/{id}", "/api/login", "/health"];
const SERVICES = ["checkout", "payments", "auth"];

/** Application logs with JSON bodies, HTTP attributes and Kubernetes resources. */
function appLogs(now: number): LogRecord[] {
  const out: LogRecord[] = [];
  for (let i = 0; i < 260; i++) {
    const service = SERVICES[i % SERVICES.length]!;
    const route = ROUTES[(i * 3) % ROUTES.length]!;
    const status = i % 17 === 0 ? 503 : i % 11 === 0 ? 404 : i % 7 === 0 ? 500 : 200;
    const level = status >= 500 ? "error" : status >= 400 ? "warn" : i % 9 === 0 ? "debug" : "info";
    const sev = { error: [17, "ERROR"], warn: [13, "WARN"], info: [9, "INFO"], debug: [5, "DEBUG"] }[level] as [number, string];
    const duration = Math.round(20 + ((i * 37) % 900));
    const body = {
      level,
      msg: status >= 500 ? "upstream request failed: connection timeout" : "request completed",
      http: { method: i % 4 === 0 ? "POST" : "GET", route, status },
      duration_ms: duration,
      user_id: `u-${(i * 13) % 40}`,
    };
    const traced = i % 3 === 0;
    out.push({
      timestamp: fx.formatTs(now - 20_000 - i * 55_000, (i * 331) % 1000),
      severity_text: sev[1],
      severity_number: sev[0],
      body: JSON.stringify(body),
      host_id: "",
      service_name: service,
      trace_id: traced ? (i % 6 === 0 ? fx.TRACE_ID : `${i.toString(16).padStart(8, "0")}${"a".repeat(24)}`) : "",
      span_id: traced ? `${(i + 1).toString(16).padStart(4, "0")}${"b".repeat(12)}` : "",
      attributes: {
        "http.request.method": body.http.method,
        "http.route": route,
        "http.response.status_code": String(status),
        "url.path": route.replace("{id}", String(1000 + i)),
        "client.address": `10.0.${i % 4}.${(i * 7) % 250}`,
        "user_agent.original": i % 5 === 0 ? "curl/8.5.0" : "Mozilla/5.0",
        "code.function": `${service}.handler`,
        "thread.name": `worker-${i % 8}`,
        ...(status >= 500 ? { "error.type": "TimeoutError", "exception.message": "context deadline exceeded" } : {}),
        "enduser.id": body.user_id,
      },
      resource_attributes: {
        "service.name": service,
        "service.version": `1.${i % 3}.0`,
        "deployment.environment": i % 10 === 0 ? "staging" : "production",
        "k8s.namespace.name": "shop",
        "k8s.pod.name": `${service}-7f9c${i % 3}`,
        "k8s.node.name": `node-${i % 2}`,
        "telemetry.sdk.language": service === "auth" ? "go" : "nodejs",
      },
    });
  }
  return out;
}

/** Log records with trace context of the mock trace's spans (as GET /logs in mocks/handlers.ts), for log ↔ trace links. */
function traceLogs(now: number): LogRecord[] {
  return fx
    .trace(now)
    .slice(0, 4)
    .map((sp, i) => ({
      timestamp: sp.start,
      severity_text: i === 3 ? "ERROR" : "INFO",
      severity_number: i === 3 ? 17 : 9,
      body: `checkout flow: ${sp.name}`,
      host_id: "",
      service_name: sp.service_name,
      trace_id: fx.TRACE_ID,
      span_id: sp.span_id,
      attributes: {},
      resource_attributes: { "service.name": sp.service_name },
    }));
}

function logRecords(now: number): Rec[] {
  const hostNames = new Map(fx.hosts(now).map((h) => [h.host_id, h.host_name]));
  return [...fx.logs(now), ...containerLogs(now), ...kubernetesLogs(now), ...traceLogs(now), ...appLogs(now)].map((log) => {
    let json: Record<string, unknown> | null = null;
    if (log.body.startsWith("{")) {
      try {
        json = JSON.parse(log.body) as Record<string, unknown>;
      } catch {
        json = null;
      }
    }
    return { ts: Date.parse(log.timestamp), log, hostName: hostNames.get(log.host_id) ?? log.resource_attributes["host.name"] ?? "", json };
  });
}

// ---- log patterns (D-128) ------------------------------------------------------------------------------------------
// A small stand-in for internal/logpattern: tokens that carry a value become "<*>", so the mock app groups messages
// the way the backend does. The id only has to be stable, not equal to the backend's hash.

const PUNCT_OPEN = "([{<";
const PUNCT_CLOSE = ")]}>,;:.!?";

const variableToken = (s: string): boolean =>
  s.length > 0 &&
  (/\d/.test(s) || /^"[^]*"$|^'[^]*'$/.test(s) || (s.includes("@") && /[A-Za-z]/.test(s)) || (s.length >= 8 && /^[a-fA-F]+$/.test(s)));

function maskToken(tok: string): string {
  const kv = /^([A-Za-z_.-]+[=:])([^]+)$/.exec(tok);
  if (kv && variableToken(kv[2]!)) return `${kv[1]}<*>`;
  let s = 0;
  let e = tok.length;
  while (s < e && PUNCT_OPEN.includes(tok[s]!)) s++;
  while (e > s && PUNCT_CLOSE.includes(tok[e - 1]!)) e--;
  const core = tok.slice(s, e);
  return core && variableToken(core) ? `${tok.slice(0, s)}<*>${tok.slice(e)}` : tok;
}

const maskBody = (body: string): string => body.split(/\s+/).filter(Boolean).map(maskToken).join(" ");

/** Stable id of a body's template ("0" when there is nothing to group). */
function patternId(body: string): string {
  const template = maskBody(body);
  if (template === "") return "0";
  let h = 2166136261;
  for (let i = 0; i < template.length; i++) h = Math.imul(h ^ template.charCodeAt(i), 16777619) >>> 0;
  return String(h);
}

const LOG_FIELDS: Record<string, { type: FieldType; get: (r: Rec) => string }> = {
  timestamp: { type: "string", get: (r) => r.log.timestamp },
  body: { type: "string", get: (r) => r.log.body },
  severity_text: { type: "string", get: (r) => r.log.severity_text },
  severity_number: { type: "number", get: (r) => String(r.log.severity_number) },
  "service.name": { type: "string", get: (r) => r.log.service_name },
  "host.id": { type: "string", get: (r) => r.log.host_id },
  "host.name": { type: "string", get: (r) => r.hostName },
  trace_id: { type: "string", get: (r) => r.log.trace_id },
  span_id: { type: "string", get: (r) => r.log.span_id },
  trace_flags: { type: "number", get: (r) => (r.log.trace_id ? "1" : "0") },
  "event.name": { type: "string", get: () => "" },
  "scope.name": { type: "string", get: () => "" },
  pattern_id: { type: "string", get: (r) => patternId(r.log.body) },
  pattern_template: { type: "string", get: (r) => maskBody(r.log.body) },
  observed_timestamp: { type: "string", get: (r) => r.log.timestamp },
};

const jsonText = (v: unknown) => (v === undefined ? undefined : typeof v === "string" ? v : JSON.stringify(v));

function logValue(r: Rec, key: string): string | undefined {
  const field = LOG_FIELDS[key];
  if (field) return field.get(r);
  if (key.startsWith("attributes.")) return r.log.attributes[key.slice(11)];
  if (key.startsWith("resource.")) return r.log.resource_attributes[key.slice(9)];
  if (key.startsWith("body.")) return jsonText(r.json?.[key.slice(5)]);
  return r.log.attributes[key] ?? r.log.resource_attributes[key];
}

function logRow(r: Rec, columns: string[], includeRecord: boolean): LogQueryRow {
  const fields: Record<string, string> = {};
  for (const c of columns) {
    const v = logValue(r, c);
    if (v !== undefined) fields[c] = v;
  }
  return {
    id: `${r.ts}-${r.log.body.length}-${r.log.service_name}-${r.log.span_id}`,
    timestamp: r.log.timestamp,
    observed_timestamp: r.log.timestamp,
    severity_text: r.log.severity_text,
    severity_number: r.log.severity_number,
    body: r.log.body,
    service_name: r.log.service_name,
    host_id: r.log.host_id,
    host_name: r.hostName,
    trace_id: r.log.trace_id,
    span_id: r.log.span_id,
    fields,
    ...(includeRecord ? { attributes: r.log.attributes, resource_attributes: r.log.resource_attributes } : {}),
  };
}

/** Attribute, resource and JSON body keys of log records with counts and approximate cardinality. */
function logKeys(recs: Rec[]): FieldKey[] {
  const acc = new Map<string, { name: string; source: FieldKey["source"]; values: Set<string>; count: number }>();
  const add = (key: string, name: string, source: FieldKey["source"], v: string) => {
    const e = acc.get(key) ?? { name, source, values: new Set<string>(), count: 0 };
    e.count++;
    e.values.add(v);
    acc.set(key, e);
  };
  for (const r of recs) {
    for (const [k, v] of Object.entries(r.log.attributes)) add(`attributes.${k}`, k, "attribute", v);
    for (const [k, v] of Object.entries(r.log.resource_attributes)) add(`resource.${k}`, k, "resource", v);
    for (const [k, v] of Object.entries(r.json ?? {})) add(`body.${k}`, k, "body", jsonText(v)!);
  }
  return [...acc.entries()]
    .map(([key, e]) => ({ key, name: e.name, source: e.source, type: typeOf([...e.values]), count: e.count, cardinality: e.values.size }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
}

// ---- spans (Traces Explorer) --------------------------------------------------------------------------------------------

interface SpanRec {
  ts: number;
  row: SpanQueryRow;
}

/** 36 copies of the mock trace over the last hour with varying durations; the newest is fx.TRACE_ID. */
function spanRecords(now: number): SpanRec[] {
  const out: SpanRec[] = [];
  for (let k = 0; k < 36; k++) {
    const traceId = k === 0 ? fx.TRACE_ID : `${(0x5000 + k).toString(16).padStart(8, "0")}${"c".repeat(24)}`;
    const factor = 0.4 + ((k * 7) % 12) / 6;
    for (const sp of fx.trace(now - k * 95_000)) {
      const ts = Date.parse(sp.start);
      const durationNs = Math.round(sp.duration_ns * factor);
      const entry = sp.parent_span_id === "" || sp.kind === "server" || sp.kind === "consumer";
      const status = sp.attributes["http.response.status_code"];
      out.push({
        ts,
        row: {
          id: `${ts}-${traceId}-${sp.span_id}`,
          timestamp: sp.start,
          trace_id: traceId,
          span_id: sp.span_id,
          parent_span_id: sp.parent_span_id,
          name: sp.name,
          kind: sp.kind,
          status_code: sp.status_code,
          status_message: sp.status_message,
          service_name: sp.service_name,
          host_id: sp.resource_attributes["host.id"] ?? "",
          duration_ns: durationNs,
          duration_ms: durationNs / 1e6,
          is_entry: entry,
          is_error: sp.status_code === "error",
          http_status_code: status ? Number(status) : sp.status_code === "error" && entry ? 503 : 0,
          transaction_name: entry ? sp.name : "",
          fields: {},
          attributes: sp.attributes,
          resource_attributes: sp.resource_attributes,
        },
      });
    }
  }
  return out;
}

const SPAN_FIELDS: Record<string, { type: FieldType; get: (r: SpanQueryRow) => string }> = {
  timestamp: { type: "string", get: (r) => r.timestamp },
  name: { type: "string", get: (r) => r.name },
  kind: { type: "string", get: (r) => r.kind },
  status_code: { type: "string", get: (r) => r.status_code },
  status_message: { type: "string", get: (r) => r.status_message },
  "service.name": { type: "string", get: (r) => r.service_name },
  "host.id": { type: "string", get: (r) => r.host_id },
  trace_id: { type: "string", get: (r) => r.trace_id },
  span_id: { type: "string", get: (r) => r.span_id },
  parent_span_id: { type: "string", get: (r) => r.parent_span_id },
  duration_ns: { type: "number", get: (r) => String(r.duration_ns) },
  duration_ms: { type: "number", get: (r) => String(r.duration_ms) },
  "http.status_code": { type: "number", get: (r) => String(r.http_status_code) },
  is_entry: { type: "bool", get: (r) => String(r.is_entry) },
  error: { type: "bool", get: (r) => String(r.is_error) },
  "transaction.name": { type: "string", get: (r) => r.transaction_name },
};

function spanValue(r: SpanQueryRow, key: string): string | undefined {
  const field = SPAN_FIELDS[key];
  if (field) return field.get(r);
  if (key.startsWith("attributes.")) return r.attributes?.[key.slice(11)];
  if (key.startsWith("resource.")) return r.resource_attributes?.[key.slice(9)];
  return r.attributes?.[key] ?? r.resource_attributes?.[key];
}

function spanKeys(recs: SpanRec[]): FieldKey[] {
  const acc = new Map<string, { name: string; source: FieldKey["source"]; values: Set<string>; count: number }>();
  const add = (key: string, name: string, source: FieldKey["source"], v: string) => {
    const e = acc.get(key) ?? { name, source, values: new Set<string>(), count: 0 };
    e.count++;
    e.values.add(v);
    acc.set(key, e);
  };
  for (const { row } of recs) {
    for (const [k, v] of Object.entries(row.attributes ?? {})) add(`attributes.${k}`, k, "attribute", v);
    for (const [k, v] of Object.entries(row.resource_attributes ?? {})) add(`resource.${k}`, k, "resource", v);
  }
  return [...acc.entries()]
    .map(([key, e]) => ({ key, name: e.name, source: e.source, type: typeOf([...e.values]), count: e.count, cardinality: e.values.size }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
}

function quantile(sorted: number[], q: number): number {
  const pos = (sorted.length - 1) * q;
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return sorted[lo]! + (sorted[hi]! - sorted[lo]!) * (pos - lo);
}

// ---- metrics ------------------------------------------------------------------------------------------------------------

type MetricKind = MetricInfo["type"];

interface XSeries {
  attributes: Record<string, string>;
  resource: Record<string, string>;
  /** raw value at t (s); `rate` for monotonic sums */
  value: (t: number, rate: boolean) => number;
}

interface XMetric {
  name: string;
  type: MetricKind;
  unit: string;
  description: string;
  temporality: MetricInfo["temporality"];
  monotonic: boolean;
  series: XSeries[];
}

const wave = (t: number, period: number, phase = 0) => (Math.sin((t / period) * Math.PI * 2 + phase) + 1) / 2;

const DESCRIPTIONS: Record<string, string> = {
  "system.cpu.utilization": "Fraction of CPU time spent in each mode.",
  "system.memory.usage": "Bytes of memory in use.",
  "system.network.io": "Bytes transmitted and received.",
};

function explorerMetrics(): XMetric[] {
  const hostList = [
    { id: fx.HOST_IDS.web, name: "web-1" },
    { id: fx.HOST_IDS.db, name: "db-1" },
  ];
  const hostMetrics = Object.entries(fx.METRICS).map(([name, def]): XMetric => ({
    name,
    type: def.type,
    unit: def.unit,
    description: DESCRIPTIONS[name] ?? "",
    temporality: def.type === "sum" ? "cumulative" : "unspecified",
    monotonic: !!def.monotonic,
    series: hostList.flatMap((h, hi) =>
      def.series.map((s) => ({
        attributes: s.attributes,
        resource: { "host.id": h.id, "host.name": h.name, ...(s.resource ?? {}) },
        value: (t: number, rate: boolean) => s.value(t + hi * 450, rate),
      })),
    ),
  }));
  const svc = (service: string, extra: Record<string, string> = {}) => ({ "service.name": service, "service.version": "1.4.2", "deployment.environment": "production", "k8s.namespace.name": "shop", ...extra });
  const routes = ["/api/cart", "/api/checkout", "/api/orders/{id}"];
  const app: XMetric[] = [
    {
      name: "http.server.request.duration",
      type: "histogram",
      unit: "s",
      description: "Duration of HTTP server requests.",
      temporality: "cumulative",
      monotonic: false,
      series: ["checkout", "payments"].flatMap((service, si) =>
        routes.flatMap((route, ri) =>
          ["200", "500"].map((code) => ({
            attributes: { "http.request.method": ri === 1 ? "POST" : "GET", "http.route": route, "http.response.status_code": code },
            resource: svc(service, { "k8s.pod.name": `${service}-7f9c0` }),
            value: (t: number) => (0.04 + 0.08 * ri + 0.03 * si) * (code === "500" ? 3 : 1) * (0.7 + 0.6 * wave(t, 1800, ri)),
          })),
        ),
      ),
    },
    {
      name: "app.orders.created",
      type: "sum",
      unit: "{order}",
      description: "Orders created (delta temporality).",
      temporality: "delta",
      monotonic: true,
      series: ["card", "paypal", "invoice"].map((method, i) => ({
        attributes: { "payment.method": method },
        resource: svc("checkout"),
        value: (t: number, rate: boolean) => (rate ? 1 : 60) * (0.5 + (3 - i) * 0.4 * wave(t, 3600, i)),
      })),
    },
    {
      name: "jvm.memory.used",
      type: "gauge",
      unit: "By",
      description: "Measure of memory used.",
      temporality: "unspecified",
      monotonic: false,
      series: ["heap", "non_heap"].flatMap((type) =>
        ["G1 Eden Space", "G1 Old Gen", "Metaspace"].map((pool, i) => ({
          attributes: { "jvm.memory.type": type, "jvm.memory.pool.name": pool },
          resource: svc("payments", { "telemetry.sdk.language": "java" }),
          value: (t: number) => (type === "heap" ? 180e6 : 60e6) * (1 + i * 0.5) * (0.6 + 0.4 * wave(t, 900, i)),
        })),
      ),
    },
    {
      name: "rpc.client.duration",
      type: "summary",
      unit: "ms",
      description: "Duration of outbound RPC calls.",
      temporality: "unspecified",
      monotonic: false,
      series: ["GetPrice", "Reserve"].map((method, i) => ({
        attributes: { "rpc.method": method, "rpc.service": "inventory.v1.Inventory" },
        resource: svc("checkout"),
        value: (t: number) => (12 + 20 * i) * (0.8 + 0.4 * wave(t, 1200, i)),
      })),
    },
    {
      name: "messaging.queue.depth",
      type: "sum",
      unit: "{message}",
      description: "Messages waiting in the queue.",
      temporality: "cumulative",
      monotonic: false,
      series: ["orders", "emails"].map((queue, i) => ({ attributes: { "messaging.destination.name": queue }, resource: svc("workers"), value: (t: number) => Math.round(40 + 300 * wave(t, 2400, i)) })),
    },
    {
      name: "custom/app/requests_in_flight",
      type: "gauge",
      unit: "{request}",
      description: "In-flight requests (a name with slashes).",
      temporality: "unspecified",
      monotonic: false,
      series: [{ attributes: {}, resource: svc("auth"), value: (t: number) => Math.round(3 + 25 * wave(t, 600)) }],
    },
  ];
  return [...hostMetrics, ...app];
}

const PERCENTILES: Partial<Record<MetricAggregation, number>> = { p50: 1, p75: 1.3, p90: 1.7, p95: 2, p99: 3 };

function aggregationsOf(m: XMetric): { list: MetricAggregation[]; def: MetricAggregation } {
  if (m.type === "histogram" || m.type === "exponential_histogram") return { list: ["p50", "p75", "p90", "p95", "p99", "avg", "count", "sum", "rate"], def: "p95" };
  if (m.type === "summary") return { list: ["p50", "p75", "p90", "p95", "p99", "avg", "count", "sum"], def: "p95" };
  if (m.type === "sum" && m.monotonic) return { list: ["rate", "increase", "sum", "last"], def: "rate" };
  return { list: ["avg", "min", "max", "sum", "last", "count"], def: m.type === "sum" ? "last" : "avg" };
}

function metricValue(m: XMetric, s: XSeries, key: string): string | undefined {
  if (key === "metric.name") return m.name;
  if (key === "metric.type") return m.type;
  if (key === "unit") return m.unit;
  if (key === "service.name" || key === "host.id" || key === "host.name") return s.resource[key];
  if (key === "scope.name") return "";
  if (key.startsWith("attributes.")) return s.attributes[key.slice(11)];
  if (key.startsWith("resource.")) return s.resource[key.slice(9)];
  return s.attributes[key] ?? s.resource[key];
}

const METRIC_FIELDS: [string, FieldType][] = [
  ["metric.name", "string"],
  ["metric.type", "string"],
  ["unit", "string"],
  ["service.name", "string"],
  ["host.id", "string"],
  ["host.name", "string"],
  ["scope.name", "string"],
  ["value", "number"],
];

function metricKeys(metrics: XMetric[], source: "attribute" | "resource"): FieldKey[] {
  const acc = new Map<string, Set<string>>();
  const counts = new Map<string, number>();
  for (const m of metrics)
    for (const s of m.series)
      for (const [k, v] of Object.entries(source === "attribute" ? s.attributes : s.resource)) {
        const key = `${source === "attribute" ? "attributes" : "resource"}.${k}`;
        acc.set(key, (acc.get(key) ?? new Set()).add(v));
        counts.set(key, (counts.get(key) ?? 0) + 1);
      }
  return [...acc.entries()]
    .map(([key, values]) => ({ key, name: key.slice(key.indexOf(".") + 1), source, type: typeOf([...values]), count: counts.get(key)!, cardinality: values.size }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
}

function info(m: XMetric, now: number): MetricInfo {
  const services = [...new Set(m.series.map((s) => s.resource["service.name"]).filter((v): v is string => !!v))].slice(0, 5);
  return { name: m.name, type: m.type, unit: m.unit, description: m.description, temporality: m.temporality, monotonic: m.monotonic, last_seen: fx.formatTs(now - 5_000), series: m.series.length, services };
}

const MERGE_SUM = new Set<MetricAggregation>(["sum", "count", "rate", "increase", "last"]);

// ---- saved views --------------------------------------------------------------------------------------------------------

const ME = "7c1e2d9a-3b4f-4e5a-8b6c-000000000001";
const OTHER = "7c1e2d9a-3b4f-4e5a-8b6c-000000000002";
type StoredView = Omit<SavedView, "can_edit">;
const seedTime = fx.formatTs(Date.UTC(2026, 8, 1, 9, 0, 0));
const savedViews: StoredView[] = [
  {
    id: "sv000000-0000-4000-8000-000000000001",
    signal: "logs",
    name: "Checkout errors",
    description: "Server errors of the checkout service",
    visibility: "org",
    state: { filters: [{ key: "service.name", op: "=", value: "checkout" }, { key: "severity_number", op: ">=", value: 17 }], groups: [], q: "", columns: ["timestamp", "severity_text", "service.name", "attributes.http.route", "body"], order: "desc", group_by: "attributes.http.route" },
    created_by_user_id: OTHER,
    created_by_email: "grace@example.com",
    created_at: seedTime,
    updated_at: seedTime,
  },
  {
    id: "sv000000-0000-4000-8000-000000000002",
    signal: "logs",
    name: "Slow requests",
    description: "",
    visibility: "private",
    state: { filters: [{ key: "body.duration_ms", op: ">", value: 500 }], groups: [], q: "", columns: ["timestamp", "service.name", "body.duration_ms", "body"], order: "desc", group_by: "service.name", range: "6h" },
    created_by_user_id: ME,
    created_by_email: "admin@example.com",
    created_at: seedTime,
    updated_at: seedTime,
  },
  {
    id: "sv000000-0000-4000-8000-000000000003",
    signal: "metrics",
    name: "p95 latency by route",
    description: "",
    visibility: "org",
    state: { queries: [{ i: "A", m: "http.server.request.duration", a: "p95", g: ["attributes.http.route"] }], formula: "" },
    created_by_user_id: ME,
    created_by_email: "admin@example.com",
    created_at: seedTime,
    updated_at: seedTime,
  },
];
let viewSeq = savedViews.length;

const canEdit = (v: StoredView, ctx: Ctx) => ctx.kind === "session" && ctx.role !== "viewer" && (v.created_by_user_id === ME || (v.visibility === "org" && (ctx.role === "admin" || ctx.role === "owner")));
const visible = (v: StoredView, ctx: Ctx) => v.visibility === "org" || (ctx.kind === "session" && v.created_by_user_id === ME);
const withEdit = (v: StoredView, ctx: Ctx): SavedView => ({ ...v, can_edit: canEdit(v, ctx) });

function validateView(b: Partial<SavedViewInput>): string | null {
  if (b.signal !== "logs" && b.signal !== "metrics" && b.signal !== "traces") return "signal must be logs, metrics or traces";
  if (typeof b.name !== "string" || b.name.trim() === "" || b.name.length > 200) return "name must be 1-200 characters";
  if (b.description !== undefined && (typeof b.description !== "string" || b.description.length > 2000)) return "description must be at most 2000 characters";
  if (b.visibility !== "private" && b.visibility !== "org") return "visibility must be private or org";
  if (!b.state || typeof b.state !== "object" || Array.isArray(b.state)) return "state must be an object";
  if (JSON.stringify(b.state).length > 32 * 1024) return "state is larger than 32 KiB";
  return null;
}

const mutator = (ctx: Ctx) => (ctx.kind !== "session" ? fail("permission_denied", "this operation requires a signed-in user; API keys are read-only") : ctx.role === "viewer" ? fail("permission_denied", "your role (viewer) does not allow this operation") : null);

// ---- handlers -----------------------------------------------------------------------------------------------------------

const ROUND_STEPS = [1, 5, 10, 30, 60, 300, 600, 1800, 3600, 10800, 21600, 43200, 86400].map((s) => s * 1000);

export const explorerHandlers = [
  http.get(`${API}/fields/keys`, authed((_ctx, { request }) => {
    const p = new URL(request.url).searchParams;
    const signal = p.get("signal");
    if (signal !== "logs" && signal !== "metrics" && signal !== "traces") return fail("invalid_argument", "signal must be logs, metrics or traces");
    const w = timeWindow(p.get("from"), p.get("to"));
    if (w instanceof Response) return w;
    const limit = Math.min(1000, Math.max(1, Number(p.get("limit") ?? 200) || 200));
    const q = (p.get("q") ?? "").toLowerCase();
    let fields: FieldKey[];
    let keys: FieldKey[];
    if (signal === "logs") {
      fields = Object.entries(LOG_FIELDS).map(([key, f]) => ({ key, name: key, source: "field", type: f.type, count: null, cardinality: null }));
      keys = logKeys(logRecords(Date.now()).filter((r) => r.ts >= w.from && r.ts <= w.to));
    } else if (signal === "metrics") {
      const metric = p.get("metric");
      const ms = explorerMetrics().filter((m) => !metric || m.name === metric);
      fields = METRIC_FIELDS.map(([key, type]) => ({ key, name: key, source: "field", type, count: null, cardinality: null }));
      keys = [...metricKeys(ms, "attribute"), ...metricKeys(ms, "resource")];
    } else {
      fields = Object.entries(SPAN_FIELDS).map(([key, f]) => ({ key, name: key, source: "field", type: f.type, count: null, cardinality: null }));
      keys = spanKeys(spanRecords(Date.now()).filter((r) => r.ts >= w.from && r.ts <= w.to));
    }
    const all = [...fields, ...keys].filter((k) => !q || k.key.toLowerCase().includes(q));
    return HttpResponse.json({ keys: all.slice(0, limit), sampled: false });
  })),

  http.get(`${API}/fields/values`, authed((_ctx, { request }) => {
    const p = new URL(request.url).searchParams;
    const signal = p.get("signal");
    const key = p.get("key") ?? "";
    if (signal !== "logs" && signal !== "metrics" && signal !== "traces") return fail("invalid_argument", "signal must be logs, metrics or traces");
    if (!key) return fail("invalid_argument", "key is required");
    const w = timeWindow(p.get("from"), p.get("to"));
    if (w instanceof Response) return w;
    let filters: QueryFilter[] = [];
    let groups: QueryFilter[][] = [];
    try {
      if (p.get("filters")) filters = JSON.parse(p.get("filters")!) as QueryFilter[];
      if (p.get("groups")) groups = JSON.parse(p.get("groups")!) as QueryFilter[][];
    } catch {
      return fail("invalid_argument", "filters/groups: invalid JSON");
    }
    const ferr = Array.isArray(filters) && Array.isArray(groups) ? validateFilterBody({ filters, groups }) : "filters and groups must be JSON arrays";
    if (ferr) return fail("invalid_argument", `filters: ${ferr}`);
    const bodyQ = (p.get("body_q") ?? "").toLowerCase();
    const rootOnly = p.get("root_only") === "true";
    const limit = Math.min(1000, Math.max(1, Number(p.get("limit") ?? 50) || 50));
    const q = (p.get("q") ?? "").toLowerCase();
    const counts = new Map<string, number>();
    const count = (v: string | undefined) => v !== undefined && (!q || v.toLowerCase().includes(q)) && counts.set(v, (counts.get(v) ?? 0) + 1);
    if (signal === "logs") {
      for (const r of logRecords(Date.now()))
        if (r.ts >= w.from && r.ts <= w.to && (!bodyQ || r.log.body.toLowerCase().includes(bodyQ)) && matchesAll((k) => logValue(r, k), { filters, groups }, key)) count(logValue(r, key));
    } else if (signal === "metrics") {
      const metric = p.get("metric");
      for (const m of explorerMetrics()) if (!metric || m.name === metric) for (const s of m.series) if (matchesAll((k) => metricValue(m, s, k), { filters, groups }, key)) count(metricValue(m, s, key));
    } else {
      for (const r of spanRecords(Date.now()))
        if (r.ts >= w.from && r.ts <= w.to && (!rootOnly || r.row.parent_span_id === "") && matchesAll((k) => spanValue(r.row, k), { filters, groups }, key)) count(spanValue(r.row, key));
    }
    const values = [...counts.entries()].map(([value, c]) => ({ value, count: c })).sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
    const total = values.reduce((n, v) => n + v.count, 0);
    return HttpResponse.json({ key, type: typeOf(values.map((v) => v.value)), values: values.slice(0, limit), total, sampled: false });
  })),

  http.post(`${API}/logs/query`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { from?: unknown; to?: unknown; order?: string; limit?: number; cursor?: string; columns?: string[]; include_record?: boolean }) | null;
    if (!b || typeof b !== "object") return fail("invalid_argument", "invalid JSON body");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    if (b.order !== undefined && b.order !== "asc" && b.order !== "desc") return fail("invalid_argument", "order must be asc or desc");
    const columns = b.columns ?? [];
    if (!Array.isArray(columns) || columns.length > 50) return fail("invalid_argument", "at most 50 columns");
    const limit = Math.min(1000, Math.max(1, b.limit ?? 100));
    let offset = 0;
    if (b.cursor) {
      try {
        offset = (JSON.parse(atob(b.cursor)) as { o: number }).o;
        if (!Number.isInteger(offset) || offset < 0) throw new Error("cursor");
      } catch {
        return fail("invalid_argument", "cursor: invalid");
      }
    }
    const q = (b.q ?? "").toLowerCase();
    const txn = transactionTraceIds(b);
    if (txn instanceof Response) return txn;
    const list = logRecords(Date.now())
      .filter((r) => r.ts >= w.from && r.ts <= w.to && (!q || r.log.body.toLowerCase().includes(q)) && (!txn || txn.has(r.log.trace_id)) && matchesAll((k) => logValue(r, k), b))
      .sort((x, y) => (b.order === "asc" ? x.ts - y.ts : y.ts - x.ts));
    const end = offset + limit;
    return HttpResponse.json({ rows: list.slice(offset, end).map((r) => logRow(r, columns, !!b.include_record)), next_cursor: end < list.length ? btoa(JSON.stringify({ o: end })) : null });
  })),

  http.post(`${API}/logs/aggregate`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { from?: unknown; to?: unknown; step?: string; group_by?: string; limit?: number }) | null;
    if (!b || typeof b !== "object") return fail("invalid_argument", "invalid JSON body");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    let step = ROUND_STEPS.find((s) => (w.to - w.from) / s <= 120) ?? ROUND_STEPS[ROUND_STEPS.length - 1]!;
    if (b.step) {
      const s = parseGoDuration(b.step);
      if (s === null || s < 1000) return fail("invalid_argument", "step must be a Go duration of at least 1s");
      step = s;
    }
    const limit = Math.min(50, Math.max(1, b.limit ?? 10));
    const q = (b.q ?? "").toLowerCase();
    const txn = transactionTraceIds(b);
    if (txn instanceof Response) return txn;
    const recs = logRecords(Date.now()).filter((r) => r.ts >= w.from && r.ts <= w.to && (!q || r.log.body.toLowerCase().includes(q)) && (!txn || txn.has(r.log.trace_id)) && matchesAll((k) => logValue(r, k), b));
    const groupOf = (r: Rec) => (b.group_by ? (logValue(r, b.group_by) ?? "") : "");
    const totals = new Map<string, number>();
    for (const r of recs) totals.set(groupOf(r), (totals.get(groupOf(r)) ?? 0) + 1);
    const top = new Set([...totals.entries()].sort((x, y) => y[1] - x[1]).slice(0, limit).map(([g]) => g));
    const buckets = new Map<string, { other: boolean; total: number; points: Map<number, number> }>();
    for (const r of recs) {
      const g = groupOf(r);
      const other = !top.has(g);
      const id = other ? "\u0000other" : g;
      const e = buckets.get(id) ?? { other, total: 0, points: new Map<number, number>() };
      const t = Math.floor(r.ts / step) * step;
      e.total++;
      e.points.set(t, (e.points.get(t) ?? 0) + 1);
      buckets.set(id, e);
    }
    const series = [...buckets.entries()]
      .map(([id, e]) => ({ group: e.other ? "" : id, other: e.other, total: e.total, points: [...e.points.entries()].sort((x, y) => x[0] - y[0]) }))
      .sort((x, y) => Number(x.other) - Number(y.other) || y.total - x.total);
    return HttpResponse.json({ step: `${step / 1000}s`, total: recs.length, series });
  })),

  http.post(`${API}/logs/patterns`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { from?: unknown; to?: unknown; limit?: number }) | null;
    if (!b || typeof b !== "object") return fail("invalid_argument", "invalid JSON body");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    if (b.limit !== undefined && (typeof b.limit !== "number" || b.limit < 1 || b.limit > 500)) return fail("invalid_argument", "limit must be between 1 and 500");
    const limit = b.limit ?? 50;
    const q = (b.q ?? "").toLowerCase();
    const txn = transactionTraceIds(b);
    if (txn instanceof Response) return txn;
    const recs = logRecords(Date.now()).filter(
      (r) => r.ts >= w.from && r.ts <= w.to && (!q || r.log.body.toLowerCase().includes(q)) && (!txn || txn.has(r.log.trace_id)) && matchesAll((k) => logValue(r, k), b),
    );
    const bucket = (n: number) => (n === 0 ? "unspecified" : n <= 4 ? "trace" : n <= 8 ? "debug" : n <= 12 ? "info" : n <= 16 ? "warn" : n <= 20 ? "error" : "fatal");
    const groups = new Map<string, Rec[]>();
    let unclassified = 0;
    for (const r of recs) {
      const id = patternId(r.log.body);
      if (id === "0") {
        unclassified++;
        continue;
      }
      groups.set(id, [...(groups.get(id) ?? []), r]);
    }
    const all = [...groups.entries()]
      .map(([id, list]) => {
        const newest = list.reduce((a, r) => (r.ts > a.ts ? r : a), list[0]!);
        const severity = { unspecified: 0, trace: 0, debug: 0, info: 0, warn: 0, error: 0, fatal: 0 };
        for (const r of list) severity[bucket(r.log.severity_number)]++;
        return {
          pattern_id: id,
          template: maskBody(newest.log.body),
          count: list.length,
          severity,
          max_severity_number: Math.max(...list.map((r) => r.log.severity_number)),
          services: [...new Set(list.map((r) => r.log.service_name).filter(Boolean))].slice(0, 5),
          first_seen: fx.formatTs(Math.min(...list.map((r) => r.ts))),
          last_seen: fx.formatTs(Math.max(...list.map((r) => r.ts))),
          sample: {
            timestamp: newest.log.timestamp,
            body: newest.log.body,
            service_name: newest.log.service_name,
            severity_text: newest.log.severity_text,
            severity_number: newest.log.severity_number,
            trace_id: newest.log.trace_id,
          },
        };
      })
      .sort((x, y) => y.count - x.count || x.pattern_id.localeCompare(y.pattern_id));
    return HttpResponse.json({ patterns: all.slice(0, limit), total: recs.length, unclassified, rollup: false, truncated: all.length > limit });
  })),

  http.post(`${API}/traces/query`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { from?: unknown; to?: unknown; order?: string; sort?: string; root_only?: boolean; limit?: number; cursor?: string; columns?: string[] }) | null;
    if (!b || typeof b !== "object") return fail("invalid_argument", "invalid JSON body");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    if (b.sort !== undefined && b.sort !== "timestamp" && b.sort !== "duration") return fail("invalid_argument", "sort must be timestamp or duration");
    const columns = b.columns ?? [];
    const limit = Math.min(1000, Math.max(1, b.limit ?? 100));
    let offset = 0;
    if (b.cursor) {
      try {
        offset = (JSON.parse(atob(b.cursor)) as { o: number }).o;
      } catch {
        return fail("invalid_argument", "cursor: invalid");
      }
    }
    const byDuration = b.sort === "duration";
    const list = spanRecords(Date.now())
      .filter((r) => r.ts >= w.from && r.ts <= w.to && (!b.root_only || r.row.parent_span_id === "") && matchesAll((k) => spanValue(r.row, k), b))
      .sort((x, y) => (byDuration ? y.row.duration_ns - x.row.duration_ns : b.order === "asc" ? x.ts - y.ts : y.ts - x.ts));
    const end = offset + limit;
    const rows = list.slice(offset, end).map(({ row }) => ({ ...row, fields: Object.fromEntries(columns.map((c) => [c, spanValue(row, c)]).filter((e): e is [string, string] => e[1] !== undefined)) }));
    return HttpResponse.json({ rows, next_cursor: !byDuration && end < list.length ? btoa(JSON.stringify({ o: end })) : null });
  })),

  http.post(`${API}/traces/aggregate`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { from?: unknown; to?: unknown; step?: string; group_by?: string; root_only?: boolean; limit?: number }) | null;
    if (!b || typeof b !== "object") return fail("invalid_argument", "invalid JSON body");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    const step = ROUND_STEPS.find((s) => (w.to - w.from) / s <= 120) ?? ROUND_STEPS[ROUND_STEPS.length - 1]!;
    const limit = Math.min(50, Math.max(1, b.limit ?? 10));
    const recs = spanRecords(Date.now()).filter((r) => r.ts >= w.from && r.ts <= w.to && (!b.root_only || r.row.parent_span_id === "") && matchesAll((k) => spanValue(r.row, k), b));
    const groupOf = (r: SpanRec) => (b.group_by ? (spanValue(r.row, b.group_by) ?? "") : "");
    const totals = new Map<string, number>();
    for (const r of recs) totals.set(groupOf(r), (totals.get(groupOf(r)) ?? 0) + 1);
    const top = new Set([...totals.entries()].sort((x, y) => y[1] - x[1]).slice(0, limit).map(([g]) => g));
    const buckets = new Map<string, { other: boolean; total: number; points: Map<number, number> }>();
    const durations = new Map<number, number[]>();
    for (const r of recs) {
      const g = groupOf(r);
      const other = !top.has(g);
      const id = other ? "\u0000other" : g;
      const e = buckets.get(id) ?? { other, total: 0, points: new Map<number, number>() };
      const t = Math.floor(r.ts / step) * step;
      e.total++;
      e.points.set(t, (e.points.get(t) ?? 0) + 1);
      buckets.set(id, e);
      durations.set(t, [...(durations.get(t) ?? []), r.row.duration_ms]);
    }
    const series = [...buckets.entries()]
      .map(([id, e]) => ({ group: e.other ? "" : id, other: e.other, total: e.total, points: [...e.points.entries()].sort((x, y) => x[0] - y[0]) }))
      .sort((x, y) => Number(x.other) - Number(y.other) || y.total - x.total);
    const times = [...durations.keys()].sort((x, y) => x - y);
    const lat = (q: number) => times.map((t) => [t, quantile([...durations.get(t)!].sort((x, y) => x - y), q)] as [number, number]);
    return HttpResponse.json({ step: `${step / 1000}s`, total: recs.length, series, latency: { p50: lat(0.5), p95: lat(0.95), p99: lat(0.99) } });
  })),

  http.get(`${API}/metrics`, authed((_ctx, { request }) => {
    const p = new URL(request.url).searchParams;
    const w = timeWindow(p.get("from"), p.get("to"));
    if (w instanceof Response) return w;
    const q = (p.get("q") ?? "").toLowerCase();
    const limit = Math.min(5000, Math.max(1, Number(p.get("limit") ?? 1000) || 1000));
    const now = Date.now();
    const all = explorerMetrics()
      .filter((m) => !q || m.name.toLowerCase().includes(q))
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((m) => info(m, now));
    return HttpResponse.json({ metrics: all.slice(0, limit), truncated: all.length > limit });
  })),

  http.post(`${API}/metrics/query`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { metric?: string; from?: unknown; to?: unknown; aggregation?: MetricAggregation; group_by?: string[]; step?: string; limit?: number }) | null;
    if (!b || typeof b !== "object" || typeof b.metric !== "string" || b.metric === "") return fail("invalid_argument", "metric is required");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    const groupBy = b.group_by ?? [];
    if (!Array.isArray(groupBy) || groupBy.length > 5) return fail("invalid_argument", "at most 5 group_by keys");
    let stepS = Math.max(10, Math.ceil((w.to - w.from) / 1000 / 300 / 10) * 10);
    if (b.step) {
      const s = parseGoDuration(b.step);
      if (s === null || s < 10_000) return fail("invalid_argument", "step must be a Go duration of at least 10s");
      stepS = Math.round(s / 1000);
    }
    const m = explorerMetrics().find((x) => x.name === b.metric);
    if (!m) return HttpResponse.json({ metric: { name: b.metric, type: "", unit: "", temporality: "unspecified", monotonic: false }, aggregation: b.aggregation ?? "avg", step: `${stepS}s`, series: [], truncated: false });
    const aggs = aggregationsOf(m);
    const agg = b.aggregation ?? aggs.def;
    if (!aggs.list.includes(agg)) return fail("invalid_argument", `aggregation ${agg} is not supported for ${m.type} metrics (use ${aggs.list.join(", ")})`);
    const limit = Math.min(200, Math.max(1, b.limit ?? 50));
    const pointValue = (s: XSeries, t: number): number => {
      if (m.type === "histogram" || m.type === "exponential_histogram" || m.type === "summary") {
        const base = s.value(t, false);
        const count = Math.round(40 + 60 * wave(t, 1800));
        if (PERCENTILES[agg]) return base * PERCENTILES[agg]!;
        return agg === "avg" ? base * 1.1 : agg === "count" ? count : agg === "sum" ? count * base * 1.1 : count / stepS;
      }
      if (m.type === "sum" && m.monotonic) return agg === "rate" ? s.value(t, true) : agg === "increase" ? s.value(t, true) * stepS : s.value(t, false);
      return agg === "count" ? 1 : s.value(t, false);
    };
    const groups = new Map<string, { attributes: Record<string, string>; points: Map<number, number[]> }>();
    for (const s of m.series) {
      if (!matchesAll((k) => metricValue(m, s, k), b)) continue;
      const attributes = Object.fromEntries(groupBy.map((k) => [k, metricValue(m, s, k) ?? ""]));
      const id = JSON.stringify(groupBy.map((k) => attributes[k]));
      const g = groups.get(id) ?? { attributes, points: new Map<number, number[]>() };
      groups.set(id, g);
      for (let t = Math.ceil(w.from / 1000 / stepS) * stepS; t * 1000 <= w.to; t += stepS) {
        const list = g.points.get(t * 1000) ?? [];
        list.push(pointValue(s, t));
        g.points.set(t * 1000, list);
      }
    }
    const merge = (vals: number[]) =>
      agg === "min" ? Math.min(...vals) : agg === "max" ? Math.max(...vals) : MERGE_SUM.has(agg) ? vals.reduce((a, v) => a + v, 0) : vals.reduce((a, v) => a + v, 0) / vals.length;
    const series: MetricSeries[] = [...groups.values()]
      .map((g) => ({ attributes: g.attributes, points: [...g.points.entries()].sort((x, y) => x[0] - y[0]).map(([t, vals]): [number, number] => [t, merge(vals)]) }))
      .sort((x, y) => JSON.stringify(x.attributes).localeCompare(JSON.stringify(y.attributes)));
    return HttpResponse.json({
      metric: { name: m.name, type: m.type, unit: m.unit, temporality: m.temporality, monotonic: m.monotonic },
      aggregation: agg,
      step: `${stepS}s`,
      series: series.slice(0, limit),
      truncated: series.length > limit,
    });
  })),

  // POST /api/v1/metrics/exemplars (D-130): traces spread over the range. The first one is the mock trace, so the
  // link actually opens a trace page; summaries carry no exemplars in OTLP.
  http.post(`${API}/metrics/exemplars`, authed(async (_ctx, { request }) => {
    const b = (await request.json().catch(() => null)) as (FilterBody & { metric?: string; from?: unknown; to?: unknown; limit?: number }) | null;
    if (!b || typeof b !== "object" || typeof b.metric !== "string" || b.metric === "") return fail("invalid_argument", "metric is required");
    const w = timeWindow(b.from, b.to);
    if (w instanceof Response) return w;
    const err = validateFilterBody(b);
    if (err) return fail("invalid_argument", err);
    if (b.limit !== undefined && (!Number.isInteger(b.limit) || b.limit < 1 || b.limit > 500)) return fail("invalid_argument", "limit must be between 1 and 500");
    const limit = b.limit ?? 50;
    const m = explorerMetrics().find((x) => x.name === b.metric);
    if (!m || m.type === "summary") return HttpResponse.json({ exemplars: [], total: 0, truncated: false });
    const count = Math.min(limit, 6);
    const spanID = fx.trace(Date.now())[0]?.span_id ?? "";
    const exemplars = Array.from({ length: count }, (_, i) => {
      const t = w.from + ((i + 0.5) * (w.to - w.from)) / count;
      const s = m.series[i % m.series.length]!;
      return {
        timestamp: fx.formatTs(t),
        // Above the line: an exemplar is one measurement, not the aggregate the chart draws.
        value: s.value(Math.floor(t / 1000), false) * 1.4,
        trace_id: i === 0 ? fx.TRACE_ID : String(i + 1).padStart(32, "0"),
        span_id: i === 0 ? spanID : "",
        service_name: s.resource["service.name"] ?? "checkout",
        attributes: s.attributes,
        filtered_attributes: i % 2 === 0 ? { "http.status_code": "500" } : {},
      };
    });
    // More exist than are returned, so the UI's "narrow the range" note is exercised.
    const total = count * 3;
    return HttpResponse.json({ exemplars, total, truncated: total > exemplars.length });
  })),

  http.get(`${API}/metrics/*`, authed((_ctx, { request }) => {
    const url = new URL(request.url);
    const name = decodeURIComponent(url.pathname.slice(url.pathname.indexOf("/api/v1/metrics/") + "/api/v1/metrics/".length));
    const w = timeWindow(url.searchParams.get("from"), url.searchParams.get("to"));
    if (w instanceof Response) return w;
    const m = explorerMetrics().find((x) => x.name === name);
    if (!m) return fail("not_found", "metric has no data points in the range");
    const aggs = aggregationsOf(m);
    const detail: MetricDetail = { ...info(m, Date.now()), attribute_keys: metricKeys([m], "attribute"), resource_keys: metricKeys([m], "resource"), aggregations: aggs.list, default_aggregation: aggs.def };
    return HttpResponse.json(detail);
  })),

  http.get(`${API}/saved-views`, authed((ctx, { request }) => {
    const signal = new URL(request.url).searchParams.get("signal");
    const views = savedViews.filter((v) => (!signal || v.signal === signal) && visible(v, ctx)).map((v) => withEdit(v, ctx));
    return HttpResponse.json({ views });
  })),

  http.post(`${API}/saved-views`, authed(async (ctx, { request }) => {
    const denied = mutator(ctx);
    if (denied) return denied;
    const b = (await request.json().catch(() => ({}))) as Partial<SavedViewInput>;
    const err = validateView(b);
    if (err) return fail("invalid_argument", err);
    const now = fx.formatTs(Date.now());
    viewSeq++;
    const view: StoredView = {
      id: `sv000000-0000-4000-8000-${String(viewSeq).padStart(12, "0")}`,
      signal: b.signal!,
      name: b.name!.trim(),
      description: b.description ?? "",
      visibility: b.visibility!,
      state: b.state!,
      created_by_user_id: ME,
      created_by_email: "admin@example.com",
      created_at: now,
      updated_at: now,
    };
    savedViews.push(view);
    return HttpResponse.json(withEdit(view, ctx), { status: 201 });
  })),

  http.get(`${API}/saved-views/:id`, authed((ctx, { params }) => {
    const v = savedViews.find((x) => x.id === params.id && visible(x, ctx));
    return v ? HttpResponse.json(withEdit(v, ctx)) : fail("not_found", "saved view not found");
  })),

  http.put(`${API}/saved-views/:id`, authed(async (ctx, { request, params }) => {
    const denied = mutator(ctx);
    if (denied) return denied;
    const v = savedViews.find((x) => x.id === params.id && visible(x, ctx));
    if (!v) return fail("not_found", "saved view not found");
    if (!canEdit(v, ctx)) return fail("permission_denied", "only the creator or an admin can change this view");
    const b = (await request.json().catch(() => ({}))) as Partial<SavedViewInput>;
    const err = validateView(b);
    if (err) return fail("invalid_argument", err);
    Object.assign(v, { signal: b.signal, name: b.name!.trim(), description: b.description ?? "", visibility: b.visibility, state: b.state, updated_at: fx.formatTs(Date.now()) });
    return HttpResponse.json(withEdit(v, ctx));
  })),

  http.delete(`${API}/saved-views/:id`, authed((ctx, { params }) => {
    const denied = mutator(ctx);
    if (denied) return denied;
    const i = savedViews.findIndex((x) => x.id === params.id && visible(x, ctx));
    if (i < 0) return fail("not_found", "saved view not found");
    if (!canEdit(savedViews[i]!, ctx)) return fail("permission_denied", "only the creator or an admin can delete this view");
    savedViews.splice(i, 1);
    return new HttpResponse(null, { status: 204 });
  })),
];
