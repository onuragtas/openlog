// Pure logic for integration panels (semantic-conventions §6): instance identity and resource filters,
// point arithmetic (ratios, differences), PostgreSQL cache hit ratio, top-N tables and overview summaries
// (recommended alerts are server templates, components/alerts/TemplateGallery). Unit-tested in integrations.test.ts.
import type { DiscoveredService, MetricSeries } from "@/api/types";
import type { RuleEditorSearch } from "@/lib/alerts";

export const INTEGRATION_IDS = ["nginx", "redis", "mysql", "postgresql"] as const;
export type IntegrationId = (typeof INTEGRATION_IDS)[number];

export const INTEGRATION_STATUSES = ["enabled", "needs_configuration", "error", "not_available"] as const;
export type IntegrationStatus = (typeof INTEGRATION_STATUSES)[number];

export type Points = [number, number][];

export function isIntegrationId(v: unknown): v is IntegrationId {
  return (INTEGRATION_IDS as readonly string[]).includes(String(v));
}

// ---- instance identity ----

/** An integration instance: one discovered service on one host (join of §6.1). */
export interface InstanceRef {
  hostId: string;
  /** `openlog.discovery.id` = discovered_service rule_id */
  discoveryId: string;
  /** `openlog.discovery.instance` = discovered_service instance */
  instance: string;
}

/** Inventory key of a discovered service: `<rule_id>:<instance>` (§3.4). */
export function serviceKey(discoveryId: string, instance: string): string {
  return `${discoveryId}:${instance}`;
}

/** Splits `<rule_id>:<instance>` at the first colon (rule ids contain none; instances may). */
export function splitServiceKey(key: string): { discoveryId: string; instance: string } | null {
  const i = key.indexOf(":");
  if (i <= 0 || i === key.length - 1) return null;
  return { discoveryId: key.slice(0, i), instance: key.slice(i + 1) };
}

/**
 * Display name of an instance: the process name (`command`, e.g. `redis-server`) is what operators recognise,
 * while `instance` is the resolved executable (`/usr/bin/redis-check-rdb` on Debian) or a container id. The
 * command is primary and the instance secondary; without a command the instance is the only name. The secondary
 * path is `displayInstance` (the invoked path, `/usr/bin/redis-server`) when the agent sent one.
 */
export function instanceLabel(s: { command?: string; instance?: string; displayInstance?: string }): { primary: string; secondary?: string } {
  const command = s.command?.trim() ?? "";
  const instance = s.displayInstance?.trim() || (s.instance?.trim() ?? "");
  if (!command) return { primary: instance };
  return { primary: command, secondary: instance && instance !== command ? instance : undefined };
}

/** Resource attribute filters selecting one instance's metrics (`resource.<key>` on /hosts/{id}/metrics). */
export function instanceResourceFilter(ref: Pick<InstanceRef, "discoveryId" | "instance">): Record<string, string> {
  return { "openlog.discovery.id": ref.discoveryId, "openlog.discovery.instance": ref.instance };
}

export interface IntegrationState {
  id?: string;
  status: IntegrationStatus;
  error?: string;
  hint?: string;
  endpoint?: string;
}

/** The integration object of a discovered service with a normalized status (unknown → not_available). */
export function integrationOf(s: DiscoveredService | undefined): IntegrationState {
  const raw = (s?.integration ?? {}) as { id?: string; status?: string; error?: string; hint?: string; endpoint?: string };
  const status = (INTEGRATION_STATUSES as readonly string[]).includes(raw.status ?? "") ? (raw.status as IntegrationStatus) : "not_available";
  return { id: raw.id || undefined, status, error: raw.error || undefined, hint: raw.hint || undefined, endpoint: raw.endpoint || undefined };
}

/** Integration of a discovery rule when the service is not in the snapshot (rule `mariadb` uses integration `mysql`). */
export function integrationForRule(ruleId: string): IntegrationId | undefined {
  if (ruleId === "mariadb") return "mysql";
  return isIntegrationId(ruleId) ? ruleId : undefined;
}

/** Whether a service has a panel: a supported integration id (or docker, whose panel shows engine reachability) that is not unavailable. */
export function hasPanel(s: DiscoveredService | undefined): boolean {
  const i = integrationOf(s);
  return (isIntegrationId(i.id) || i.id === "docker") && i.status !== "not_available";
}

/** Whether the panel should show configuration help instead of charts. */
export function needsAttention(status: IntegrationStatus): boolean {
  return status === "needs_configuration" || status === "error";
}

// ---- point arithmetic ----

/** Joins two point lists on equal timestamps and applies fn; null results are dropped. */
export function combinePoints(a: Points, b: Points, fn: (x: number, y: number) => number | null): Points {
  const byTs = new Map<number, number>();
  for (const [t, v] of b) byTs.set(t, v);
  const out: Points = [];
  for (const [t, x] of a) {
    const y = byTs.get(t);
    if (y === undefined) continue;
    const v = fn(x, y);
    if (v !== null && Number.isFinite(v)) out.push([t, v]);
  }
  return out;
}

/** hit / (hit + miss) per timestamp; buckets without activity have no value. */
export function hitRatio(hits: Points, misses: Points): Points {
  return combinePoints(hits, misses, (h, m) => (h + m > 0 ? h / (h + m) : null));
}

/** a − b per timestamp, clamped at 0 (e.g. accepted − handled connections, primary − replica offset). */
export function differencePoints(a: Points, b: Points): Points {
  return combinePoints(a, b, (x, y) => Math.max(0, x - y));
}

