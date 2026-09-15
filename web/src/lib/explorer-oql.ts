// Explorer conditions as OQL (docs/contracts/oql.md) for "Add to dashboard": the shared predicate translation of the
// Logs, Traces and Metrics explorers and the OQL of the Logs Explorer volume chart and the Traces Explorer charts.
import type { ExplorerContext, FilterState, QueryFilter } from "@/api/explorer";

export type ExplorerOqlUnsupported = "regex" | "key" | "transaction";
export type ExplorerOqlResult = { ok: true; query: string } | { ok: false; reason: ExplorerOqlUnsupported };
type Scalar = string | number | boolean;

export const oqlString = (s: string) => `'${s.replace(/\\/g, "\\\\").replace(/'/g, "''")}'`;
const oqlValue = (v: Scalar) => (typeof v === "string" ? oqlString(v) : String(v));

/**
 * OQL form of an explorer key. `fallback`: a bare map key, read from the attribute when present, else the resource
 * attribute. `value` maps condition values (null: no equivalent value).
 */
export interface OqlKey {
  attr: string;
  fallback?: string;
  value?: (v: Scalar) => Scalar | null;
}

/** Resolves an explorer key, or null when OQL has no equivalent attribute. */
export type OqlKeyResolver = (key: string) => OqlKey | null;

type Conditions = { ok: true; conds: string[] } | { ok: false; reason: ExplorerOqlUnsupported };

function oqlPredicate(attr: string, f: QueryFilter): string | null {
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
      return `${attr} CONTAINS ${oqlString(String(v))}`;
    case "not_contains":
      return `${attr} NOT CONTAINS ${oqlString(String(v))}`;
    case "exists":
      return `${attr} IS NOT NULL`;
    case "not_exists":
      return `${attr} IS NULL`;
    default:
      return null;
  }
}

/**
 * OQL predicate of one explorer condition. `contains` becomes `CONTAINS` (case-insensitive, literal — identical to the
 * explorers, D-122). A bare key is written as
 * `((attributes['k'] IS NOT NULL AND attributes['k'] …) OR (attributes['k'] IS NULL AND resource['k'] …))`.
 */
function oqlCondition(f: QueryFilter, resolve: OqlKeyResolver): { ok: true; cond: string } | { ok: false; reason: ExplorerOqlUnsupported } {
  if (f.op === "regex" || f.op === "not_regex") return { ok: false, reason: "regex" };
  const k = resolve(f.key);
  if (!k) return { ok: false, reason: "key" };
  let mapped = f;
  if (k.value) {
    const value = f.value === undefined ? undefined : k.value(f.value);
    const values = f.values?.map(k.value);
    if (value === null || values?.some((x) => x === null)) return { ok: false, reason: "key" };
    mapped = { ...f, value: value ?? undefined, values: values as Scalar[] | undefined };
  }
  if (k.fallback) {
    const { attr, fallback: res } = k;
    if (f.op === "exists") return { ok: true, cond: `(${attr} IS NOT NULL OR ${res} IS NOT NULL)` };
    if (f.op === "not_exists") return { ok: true, cond: `(${attr} IS NULL AND ${res} IS NULL)` };
    return { ok: true, cond: `((${attr} IS NOT NULL AND ${oqlPredicate(attr, mapped)}) OR (${attr} IS NULL AND ${oqlPredicate(res, mapped)}))` };
  }
  const cond = oqlPredicate(k.attr, mapped);
  return cond === null ? { ok: false, reason: "key" } : { ok: true, cond };
}

/** OQL predicates of AND-ed `filters` and OR-ed AND-`groups` (one predicate for all groups). */
export function oqlConditions(filter: Pick<FilterState, "filters" | "groups">, resolve: OqlKeyResolver): Conditions {
  const conds: string[] = [];
  for (const f of filter.filters) {
    const c = oqlCondition(f, resolve);
    if (!c.ok) return c;
    conds.push(c.cond);
  }
  const alts: string[] = [];
  for (const g of filter.groups.filter((g) => g.length > 0)) {
    const parts: string[] = [];
    for (const f of g) {
      const c = oqlCondition(f, resolve);
      if (!c.ok) return c;
      parts.push(c.cond);
    }
    alts.push(parts.length > 1 ? `(${parts.join(" AND ")})` : parts[0]!);
  }
  if (alts.length) conds.push(alts.length > 1 ? `(${alts.join(" OR ")})` : alts[0]!);
  return { ok: true, conds };
}

/** `attributes.<k>`, `attr.<k>`, `resource.<k>` and bare keys (attribute, else resource attribute). */
function mapKey(key: string): OqlKey | null {
  for (const [prefix, map] of [["attributes.", "attributes"], ["attr.", "attributes"], ["resource.", "resource"]] as const) {
    if (key.startsWith(prefix)) return key.length > prefix.length ? { attr: `${map}[${oqlString(key.slice(prefix.length))}]` } : null;
  }
  return { attr: `attributes[${oqlString(key)}]`, fallback: `resource[${oqlString(key)}]` };
}

