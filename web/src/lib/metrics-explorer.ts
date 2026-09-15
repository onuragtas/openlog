// Metrics Explorer (routes/metrics.tsx): URL state of the queries, display units, client-side formulas over query
// results (`A / B * 100`) and the equivalent OQL for "Add to dashboard" (docs/contracts/oql.md).
import type { MetricAggregation, MetricDetail, QueryFilter } from "@/api/explorer";
import type { MetricSeries } from "@/api/types";
import type { UnitKind } from "@/lib/format";
import { decodeFilterState, encodeFilterState, type CompactFilterState } from "@/lib/querybuilder";
import { validateRangeSearch, type RangeSpec } from "@/lib/time";

export const QUERY_IDS = ["A", "B", "C", "D", "E", "F"] as const;
export const MAX_GROUP_BY = 5;
export const FORMULA_MAX_LENGTH = 256;
const AGGREGATIONS: readonly MetricAggregation[] = ["avg", "min", "max", "sum", "last", "count", "rate", "increase", "p50", "p75", "p90", "p95", "p99"];

export interface MetricQueryState {
  id: string;
  metric: string;
  filters: QueryFilter[];
  groups: QueryFilter[][];
  aggregation?: MetricAggregation;
  groupBy: string[];
}

/** Compact URL form of one query. */
export interface CompactMetricQuery {
  i: string;
  m?: string;
  f?: CompactFilterState;
  a?: MetricAggregation;
  g?: string[];
}

export const newMetricQuery = (id: string, metric = ""): MetricQueryState => ({ id, metric, filters: [], groups: [], groupBy: [] });

/** First query id not in use, or null when all are. */
export function nextQueryId(queries: readonly MetricQueryState[]): string | null {
  return QUERY_IDS.find((id) => !queries.some((q) => q.id === id)) ?? null;
}

export function encodeMetricQueries(queries: readonly MetricQueryState[]): CompactMetricQuery[] | undefined {
  const out = queries.map((q) => {
    const c: CompactMetricQuery = { i: q.id };
    if (q.metric) c.m = q.metric;
    const f = encodeFilterState({ filters: q.filters, groups: q.groups, q: "" });
    if (f) c.f = f;
    if (q.aggregation) c.a = q.aggregation;
    if (q.groupBy.length) c.g = q.groupBy;
    return c;
  });
  return out.length === 1 && !out[0]!.m && !out[0]!.f && !out[0]!.a && !out[0]!.g ? undefined : out;
}

/** Queries from untrusted URL input: unique ids in QUERY_IDS, at least one query (A). */
export function decodeMetricQueries(raw: unknown): MetricQueryState[] {
  let v = raw;
  if (typeof v === "string") {
    try {
      v = JSON.parse(v);
    } catch {
      v = null;
    }
  }
  const out: MetricQueryState[] = [];
  if (Array.isArray(v)) {
    for (const item of v) {
      if (!item || typeof item !== "object") continue;
      const o = item as Record<string, unknown>;
      const id = typeof o.i === "string" && (QUERY_IDS as readonly string[]).includes(o.i) ? o.i : null;
      if (!id || out.some((q) => q.id === id)) continue;
      const filter = decodeFilterState(o.f);
      out.push({
        id,
        metric: typeof o.m === "string" ? o.m.slice(0, 512) : "",
        filters: filter.filters,
        groups: filter.groups,
        aggregation: typeof o.a === "string" && (AGGREGATIONS as readonly string[]).includes(o.a) ? (o.a as MetricAggregation) : undefined,
        groupBy: Array.isArray(o.g) ? [...new Set(o.g.filter((k): k is string => typeof k === "string" && k !== "" && k.length <= 256))].slice(0, MAX_GROUP_BY) : [],
      });
    }
  }
  return out.length ? out.sort((a, b) => a.id.localeCompare(b.id)) : [newMetricQuery("A")];
}

// ---- units and statistics -------------------------------------------------------------------------------------------

export interface UnitDisplay {
  kind: UnitKind;
  /** Factor applied to values before formatting (e.g. 0.01 for "%" shown as a percentage). */
  scale: number;
}

