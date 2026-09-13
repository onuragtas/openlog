// Curated charts per integration (metric names: semantic-conventions §6.3–§6.5). Each chart fetches one or
// more metrics filtered to the instance and combines them with the pure helpers of lib/integrations.ts.
import type { Aggregation, MetricSeries } from "@/api/types";
import type { UnitKind } from "@/lib/format";
import { differencePoints, hitRatio, pgCacheHitRatio, pickSeries, sumSeries, type IntegrationId, type Points } from "@/lib/integrations";
import { seriesLabel, type ChartSeriesInput } from "@/lib/series";

export type SeriesLabelKey =
  | "requests"
  | "connections"
  | "accepted"
  | "handled"
  | "dropped"
  | "commands"
  | "used"
  | "rss"
  | "max"
  | "connected"
  | "blocked"
  | "hitRatio"
  | "evicted"
  | "expired"
  | "lag"
  | "queries"
  | "questions"
  | "slow"
  | "limit"
  | "rowLockWaits"
  | "tableLockWaits"
  | "backends"
  | "maxConnections"
  | "commits"
  | "rollbacks"
  | "deadlocks";

export type PanelChartId =
  | "nginxRequests"
  | "nginxConnections"
  | "nginxAcceptedHandled"
  | "redisOps"
  | "redisMemory"
  | "redisClients"
  | "redisHitRatio"
  | "redisKeys"
  | "redisReplication"
  | "mysqlQueries"
  | "mysqlCommands"
  | "mysqlThreads"
  | "mysqlBufferPool"
  | "mysqlSlow"
  | "mysqlRows"
  | "mysqlReplicaLag"
  | "mysqlLocks"
  | "pgBackends"
  | "pgTransactions"
  | "pgDbSize"
  | "pgCacheHit"
  | "pgRows"
  | "pgDeadlocks"
  | "pgReplicationLag";

export interface PanelQuery {
  name: string;
  agg: Aggregation;
  groupBy?: string[];
}

export type PanelData = Record<string, MetricSeries[]>;

export interface PanelChart {
  id: PanelChartId;
  queries: Record<string, PanelQuery>;
  unit: UnitKind;
  stacked?: boolean;
  yMax?: number;
  order?: string[];
  /** Query key whose metric the chart's "create alert" shortcut uses. */
  alert?: string;
  build: (d: PanelData, label: (k: SeriesLabelKey) => string) => ChartSeriesInput[];
}

/** PostgreSQL database/table resource attributes, grouped via `group_by=resource.<key>`. */
export const PG_DATABASE = "resource.postgresql.database.name";
export const PG_TABLE = "resource.postgresql.table.name";

const get = (d: PanelData, k: string): MetricSeries[] => d[k] ?? [];
const one = (label: string, points: Points): ChartSeriesInput[] => (points.length > 0 ? [{ label, points }] : []);
const by = (series: MetricSeries[], keys: string[], fallback: string): ChartSeriesInput[] =>
  series.filter((s) => s.points.length > 0).map((s) => ({ label: seriesLabel(s.attributes, keys) || fallback, points: s.points }));

