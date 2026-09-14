// MSW handlers for OQL (docs/contracts/oql.md, api.md "Query language (OQL)"). The data is synthetic but
// deterministic and shaped by the query: event type, SELECT columns (aliases, percentile levels), FACET values
// (filtered by {{variables}}), TIMESERIES buckets for the requested range, histogram() and COMPARE WITH.
// Validation reports positions for obvious mistakes. Magic words: a query containing `__timeout__` answers 504,
// `__limit__` answers 422.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { OqlColumn, OqlDiagnostic, OqlResult, OqlRow, OqlSchema, OqlSeries, OqlValidation, OqlVariables } from "@/api/oql";
import { lexOql, lineColumn, OQL_EVENT_TYPES, OQL_FUNCTIONS, type OqlToken } from "@/lib/oql";
import { authenticate } from "./account";
import { formatTs, METRICS } from "./fixtures";

const API = "*/api/v1/query";

type EventType = (typeof OQL_EVENT_TYPES)[number];
type AttrType = "string" | "number" | "bool";

const S = (names: string): [string, AttrType][] => names.split(" ").map((n) => [n, "string"]);
const N = (names: string): [string, AttrType][] => names.split(" ").map((n) => [n, "number"]);

const SPAN_ATTRS: [string, AttrType][] = [
  ...S("name span.name kind status.code status.message trace.id span.id parent.id service.name service.namespace deployment.environment host.id"),
  ...N("duration duration.ms"),
  ...S("transaction.name transaction.type"),
  ["entry", "bool"],
  ["error", "bool"],
  ...N("http.status_code"),
  ...S("db.system db.name db.operation db.statement peer.type peer.name error.type error.message"),
  ...N("sample.weight"),
  ...S("scope.name"),
];

export const EVENT_ATTRIBUTES: Record<EventType, [string, AttrType][]> = {
  Log: [...S("service.name host.id host.name severity"), ...N("severity.number"), ...S("message trace.id span.id event.name scope.name")],
  Span: SPAN_ATTRS,
  Transaction: SPAN_ATTRS,
  Metric: [...S("metricName metric.type unit service.name host.id host.name"), ...N("value count sum"), ...S("scope.name")],
  Host: S("host.id host.name os.type os.description arch agent.name agent.version"),
  Container: [...S("container.id container.name host.id host.name image.name image.tags runtime compose.project compose.service k8s.pod.name k8s.namespace.name k8s.container.name state health"), ...N("restarts")],
};

const MAPS: Record<EventType, ("attributes" | "resource")[]> = {
  Log: ["attributes", "resource"], Span: ["attributes", "resource"], Transaction: ["attributes", "resource"], Metric: ["attributes", "resource"], Host: ["resource"], Container: ["attributes"],
};

const ALIASES: Record<string, string[]> = { severity: ["severity.text"], message: ["body"], metricName: ["metric.name"], "service.name": ["appName"] };
const ROLLUP = new Set(["metricName", "metric.type", "unit", "service.name", "host.id", "value"]);

const FUNCTION_DOCS: Record<string, [string, string]> = {
  count: ["count(* | attr)", "Number of events"],
  sum: ["sum(attr)", "Sum of a number attribute"],
  average: ["average(attr)", "Average of a number attribute"],
  avg: ["avg(attr)", "Alias of average"],
  min: ["min(attr)", "Minimum"],
  max: ["max(attr)", "Maximum"],
  uniqueCount: ["uniqueCount(attr)", "Exact number of distinct values"],
  median: ["median(attr)", "50th percentile"],
  latest: ["latest(attr)", "Value of the newest event"],
  earliest: ["earliest(attr)", "Value of the oldest event"],
  percentile: ["percentile(attr, p…)", "Quantiles, one column per level"],
  rate: ["rate(agg, duration)", "Aggregate per duration"],
  filter: ["filter(agg, WHERE cond)", "Aggregate over matching events"],
  histogram: ["histogram(attr, ceiling, buckets)", "Event counts in equal buckets"],
};