const lower = (v: Scalar): Scalar => (typeof v === "string" ? v.toLowerCase() : v);
const bool = (v: Scalar): Scalar | null => (typeof v === "boolean" ? v : v === "true" ? true : v === "false" ? false : null);
const field = (attr: string, value?: OqlKey["value"]): OqlKey => ({ attr, value });

// Top-level explorer fields (api.md "Fields" › Keys) and their OQL attributes; other top-level fields (timestamps,
// trace_flags) and JSON body paths have none.
const LOG_FIELDS: Record<string, OqlKey> = {
  severity_text: field("severity"),
  severity: field("severity"),
  severity_number: field("severity.number"),
  body: field("message"),
  "service.name": field("service.name"),
  "host.id": field("host.id"),
  "host.name": field("host.name"),
  trace_id: field("trace.id", lower),
  span_id: field("span.id", lower),
  "event.name": field("event.name"),
  "scope.name": field("scope.name"),
};
const LOG_UNSUPPORTED = new Set(["timestamp", "observed_timestamp", "trace_flags"]);

const SPAN_FIELDS: Record<string, OqlKey> = {
  name: field("name"),
  kind: field("kind"),
  status_code: field("status.code"),
  status_message: field("status.message"),
  "service.name": field("service.name"),
  "service.namespace": field("service.namespace"),
  "deployment.environment": field("deployment.environment"),
  "host.id": field("host.id"),
  trace_id: field("trace.id", lower),
  span_id: field("span.id", lower),
  parent_span_id: field("parent.id"),
  duration_ms: field("duration.ms"),
  // duration.ms is duration_ns / 1e6, so nanosecond values convert exactly.
  duration_ns: field("duration.ms", (v) => (Number.isFinite(Number(v)) && v !== "" && typeof v !== "boolean" ? Number(v) / 1e6 : null)),
  "http.status_code": field("http.status_code"),
  is_entry: field("entry", bool),
  error: field("error", bool),
  "transaction.name": field("transaction.name"),
  "transaction.type": field("transaction.type"),
  "db.system": field("db.system"),
  "peer.name": field("peer.name"),
  "scope.name": field("scope.name"),
};

export const logKey: OqlKeyResolver = (key) => LOG_FIELDS[key] ?? (LOG_UNSUPPORTED.has(key) || key.startsWith("body.") ? null : mapKey(key));
export const spanKey: OqlKeyResolver = (key) => SPAN_FIELDS[key] ?? (key === "timestamp" ? null : mapKey(key));

/** Log records excluded by every Logs Explorer request (inventory events). */
const LOG_BASE = `event.name NOT LIKE ${oqlString("openlog.inventory.%")}`;
const FACET_LIMIT = 10;

function build(select: string, from: "Log" | "Span", conds: string[], groupBy: string | undefined, resolve: OqlKeyResolver): ExplorerOqlResult {
  let facet = "";
  if (groupBy) {
    const k = resolve(groupBy);
    if (!k) return { ok: false, reason: "key" };
    facet = ` FACET ${k.attr} LIMIT ${FACET_LIMIT}`;
  }
  const where = conds.length ? ` WHERE ${conds.join(" AND ")}` : "";
  return { ok: true, query: `SELECT ${select} FROM ${from}${where}${facet} TIMESERIES AUTO` };
}

/** Logs Explorer volume chart: `SELECT count(*) FROM Log WHERE … FACET <group by> LIMIT 10 TIMESERIES AUTO`. */
export function logsVolumeOql(p: { filter: FilterState; groupBy?: string; context?: ExplorerContext }): ExplorerOqlResult {
  // "Logs of this transaction" selects the traces of a transaction first, which one OQL query cannot express.
  if (p.context?.transaction) return { ok: false, reason: "transaction" };
  const c = oqlConditions(p.filter, logKey);
  if (!c.ok) return c;
  const q = p.filter.q.trim();
  return build("count(*)", "Log", [LOG_BASE, ...(q ? [`message CONTAINS ${oqlString(q)}`] : []), ...c.conds], p.groupBy, logKey);
}

type SpanChartRequest = { filter: Pick<FilterState, "filters" | "groups">; rootOnly: boolean };

function spanConditions(p: SpanChartRequest): Conditions {
  const c = oqlConditions(p.filter, spanKey);
  return c.ok && p.rootOnly ? { ok: true, conds: ["parent.id IS NULL", ...c.conds] } : c;
}

/** Traces Explorer span count chart: `SELECT count(*) FROM Span WHERE … FACET <group by> LIMIT 10 TIMESERIES AUTO`. */
export function spanCountOql(p: SpanChartRequest & { groupBy?: string }): ExplorerOqlResult {
  const c = spanConditions(p);
  return c.ok ? build("count(*)", "Span", c.conds, p.groupBy, spanKey) : c;
}

/** Traces Explorer duration chart: p50, p95 and p99 of `duration.ms`. */
export function spanLatencyOql(p: SpanChartRequest): ExplorerOqlResult {
  const c = spanConditions(p);
  return c.ok ? build("percentile(duration.ms, 50, 95, 99)", "Span", c.conds, undefined, spanKey) : c;
}