export const PANELS: Record<IntegrationId, PanelChart[]> = {
  nginx: [
    {
      id: "nginxRequests",
      queries: { r: { name: "nginx.requests", agg: "rate" } },
      unit: "number",
      alert: "r",
      build: (d, L) => one(L("requests"), sumSeries(get(d, "r"))),
    },
    {
      id: "nginxConnections",
      queries: { c: { name: "nginx.connections_current", agg: "last", groupBy: ["state"] } },
      unit: "number",
      order: ["active", "reading", "writing", "waiting"],
      alert: "c",
      build: (d, L) => by(get(d, "c"), ["state"], L("connections")),
    },
    {
      id: "nginxAcceptedHandled",
      queries: { a: { name: "nginx.connections_accepted", agg: "rate" }, h: { name: "nginx.connections_handled", agg: "rate" } },
      unit: "number",
      build: (d, L) => {
        const a = sumSeries(get(d, "a"));
        const h = sumSeries(get(d, "h"));
        return [...one(L("accepted"), a), ...one(L("handled"), h), ...one(L("dropped"), differencePoints(a, h))];
      },
    },
  ],
  redis: [
    {
      id: "redisOps",
      queries: { c: { name: "redis.commands", agg: "avg" } },
      unit: "number",
      alert: "c",
      build: (d, L) => one(L("commands"), sumSeries(get(d, "c"))),
    },
    {
      id: "redisMemory",
      queries: { u: { name: "redis.memory.used", agg: "avg" }, r: { name: "redis.memory.rss", agg: "avg" }, m: { name: "redis.maxmemory", agg: "avg" } },
      unit: "bytes",
      alert: "u",
      build: (d, L) => {
        // maxmemory 0 = unlimited: no max line.
        const max = sumSeries(get(d, "m"));
        return [...one(L("used"), sumSeries(get(d, "u"))), ...one(L("rss"), sumSeries(get(d, "r"))), ...(max.some(([, v]) => v > 0) ? one(L("max"), max) : [])];
      },
    },
    {
      id: "redisClients",
      queries: { c: { name: "redis.clients.connected", agg: "last" }, b: { name: "redis.clients.blocked", agg: "last" } },
      unit: "number",
      alert: "c",
      build: (d, L) => [...one(L("connected"), sumSeries(get(d, "c"))), ...one(L("blocked"), sumSeries(get(d, "b")))],
    },
    {
      id: "redisHitRatio",
      queries: { h: { name: "redis.keyspace.hits", agg: "rate" }, m: { name: "redis.keyspace.misses", agg: "rate" } },
      unit: "percent",
      yMax: 1,
      build: (d, L) => one(L("hitRatio"), hitRatio(sumSeries(get(d, "h")), sumSeries(get(d, "m")))),
    },
    {
      id: "redisKeys",
      queries: { e: { name: "redis.keys.evicted", agg: "rate" }, x: { name: "redis.keys.expired", agg: "rate" } },
      unit: "number",
      alert: "e",
      build: (d, L) => [...one(L("evicted"), sumSeries(get(d, "e"))), ...one(L("expired"), sumSeries(get(d, "x")))],
    },
    {
      id: "redisReplication",
      queries: { o: { name: "redis.replication.offset", agg: "last" }, r: { name: "redis.replication.replica_offset", agg: "last" } },
      unit: "bytes",
      // slave_repl_offset exists only on replicas; primaries show no data.
      build: (d, L) => {
        const replica = sumSeries(get(d, "r"));
        return replica.length > 0 ? one(L("lag"), differencePoints(sumSeries(get(d, "o")), replica)) : [];
      },
    },
  ],
  mysql: [
    {
      id: "mysqlQueries",
      queries: { q: { name: "mysql.query.count", agg: "rate" }, c: { name: "mysql.query.client.count", agg: "rate" } },
      unit: "number",
      alert: "q",
      build: (d, L) => [...one(L("queries"), sumSeries(get(d, "q"))), ...one(L("questions"), sumSeries(get(d, "c")))],
    },
    {
      id: "mysqlCommands",
      queries: { c: { name: "mysql.commands", agg: "rate", groupBy: ["command"] } },
      unit: "number",
      stacked: true,
      order: ["select", "insert", "update", "delete"],
      build: (d, L) => by(get(d, "c"), ["command"], L("commands")),
    },
    {
      id: "mysqlThreads",
      queries: { t: { name: "mysql.threads", agg: "last", groupBy: ["kind"] } },
      unit: "number",
      order: ["connected", "running", "cached"],
      alert: "t",
      // `created` is a running total of created threads, not a current count.
      build: (d, L) => by(pickSeries(get(d, "t"), "kind", ["connected", "running", "cached"]), ["kind"], L("connected")),
    },
    {
      id: "mysqlBufferPool",
      queries: { u: { name: "mysql.buffer_pool.usage", agg: "last", groupBy: ["status"] }, l: { name: "mysql.buffer_pool.limit", agg: "last" } },
      unit: "bytes",
      order: ["dirty", "clean"],
      build: (d, L) => [...by(get(d, "u"), ["status"], L("used")), ...one(L("limit"), sumSeries(get(d, "l")))],
    },
    {
      id: "mysqlSlow",
      queries: { s: { name: "mysql.query.slow.count", agg: "rate" } },
      unit: "number",
      alert: "s",
      build: (d, L) => one(L("slow"), sumSeries(get(d, "s"))),
    },
    {
      id: "mysqlRows",
      queries: { r: { name: "mysql.row_operations", agg: "rate", groupBy: ["operation"] } },
      unit: "number",
      order: ["read", "inserted", "updated", "deleted"],
      build: (d, L) => by(get(d, "r"), ["operation"], L("queries")),
    },
    {
      id: "mysqlReplicaLag",
      queries: { l: { name: "mysql.replica.time_behind_source", agg: "last" } },
      unit: "number",
      alert: "l",
      build: (d, L) => one(L("lag"), sumSeries(get(d, "l"))),
    },
    {
      id: "mysqlLocks",
      queries: { r: { name: "mysql.row_locks", agg: "rate", groupBy: ["kind"] }, t: { name: "mysql.locks", agg: "rate", groupBy: ["kind"] } },
      unit: "number",
      build: (d, L) => [
        ...one(L("rowLockWaits"), sumSeries(pickSeries(get(d, "r"), "kind", ["waits"]))),
        ...one(L("tableLockWaits"), sumSeries(pickSeries(get(d, "t"), "kind", ["waited"]))),
      ],
    },
  ],
  postgresql: [
    {
      id: "pgBackends",
      queries: { b: { name: "postgresql.backends", agg: "last", groupBy: [PG_DATABASE] }, m: { name: "postgresql.connection.max", agg: "last" } },
      unit: "number",
      alert: "b",
      build: (d, L) => [...by(get(d, "b"), [PG_DATABASE], L("backends")), ...one(L("maxConnections"), sumSeries(get(d, "m")))],
    },
    {
      id: "pgTransactions",
      queries: { c: { name: "postgresql.commits", agg: "rate" }, r: { name: "postgresql.rollbacks", agg: "rate" } },
      unit: "number",
      alert: "r",
      build: (d, L) => [...one(L("commits"), sumSeries(get(d, "c"))), ...one(L("rollbacks"), sumSeries(get(d, "r")))],
    },
    {
      id: "pgDbSize",
      queries: { s: { name: "postgresql.db_size", agg: "last", groupBy: [PG_DATABASE] } },
      unit: "bytes",
      alert: "s",
      build: (d, L) => by(get(d, "s"), [PG_DATABASE], L("used")),
    },
    {
      id: "pgCacheHit",
      queries: { b: { name: "postgresql.blocks_read", agg: "rate", groupBy: ["source"] } },
      unit: "percent",
      yMax: 1,
      build: (d, L) => one(L("hitRatio"), pgCacheHitRatio(get(d, "b"))),
    },
    {
      id: "pgRows",
      queries: { o: { name: "postgresql.operations", agg: "rate", groupBy: ["operation"] } },
      unit: "number",
      order: ["ins", "upd", "del", "hot_upd"],
      build: (d, L) => by(get(d, "o"), ["operation"], L("queries")),
    },
    {
      id: "pgDeadlocks",
      queries: { d: { name: "postgresql.deadlocks", agg: "rate" } },
      unit: "number",
      alert: "d",
      build: (d, L) => one(L("deadlocks"), sumSeries(get(d, "d"))),
    },
    {
      id: "pgReplicationLag",
      queries: { l: { name: "postgresql.wal.lag", agg: "max", groupBy: ["operation", "replication_client"] } },
      unit: "number",
      alert: "l",
      build: (d, L) => by(get(d, "l"), ["operation", "replication_client"], L("lag")),
    },
  ],
};