export function mockOqlSchema(eventType: EventType | null): OqlSchema {
  const keys: Record<EventType, string[]> = {
    Log: ["log.file.path", "openlog.log.source", "http.route", "log.iostream"],
    Span: ["http.route", "http.request.method", "url.path"],
    Transaction: ["http.route", "http.request.method", "url.path"],
    Metric: ["cpu.mode", "system.memory.state", "system.filesystem.mountpoint", "disk.io.direction"],
    Host: [],
    Container: ["com.docker.compose.service"],
  };
  return {
    event_types: OQL_EVENT_TYPES.map((name) => ({
      name,
      description: `${name} events`,
      maps: MAPS[name],
      max_range_seconds: name === "Metric" ? 400 * 86400 : 31 * 86400,
      attributes: EVENT_ATTRIBUTES[name].map(([n, type]) => ({ name: n, type, aliases: ALIASES[n] ?? [], rollup: name === "Metric" && ROLLUP.has(n) })),
    })),
    functions: OQL_FUNCTIONS.map((name) => ({ name, signature: FUNCTION_DOCS[name]![0], description: FUNCTION_DOCS[name]![1] })),
    keywords: ["SELECT", "FROM", "WHERE", "FACET", "SINCE", "UNTIL", "TIMESERIES", "LIMIT", "COMPARE WITH", "AGO", "AS", "AND", "OR", "NOT", "IN", "LIKE", "IS NULL", "AUTO", "NOW"],
    attribute_keys: eventType ? keys[eventType] : [],
    resource_keys: eventType && MAPS[eventType].includes("resource") ? ["service.version", "deployment.environment", "host.arch"] : [],
    metric_names: eventType === "Metric" ? Object.keys(METRICS).sort() : [],
  };
}

// ---- validation ----

const sig = (toks: OqlToken[]) => toks.filter((t) => t.type !== "whitespace" && t.type !== "comment");
const kw = (t: OqlToken | undefined, word: string) => t?.type === "keyword" && t.text.toUpperCase() === word;
const CLAUSE_WORDS = ["WHERE", "FACET", "SINCE", "UNTIL", "TIMESERIES", "LIMIT", "COMPARE"];

function levenshtein(a: string, b: string): number {
  const dp = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    let prev = dp[0]!;
    dp[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const tmp = dp[j]!;
      dp[j] = Math.min(dp[j]! + 1, dp[j - 1]! + 1, prev + (a[i - 1] === b[j - 1] ? 0 : 1));
      prev = tmp;
    }
  }
  return dp[b.length]!;
}

export interface MockValidation extends OqlValidation {
  canonical: EventType | null;
}

