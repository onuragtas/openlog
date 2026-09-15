// Logs Explorer state (routes/logs.tsx): table columns, legacy /logs URL parameters, log volume buckets and saved view
// state. Pure helpers; the storage helpers tolerate unavailable localStorage.
import type { FieldSource, FieldType, LogQueryRow, LogsAggregateResponse, QueryFilter } from "@/api/explorer";
import type { ChartSeriesInput } from "@/lib/series";
import { decodeFilterState, encodeFilterState, type CompactFilterState } from "@/lib/querybuilder";
import type { RangeSpec } from "@/lib/time";
import { validateRangeSearch } from "@/lib/time";

export const DEFAULT_COLUMNS = ["timestamp", "severity_text", "service.name", "body"] as const;
export const MAX_COLUMNS = 50;
export const DEFAULT_GROUP_BY = "severity_text";
/** `gb` value for an ungrouped volume chart (not a valid key). */
export const NO_GROUP = "~";

/** Top-level log fields: values come from the row itself, not from `fields`. */
const ROW_FIELDS: Record<string, (r: LogQueryRow) => string> = {
  timestamp: (r) => r.timestamp,
  observed_timestamp: (r) => r.observed_timestamp,
  body: (r) => r.body,
  severity_text: (r) => r.severity_text,
  severity_number: (r) => String(r.severity_number),
  "service.name": (r) => r.service_name,
  "host.id": (r) => r.host_id,
  "host.name": (r) => r.host_name,
  trace_id: (r) => r.trace_id,
  span_id: (r) => r.span_id,
};

/** Validated column list: unique non-empty keys, `timestamp` pinned first, at most MAX_COLUMNS; defaults when empty. */
export function normalizeColumns(raw: unknown, defaults: readonly string[] = DEFAULT_COLUMNS): string[] {
  const list = Array.isArray(raw) ? raw : typeof raw === "string" ? raw.split(",") : [];
  const keys = list.filter((k): k is string => typeof k === "string" && k.trim() !== "" && k.length <= 256).map((k) => k.trim());
  const unique = [...new Set(keys)].filter((k) => k !== "timestamp");
  if (unique.length === 0) return [...defaults];
  return ["timestamp", ...unique].slice(0, MAX_COLUMNS);
}

export const isDefaultColumns = (cols: readonly string[]) => cols.length === DEFAULT_COLUMNS.length && cols.every((c, i) => c === DEFAULT_COLUMNS[i]);

/** Moves the column at `from` to `to`; the pinned timestamp column stays first. */
export function moveColumn(cols: readonly string[], from: number, to: number): string[] {
  if (from === to || from <= 0 || to <= 0 || from >= cols.length || to >= cols.length) return [...cols];
  const next = [...cols];
  const [c] = next.splice(from, 1);
  next.splice(to, 0, c!);
  return next;
}

/** Adds the key as the last column, or removes it (timestamp cannot be removed). */
export function toggleColumn(cols: readonly string[], key: string): string[] {
  if (key === "timestamp") return [...cols];
  if (cols.includes(key)) return cols.filter((c) => c !== key);
  return normalizeColumns([...cols, key]);
}

/** Whether a value can be used in a filter condition (API limit of 1024 bytes). */
export const isFilterableValue = (value: string) => new TextEncoder().encode(value).length <= 1024;

/** Share of a top value in percent (0 when the total is unknown). */
export const valueShare = (count: number, total: number) => (total > 0 ? Math.min(100, (count / total) * 100) : 0);

/** Keys requested in `columns` (row fields are always returned). */
export const requestColumns = (cols: readonly string[]) => cols.filter((c) => !(c in ROW_FIELDS));

/** Cell text of a column; undefined when the record has no such key. */
export function cellValue(row: LogQueryRow, key: string): string | undefined {
  const own = ROW_FIELDS[key];
  if (own) return own(row);
  if (row.fields[key] !== undefined) return row.fields[key];
  if (key.startsWith("attributes.")) return row.attributes?.[key.slice(11)];
  if (key.startsWith("resource.")) return row.resource_attributes?.[key.slice(9)];
  return row.attributes?.[key] ?? row.resource_attributes?.[key];
}

