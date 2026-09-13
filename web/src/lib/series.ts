// Pure transforms from API metric series to uPlot's aligned data format.
import type { MetricSeries } from "@/api/types";

export interface ChartSeriesInput {
  /** Label shown in legend/tooltip. */
  label: string;
  points: [number, number][];
}

export interface AlignedData {
  /** x values in seconds (uPlot convention), ascending, unique. */
  xs: number[];
  /** raw values per series, null where the series has no point. */
  ys: (number | null)[][];
  /** values drawn (cumulative when stacked). */
  drawn: (number | null)[][];
  labels: string[];
}

/** Builds a series label from attributes (values joined; "" when none). */
export function seriesLabel(attributes: Record<string, string>, keys?: string[]): string {
  const ks = keys && keys.length > 0 ? keys : Object.keys(attributes).sort();
  return ks
    .map((k) => attributes[k])
    .filter((v): v is string => v !== undefined && v !== "")
    .join(" · ");
}

export function fromMetricSeries(series: MetricSeries[], opts: { keys?: string[]; fallbackLabel: string }): ChartSeriesInput[] {
  return series.map((s) => ({
    label: seriesLabel(s.attributes, opts.keys) || opts.fallbackLabel,
    points: s.points,
  }));
}

/**
 * Aligns series on the union of their timestamps (ms → s).
 * With `stacked`, drawn[i] is the cumulative sum of series 0..i, treating
 * missing values as 0 for the running total but keeping gaps (null) in the
 * series itself.
 * `order` optionally sorts series by label using the given preferred order
 * (e.g. cpu modes), unknown labels last alphabetically.
 */
export function alignSeries(input: ChartSeriesInput[], opts: { stacked?: boolean; order?: string[] } = {}): AlignedData {
  const sorted = [...input];
  if (opts.order) {
    const rank = (l: string) => {
      const i = opts.order!.indexOf(l);
      return i === -1 ? Number.MAX_SAFE_INTEGER : i;
    };
    sorted.sort((a, b) => rank(a.label) - rank(b.label) || a.label.localeCompare(b.label));
  }
  const xset = new Set<number>();
  for (const s of sorted) for (const [t] of s.points) xset.add(t);
  const xsMs = [...xset].sort((a, b) => a - b);
  const index = new Map<number, number>();
  xsMs.forEach((t, i) => index.set(t, i));

  const ys = sorted.map((s) => {
    const row: (number | null)[] = new Array(xsMs.length).fill(null);
    for (const [t, v] of s.points) {
      if (Number.isFinite(v)) row[index.get(t)!] = v;
    }
    return row;
  });

  let drawn = ys;
  if (opts.stacked) {
    const acc: number[] = new Array(xsMs.length).fill(0);
    drawn = ys.map((row) =>
      row.map((v, i) => {
        if (v === null) return null;
        acc[i]! += v;
        return acc[i]!;
      }),
    );
  }

  return { xs: xsMs.map((t) => t / 1000), ys, drawn, labels: sorted.map((s) => s.label) };
}

/** Initial visibility per label: labels in `hidden` (e.g. cpu "idle") start hidden. */
export function defaultVisibility(labels: string[], hidden: readonly string[] = []): boolean[] {
  return labels.map((l) => !hidden.includes(l));
}

/**
 * Cumulative stacking over visible series only. A hidden series is drawn at
 * the running total below it (zero height), so the band of the next visible
 * series still fills down to the right baseline.
 */
export function stackVisible(ys: (number | null)[][], visible: boolean[]): (number | null)[][] {
  const len = ys[0]?.length ?? 0;
  const acc: number[] = new Array(len).fill(0);
  return ys.map((row, s) => {
    const show = visible[s] !== false;
    return row.map((v, i) => {
      if (!show) return acc[i]!;
      if (v === null) return null;
      acc[i]! += v;
      return acc[i]!;
    });
  });
}

/** Largest drawn value among visible series (null when nothing is visible/has data). */
export function visibleMax(drawn: (number | null)[][], visible: boolean[]): number | null {
  let max: number | null = null;
  drawn.forEach((row, s) => {
    if (visible[s] === false) return;
    for (const v of row) if (v !== null && (max === null || v > max)) max = v;
  });
  return max;
}

/**
 * y scale range. `yMax` is a fixed minimum top (e.g. 100% for filesystems);
 * `yCap` auto-scales to the visible data (+10% headroom, rounded up to a 5%
 * step of the cap) but never beyond the cap (e.g. CPU without idle).
 */
export function yRange(dataMin: number | null, dataMax: number | null, opts: { yMax?: number; yCap?: number } = {}): [number, number] {
  const lo = Math.min(0, dataMin ?? 0);
  if (opts.yMax !== undefined) return [lo, Math.max(opts.yMax, dataMax ?? 0)];
  if (dataMax === null || dataMax <= 0) return [lo, opts.yCap ?? 1];
  let hi = dataMax * 1.1;
  if (opts.yCap !== undefined) {
    const step = opts.yCap / 20;
    hi = Math.min(opts.yCap, Math.ceil(hi / step) * step);
    hi = Math.max(hi, dataMax);
  }
  return [lo, hi];
}

/** Last non-null value of a row, or null. */
export function lastValue(row: (number | null)[] | undefined): number | null {
  if (!row) return null;
  for (let i = row.length - 1; i >= 0; i--) if (row[i] !== null && row[i] !== undefined) return row[i]!;
  return null;
}

/**
 * Legend values: the hovered index's raw values, or each series' latest value
 * when not hovering. `time` is the hovered x (s) or the last x.
 */
export function legendValues(data: Pick<AlignedData, "xs" | "ys">, idx: number | null | undefined): { time: number | null; values: (number | null)[] } {
  if (idx === null || idx === undefined || idx < 0 || idx >= data.xs.length) {
    return { time: data.xs.length > 0 ? data.xs[data.xs.length - 1]! : null, values: data.ys.map(lastValue) };
  }
  return { time: data.xs[idx]!, values: data.ys.map((row) => row[idx] ?? null) };
}

/**
 * Start of the data when it begins well after the selected range start
 * (more than 10% of the range and at least 2 minutes); otherwise null.
 */
export function dataStartHint(firstMs: number | undefined, from: number | undefined, to: number | undefined): number | null {
  if (firstMs === undefined || from === undefined || to === undefined || to <= from) return null;
  const gap = firstMs - from;
  return gap > Math.max((to - from) * 0.1, 120_000) ? firstMs : null;
}

/** Band definitions for stacked areas: series i fills down to series i-1. */
export function stackBands(count: number): { series: [number, number] }[] {
  const bands: { series: [number, number] }[] = [];
  // uPlot series indices are 1-based (0 is x). Higher series fills to the one below.
  for (let i = count; i > 1; i--) bands.push({ series: [i, i - 1] });
  return bands;
}