export function validateOql(query: string): MockValidation {
  const errors: OqlDiagnostic[] = [];
  const warnings: OqlDiagnostic[] = [];
  const diag = (list: OqlDiagnostic[], message: string, offset: number, length: number) => {
    const { line, column } = lineColumn(query, offset);
    list.push({ message, offset, length, line, column });
  };
  const toks = sig(lexOql(query));
  const variables = [...new Set(toks.filter((t) => t.type === "variable").map((t) => t.text.slice(2, -2).trim()))];
  const result = (eventType: EventType | null): MockValidation => {
    const valid = errors.length === 0;
    let kind: OqlValidation["kind"] = null;
    if (valid) {
      if (toks.some((t) => t.type === "function" && t.text.toLowerCase() === "histogram")) kind = "histogram";
      else if (toks.some((t) => kw(t, "TIMESERIES"))) kind = "timeseries";
      else if (toks.some((t) => kw(t, "FACET"))) kind = "facets";
      else kind = "single";
    }
    return { valid, event_type: valid ? eventType : null, kind, variables, errors, warnings, canonical: eventType };
  };

  if (toks.length === 0) {
    diag(errors, "query is empty: expected SELECT", 0, 0);
    return result(null);
  }
  if (!kw(toks[0], "SELECT")) {
    diag(errors, `expected SELECT, found "${toks[0]!.text}"`, toks[0]!.from, toks[0]!.to - toks[0]!.from);
    return result(null);
  }
  for (const t of toks) {
    if (t.type === "invalid") {
      diag(errors, `unexpected "${t.text}"`, t.from, t.to - t.from);
      return result(null);
    }
  }
  const fromIdx = toks.findIndex((t) => kw(t, "FROM"));
  if (fromIdx < 0) {
    diag(errors, "expected FROM", query.length, 0);
    return result(null);
  }
  if (fromIdx === 1) {
    diag(errors, "expected an aggregate function after SELECT", toks[1]!.from, toks[1]!.to - toks[1]!.from);
    return result(null);
  }
  const et = toks[fromIdx + 1];
  if (!et) {
    diag(errors, "expected an event type after FROM", query.length, 0);
    return result(null);
  }
  const canonical = OQL_EVENT_TYPES.find((e) => e.toLowerCase() === et.text.toLowerCase()) ?? null;
  if (et.type !== "identifier" || !canonical) {
    diag(errors, `unknown event type "${et.text}" (expected ${OQL_EVENT_TYPES.join(", ")})`, et.from, et.to - et.from);
    return result(null);
  }
  const attrs = EVENT_ATTRIBUTES[canonical].flatMap(([n]) => [n, ...(ALIASES[n] ?? [])]);
  let clause = "SELECT";
  for (let i = 1; i < toks.length; i++) {
    const t = toks[i]!;
    const next = toks[i + 1];
    if (t.type === "keyword" && (CLAUSE_WORDS.includes(t.text.toUpperCase()) || t.text.toUpperCase() === "FROM")) clause = t.text.toUpperCase();
    if (t.type === "identifier" && next?.text === "(" && !["attributes", "resource"].includes(t.text)) {
      diag(errors, `unknown function "${t.text}"`, t.from, t.to - t.from);
      continue;
    }
    if (t.type !== "identifier" || i === fromIdx + 1 || next?.text === "[") continue;
    if (t.text === "attributes" || t.text === "resource") continue;
    // Attribute positions: inside SELECT calls, WHERE predicates (left side) and FACET lists.
    const inAttrPosition = clause === "SELECT" || clause === "WHERE" || clause === "FACET";
    if (!inAttrPosition || attrs.includes(t.text) || t.text.startsWith("resource.")) continue;
    if (t.text === "tenant_id" || t.text.startsWith("_")) {
      diag(errors, `attribute "${t.text}" is not allowed`, t.from, t.to - t.from);
      continue;
    }
    const close = attrs.some((a) => levenshtein(a, t.text) <= 2);
    if (MAPS[canonical].includes("attributes") && !close) {
      diag(warnings, `attribute ${t.text} is read from attributes['${t.text}']`, t.from, t.to - t.from);
    } else {
      diag(errors, `unknown attribute "${t.text}" for ${canonical}`, t.from, t.to - t.from);
    }
  }
  return result(canonical);
}

// ---- query execution ----

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 100000) / 100000;
}

const UNIT_SECONDS: Record<string, number> = {
  second: 1, seconds: 1, sec: 1, minute: 60, minutes: 60, min: 60, hour: 3600, hours: 3600, day: 86400, days: 86400, week: 604800, weeks: 604800,
};
const AUTO_BUCKETS = [10, 30, 60, 300, 600, 900, 1800, 3600, 10800, 21600, 43200, 86400, 604800];

const FACET_VALUES: Record<string, string[]> = {
  "service.name": ["checkout", "orders", "payments", "frontend", "inventory"],
  "host.name": ["web-1", "db-1", "worker-1"],
  severity: ["INFO", "WARN", "ERROR", "DEBUG"],
  name: ["GET /api/orders", "POST /api/checkout", "GET /health", "GET /api/products"],
  "transaction.name": ["GET /api/orders", "POST /api/checkout", "GET /health", "GET /api/products"],
  metricName: ["system.cpu.utilization", "system.memory.usage", "system.network.io"],
  "container.name": ["api", "postgres", "redis", "nginx"],
  "http.status_code": ["200", "201", "404", "500"],
  kind: ["server", "client", "internal"],
  "deployment.environment": ["prod", "staging"],
  "os.type": ["linux"],
  state: ["running", "exited"],
};

interface Parsed {
  columns: (OqlColumn & { attr: string })[];
  facets: string[];
  limit: number;
  timeseries: number | "auto" | null;
  since: number | null;
  compare: number | null;
  histogram: { ceiling: number; buckets: number } | null;
  variableFilters: { attr: string; variable: string }[];
}

function splitTopLevel(toks: OqlToken[]): OqlToken[][] {
  const items: OqlToken[][] = [[]];
  let depth = 0;
  for (const t of toks) {
    if (t.text === "(") depth++;
    if (t.text === ")") depth--;
    if (t.text === "," && depth === 0) items.push([]);
    else items[items.length - 1]!.push(t);
  }
  return items.filter((i) => i.length > 0);
}

