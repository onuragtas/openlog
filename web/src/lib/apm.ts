// Pure helpers for the APM screens (docs/contracts/apm.md): formatting, Apdex levels, chart series,
// histogram shaping and stack trace frames. Rendering-independent and unit-tested.
import type { ChartSeriesInput } from "@/lib/series";

export interface RedLike {
  requests: number;
  throughput: number;
  errors: number;
  error_rate: number;
  avg_ms: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
  p99_ms: number | null;
  apdex: number | null;
}

export interface PointLike extends RedLike {
  t: number;
}

export interface HistogramBin {
  from_ms: number;
  to_ms: number;
  count: number;
}

/** Milliseconds as "0.42 ms", "123 ms", "1.24 s", "2.5 min". */
export function formatMs(ms: number | null | undefined, locale?: string): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return "–";
  const nf = (v: number, digits: number) => new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(v);
  if (ms < 1) return `${nf(ms, 2)} ms`;
  if (ms < 1000) return `${nf(ms, ms < 10 ? 1 : 0)} ms`;
  if (ms < 60_000) return `${nf(ms / 1000, 2)} s`;
  return `${nf(ms / 60_000, 1)} min`;
}

/** Requests per minute. */
export function formatRpm(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: v < 10 ? 2 : 1 }).format(v)} rpm`;
}

/** A 0..1 rate as a percentage with more precision below 1%. */
export function formatRate(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  const pct = v * 100;
  const digits = pct === 0 ? 0 : pct < 1 ? 2 : 1;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: digits, minimumFractionDigits: pct > 0 && pct < 1 ? 2 : 0 }).format(pct)}%`;
}

export function formatApdex(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return new Intl.NumberFormat(locale, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(v);
}

export type ApdexLevel = "excellent" | "good" | "fair" | "poor" | "unacceptable" | "none";

/** Apdex rating bands (apdex.org): ≥0.94 excellent, ≥0.85 good, ≥0.70 fair, ≥0.50 poor. */
export function apdexLevel(v: number | null | undefined): ApdexLevel {
  if (v === null || v === undefined || !Number.isFinite(v)) return "none";
  if (v >= 0.94) return "excellent";
  if (v >= 0.85) return "good";
  if (v >= 0.7) return "fair";
  if (v >= 0.5) return "poor";
  return "unacceptable";
}

export function apdexBadgeVariant(level: ApdexLevel): "success" | "warning" | "destructive" | "muted" {
  switch (level) {
    case "excellent":
    case "good":
      return "success";
    case "fair":
    case "poor":
      return "warning";
    case "unacceptable":
      return "destructive";
    default:
      return "muted";
  }
}

export type RedMetric = "throughput" | "error_rate" | "apdex" | "avg_ms" | "p50_ms" | "p95_ms" | "p99_ms" | "requests" | "errors";

/** [ms, value] points of one metric; buckets where the metric is null are skipped. */
export function metricPoints(points: PointLike[], metric: RedMetric): [number, number][] {
  const out: [number, number][] = [];
  for (const p of points) {
    const v = p[metric];
    if (v !== null && v !== undefined && Number.isFinite(v)) out.push([p.t, v]);
  }
  return out;
}

export function latencySeries(points: PointLike[]): ChartSeriesInput[] {
  return [
    { label: "p50", points: metricPoints(points, "p50_ms") },
    { label: "p95", points: metricPoints(points, "p95_ms") },
    { label: "p99", points: metricPoints(points, "p99_ms") },
  ];
}

const BUCKETS_PER_OCTAVE = 8;

/** Histogram bucket index of a bin (apm.md §4.1: upper bound 2^(b/8) ms). */
export function bucketIndex(bin: HistogramBin): number {
  return Math.round(BUCKETS_PER_OCTAVE * Math.log2(bin.to_ms));
}

/**
 * Fills empty buckets between the first and last non-empty bin and merges adjacent buckets
 * so that at most maxBins bars remain (log-scale latency distribution).
 */
export function shapeHistogram(bins: HistogramBin[], maxBins = 32): HistogramBin[] {
  if (bins.length === 0) return [];
  const byIndex = new Map<number, number>();
  for (const b of bins) byIndex.set(bucketIndex(b), (byIndex.get(bucketIndex(b)) ?? 0) + b.count);
  const indexes = [...byIndex.keys()].sort((a, b) => a - b);
  const first = indexes[0]!;
  const last = indexes[indexes.length - 1]!;
  const n = last - first + 1;
  const group = Math.max(1, Math.ceil(n / maxBins));
  const out: HistogramBin[] = [];
  for (let start = first; start <= last; start += group) {
    const end = Math.min(last, start + group - 1);
    let count = 0;
    for (let i = start; i <= end; i++) count += byIndex.get(i) ?? 0;
    out.push({ from_ms: start <= -80 ? 0 : 2 ** ((start - 1) / BUCKETS_PER_OCTAVE), to_ms: 2 ** (end / BUCKETS_PER_OCTAVE), count });
  }
  return out;
}

export interface StackFrameLine {
  text: string;
  /** Application code (not runtime or library frames). */
  inApp: boolean;
  /** A frame line (as opposed to a message or header line). */
  frame: boolean;
}

const LIBRARY_PARTS = ["/vendor/", "node_modules", "/usr/local/go/", "/usr/lib/go", "/go/pkg/mod/", "node:", "/usr/share/php", "site-packages", "/usr/lib/python"];
const FRAME_RE = /^(\s+at |#\d+ |\t\/|\tat |\s*File ")/;

/** Splits a stack trace into lines and marks application frames (same rules as apm.md §3.2, simplified). */
export function stackLines(stack: string): StackFrameLine[] {
  if (!stack) return [];
  return stack
    .replace(/\r\n/g, "\n")
    .split("\n")
    .filter((l, i, all) => l !== "" || i < all.length - 1)
    .map((text) => {
      const frame = FRAME_RE.test(text) || /^[\w./*()[\]-]+\(.*\)$/.test(text.trim());
      const inApp = frame && !LIBRARY_PARTS.some((p) => text.includes(p)) && !/^\s*(runtime[./]|net\/http\.|panic)/.test(text.trim());
      return { text, inApp, frame };
    });
}

/** Service identity from URL search values: omitted namespace/environment mean "all". */
export interface ServiceScope {
  service: string;
  namespace?: string;
  environment?: string;
}

/** Map node id of a service (internal/api/apm.go serviceNodeID). */
export function serviceNodeId(name: string, namespace = "", environment = ""): string {
  return `service:${name}|${namespace}|${environment}`;
}

/** Parses a service node id back into its identity (null for dependency nodes). */
export function parseServiceNodeId(id: string): { name: string; namespace: string; environment: string } | null {
  if (!id.startsWith("service:")) return null;
  const [name = "", namespace = "", environment = ""] = id.slice("service:".length).split("|");
  return { name, namespace, environment };
}

/** Parses "key=value" lines/pairs of the trace search attribute filter (empty or malformed parts dropped). */
export function parseAttrFilter(v: string | undefined): [string, string][] {
  if (!v) return [];
  return v
    .split(/[\n,]/)
    .map((part) => part.trim())
    .filter(Boolean)
    .flatMap((part): [string, string][] => {
      const eq = part.indexOf("=");
      if (eq <= 0 || eq === part.length - 1) return [];
      return [[part.slice(0, eq).trim(), part.slice(eq + 1).trim()]];
    })
    .slice(0, 10);
}
