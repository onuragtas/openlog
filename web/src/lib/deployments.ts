// Deployment markers and before/after comparison helpers (GET …/deployments, …/deployments/compare). Pure.

export interface ChartMarker {
  /** unix ms */
  t: number;
  label: string;
}

/** "1.4.3" → "v1.4.3" (versions that already start with v are kept). */
export function versionLabel(version: string): string {
  if (!version) return "–";
  return /^v\d/i.test(version) ? version : `v${version}`;
}

/** Chart markers of the deployments inside [from, to] (all when the window is unknown), oldest first. */
export function deploymentMarkers(deployments: { t: number; version: string }[] | undefined, from?: number, to?: number): ChartMarker[] {
  return (deployments ?? [])
    .filter((d) => Number.isFinite(d.t) && (from === undefined || d.t >= from) && (to === undefined || d.t <= to))
    .sort((a, b) => a.t - b.t)
    .map((d) => ({ t: d.t, label: versionLabel(d.version) }));
}

export const COMPARE_WINDOWS = ["15m", "30m", "1h", "3h"] as const;
export type CompareWindow = (typeof COMPARE_WINDOWS)[number];

export type CompareMetric = "throughput" | "error_rate" | "avg_ms" | "p95_ms" | "p99_ms" | "apdex";
export const COMPARE_METRICS: CompareMetric[] = ["throughput", "error_rate", "avg_ms", "p95_ms", "p99_ms", "apdex"];

export interface CompareDelta {
  /** after − before (null when either is missing) */
  delta: number | null;
  /** relative change (null when before is 0 or missing) */
  ratio: number | null;
  direction: "up" | "down" | "flat";
  /** good/bad for the service; throughput changes are neutral */
  verdict: "good" | "bad" | "neutral";
}

/** Relative changes below this count as flat. */
const FLAT = 0.02;

export function compareDelta(metric: CompareMetric, before: number | null | undefined, after: number | null | undefined): CompareDelta {
  if (before === null || before === undefined || after === null || after === undefined || !Number.isFinite(before) || !Number.isFinite(after)) {
    return { delta: null, ratio: null, direction: "flat", verdict: "neutral" };
  }
  const delta = after - before;
  const ratio = before !== 0 ? delta / Math.abs(before) : null;
  // Absolute tolerance for rates and Apdex (0..1), relative for the rest.
  const small = metric === "error_rate" || metric === "apdex" ? Math.abs(delta) < 0.001 : ratio !== null ? Math.abs(ratio) < FLAT : delta === 0;
  if (delta === 0 || small) return { delta, ratio, direction: "flat", verdict: "neutral" };
  const direction = delta > 0 ? "up" : "down";
  if (metric === "throughput") return { delta, ratio, direction, verdict: "neutral" };
  const higherIsBetter = metric === "apdex";
  return { delta, ratio, direction, verdict: (direction === "up") === higherIsBetter ? "good" : "bad" };
}