function duration(toks: OqlToken[], at: number): number | null {
  const n = toks[at];
  const u = toks[at + 1];
  if (n?.type !== "number" || !u) return null;
  const secs = UNIT_SECONDS[u.text.toLowerCase()];
  return secs ? Number(n.text) * secs : null;
}

function parse(query: string): Parsed {
  const toks = sig(lexOql(query));
  const fromIdx = toks.findIndex((t) => kw(t, "FROM"));
  const columns: Parsed["columns"] = [];
  let histogram: Parsed["histogram"] = null;
  for (const item of splitTopLevel(toks.slice(1, fromIdx))) {
    let body = item;
    let alias: string | null = null;
    const asIdx = item.findIndex((t) => kw(t, "AS"));
    if (asIdx > 0) {
      const a = item[asIdx + 1];
      alias = a ? (a.type === "string" ? a.text.slice(1, -1) : a.text) : null;
      body = item.slice(0, asIdx);
    }
    const fn = body.find((t) => t.type === "function")?.text ?? "count";
    const attr = body.find((t) => t.type === "identifier" || t.type === "backtick")?.text ?? "*";
    const text = query.slice(body[0]!.from, body[body.length - 1]!.to).replace(/\s+/g, " ");
    if (fn.toLowerCase() === "percentile") {
      const levels = body.filter((t) => t.type === "number").map((t) => t.text);
      for (const lvl of levels.length ? levels : ["95"]) {
        columns.push({ name: alias && levels.length <= 1 ? alias : `percentile(${attr}, ${lvl})`, function: "percentile", type: "number", attr });
      }
      continue;
    }
    if (fn.toLowerCase() === "histogram") {
      const nums = body.filter((t) => t.type === "number").map((t) => Number(t.text));
      histogram = { ceiling: nums[0] ?? 100, buckets: Math.min(200, Math.max(1, nums[1] ?? 40)) };
    }
    const isString = ["latest", "earliest"].includes(fn.toLowerCase()) && !/duration|value|count|restarts|status_code/.test(attr);
    columns.push({ name: alias ?? text, function: fn.toLowerCase() === "avg" ? "average" : fn, type: isString ? "string" : "number", attr });
  }
  const p: Parsed = { columns, facets: [], limit: 10, timeseries: null, since: null, compare: null, histogram, variableFilters: [] };
  for (let i = fromIdx + 2; i < toks.length; i++) {
    const t = toks[i]!;
    if (kw(t, "FACET")) {
      let j = i + 1;
      while (j < toks.length && !(toks[j]!.type === "keyword" && CLAUSE_WORDS.includes(toks[j]!.text.toUpperCase()))) {
        if (toks[j]!.type === "identifier" || toks[j]!.type === "backtick") p.facets.push(toks[j]!.text.replace(/`/g, ""));
        j++;
      }
    } else if (kw(t, "LIMIT") && toks[i + 1]?.type === "number") p.limit = Math.min(2000, Number(toks[i + 1]!.text));
    else if (kw(t, "TIMESERIES")) p.timeseries = duration(toks, i + 1) ?? "auto";
    else if (kw(t, "SINCE")) p.since = duration(toks, i + 1);
    else if (kw(t, "COMPARE") && kw(toks[i + 1], "WITH")) p.compare = duration(toks, i + 2);
    else if (t.type === "variable") {
      // attr = {{v}} | attr IN ({{v}})
      let j = i - 1;
      while (j > 0 && (toks[j]!.text === "(" || kw(toks[j], "IN") || kw(toks[j], "NOT") || toks[j]!.type === "operator")) j--;
      if (toks[j]?.type === "identifier") p.variableFilters.push({ attr: toks[j]!.text, variable: t.text.slice(2, -2).trim() });
    }
  }
  return p;
}

function baseValue(col: Parsed["columns"][number], key: string): number | string {
  const r = hash(`${col.name}|${key}`);
  const fn = col.function.toLowerCase();
  if (col.type === "string") return FACET_VALUES[col.attr]?.[Math.floor(r * 3)] ?? `value-${Math.floor(r * 100)}`;
  if (fn === "count") return Math.round(200 + r * 4800);
  if (fn === "uniquecount") return Math.round(1 + r * 40);
  if (/duration\.ms/.test(col.attr) || fn === "percentile" || fn === "median") return Math.round((20 + r * 600) * 10) / 10;
  if (/duration/.test(col.attr)) return Math.round((0.02 + r * 0.6) * 1000) / 1000;
  if (/utilization/.test(key)) return Math.round(r * 1000) / 1000;
  return Math.round(r * 1000) / 10;
}

function facetCombos(p: Parsed, variables: OqlVariables | undefined): string[][] {
  if (p.facets.length === 0) return [[]];
  let combos: string[][] = [[]];
  for (const f of p.facets) {
    let values = FACET_VALUES[f] ?? ["alpha", "beta", "gamma"];
    for (const vf of p.variableFilters) {
      const v = variables?.[vf.variable];
      const selected = v === undefined ? [] : Array.isArray(v) ? v : [v];
      if (vf.attr === f && selected.length > 0 && !selected.includes("*")) values = values.filter((x) => selected.includes(x));
    }
    combos = combos.flatMap((c) => values.map((v) => [...c, v]));
  }
  return combos;
}

function selectedShare(p: Parsed, variables: OqlVariables | undefined): number {
  let share = 1;
  for (const vf of p.variableFilters) {
    if (p.facets.includes(vf.attr)) continue;
    const all = FACET_VALUES[vf.attr];
    const v = variables?.[vf.variable];
    const selected = v === undefined ? [] : Array.isArray(v) ? v : [v];
    if (all && selected.length > 0 && !selected.includes("*")) share *= Math.max(0, selected.filter((x) => all.includes(x)).length) / all.length;
  }
  return share;
}

export function runMockQuery(query: string, from: number, to: number, variables?: OqlVariables, salt = ""): Omit<OqlResult, "compare" | "metadata"> & { bucketSeconds: number | null; truncated: boolean } {
  const v = validateOql(query);
  const p = parse(query);
  const eventType = v.canonical ?? "Log";
  const share = selectedShare(p, variables);
  const scale = (x: number | string) => (typeof x === "number" ? Math.round(x * share * (salt ? 0.8 : 1) * 1000) / 1000 : x);
  if (p.histogram) {
    const { ceiling, buckets: n } = p.histogram;
    const width = ceiling / n;
    const buckets = Array.from({ length: n }, (_, i) => {
      const center = (i + 0.5) / n;
      const shape = Math.exp(-((center - 0.25) ** 2) / 0.02) * 900 + hash(`${query}|${i}${salt}`) * 40;
      return { from: i * width, to: (i + 1) * width, count: Math.round(shape * share) };
    });
    return { kind: "histogram", event_type: eventType, columns: p.columns.slice(0, 1), facets: [], rows: [], series: [], buckets, bucketSeconds: null, truncated: false };
  }
  const combos = facetCombos(p, variables);
  const limit = p.timeseries !== null ? Math.min(p.limit, 50) : p.limit;
  const rowsAll: OqlRow[] = combos.map((facets) => ({ facets, values: p.columns.map((c) => scale(baseValue(c, facets.join("|")))) }));
  rowsAll.sort((a, b) => (typeof b.values[0] === "number" ? (b.values[0] as number) : 0) - (typeof a.values[0] === "number" ? (a.values[0] as number) : 0));
  const rows = rowsAll.slice(0, limit);
  const truncated = rowsAll.length > limit;
  if (p.timeseries === null) {
    return { kind: p.facets.length ? "facets" : "single", event_type: eventType, columns: p.columns, facets: p.facets, rows: p.facets.length ? rows : rows.slice(0, 1), series: [], buckets: [], bucketSeconds: null, truncated };
  }
  const rangeS = Math.max(1, (to - from) / 1000);
  const bucket = p.timeseries === "auto" ? (AUTO_BUCKETS.find((b) => rangeS / b <= 300) ?? 604800) : Math.max(10, p.timeseries);
  const bucketMs = bucket * 1000;
  const start = Math.floor(from / bucketMs) * bucketMs;
  const series: OqlSeries[] = [];
  for (const r of rows) {
    p.columns.forEach((c, ci) => {
      const base = r.values[ci];
      const phase = hash(`${r.facets.join("|")}|${ci}`) * 6;
      const points: [number, number | null][] = [];
      for (let t = start; t < to; t += bucketMs) {
        if (typeof base !== "number") {
          points.push([t, null]);
          continue;
        }
        const wave = 0.65 + 0.35 * Math.sin(t / bucketMs / 9 + phase) + (hash(`${t}|${phase}${salt}`) - 0.5) * 0.2;
        const perBucket = c.function.toLowerCase() === "count" ? base / 60 : base;
        points.push([t, Math.round(perBucket * wave * 100) / 100]);
      }
      series.push({ facets: r.facets, column: ci, points });
    });
  }
  return { kind: "timeseries", event_type: eventType, columns: p.columns, facets: p.facets, rows: [], series, buckets: [], bucketSeconds: bucket, truncated };
}

function fail(status: number, code: string, message: string) {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

function parseTime(v: string | undefined): number | null {
  if (v === undefined || v === "") return null;
  if (/^\d+$/.test(v)) return Number(v);
  const t = Date.parse(v);
  return Number.isNaN(t) ? null : t;
}

const TABLES: Record<EventType, string> = { Log: "logs", Span: "spans", Transaction: "spans", Metric: "metrics", Host: "hosts", Container: "containers" };

export const oqlHandlers = [
  http.get(`${API}/schema`, authed(({ request }) => {
    const et = new URL(request.url).searchParams.get("event_type");
    if (et && !(OQL_EVENT_TYPES as readonly string[]).includes(et)) return fail(400, "invalid_argument", `event_type: must be one of ${OQL_EVENT_TYPES.join(", ")}`);
    return HttpResponse.json(mockOqlSchema((et as EventType | null) ?? null));
  })),

  http.post(`${API}/validate`, authed(async ({ request }) => {
    const b = (await request.json().catch(() => ({}))) as { query?: string };
    if (typeof b.query !== "string") return fail(400, "invalid_argument", "query: required");
    const { canonical: _c, ...v } = validateOql(b.query);
    return HttpResponse.json(v);
  })),

  http.post(API, authed(async ({ request }) => {
    const b = (await request.json().catch(() => ({}))) as { query?: string; from?: string; to?: string; variables?: OqlVariables };
    const query = b.query ?? "";
    if (query.length > 8192) return fail(400, "invalid_argument", "query: at most 8 KiB");
    const v = validateOql(query);
    if (!v.valid) {
      const e = v.errors[0]!;
      return fail(400, "invalid_argument", `line ${e.line}, column ${e.column}: ${e.message}`);
    }
    if (query.includes("__timeout__")) return fail(504, "timeout", "query timed out");
    if (query.includes("__limit__")) return fail(422, "resource_exhausted", "query exceeded max_rows_to_read");
    const now = Date.now();
    let from = parseTime(b.from);
    let to = parseTime(b.to);
    if ((from === null) !== (to === null)) return fail(400, "invalid_argument", "from and to must be given together");
    if (from === null || to === null) {
      const p = parse(query);
      to = now;
      from = now - (p.since ?? 3600) * 1000;
    }
    if (from >= to) return fail(400, "invalid_argument", "SINCE must be before UNTIL");
    const cur = runMockQuery(query, from, to, b.variables);
    const offset = parse(query).compare;
    const prev = offset ? runMockQuery(query, from, to, b.variables, "previous") : null;
    const et = v.canonical ?? "Log";
    const result: OqlResult = {
      kind: cur.kind,
      event_type: cur.event_type,
      columns: cur.columns.map(({ name, function: fn, type }) => ({ name, function: fn, type })),
      facets: cur.facets,
      rows: cur.rows,
      series: cur.series,
      buckets: cur.buckets,
      compare: prev && offset ? { offset_seconds: offset, rows: prev.rows, series: prev.series, buckets: prev.buckets } : null,
      metadata: {
        from: formatTs(from),
        to: formatTs(to),
        bucket_seconds: cur.bucketSeconds,
        rollup: et === "Metric" && to - from > 6 * 3_600_000,
        table: et === "Metric" && to - from > 6 * 3_600_000 ? "metrics_1m" : TABLES[et],
        rows_read: Math.round(10_000 + hash(query) * 900_000),
        bytes_read: Math.round(1_000_000 + hash(`${query}b`) * 90_000_000),
        elapsed_ms: Math.round(8 + hash(`${query}e`) * 80),
        queries: prev ? 2 : 1,
        facet_limit: parse(query).limit,
        truncated: cur.truncated,
        warnings: v.warnings.map((w) => w.message),
      },
    };
    return HttpResponse.json(result);
  })),
];