/** Display unit of a query result from the OpenTelemetry unit (UCUM), only mapping the obvious cases. */
export function unitDisplay(name: string, unit: string, aggregation: MetricAggregation | undefined): UnitDisplay {
  if (aggregation === "count") return { kind: "number", scale: 1 };
  const rate = aggregation === "rate";
  switch (unit) {
    case "By":
      return rate ? { kind: "bytesPerSec", scale: 1 } : { kind: "bytes", scale: 1 };
    case "By/s":
      return rate ? { kind: "number", scale: 1 } : { kind: "bytesPerSec", scale: 1 };
    case "s":
      return rate ? { kind: "number", scale: 1 } : { kind: "s", scale: 1 };
    case "ms":
      return rate ? { kind: "number", scale: 1 } : { kind: "ms", scale: 1 };
    case "%":
      return rate ? { kind: "number", scale: 1 } : { kind: "percent", scale: 0.01 };
    case "1":
      // Dimensionless: a percentage only for ratio metrics (e.g. system.cpu.utilization in [0, 1]).
      return !rate && /(utilization|ratio)$/.test(name) ? { kind: "percent", scale: 1 } : { kind: "number", scale: 1 };
    default:
      return { kind: "number", scale: 1 };
  }
}

export const scaleSeries = (series: MetricSeries[], scale: number): MetricSeries[] =>
  scale === 1 ? series : series.map((s) => ({ ...s, points: s.points.map(([t, v]): [number, number] => [t, v * scale]) }));

export function seriesStats(points: readonly (readonly [number, number])[]): { last: number | null; avg: number | null; min: number | null; max: number | null } {
  const vals = points.map(([, v]) => v).filter(Number.isFinite);
  if (vals.length === 0) return { last: null, avg: null, min: null, max: null };
  return { last: vals[vals.length - 1]!, avg: vals.reduce((a, b) => a + b, 0) / vals.length, min: Math.min(...vals), max: Math.max(...vals) };
}

/** Attributes as a stable key (series matching, React keys). */
export const attributesKey = (a: Record<string, string>) => JSON.stringify(Object.entries(a).sort(([x], [y]) => x.localeCompare(y)));

// ---- formulas ---------------------------------------------------------------------------------------------------------

export type FormulaNode =
  | { type: "num"; value: number }
  | { type: "ref"; id: string }
  | { type: "neg"; arg: FormulaNode }
  | { type: "bin"; op: "+" | "-" | "*" | "/"; left: FormulaNode; right: FormulaNode };

export type FormulaError = "empty" | "tooLong" | "syntax" | "unknownQuery";
export type FormulaParse = { ok: true; ast: FormulaNode; refs: string[] } | { ok: false; error: FormulaError; position: number; token?: string };

/** Parses `+ - * /`, parentheses, numbers and query ids (recursive descent, no evaluation of arbitrary code). */
export function parseFormula(text: string, ids: readonly string[]): FormulaParse {
  if (text.length > FORMULA_MAX_LENGTH) return { ok: false, error: "tooLong", position: FORMULA_MAX_LENGTH };
  const tokens: { t: string; pos: number }[] = [];
  const re = /\s*(?:(\d+(?:\.\d+)?(?:e[+-]?\d+)?|\.\d+)|([A-Za-z]\w*)|([-+*/()]))/gy;
  let m: RegExpExecArray | null;
  let pos = 0;
  while (pos < text.length) {
    if (/^\s*$/.test(text.slice(pos))) break;
    re.lastIndex = pos;
    m = re.exec(text);
    if (!m) return { ok: false, error: "syntax", position: pos, token: text.slice(pos).trim()[0] };
    tokens.push({ t: m[1] ?? m[2] ?? m[3]!, pos: m.index + m[0].length - (m[1] ?? m[2] ?? m[3]!).length });
    pos = re.lastIndex;
  }
  if (tokens.length === 0) return { ok: false, error: "empty", position: 0 };
  let i = 0;
  const refs = new Set<string>();
  const fail = (error: FormulaError = "syntax"): FormulaParse => ({ ok: false, error, position: tokens[i]?.pos ?? text.length, token: tokens[i]?.t });
  class Fail extends Error {
    constructor(readonly result: FormulaParse) {
      super("formula");
    }
  }
  const peek = () => tokens[i]?.t;
  const primary = (): FormulaNode => {
    const tok = peek();
    if (tok === undefined) throw new Fail(fail());
    if (tok === "(") {
      i++;
      const e = expr();
      if (peek() !== ")") throw new Fail(fail());
      i++;
      return e;
    }
    if (tok === "-" || tok === "+") {
      i++;
      const arg = primary();
      return tok === "-" ? { type: "neg", arg } : arg;
    }
    if (/^[\d.]/.test(tok)) {
      i++;
      return { type: "num", value: Number(tok) };
    }
    if (/^[A-Za-z]/.test(tok)) {
      const id = tok.toUpperCase();
      if (!ids.includes(id)) throw new Fail(fail("unknownQuery"));
      i++;
      refs.add(id);
      return { type: "ref", id };
    }
    throw new Fail(fail());
  };
  const term = (): FormulaNode => {
    let left = primary();
    while (peek() === "*" || peek() === "/") {
      const op = tokens[i++]!.t as "*" | "/";
      left = { type: "bin", op, left, right: primary() };
    }
    return left;
  };
  const expr = (): FormulaNode => {
    let left = term();
    while (peek() === "+" || peek() === "-") {
      const op = tokens[i++]!.t as "+" | "-";
      left = { type: "bin", op, left, right: term() };
    }
    return left;
  };
  try {
    const ast = expr();
    if (i < tokens.length) return fail();
    return { ok: true, ast, refs: [...refs].sort() };
  } catch (e) {
    if (e instanceof Fail) return e.result;
    throw e;
  }
}