export interface RecordField {
  key: string;
  value: string;
  source: FieldSource;
}

/** All fields of a record for the detail panel: filter keys with their values and sources (JSON body keys included). */
export function recordFields(row: LogQueryRow): RecordField[] {
  const out: RecordField[] = [];
  for (const [key, get] of Object.entries(ROW_FIELDS)) {
    const value = get(row);
    if (value !== "" && !(key === "severity_number" && value === "0")) out.push({ key, value, source: "field" });
  }
  for (const [k, v] of Object.entries(row.attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))) out.push({ key: `attributes.${k}`, value: v, source: "attribute" });
  for (const [k, v] of Object.entries(row.resource_attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))) out.push({ key: `resource.${k}`, value: v, source: "resource" });
  for (const [k, v] of Object.entries(jsonBody(row.body) ?? {})) out.push({ key: `body.${k}`, value: typeof v === "string" ? v : JSON.stringify(v), source: "body" });
  return out;
}

/** The body parsed as a JSON object, or null. */
export function jsonBody(body: string): Record<string, unknown> | null {
  const s = body.trim();
  if (!s.startsWith("{")) return null;
  try {
    const v: unknown = JSON.parse(s);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

/** Filter for "filter in/out" on a value: numbers compare as numbers for numeric keys. */
export function valueFilter(key: string, value: string, exclude: boolean, type?: FieldType): QueryFilter {
  const numeric = (type === "number" || key === "severity_number") && value.trim() !== "" && Number.isFinite(Number(value));
  return { key, op: exclude ? "!=" : "=", value: numeric ? Number(value) : value };
}

// ---- legacy URL parameters (APM, trace and host links) -------------------------------------------------------------

const SEVERITY_NUMBERS: Record<string, number> = { TRACE: 1, DEBUG: 5, INFO: 9, WARN: 13, WARNING: 13, ERROR: 17, FATAL: 21 };

export function severityNumber(v: string): number | null {
  if (/^\d+$/.test(v)) {
    const n = Number(v);
    return n >= 1 && n <= 24 ? n : null;
  }
  return SEVERITY_NUMBERS[v.trim().toUpperCase()] ?? null;
}

export interface LegacyLogsParams {
  severity?: string;
  service?: string;
  host?: string;
  trace?: string;
  span?: string;
}

/** Pre-explorer parameters of the host, container and pod log tabs (`lq` stays the body search). */
export interface LegacyTabParams {
  severity?: string;
  /** openlog.log.source, log.file.path, openlog.discovery.id, openlog.systemd.unit (host tab) */
  source?: string;
  file?: string;
  discovery?: string;
  unit?: string;
  /** log.iostream (container tab) */
  stream?: string;
}

/** Conditions equivalent to the pre-explorer log tab parameters (the GET /api/v1/logs `attr.*` filters). */
export function legacyTabFilters(p: LegacyTabParams): QueryFilter[] {
  const out: QueryFilter[] = [];
  const sev = p.severity ? severityNumber(p.severity) : null;
  if (sev !== null) out.push({ key: "severity_number", op: ">=", value: sev });
  const attrs: [keyof LegacyTabParams, string][] = [
    ["source", "openlog.log.source"],
    ["file", "log.file.path"],
    ["discovery", "openlog.discovery.id"],
    ["unit", "openlog.systemd.unit"],
    ["stream", "log.iostream"],
  ];
  for (const [param, key] of attrs) {
    const v = p[param]?.trim();
    if (v) out.push({ key: `attributes.${key}`, op: "=", value: v });
  }
  return out;
}

/** Conditions equivalent to the pre-explorer /logs parameters. */
export function legacyFilters(p: LegacyLogsParams): QueryFilter[] {
  const out: QueryFilter[] = [];
  const sev = p.severity ? severityNumber(p.severity) : null;
  if (sev !== null) out.push({ key: "severity_number", op: ">=", value: sev });
  if (p.service) out.push({ key: "service.name", op: "=", value: p.service });
  if (p.host) out.push({ key: "host.id", op: "=", value: p.host });
  if (p.trace) out.push({ key: "trace_id", op: "=", value: p.trace.toLowerCase() });
  if (p.span) out.push({ key: "span_id", op: "=", value: p.span.toLowerCase() });
  return out;
}

// ---- log volume -----------------------------------------------------------------------------------------------------

const DURATION_UNITS: Record<string, number> = { ns: 1e-6, us: 1e-3, "µs": 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

/** Go duration text ("60s", "1m30s", "1h") in milliseconds, or null. */
export function parseGoDuration(s: string): number | null {
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
  let total = 0;
  let matched = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    total += Number(m[1]) * DURATION_UNITS[m[2]!]!;
    matched += m[0].length;
  }
  return matched === s.length && matched > 0 && total > 0 ? total : null;
}

export const SEVERITY_ORDER = ["TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"] as const;

// Same hues as lib/utils PALETTE / PALETTE_DARK (dark variants keep contrast on dark cards).
const SEVERITY_COLORS: Record<(typeof SEVERITY_ORDER)[number], [light: string, dark: string]> = {
  TRACE: ["#65a30d", "#a3e635"],
  DEBUG: ["#0891b2", "#22d3ee"],
  INFO: ["#2563eb", "#60a5fa"],
  WARN: ["#d97706", "#fbbf24"],
  ERROR: ["#dc2626", "#f87171"],
  FATAL: ["#7c3aed", "#a78bfa"],
};

/** Chart color of a severity label (`WARN`, `warning`, `ERROR2`); undefined for other labels. */
export function severityColor(label: string, theme: "light" | "dark"): string | undefined {
  const n = severityNumber(label.replace(/\d$/, ""));
  if (n === null) return undefined;
  const pair = SEVERITY_COLORS[SEVERITY_ORDER[Math.floor((n - 1) / 4)]!];
  return theme === "dark" ? pair[1] : pair[0];
}

/** Badge variant of a severity number (as components/LogTable). */
export function severityBadgeVariant(n: number): "destructive" | "warning" | "secondary" | "muted" {
  if (n >= 17) return "destructive";
  if (n >= 13) return "warning";
  if (n >= 9) return "secondary";
  return "muted";
}

/** Bucket limit of the zero-filled volume series (the API returns at most ~120 buckets by default). */
const MAX_BUCKETS = 2000;

/**
 * Chart series of POST /logs/aggregate with empty buckets filled with 0 (the API omits them) over [from, to], on the
 * bucket grid of the returned points.
 */
export function volumeSeries(res: Pick<LogsAggregateResponse, "step" | "series">, from: number, to: number, labelOf: (s: LogsAggregateResponse["series"][number]) => string): ChartSeriesInput[] {
  const step = parseGoDuration(res.step);
  const first = res.series.find((s) => s.points.length > 0)?.points[0]?.[0];
  if (!step || first === undefined) return res.series.map((s) => ({ label: labelOf(s), points: s.points }));
  const phase = ((first % step) + step) % step;
  const start = Math.floor((from - phase) / step) * step + phase;
  const grid: number[] = [];
  for (let t = start; t < to && grid.length < MAX_BUCKETS; t += step) grid.push(t);
  return res.series.map((s) => {
    const byT = new Map(s.points.map(([t, v]) => [t, v]));
    const extra = s.points.filter(([t]) => t < start || t >= to).map(([t]) => t);
    const ts = extra.length ? [...new Set([...grid, ...extra])].sort((a, b) => a - b) : grid;
    return { label: labelOf(s), points: ts.map((t): [number, number] => [t, byT.get(t) ?? 0]) };
  });
}

// ---- column widths and preferences (per viewer) --------------------------------------------------------------------

const STORAGE = { columns: "openlog.logsExplorer.columns", widths: "openlog.logsExplorer.widths", prefs: "openlog.logsExplorer.prefs" } as const;

function readJson(key: string): unknown {
  try {
    const raw = globalThis.localStorage?.getItem(key);
    return raw ? (JSON.parse(raw) as unknown) : null;
  } catch {
    return null;
  }
}

function writeJson(key: string, value: unknown): void {
  try {
    globalThis.localStorage?.setItem(key, JSON.stringify(value));
  } catch {
    // Storage unavailable (private mode, quota): the preference is simply not remembered.
  }
}

/** Last used column set (fallback when the URL has none); other explorers pass their own storage key and defaults. */
export const storedColumns = (key: string = STORAGE.columns, defaults: readonly string[] = DEFAULT_COLUMNS): string[] | null => {
  const v = readJson(key);
  return Array.isArray(v) ? normalizeColumns(v, defaults) : null;
};
export const storeColumns = (cols: readonly string[], key: string = STORAGE.columns) => writeJson(key, cols);

export const MIN_COLUMN_WIDTH = 64;
export const MAX_COLUMN_WIDTH = 1200;

export function storedWidths(key: string = STORAGE.widths): Record<string, number> {
  const v = readJson(key);
  if (!v || typeof v !== "object") return {};
  return Object.fromEntries(Object.entries(v as Record<string, unknown>).filter((e): e is [string, number] => typeof e[1] === "number" && e[1] >= MIN_COLUMN_WIDTH && e[1] <= MAX_COLUMN_WIDTH));
}
export const storeWidths = (w: Record<string, number>, key: string = STORAGE.widths) => writeJson(key, w);

export interface TablePrefs {
  density: "compact" | "comfortable";
  wrap: boolean;
}

export function storedPrefs(key: string = STORAGE.prefs): TablePrefs {
  const v = readJson(key) as Partial<TablePrefs> | null;
  return { density: v?.density === "comfortable" ? "comfortable" : "compact", wrap: v?.wrap === true };
}
export const storePrefs = (p: TablePrefs, key: string = STORAGE.prefs) => writeJson(key, p);

// ---- saved views ----------------------------------------------------------------------------------------------------

/** URL state of the explorer (router LogsSearch subset). */
export interface LogsExplorerSearch extends RangeSpec {
  q?: string;
  f?: CompactFilterState;
  cols?: string[];
  order?: "asc";
  gb?: string;
}

/** Saved view `state` (openapi SavedViewInput): the API filter shape plus table and range settings. */
export function viewStateFromSearch(s: LogsExplorerSearch, columns: readonly string[], groupBy: string): Record<string, unknown> {
  const filter = decodeFilterState(s.f);
  return {
    filters: filter.filters,
    groups: filter.groups,
    q: s.q ?? "",
    columns,
    order: s.order ?? "desc",
    group_by: groupBy,
    ...(s.from && s.to ? { from: s.from, to: s.to } : s.range ? { range: s.range } : {}),
  };
}

/** URL search of a saved view state (untrusted: validated like the URL). */
export function searchFromViewState(state: Record<string, unknown>): LogsExplorerSearch {
  const filter = decodeFilterState({ filters: state.filters, groups: state.groups });
  const cols = normalizeColumns(state.columns);
  const gb = typeof state.group_by === "string" && state.group_by.length <= 256 ? state.group_by : DEFAULT_GROUP_BY;
  return {
    ...validateRangeSearch(state),
    q: typeof state.q === "string" && state.q ? state.q.slice(0, 1024) : undefined,
    f: encodeFilterState(filter),
    cols: isDefaultColumns(cols) ? undefined : cols,
    order: state.order === "asc" ? "asc" : undefined,
    gb: gb === DEFAULT_GROUP_BY ? undefined : gb,
  };
}
