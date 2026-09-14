// Pure conversions from an OQL result (api.md "Query language (OQL)") to what the visualizations draw: chart series,
// summary rows for tables/billboards/bar lists, pie slices, heatmap grids and threshold states. Unit-tested in
// oql-result.test.ts; components/oql/QueryResult.tsx renders them.
import type { DashboardThreshold, DashboardUnit, DashboardVisualization } from "@/api/dashboards";
import type { OqlColumn, OqlResult, OqlSeries } from "@/api/oql";
import type { UnitKind } from "@/lib/format";
import type { ChartSeriesInput } from "@/lib/series";

export type CellValue = number | string | null;

export function unitKind(unit: DashboardUnit | undefined): UnitKind {
  return unit ? unit : "number";
}

const facetKey = (facets: readonly string[]) => JSON.stringify(facets);

/** Facet values joined, plus the column name when the query has several columns (or no facets). */
export function seriesLabel(columns: readonly OqlColumn[], facets: readonly string[], column: number): string {
  const f = facets.join(" · ");
  const col = columns[column]?.name ?? "";
  if (facets.length === 0 || f === "") return col || f;
  return columns.length > 1 ? `${f} · ${col}` : f;
}

export interface ChartData {
  series: ChartSeriesInput[];
  /** Labels drawn dashed (COMPARE WITH series). */
  dashed: string[];
}

function uniqueLabel(used: Map<string, number>, label: string): string {
  const n = used.get(label) ?? 0;
  used.set(label, n + 1);
  return n > 0 ? `${label} (${n + 1})` : label;
}

function toInput(columns: readonly OqlColumn[], s: OqlSeries, used: Map<string, number>, suffix = ""): ChartSeriesInput {
  // Null points stay gaps: alignSeries keeps only finite values.
  return { label: uniqueLabel(used, seriesLabel(columns, s.facets, s.column) + suffix), points: s.points as unknown as [number, number][] };
}

/** Timeseries → chart series; COMPARE WITH series are labelled "<label> (<previousLabel>)" and drawn dashed. */
export function toChartData(result: OqlResult, previousLabel: string): ChartData {
  const used = new Map<string, number>();
  const series = result.series.map((s) => toInput(result.columns, s, used));
  const dashed: string[] = [];
  for (const s of result.compare?.series ?? []) {
    const input = toInput(result.columns, s, used, ` (${previousLabel})`);
    dashed.push(input.label);
    series.push(input);
  }
  return { series, dashed };
}

/** Histogram buckets → one bar series over synthetic x positions is not meaningful; use barItems instead. */

export interface SummaryRow {
  facets: string[];
  values: CellValue[];
  /** Values of the COMPARE WITH range (same columns), or null without compare. */
  previous: CellValue[] | null;
}

const SUMMABLE = new Set(["count", "sum", "rate", "uniquecount"]);

function lastValue(points: OqlSeries["points"]): number | null {
  for (let i = points.length - 1; i >= 0; i--) {
    const v = points[i]![1];
    if (v !== null && Number.isFinite(v)) return v;
  }
  return null;
}

function totalValue(points: OqlSeries["points"]): number | null {
  let sum = 0;
  let any = false;
  for (const [, v] of points) {
    if (v !== null && Number.isFinite(v)) {
      sum += v;
      any = true;
    }
  }
  return any ? sum : null;
}

/** Collapses timeseries to one value per series: the total for count/sum/rate/uniqueCount, else the last value. */
function collapse(columns: readonly OqlColumn[], series: readonly OqlSeries[]): Map<string, { facets: string[]; values: CellValue[] }> {
  const groups = new Map<string, { facets: string[]; values: CellValue[] }>();
  for (const s of series) {
    const key = facetKey(s.facets);
    const g = groups.get(key) ?? { facets: s.facets, values: columns.map(() => null) };
    groups.set(key, g);
    const fn = (columns[s.column]?.function ?? "").toLowerCase();
    g.values[s.column] = SUMMABLE.has(fn) ? totalValue(s.points) : lastValue(s.points);
  }
  return groups;
}

/** Columns of summary rows (histograms have one synthetic "count" column). */
export function summaryColumns(result: OqlResult): OqlColumn[] {
  return result.kind === "histogram" ? [{ name: "count", function: "count", type: "number" }] : result.columns;
}

export function bucketLabel(from: number, to: number): string {
  const f = (n: number) => String(Math.round(n * 1000) / 1000);
  return `${f(from)}–${f(to)}`;
}

/** One row per group (single, facets), per series group (timeseries) or per bucket (histogram). */
export function summarize(result: OqlResult): SummaryRow[] {
  const hasCompare = result.compare !== null && result.compare !== undefined;
  switch (result.kind) {
    case "histogram":
      return result.buckets.map((b, i) => ({
        facets: [bucketLabel(b.from, b.to)],
        values: [b.count],
        previous: hasCompare ? [result.compare!.buckets[i]?.count ?? null] : null,
      }));
    case "timeseries": {
      const cur = collapse(result.columns, result.series);
      const prev = hasCompare ? collapse(result.columns, result.compare!.series) : null;
      return [...cur.entries()].map(([key, g]) => ({ facets: g.facets, values: g.values, previous: prev ? (prev.get(key)?.values ?? result.columns.map(() => null)) : null }));
    }
    default: {
      const prev = hasCompare ? new Map(result.compare!.rows.map((r) => [facetKey(r.facets), r.values])) : null;
      return result.rows.map((r) => ({ facets: r.facets, values: r.values, previous: prev ? (prev.get(facetKey(r.facets)) ?? result.columns.map(() => null)) : null }));
    }
  }
}

