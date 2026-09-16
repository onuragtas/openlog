// Guided query builder (QueryWizard): the picks of someone who does not know OQL become a query text
// (docs/contracts/oql.md). One-way: the wizard writes OQL, it never parses it back.
import type { OqlEventType } from "@/api/oql";

export type BuilderMeasure = "count" | "average" | "sum" | "min" | "max" | "uniqueCount" | "median" | "percentile" | "latest";
export type BuilderOp = "=" | "!=" | "contains" | "like" | ">" | ">=" | "<" | "<=" | "is_null" | "is_not_null";

export interface BuilderCondition {
  /** Qualified key: a top-level attribute (`service.name`), `attributes.<k>` or `resource.<k>`. */
  key: string;
  op: BuilderOp;
  value: string;
}

export interface BuilderState {
  eventType: OqlEventType;
  measure: BuilderMeasure;
  /** Attribute the measure runs on (every measure except count). */
  attribute: string;
  /** Metric event type: the metric to read. */
  metricName: string;
  conditions: BuilderCondition[];
  groupBy: string[];
  timeseries: boolean;
  limit: number;
}

export const BUILDER_OPS: readonly BuilderOp[] = ["=", "!=", "contains", "like", ">", ">=", "<", "<=", "is_null", "is_not_null"];
export const BUILDER_MEASURES: readonly BuilderMeasure[] = ["count", "average", "sum", "min", "max", "uniqueCount", "median", "percentile", "latest"];
/** Measures that need a number attribute; the others take any attribute. */
export const NUMBER_MEASURES: readonly BuilderMeasure[] = ["average", "sum", "min", "max", "median", "percentile"];
export const MAX_GROUP_BY = 5;
export const MAX_CONDITIONS = 10;

export const defaultBuilderState = (eventType: OqlEventType = "Log"): BuilderState => ({
  eventType,
  measure: "count",
  attribute: eventType === "Metric" ? "value" : "",
  metricName: "",
  conditions: [],
  groupBy: [],
  timeseries: true,
  limit: 10,
});

const quote = (s: string) => `'${s.replace(/\\/g, "\\\\").replace(/'/g, "''")}'`;
const isNumeric = (v: string) => /^-?\d+(\.\d+)?$/.test(v.trim());

/** OQL form of a qualified key: `attributes.k` / `attr.k` / `resource.k` become map lookups, everything else stays. */
export function oqlKey(key: string): string {
  for (const [prefix, map] of [["attributes.", "attributes"], ["attr.", "attributes"], ["resource.", "resource"]] as const) {
    if (key.startsWith(prefix) && key.length > prefix.length) return `${map}[${quote(key.slice(prefix.length))}]`;
  }
  return key;
}

/** Select expression of a measure: `count(*)`, `average(duration.ms)`, `percentile(duration.ms, 50, 95, 99)`. */
export function measureExpression(measure: BuilderMeasure, attribute: string): string {
  if (measure === "count") return "count(*)";
  const attr = oqlKey(attribute);
  return measure === "percentile" ? `percentile(${attr}, 50, 95, 99)` : `${measure}(${attr})`;
}

function condition(c: BuilderCondition): string | null {
  const key = oqlKey(c.key);
  if (c.op === "is_null") return `${key} IS NULL`;
  if (c.op === "is_not_null") return `${key} IS NOT NULL`;
  const raw = c.value.trim();
  if (raw === "") return null;
  // Numbers and booleans are written unquoted; map values are strings, so a quoted number still matches there.
  const literal = isNumeric(raw) || raw === "true" || raw === "false" ? raw : quote(raw);
  switch (c.op) {
    case "contains":
      return `${key} CONTAINS ${quote(raw)}`;
    case "like":
      return `${key} LIKE ${quote(raw)}`;
    default:
      return `${key} ${c.op} ${literal}`;
  }
}

/** True when the state cannot produce a query yet (the wizard disables "run" and says why). */
export function builderIssue(s: BuilderState): "attribute" | "metric" | null {
  if (s.measure !== "count" && s.attribute.trim() === "") return "attribute";
  if (s.eventType === "Metric" && s.metricName.trim() === "") return "metric";
  return null;
}

/** OQL of the wizard's state, or "" while it is incomplete. */
export function buildOql(s: BuilderState): string {
  if (builderIssue(s)) return "";
  const conds: string[] = [];
  if (s.eventType === "Metric" && s.metricName.trim() !== "") conds.push(`metricName = ${quote(s.metricName.trim())}`);
  for (const c of s.conditions.slice(0, MAX_CONDITIONS)) {
    if (c.key.trim() === "") continue;
    const text = condition(c);
    if (text) conds.push(text);
  }
  const groups = s.groupBy.filter((g) => g.trim() !== "").slice(0, MAX_GROUP_BY);
  let q = `SELECT ${measureExpression(s.measure, s.attribute)} FROM ${s.eventType}`;
  if (conds.length) q += ` WHERE ${conds.join(" AND ")}`;
  if (groups.length) q += ` FACET ${groups.map(oqlKey).join(", ")} LIMIT ${Math.max(1, Math.round(s.limit))}`;
  if (s.timeseries) q += " TIMESERIES AUTO";
  return q;
}
