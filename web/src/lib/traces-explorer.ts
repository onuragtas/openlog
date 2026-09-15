// Traces Explorer state (routes/traces.tsx, D-122): span table columns, cell values, detail fields, chart series and
// saved view state. Pure helpers; column and preference storage reuses lib/logs-explorer with its own keys.
import type { FieldSource, SpanQueryRow, TracesAggregateResponse } from "@/api/explorer";
import type { ChartSeriesInput } from "@/lib/series";
import { normalizeColumns, volumeSeries } from "@/lib/logs-explorer";
import { decodeFilterState, encodeFilterState, type CompactFilterState } from "@/lib/querybuilder";
import type { RangeSpec } from "@/lib/time";
import { validateRangeSearch } from "@/lib/time";

export const DEFAULT_SPAN_COLUMNS = ["timestamp", "service.name", "name", "duration_ms", "status_code", "trace_id"] as const;
export const SPAN_STORAGE = { columns: "openlog.tracesExplorer.columns", widths: "openlog.tracesExplorer.widths", prefs: "openlog.tracesExplorer.prefs" } as const;
/** `gb` value for an ungrouped span count chart. */
export const NO_SPAN_GROUP = "~";
export const DEFAULT_SPAN_GROUP_BY = "service.name";

/** Top-level span fields whose values come from the row itself, not from `fields`. */
const SPAN_FIELDS: Record<string, (r: SpanQueryRow) => string> = {
  timestamp: (r) => r.timestamp,
  name: (r) => r.name,
  kind: (r) => r.kind,
  status_code: (r) => r.status_code,
  status_message: (r) => r.status_message,
  "service.name": (r) => r.service_name,
  "host.id": (r) => r.host_id,
  trace_id: (r) => r.trace_id,
  span_id: (r) => r.span_id,
  parent_span_id: (r) => r.parent_span_id,
  duration_ns: (r) => String(r.duration_ns),
  duration_ms: (r) => String(r.duration_ms),
  "http.status_code": (r) => (r.http_status_code ? String(r.http_status_code) : ""),
  is_entry: (r) => String(r.is_entry),
  error: (r) => String(r.is_error),
  "transaction.name": (r) => r.transaction_name,
};

/** Keys with numeric values (filter in/out compares numbers). */
export const SPAN_NUMBER_KEYS = new Set(["duration_ns", "duration_ms", "http.status_code"]);
export const SPAN_BOOL_KEYS = new Set(["is_entry", "error"]);

export const normalizeSpanColumns = (raw: unknown) => normalizeColumns(raw, DEFAULT_SPAN_COLUMNS);
export const isDefaultSpanColumns = (cols: readonly string[]) => cols.length === DEFAULT_SPAN_COLUMNS.length && cols.every((c, i) => c === DEFAULT_SPAN_COLUMNS[i]);

/** Keys requested in `columns` (row fields are always returned). */
export const requestSpanColumns = (cols: readonly string[]) => cols.filter((c) => !(c in SPAN_FIELDS));

/** Cell text of a column; undefined when the span has no such key. */
export function spanCellValue(row: SpanQueryRow, key: string): string | undefined {
  const own = SPAN_FIELDS[key];
  if (own) {
    const v = own(row);
    return v === "" && key !== "name" ? undefined : v;
  }
  if (row.fields[key] !== undefined) return row.fields[key];
  if (key.startsWith("attributes.")) return row.attributes?.[key.slice(11)];
  if (key.startsWith("resource.")) return row.resource_attributes?.[key.slice(9)];
  return row.attributes?.[key] ?? row.resource_attributes?.[key];
}

export interface SpanField {
  key: string;
  value: string;
  source: FieldSource;
}

/** Every field of a span for the detail panel (row fields, attributes, resource attributes). */
export function spanRecordFields(row: SpanQueryRow): SpanField[] {
  const out: SpanField[] = [];
  for (const [key, get] of Object.entries(SPAN_FIELDS)) {
    const value = get(row);
    if (value !== "") out.push({ key, value, source: "field" });
  }
  for (const [k, v] of Object.entries(row.attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))) out.push({ key: `attributes.${k}`, value: v, source: "attribute" });
  for (const [k, v] of Object.entries(row.resource_attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))) out.push({ key: `resource.${k}`, value: v, source: "resource" });
  return out;
}

/** Duration text in the most readable unit (µs below 1 ms, s from 1 s). */
export function formatSpanDuration(ms: number, locale: string): string {
  if (!Number.isFinite(ms)) return "–";
  if (ms < 1) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(ms * 1000)} µs`;
  if (ms < 1000) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: ms < 10 ? 2 : 1 }).format(ms)} ms`;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(ms / 1000)} s`;
}

/** Latency chart series (p50, p95, p99 in ms) of POST /traces/aggregate; gaps stay gaps. */
export function latencySeries(res: Pick<TracesAggregateResponse, "latency">): ChartSeriesInput[] {
  return (["p50", "p95", "p99"] as const).map((p) => ({ label: p, points: res.latency[p].map(([t, v]): [number, number] => [t, v]) }));
}

/** Span count series with empty buckets filled with 0 (as the log volume chart). */
export const spanCountSeries = (res: Pick<TracesAggregateResponse, "step" | "series">, from: number, to: number, labelOf: (s: TracesAggregateResponse["series"][number]) => string) =>
  volumeSeries(res, from, to, labelOf);

// ---- URL and saved view state -----------------------------------------------------------------------------------------

/** URL state of the Traces Explorer (router TracesSearch subset). */
export interface TracesExplorerSearch extends RangeSpec {
  f?: CompactFilterState;
  cols?: string[];
  order?: "asc";
  sort?: "duration";
  root?: boolean;
  gb?: string;
}

/** Saved view `state` of the Traces Explorer. */
export function tracesViewStateFromSearch(s: TracesExplorerSearch, columns: readonly string[], groupBy: string): Record<string, unknown> {
  const filter = decodeFilterState(s.f);
  return {
    filters: filter.filters,
    groups: filter.groups,
    columns,
    order: s.order ?? "desc",
    sort: s.sort ?? "timestamp",
    root_only: s.root === true,
    group_by: groupBy,
    ...(s.from && s.to ? { from: s.from, to: s.to } : s.range ? { range: s.range } : {}),
  };
}

/** URL search of a saved Traces Explorer view (untrusted: validated like the URL). */
export function searchFromTracesViewState(state: Record<string, unknown>): TracesExplorerSearch {
  const filter = decodeFilterState({ filters: state.filters, groups: state.groups });
  const cols = normalizeSpanColumns(state.columns);
  const gb = typeof state.group_by === "string" && state.group_by.length <= 256 ? state.group_by : DEFAULT_SPAN_GROUP_BY;
  return {
    ...validateRangeSearch(state),
    f: encodeFilterState({ ...filter, q: "" }),
    cols: isDefaultSpanColumns(cols) ? undefined : cols,
    order: state.order === "asc" ? "asc" : undefined,
    sort: state.sort === "duration" ? "duration" : undefined,
    root: state.root_only === true ? true : undefined,
    gb: gb === DEFAULT_SPAN_GROUP_BY ? undefined : gb,
  };
}