/** Change in percent from `previous` to `current`; null when not computable. */
export function deltaPercent(current: CellValue | undefined, previous: CellValue | undefined): number | null {
  if (typeof current !== "number" || typeof previous !== "number" || !Number.isFinite(current) || !Number.isFinite(previous) || previous === 0) return null;
  return ((current - previous) / Math.abs(previous)) * 100;
}

export type ThresholdState = "critical" | "warning" | null;

/**
 * Threshold state of a value. Thresholds breach at value >= threshold; when the critical threshold is below the
 * warning threshold, lower values are worse and they breach at value <= threshold.
 */
export function thresholdState(value: CellValue | undefined, thresholds: readonly DashboardThreshold[] | undefined): ThresholdState {
  if (typeof value !== "number" || !Number.isFinite(value) || !thresholds?.length) return null;
  const crit = thresholds.filter((t) => t.severity === "critical").map((t) => t.value);
  const warn = thresholds.filter((t) => t.severity === "warning").map((t) => t.value);
  const descending = crit.length > 0 && warn.length > 0 && Math.min(...crit) < Math.min(...warn);
  const hit = (vals: number[]) => (descending ? vals.some((t) => value <= t) : vals.some((t) => value >= t));
  if (hit(crit)) return "critical";
  if (hit(warn)) return "warning";
  return null;
}

export function firstNumberColumn(columns: readonly OqlColumn[]): number {
  const i = columns.findIndex((c) => c.type === "number");
  return i < 0 ? 0 : i;
}

export interface BarItem {
  label: string;
  value: number;
  previous: number | null;
}

/** Horizontal bar list items: one per group (first number column), per column (single) or per bucket (histogram). */
export function barItems(result: OqlResult): BarItem[] {
  const rows = summarize(result);
  const num = (v: CellValue | undefined) => (typeof v === "number" && Number.isFinite(v) ? v : null);
  if (result.kind === "single") {
    const row = rows[0];
    if (!row) return [];
    return result.columns.flatMap((c, i) => {
      const v = num(row.values[i]);
      return v === null ? [] : [{ label: c.name, value: v, previous: num(row.previous?.[i]) }];
    });
  }
  const col = result.kind === "histogram" ? 0 : firstNumberColumn(result.columns);
  return rows.flatMap((r) => {
    const v = num(r.values[col]);
    return v === null ? [] : [{ label: r.facets.join(" · "), value: v, previous: num(r.previous?.[col]) }];
  });
}

export interface PieSlice {
  label: string;
  value: number;
  fraction: number;
}

/** Pie slices of positive values (at most `max`; the rest is merged into "other"). */
export function pieSlices(result: OqlResult, otherLabel: string, max = 8): PieSlice[] {
  const items = barItems(result).filter((b) => b.value > 0);
  items.sort((a, b) => b.value - a.value);
  const shown = items.slice(0, max);
  const rest = items.slice(max).reduce((s, b) => s + b.value, 0);
  const list = rest > 0 ? [...shown, { label: otherLabel, value: rest, previous: null }] : shown;
  const total = list.reduce((s, b) => s + b.value, 0);
  return total > 0 ? list.map((b) => ({ label: b.label, value: b.value, fraction: b.value / total })) : [];
}

export interface HeatmapModel {
  rows: { label: string; cells: (number | null)[] }[];
  /** Bucket start times (ms) for timeseries, or bucket ranges for histograms. */
  columns: { key: string; time?: number; label?: string }[];
  max: number;
}

/** Facets × buckets grid of the first column (timeseries), or one strip of bucket counts (histogram). */
export function heatmapModel(result: OqlResult): HeatmapModel | null {
  let max = 0;
  const track = (v: number | null) => {
    if (v !== null && Number.isFinite(v) && v > max) max = v;
    return v;
  };
  if (result.kind === "histogram") {
    return {
      rows: [{ label: "count", cells: result.buckets.map((b) => track(b.count)) }],
      columns: result.buckets.map((b) => ({ key: `${b.from}`, label: bucketLabel(b.from, b.to) })),
      max,
    };
  }
  if (result.kind !== "timeseries") return null;
  const col = firstNumberColumn(result.columns);
  const series = result.series.filter((s) => s.column === col);
  const times = [...new Set(series.flatMap((s) => s.points.map((p) => p[0])))].sort((a, b) => a - b);
  const index = new Map(times.map((t, i) => [t, i]));
  const rows = series.map((s) => {
    const cells: (number | null)[] = times.map(() => null);
    for (const [t, v] of s.points) cells[index.get(t)!] = track(v);
    return { label: s.facets.join(" · ") || (result.columns[col]?.name ?? ""), cells };
  });
  return { rows, columns: times.map((t) => ({ key: String(t), time: t })), max };
}

/** Visualization for the console: chart view picks by result kind, table view is always a table. */
export function consoleVisualization(kind: OqlResult["kind"], view: "chart" | "table"): DashboardVisualization {
  if (view === "table") return "table";
  switch (kind) {
    case "timeseries":
      return "line";
    case "single":
      return "billboard";
    default:
      return "bar";
  }
}
