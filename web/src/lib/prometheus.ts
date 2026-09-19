// Prometheus/OpenMetrics targets scraped by infra agents (semantic-conventions §6.9, D-137). A target has no inventory
// item of its own: every scrape sends the health gauge `up` (1/0) on the target's resource, so the latest `up` point of
// each series is the target list.
import type { QueryFilter } from "@/api/explorer";
import type { components } from "@/api/schema.gen";

type MetricSeries = components["schemas"]["MetricSeries"];

/** Keys the `up` series are grouped by (metrics explorer keys; at most 5). */
export const PROM_GROUP_BY = ["host.id", "host.name", "service.name", "resource.service.instance.id", "resource.openlog.scrape.source"] as const;

/** Only series of scraped targets (another sender may also report a metric called `up`). */
export const PROM_FILTERS: QueryFilter[] = [{ key: "resource.openlog.integration.id", op: "=", value: "prometheus" }];

export type ScrapeSource = "static" | "container" | "pod";

export interface PrometheusTarget {
  hostId: string;
  hostName: string;
  job: string;
  instance: string;
  source: ScrapeSource | "";
  up: boolean;
  /** Time of the latest `up` point (ms). */
  lastSeen: number;
}

/**
 * Targets from `up` series (aggregation `last`): the latest non-null point of each series decides up/down. Down targets
 * come first, then by job and instance, so a broken exporter is the first thing on the card.
 */
export function prometheusTargets(series: readonly MetricSeries[]): PrometheusTarget[] {
  const out: PrometheusTarget[] = [];
  for (const s of series) {
    let last: [number, number] | undefined;
    for (const p of s.points) {
      if (p[1] !== null && Number.isFinite(p[1]) && (!last || p[0] >= last[0])) last = [p[0], p[1]];
    }
    if (!last) continue;
    const a = s.attributes;
    const src = a["resource.openlog.scrape.source"] ?? "";
    out.push({
      hostId: a["host.id"] ?? "",
      hostName: a["host.name"] || a["host.id"] || "",
      job: a["service.name"] ?? "",
      instance: a["resource.service.instance.id"] ?? "",
      source: src === "static" || src === "container" || src === "pod" ? src : "",
      up: last[1] >= 1,
      lastSeen: last[0],
    });
  }
  return out.sort((x, y) => Number(x.up) - Number(y.up) || x.job.localeCompare(y.job) || x.instance.localeCompare(y.instance) || x.hostName.localeCompare(y.hostName));
}

/** Metrics explorer search of one target's `up` series (link from the card). */
export function targetExplorerFilters(t: PrometheusTarget): QueryFilter[] {
  const f: QueryFilter[] = [...PROM_FILTERS, { key: "service.name", op: "=", value: t.job }];
  if (t.instance) f.push({ key: "resource.service.instance.id", op: "=", value: t.instance });
  if (t.hostId) f.push({ key: "host.id", op: "=", value: t.hostId });
  return f;
}
