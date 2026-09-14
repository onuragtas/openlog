// Key figures per integration for the fleet dashboard on the Integrations page (metric names: semantic-conventions
// §6.3–§6.5). Each KPI fetches a few metrics of one instance and reduces them to the latest value.
import type { UnitKind } from "@/lib/format";
import { hitRatio, lastValue, latestMax, pgCacheHitRatio, pickSeries, sumSeries, type IntegrationId } from "@/lib/integrations";
import { PG_DATABASE, type PanelData, type PanelQuery } from "./panels";

export type KpiId =
  | "requests"
  | "activeConnections"
  | "ops"
  | "memory"
  | "hitRatio"
  | "clients"
  | "qps"
  | "connections"
  | "slow"
  | "replicaLag"
  | "backends"
  | "connectionUsage"
  | "tps"
  | "cacheHit";

export interface KpiSpec {
  id: KpiId;
  queries: Record<string, PanelQuery>;
  unit: UnitKind;
  compute: (d: PanelData) => number | null;
}

const get = (d: PanelData, k: string) => d[k] ?? [];
const last = (d: PanelData, k: string) => lastValue(sumSeries(get(d, k)));

export const KPIS: Record<IntegrationId, KpiSpec[]> = {
  nginx: [
    { id: "requests", queries: { r: { name: "nginx.requests", agg: "rate" } }, unit: "number", compute: (d) => last(d, "r") },
    {
      id: "activeConnections",
      queries: { c: { name: "nginx.connections_current", agg: "last", groupBy: ["state"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "c"), "state", ["active"]))),
    },
  ],
  redis: [
    { id: "ops", queries: { c: { name: "redis.commands", agg: "avg" } }, unit: "number", compute: (d) => last(d, "c") },
    { id: "memory", queries: { u: { name: "redis.memory.used", agg: "avg" } }, unit: "bytes", compute: (d) => last(d, "u") },
    {
      id: "hitRatio",
      queries: { h: { name: "redis.keyspace.hits", agg: "rate" }, m: { name: "redis.keyspace.misses", agg: "rate" } },
      unit: "percent",
      compute: (d) => lastValue(hitRatio(sumSeries(get(d, "h")), sumSeries(get(d, "m")))),
    },
    { id: "clients", queries: { c: { name: "redis.clients.connected", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
  ],
  mysql: [
    { id: "qps", queries: { q: { name: "mysql.query.client.count", agg: "rate" } }, unit: "number", compute: (d) => last(d, "q") },
    {
      id: "connections",
      queries: { t: { name: "mysql.threads", agg: "last", groupBy: ["kind"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "t"), "kind", ["connected"]))),
    },
    { id: "slow", queries: { s: { name: "mysql.query.slow.count", agg: "rate" } }, unit: "number", compute: (d) => last(d, "s") },
    { id: "replicaLag", queries: { l: { name: "mysql.replica.time_behind_source", agg: "last" } }, unit: "number", compute: (d) => last(d, "l") },
  ],
  postgresql: [
    { id: "backends", queries: { b: { name: "postgresql.backends", agg: "last", groupBy: [PG_DATABASE] } }, unit: "number", compute: (d) => last(d, "b") },
    {
      id: "connectionUsage",
      queries: { b: { name: "postgresql.backends", agg: "last", groupBy: [PG_DATABASE] }, m: { name: "postgresql.connection.max", agg: "last" } },
      unit: "percent",
      compute: (d) => {
        const b = last(d, "b");
        const m = latestMax(get(d, "m"));
        return b === null || !m ? null : b / m;
      },
    },
    {
      id: "tps",
      queries: { c: { name: "postgresql.commits", agg: "rate" }, r: { name: "postgresql.rollbacks", agg: "rate" } },
      unit: "number",
      compute: (d) => {
        const c = last(d, "c");
        const r = last(d, "r");
        return c === null && r === null ? null : (c ?? 0) + (r ?? 0);
      },
    },
    { id: "cacheHit", queries: { b: { name: "postgresql.blocks_read", agg: "rate", groupBy: ["source"] } }, unit: "percent", compute: (d) => lastValue(pgCacheHitRatio(get(d, "b"))) },
  ],
};
