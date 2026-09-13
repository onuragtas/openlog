// Evaluation history (GET /alerts/rules/{id}/evaluations, alerting.md §3.6) → chart data for EvaluationHistory.
import type { AlertRuleEvaluations } from "@/api/alerts";
import { labelsText, type PreviewBand } from "@/lib/alerts";
import type { ChartSeriesInput } from "@/lib/series";

export const HISTORY_MAX_SERIES = 10;

const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

export interface HistoryChartData {
  series: ChartSeriesInput[];
  bands: PreviewBand[];
  evaluations: number;
  errors: number;
  lastDurationMs: number | null;
  hidden: number;
}

/** Series values (≤ maxSeries, null points dropped) and shaded bands over consecutive buckets with firing series. */
export function historyChartData(h: AlertRuleEvaluations, fallbackLabel: string, maxSeries = HISTORY_MAX_SERIES): HistoryChartData {
  const step = h.step_seconds * 1000;
  const bands: PreviewBand[] = [];
  for (const e of h.evaluations) {
    if (!e.firing_series) continue;
    const at = parse(e.at);
    const last = bands[bands.length - 1];
    if (last && at - step <= last.to + step) last.to = at;
    else bands.push({ from: at - step, to: at });
  }
  const series: ChartSeriesInput[] = h.series.slice(0, maxSeries).map((s) => ({
    label: labelsText(s.labels) || s.series_key || fallbackLabel,
    points: s.points.filter((p) => p[1] !== null).map((p) => [p[0], p[1] as number] as [number, number]),
  }));
  let evaluations = 0;
  let errors = 0;
  for (const e of h.evaluations) {
    evaluations += e.evaluations;
    errors += e.errors;
  }
  const last = h.evaluations[h.evaluations.length - 1];
  return { series, bands, evaluations, errors, lastDurationMs: last?.duration_ms ?? null, hidden: Math.max(0, h.series.length - maxSeries) };
}