function evalNode(n: FormulaNode, env: Record<string, number>): number {
  switch (n.type) {
    case "num":
      return n.value;
    case "ref":
      return env[n.id]!;
    case "neg":
      return -evalNode(n.arg, env);
    case "bin": {
      const a = evalNode(n.left, env);
      const b = evalNode(n.right, env);
      return n.op === "+" ? a + b : n.op === "-" ? a - b : n.op === "*" ? a * b : a / b;
    }
  }
}

/**
 * Evaluates a formula per timestamp. Series are matched by identical attribute sets; a query with exactly one series
 * applies to every group of the others. Points exist where every referenced series has a value and the result is
 * finite (division by zero leaves a gap). `unmatched`: grouped queries share no attribute set.
 */
export function evaluateFormula(ast: FormulaNode, refs: readonly string[], data: Record<string, MetricSeries[] | undefined>): { series: MetricSeries[]; unmatched: boolean } {
  const lists = refs.map((id) => data[id] ?? []);
  // A constant formula has no timestamps to evaluate at.
  if (lists.length === 0 || lists.some((l) => l.length === 0)) return { series: [], unmatched: false };
  const multi = lists.filter((l) => l.length > 1);
  let keys: string[];
  if (multi.length === 0) keys = [""];
  else {
    const sets = multi.map((l) => new Set(l.map((s) => attributesKey(s.attributes))));
    keys = [...sets[0]!].filter((k) => sets.every((s) => s.has(k)));
    if (keys.length === 0) return { series: [], unmatched: true };
  }
  const series: MetricSeries[] = [];
  for (const key of keys) {
    const picked = lists.map((l) => (l.length === 1 ? l[0]! : l.find((s) => attributesKey(s.attributes) === key)!));
    const maps = picked.map((s) => new Map(s.points.map(([t, v]) => [t, v])));
    const points: [number, number][] = [];
    for (const [t] of picked[0]!.points) {
      const env: Record<string, number> = {};
      let complete = true;
      refs.forEach((id, i) => {
        const v = maps[i]!.get(t);
        if (v === undefined || !Number.isFinite(v)) complete = false;
        else env[id] = v;
      });
      if (!complete) continue;
      const v = evalNode(ast, env);
      if (Number.isFinite(v)) points.push([t, v]);
    }
    const attributes = key === "" ? {} : Object.fromEntries(JSON.parse(key) as [string, string][]);
    series.push({ attributes, points });
  }
  return { series, unmatched: false };
}

// ---- OQL ------------------------------------------------------------------------------------------------------------

export type OqlUnsupported = "noMetric" | "regex" | "aggregation";
export type MetricOqlResult = { ok: true; query: string } | { ok: false; reason: OqlUnsupported };

const oqlString = (s: string) => `'${s.replace(/\\/g, "\\\\").replace(/'/g, "''")}'`;
const DIRECT_KEYS = new Set(["service.name", "host.id", "host.name", "metric.type", "unit", "scope.name", "value"]);

/** OQL attribute for a filter or group-by key of the Metric event type. */
export function oqlAttribute(key: string): string {
  if (key === "metric.name") return "metricName";
  if (DIRECT_KEYS.has(key)) return key;
  if (key.startsWith("resource.")) return `resource[${oqlString(key.slice(9))}]`;
  if (key.startsWith("attributes.")) return `attributes[${oqlString(key.slice(11))}]`;
  return `attributes[${oqlString(key)}]`;
}

const oqlValue = (v: string | number | boolean) => (typeof v === "string" ? oqlString(v) : String(v));

function oqlCondition(f: QueryFilter): string | null {
  const attr = oqlAttribute(f.key);
  const v = f.value ?? "";
  switch (f.op) {
    case "=":
    case "!=":
    case ">":
    case ">=":
    case "<":
    case "<=":
      return `${attr} ${f.op} ${oqlValue(v)}`;
    case "in":
    case "not_in":
      return `${attr} ${f.op === "in" ? "IN" : "NOT IN"} (${(f.values ?? []).map(oqlValue).join(", ")})`;
    case "like":
      return `${attr} LIKE ${oqlValue(String(v))}`;
    case "not_like":
      return `${attr} NOT LIKE ${oqlValue(String(v))}`;
    case "contains":
      return `${attr} LIKE ${oqlString(`%${String(v)}%`)}`;
    case "not_contains":
      return `${attr} NOT LIKE ${oqlString(`%${String(v)}%`)}`;
    case "exists":
      return `${attr} IS NOT NULL`;
    case "not_exists":
      return `${attr} IS NULL`;
    default:
      return null;
  }
}