/** Sums the points of all series (optionally only those matching `pick`) per timestamp, ascending. */
export function sumSeries(series: MetricSeries[], pick: (attributes: Record<string, string>) => boolean = () => true): Points {
  const acc = new Map<number, number>();
  for (const s of series) {
    if (!pick(s.attributes)) continue;
    for (const [t, v] of s.points) if (Number.isFinite(v)) acc.set(t, (acc.get(t) ?? 0) + v);
  }
  return [...acc.entries()].sort((x, y) => x[0] - y[0]);
}

/** Series whose attribute `key` is one of `values`. */
export function pickSeries(series: MetricSeries[], key: string, values: readonly string[]): MetricSeries[] {
  return series.filter((s) => values.includes(s.attributes[key] ?? ""));
}

/**
 * PostgreSQL buffer cache hit ratio from `postgresql.blocks_read` rates grouped by `source`
 * (heap/idx/toast/tidx × hit/read): Σ*_hit / (Σ*_hit + Σ*_read).
 */
export function pgCacheHitRatio(blocks: MetricSeries[]): Points {
  const hits = sumSeries(blocks, (a) => (a.source ?? "").endsWith("_hit"));
  const reads = sumSeries(blocks, (a) => (a.source ?? "").endsWith("_read"));
  return hitRatio(hits, reads);
}

/** Last point value, or null. */
export function lastValue(points: Points | undefined): number | null {
  if (!points || points.length === 0) return null;
  return points[points.length - 1]![1];
}

/** Latest value across series (max of each series' last value), or null. */
export function latestMax(series: MetricSeries[] | undefined): number | null {
  let out: number | null = null;
  for (const s of series ?? []) {
    const v = lastValue(s.points);
    if (v !== null && Number.isFinite(v) && (out === null || v > out)) out = v;
  }
  return out;
}

export interface RankedRow {
  labels: Record<string, string>;
  value: number;
}

/** Top n series by their latest value, descending (e.g. largest PostgreSQL tables). */
export function topByLast(series: MetricSeries[], n: number): RankedRow[] {
  return series
    .map((s) => ({ labels: s.attributes, value: lastValue(s.points) }))
    .filter((r): r is RankedRow => r.value !== null && Number.isFinite(r.value))
    .sort((a, b) => b.value - a.value || JSON.stringify(a.labels).localeCompare(JSON.stringify(b.labels)))
    .slice(0, n);
}

// ---- overview ----

export interface IntegrationRow {
  hostId: string;
  hostName: string;
  key: string;
  discoveryId: string;
  instance: string;
  /** Process name (argv0 basename); shown instead of `instance` when present, see instanceLabel. */
  command?: string;
  /** Invoked path of a multi-call executable; shown instead of `instance` as the path (display only). */
  displayInstance?: string;
  name: string;
  integration: IntegrationState;
  panel: boolean;
}

export type StatusCounts = Record<IntegrationStatus, number>;

interface ServiceItem {
  host_id: string;
  host_name?: string;
  key: string;
  data?: unknown;
}

/** Discovered services with an integration id across hosts, sorted by host then service; plus status counts. */
export function summarizeIntegrations(items: ServiceItem[]): { rows: IntegrationRow[]; counts: StatusCounts } {
  const counts: StatusCounts = { enabled: 0, needs_configuration: 0, error: 0, not_available: 0 };
  const rows: IntegrationRow[] = [];
  for (const it of items) {
    const s = (it.data && typeof it.data === "object" ? it.data : {}) as DiscoveredService;
    const integration = integrationOf(s);
    if (!integration.id) continue;
    const split = splitServiceKey(it.key);
    const discoveryId = s.rule_id || split?.discoveryId || "";
    const instance = s.instance || split?.instance || "";
    counts[integration.status]++;
    rows.push({
      hostId: it.host_id,
      hostName: it.host_name || it.host_id,
      key: it.key,
      discoveryId,
      instance,
      command: s.command?.trim() || undefined,
      displayInstance: s.display_instance?.trim() || undefined,
      name: s.name || discoveryId,
      integration,
      panel: hasPanel(s) && discoveryId !== "" && instance !== "",
    });
  }
  rows.sort((a, b) => a.hostName.localeCompare(b.hostName) || a.name.localeCompare(b.name) || a.instance.localeCompare(b.instance));
  return { rows, counts };
}

/** Rows with the given status (undefined = all) whose host, service, integration, command, instance or endpoint contain every term. */
export function filterIntegrationRows(rows: IntegrationRow[], status: IntegrationStatus | undefined, q: string): IntegrationRow[] {
  const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
  return rows.filter((r) => {
    if (status && r.integration.status !== status) return false;
    if (terms.length === 0) return true;
    const text = [r.hostName, r.hostId, r.name, r.discoveryId, r.integration.id ?? "", r.command ?? "", r.instance, r.integration.endpoint ?? ""].join(" ").toLowerCase();
    return terms.every((t) => text.includes(t));
  });
}

// ---- recommended alerts ----

/** Rule editor prefill for a panel chart metric on one instance (no threshold). */
export function instanceAlertSearch(spec: { metric: string; agg: string; ref: InstanceRef; hostName?: string; name: string }): RuleEditorSearch {
  return {
    type: "metric_threshold",
    metric: spec.metric,
    host: spec.ref.hostId,
    hostName: spec.hostName,
    agg: spec.agg,
    groupBy: "host",
    filters: JSON.stringify(Object.entries(instanceResourceFilter(spec.ref)).map(([k, v]) => ({ field: `resource.${k}`, op: "eq", values: [v] }))),
    name: spec.name,
  };
}