/** OQL aggregate of an explorer aggregation for the metric's type, or null without an equivalent. */
function oqlAggregate(agg: MetricAggregation, meta: Pick<MetricDetail, "type" | "monotonic" | "temporality">): string | null {
  const distribution = meta.type === "histogram" || meta.type === "exponential_histogram" || meta.type === "summary";
  if (distribution) {
    // Data points carry `count` and `sum` (raw points only); bucket percentiles, averages and rates have no equivalent.
    if (agg === "count") return "sum(count)";
    if (agg === "sum") return "sum(sum)";
    return null;
  }
  switch (agg) {
    case "avg":
      return "average(value)";
    case "min":
    case "max":
    case "sum":
      return `${agg}(value)`;
    case "last":
      return "latest(value)";
    case "count":
      return "count(*)";
    // Delta sums store per-interval increments: their sum is the increase and its per-second rate the rate.
    // Cumulative counters would need per-series differences, which OQL cannot express.
    case "rate":
      return meta.monotonic && meta.temporality === "delta" ? "rate(sum(value), 1 second)" : null;
    case "increase":
      return meta.monotonic && meta.temporality === "delta" ? "sum(value)" : null;
    default:
      return null;
  }
}

/** Equivalent OQL of a query (`SELECT average(value) FROM Metric WHERE metricName = '…' … FACET … TIMESERIES AUTO`). */
export function metricOql(q: MetricQueryState, meta: Pick<MetricDetail, "type" | "monotonic" | "temporality" | "default_aggregation">): MetricOqlResult {
  if (!q.metric) return { ok: false, reason: "noMetric" };
  const aggregate = oqlAggregate(q.aggregation ?? meta.default_aggregation, meta);
  if (!aggregate) return { ok: false, reason: "aggregation" };
  const conds = [`metricName = ${oqlString(q.metric)}`];
  for (const f of q.filters) {
    const c = oqlCondition(f);
    if (!c) return { ok: false, reason: "regex" };
    conds.push(c);
  }
  const groups = q.groups.filter((g) => g.length > 0);
  if (groups.length) {
    const alts: string[] = [];
    for (const g of groups) {
      const parts = g.map(oqlCondition);
      if (parts.some((p) => p === null)) return { ok: false, reason: "regex" };
      alts.push(parts.length > 1 ? `(${parts.join(" AND ")})` : parts[0]!);
    }
    conds.push(alts.length > 1 ? `(${alts.join(" OR ")})` : alts[0]!);
  }
  const facet = q.groupBy.length ? ` FACET ${q.groupBy.slice(0, MAX_GROUP_BY).map(oqlAttribute).join(", ")} LIMIT 50` : "";
  return { ok: true, query: `SELECT ${aggregate} FROM Metric WHERE ${conds.join(" AND ")}${facet} TIMESERIES AUTO` };
}

/** Legend label of a series: its group-by values (keys with or without the `attributes.`/`resource.` prefix). */
export function metricSeriesLabel(s: MetricSeries, groupBy: readonly string[], fallback: string): string {
  const keys = groupBy.length ? groupBy : Object.keys(s.attributes).sort();
  const parts = keys.map((k) => s.attributes[k] ?? s.attributes[k.replace(/^(attributes|resource)\./, "")]).filter((v): v is string => v !== undefined && v !== "");
  return parts.join(" · ") || fallback;
}

// ---- saved views ----------------------------------------------------------------------------------------------------

export interface MetricsExplorerSearch extends RangeSpec {
  mq?: CompactMetricQuery[];
  formula?: string;
}

export const sanitizeFormula = (v: unknown) => (typeof v === "string" && v.trim() ? v.trim().slice(0, FORMULA_MAX_LENGTH) : undefined);

/** Saved view `state` of the metrics explorer. */
export function metricsViewState(s: MetricsExplorerSearch): Record<string, unknown> {
  return { queries: s.mq ?? [], formula: s.formula ?? "", ...(s.from && s.to ? { from: s.from, to: s.to } : s.range ? { range: s.range } : {}) };
}

/** URL search of a saved view state (untrusted: validated like the URL). */
export function metricsSearchFromViewState(state: Record<string, unknown>): MetricsExplorerSearch {
  return { ...validateRangeSearch(state), mq: encodeMetricQueries(decodeMetricQueries(state.queries)), formula: sanitizeFormula(state.formula) };
}
