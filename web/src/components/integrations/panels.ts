// Curated charts per integration (metric names: semantic-conventions §6.3–§6.5). Each chart fetches one or
// more metrics filtered to the instance and combines them with the pure helpers of lib/integrations.ts.
import type { Aggregation, MetricSeries } from "@/api/types";
import type { UnitKind } from "@/lib/format";
import {
  differencePoints,
  hitRatio,
  instanceResourceFilter,
  pgCacheHitRatio,
  pickSeries,
  sumSeries,
  topSeries,
  type InstanceRef,
  type IntegrationId,
  type Points,
} from "@/lib/integrations";
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
  | "deadlocks"
  | "slotsOk"
  | "slotsPfail"
  | "slotsFail"
  | "locks"
  | "blockedProcesses"
  | "batchRequests"
  | "compilations"
  | "recompilations"
  | "transactions"
  | "pageSplits"
  | "lockWaits"
  | "pageLife"
  | "waitTime"
  | "sent"
  | "received"
  | "attempts"
  | "anonymous"
  | "notFound"
  | "site"
  | "busy"
  | "idle"
  | "traffic"
  | "get"
  | "set"
  | "items"
  | "evictions"
  | "expiredUnfetched"
  | "sessions"
  | "current"
  | "ready"
  | "unacknowledged"
  | "published"
  | "delivered"
  | "acknowledged"
  | "redelivered"
  | "messages"
  | "consumers"
  | "queued"
  | "rejected"
  | "indexed"
  | "searched"
  | "fetched"
  | "merged"
  | "heapUsed"
  | "heapMax"
  | "active"
  | "unassigned"
  | "relocating"
  | "initializing"
  | "documents"
  | "deletedDocuments";

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
  | "pgReplicationLag"
  | "nginxStatus"
  | "nginxUpstreams"
  | "redisCluster"
  | "pgLocks"
  | "mssqlConnections"
  | "mssqlBatchRequests"
  | "mssqlTransactions"
  | "mssqlLockWaits"
  | "mssqlDeadlocks"
  | "mssqlBufferCache"
  | "mssqlPageLife"
  | "mssqlDbSize"
  | "mssqlWaits"
  | "iisRequests"
  | "iisConnections"
  | "iisConnectionAttempts"
  | "iisNetworkIo"
  | "iisFiles"
  | "iisNotFound"
  | "iisRequestsBySite"
  | "iisNotFoundBySite"
  | "iisBytesBySite"
  | "apacheRequests"
  | "apacheWorkers"
  | "apacheTraffic"
  | "apacheScoreboard"
  | "memcachedCommands"
  | "memcachedHitRatio"
  | "memcachedConnections"
  | "memcachedItems"
  | "memcachedNetwork"
  | "haproxySessions"
  | "haproxyRequests"
  | "haproxyResponses"
  | "haproxyErrors"
  | "haproxyTimings"
  | "haproxyStatus"
  | "rabbitmqMessages"
  | "rabbitmqRates"
  | "rabbitmqConsumers"
  | "rabbitmqObjects"
  | "rabbitmqNodeMemory"
  | "esClusterShards"
  | "esOperations"
  | "esOperationTime"
  | "esDocuments"
  | "esHeap"
  | "esThreadPool";

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
  /** Hidden when the instance reports no data (metrics only some setups send: nginx Plus/VTS, Redis Cluster). */
  optional?: boolean;
  /** Query key whose metric the chart's "create alert" shortcut uses. */
  alert?: string;
  /** Breakdown by IIS site: shown for all sites, hidden when one site is selected. */
  siteBreakdown?: boolean;
  build: (d: PanelData, label: (k: SeriesLabelKey) => string) => ChartSeriesInput[];
}

/** PostgreSQL database/table resource attributes, grouped via `group_by=resource.<key>`. */
export const PG_DATABASE = "resource.postgresql.database.name";
export const PG_TABLE = "resource.postgresql.table.name";
/** pg_stat_statements query resources (semantic-conventions §6.5). */
export const PG_QUERY_ID = "resource.postgresql.queryid";
export const PG_QUERY_TEXT = "resource.db.query.text";
/** SQL Server data point attributes (semantic-conventions §6.7). */
export const MSSQL_DATABASE = "sqlserver.database.name";
export const MSSQL_WAIT_TYPE = "wait.type";
/** IIS site and application pool resource attributes (semantic-conventions §6.8). */
export const IIS_SITE_KEY = "iis.site";
export const IIS_SITE = `resource.${IIS_SITE_KEY}`;
export const IIS_APP_POOL = "resource.iis.application_pool";
/** Sites shown in the per-site breakdown charts (largest first). */
export const IIS_TOP_SITES = 8;
/** Site names of an instance: one series per site. */
export const IIS_SITES_QUERY: PanelQuery = { name: "iis.connection.active", agg: "last", groupBy: [IIS_SITE] };
/** Latest state per application pool. */
export const IIS_POOLS_QUERY: PanelQuery = { name: "iis.application_pool.state", agg: "last", groupBy: [IIS_APP_POOL] };

/** Resource filters of an instance's panel queries, narrowed to one IIS site when a site is selected. */
export function panelResource(id: IntegrationId, inst: Pick<InstanceRef, "discoveryId" | "instance">, site?: string): Record<string, string> {
  const base = instanceResourceFilter(inst);
  return id === "iis" && site ? { ...base, [IIS_SITE_KEY]: site } : base;
}

/** Charts of an integration panel: per-site breakdowns only while all IIS sites are shown. */
export function panelCharts(id: IntegrationId, site?: string): PanelChart[] {
  return PANELS[id].filter((c) => !(id === "iis" && site && c.siteBreakdown));
}

/** `sqlserver.page.buffer_cache.hit_ratio` is reported in % (0–100); charts and KPIs use ratios (0–1). */
export function percentToRatio(points: Points): Points {
  return points.map(([t, v]) => [t, v / 100]);
}

const get =(d: PanelData, k: string): MetricSeries[] => d[k] ?? [];
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
    {
      id: "nginxStatus",
      queries: { s: { name: "nginx.http.response.status", agg: "rate", groupBy: ["nginx.status_range"] } },
      unit: "number",
      stacked: true,
      optional: true,
      order: ["2xx", "3xx", "4xx", "5xx", "1xx"],
      alert: "s",
      build: (d, L) => by(get(d, "s"), ["nginx.status_range"], L("requests")),
    },
    {
      id: "nginxUpstreams",
      queries: { p: { name: "nginx.http.upstream.peer.state", agg: "last", groupBy: ["nginx.peer.state"] } },
      unit: "number",
      stacked: true,
      optional: true,
      order: ["UP", "DRAINING", "CHECKING", "UNHEALTHY", "UNAVAILABLE", "DOWN"],
      build: (d, L) => by(get(d, "p"), ["nginx.peer.state"], L("connections")),
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
    {
      id: "redisCluster",
      queries: { o: { name: "redis.cluster.slots_ok", agg: "last" }, p: { name: "redis.cluster.slots_pfail", agg: "last" }, f: { name: "redis.cluster.slots_fail", agg: "last" } },
      unit: "number",
      optional: true,
      alert: "f",
      build: (d, L) => [...one(L("slotsOk"), sumSeries(get(d, "o"))), ...one(L("slotsPfail"), sumSeries(get(d, "p"))), ...one(L("slotsFail"), sumSeries(get(d, "f")))],
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
    {
      id: "pgLocks",
      queries: { l: { name: "postgresql.database.locks", agg: "last", groupBy: ["mode"] } },
      unit: "number",
      stacked: true,
      optional: true,
      build: (d, L) => by(get(d, "l"), ["mode"], L("locks")),
    },
  ],
  // SQL Server (§6.7): `.rate` metrics are per-second gauges computed by the agent, so they are averaged, not rated.
  mssql: [
    {
      id: "mssqlConnections",
      queries: { c: { name: "sqlserver.user.connection.count", agg: "last" }, b: { name: "sqlserver.processes.blocked", agg: "last" } },
      unit: "number",
      alert: "c",
      build: (d, L) => [...one(L("connections"), sumSeries(get(d, "c"))), ...one(L("blockedProcesses"), sumSeries(get(d, "b")))],
    },
    {
      id: "mssqlBatchRequests",
      queries: {
        b: { name: "sqlserver.batch.request.rate", agg: "avg" },
        c: { name: "sqlserver.batch.sql_compilation.rate", agg: "avg" },
        r: { name: "sqlserver.batch.sql_recompilation.rate", agg: "avg" },
      },
      unit: "number",
      alert: "b",
      build: (d, L) => [...one(L("batchRequests"), sumSeries(get(d, "b"))), ...one(L("compilations"), sumSeries(get(d, "c"))), ...one(L("recompilations"), sumSeries(get(d, "r")))],
    },
    {
      id: "mssqlTransactions",
      queries: { t: { name: "sqlserver.transaction.rate", agg: "avg" }, p: { name: "sqlserver.page.split.rate", agg: "avg" } },
      unit: "number",
      alert: "t",
      build: (d, L) => [...one(L("transactions"), sumSeries(get(d, "t"))), ...one(L("pageSplits"), sumSeries(get(d, "p")))],
    },
    {
      id: "mssqlLockWaits",
      queries: { l: { name: "sqlserver.lock.wait.rate", agg: "avg" } },
      unit: "number",
      alert: "l",
      build: (d, L) => one(L("lockWaits"), sumSeries(get(d, "l"))),
    },
    {
      id: "mssqlDeadlocks",
      // The cumulative counter (openlog extension) loses no deadlock between collections, unlike sqlserver.deadlock.rate.
      queries: { d: { name: "sqlserver.deadlock.count", agg: "rate" } },
      unit: "number",
      alert: "d",
      build: (d, L) => one(L("deadlocks"), sumSeries(get(d, "d"))),
    },
    {
      id: "mssqlBufferCache",
      queries: { h: { name: "sqlserver.page.buffer_cache.hit_ratio", agg: "avg" } },
      unit: "percent",
      yMax: 1,
      alert: "h",
      build: (d, L) => one(L("hitRatio"), percentToRatio(sumSeries(get(d, "h")))),
    },
    {
      id: "mssqlPageLife",
      queries: { p: { name: "sqlserver.page.life_expectancy", agg: "last" } },
      unit: "s",
      alert: "p",
      build: (d, L) => one(L("pageLife"), sumSeries(get(d, "p"))),
    },
    {
      id: "mssqlDbSize",
      // Rows, log, filestream and full-text files summed per database.
      queries: { s: { name: "sqlserver.database.size", agg: "last", groupBy: [MSSQL_DATABASE] } },
      unit: "bytes",
      stacked: true,
      alert: "s",
      build: (d, L) => by(get(d, "s"), [MSSQL_DATABASE], L("used")),
    },
    {
      id: "mssqlWaits",
      // Seconds waited per second by wait type (top N wait types; needs VIEW SERVER STATE).
      queries: { w: { name: "sqlserver.os.wait.duration", agg: "rate", groupBy: [MSSQL_WAIT_TYPE] } },
      unit: "number",
      stacked: true,
      optional: true,
      build: (d, L) => by(get(d, "w"), [MSSQL_WAIT_TYPE], L("waitTime")),
    },
  ],
  apache: [
    {
      id: "apacheRequests",
      queries: { r: { name: "apache.requests", agg: "rate" } },
      unit: "number",
      alert: "r",
      build: (d, L) => one(L("requests"), sumSeries(get(d, "r"))),
    },
    {
      id: "apacheWorkers",
      queries: { w: { name: "apache.workers", agg: "last", groupBy: ["state"] } },
      unit: "number",
      stacked: true,
      order: ["busy", "idle"],
      alert: "w",
      build: (d, L) => by(get(d, "w"), ["state"], L("busy")),
    },
    {
      id: "apacheTraffic",
      queries: { t: { name: "apache.traffic", agg: "rate" } },
      unit: "bytesPerSec",
      build: (d, L) => one(L("traffic"), sumSeries(get(d, "t"))),
    },
    {
      // The scoreboard is what mod_status shows as a row of letters: how many workers are in each state.
      id: "apacheScoreboard",
      queries: { s: { name: "apache.scoreboard", agg: "last", groupBy: ["state"] } },
      unit: "number",
      stacked: true,
      order: ["waiting", "open", "sending", "reading", "keepalive", "dnslookup", "closing", "logging", "finishing", "idle_cleanup"],
      build: (d, L) => by(get(d, "s"), ["state"], L("busy")),
    },
  ],
  memcached: [
    {
      id: "memcachedCommands",
      queries: { c: { name: "memcached.commands", agg: "rate", groupBy: ["command"] } },
      unit: "number",
      stacked: true,
      order: ["get", "set", "flush", "touch"],
      alert: "c",
      build: (d, L) => by(get(d, "c"), ["command"], L("commands")),
    },
    {
      id: "memcachedHitRatio",
      queries: { h: { name: "memcached.operation_hit_ratio", agg: "avg", groupBy: ["operation"] } },
      unit: "percent",
      yMax: 1,
      build: (d, L) =>
        by(get(d, "h"), ["operation"], L("hitRatio")).map((sr) => ({ ...sr, points: percentToRatio(sr.points) })),
    },
    {
      id: "memcachedConnections",
      queries: { c: { name: "memcached.connections.current", agg: "last" }, t: { name: "memcached.threads", agg: "last" } },
      unit: "number",
      alert: "c",
      build: (d, L) => [...one(L("connected"), sumSeries(get(d, "c"))), ...one(L("limit"), sumSeries(get(d, "t")))],
    },
    {
      id: "memcachedItems",
      queries: { i: { name: "memcached.current_items", agg: "last" }, e: { name: "memcached.evictions", agg: "rate" } },
      unit: "number",
      alert: "e",
      build: (d, L) => [...one(L("items"), sumSeries(get(d, "i"))), ...one(L("evictions"), sumSeries(get(d, "e")))],
    },
    {
      id: "memcachedNetwork",
      queries: { n: { name: "memcached.network", agg: "rate", groupBy: ["direction"] } },
      unit: "bytesPerSec",
      order: ["sent", "received"],
      build: (d, L) => by(get(d, "n"), ["direction"], L("sent")),
    },
  ],
  // HAProxy (§6.12): one resource per proxy row (frontend, backend, server), so the charts sum the rows of
  // the instance; the status chart counts the rows in each state.
  haproxy: [
    {
      id: "haproxyRequests",
      queries: { r: { name: "haproxy.requests.total", agg: "rate" } },
      unit: "number",
      alert: "r",
      build: (d, L) => one(L("requests"), sumSeries(get(d, "r"))),
    },
    {
      id: "haproxySessions",
      queries: { c: { name: "haproxy.sessions.current", agg: "last" }, l: { name: "haproxy.sessions.limit", agg: "last" } },
      unit: "number",
      alert: "c",
      build: (d, L) => [...one(L("sessions"), sumSeries(get(d, "c"))), ...one(L("limit"), sumSeries(get(d, "l")))],
    },
    {
      id: "haproxyResponses",
      queries: { r: { name: "haproxy.responses.count", agg: "rate", groupBy: ["status_code"] } },
      unit: "number",
      stacked: true,
      order: ["2xx", "3xx", "4xx", "5xx", "1xx", "other"],
      alert: "r",
      build: (d, L) => by(get(d, "r"), ["status_code"], L("requests")),
    },
    {
      id: "haproxyErrors",
      queries: {
        c: { name: "haproxy.connections.errors", agg: "rate" },
        r: { name: "haproxy.responses.errors", agg: "rate" },
        q: { name: "haproxy.requests.errors", agg: "rate" },
      },
      unit: "number",
      alert: "r",
      build: (d, L) => [
        ...one(L("connections"), sumSeries(get(d, "c"))),
        ...one(L("requests"), sumSeries(get(d, "q"))),
        ...one(L("received"), sumSeries(get(d, "r"))),
      ],
    },
    {
      id: "haproxyTimings",
      queries: { q: { name: "haproxy.queue.time", agg: "avg" }, c: { name: "haproxy.connect.time", agg: "avg" }, r: { name: "haproxy.response.time", agg: "avg" } },
      unit: "ms",
      build: (d, L) => [
        ...one(L("queued"), sumSeries(get(d, "q"))),
        ...one(L("connections"), sumSeries(get(d, "c"))),
        ...one(L("requests"), sumSeries(get(d, "r"))),
      ],
    },
    {
      // Each row reports 1 for its own state, so the sum per state is how many are up, down or in maintenance.
      id: "haproxyStatus",
      queries: { s: { name: "haproxy.status", agg: "last", groupBy: ["state"] } },
      unit: "number",
      stacked: true,
      order: ["up", "open", "down", "maint", "drain", "nolb"],
      alert: "s",
      build: (d, L) => by(get(d, "s"), ["state"], L("sessions")),
    },
  ],
  // RabbitMQ (§6.13): one resource per queue and one per node; the message charts sum the queues.
  rabbitmq: [
    {
      id: "rabbitmqMessages",
      queries: { m: { name: "rabbitmq.message.current", agg: "last", groupBy: ["state"] } },
      unit: "number",
      stacked: true,
      order: ["ready", "unacknowledged"],
      alert: "m",
      build: (d, L) => by(get(d, "m"), ["state"], L("messages")),
    },
    {
      id: "rabbitmqRates",
      queries: {
        p: { name: "rabbitmq.message.published", agg: "rate" },
        d: { name: "rabbitmq.message.delivered", agg: "rate" },
        a: { name: "rabbitmq.message.acknowledged", agg: "rate" },
      },
      unit: "number",
      alert: "p",
      build: (d, L) => [
        ...one(L("published"), sumSeries(get(d, "p"))),
        ...one(L("delivered"), sumSeries(get(d, "d"))),
        ...one(L("acknowledged"), sumSeries(get(d, "a"))),
      ],
    },
    {
      id: "rabbitmqConsumers",
      queries: { c: { name: "rabbitmq.consumer.count", agg: "last" }, r: { name: "rabbitmq.message.redelivered", agg: "rate" } },
      unit: "number",
      alert: "c",
      build: (d, L) => [...one(L("consumers"), sumSeries(get(d, "c"))), ...one(L("redelivered"), sumSeries(get(d, "r")))],
    },
    {
      id: "rabbitmqObjects",
      queries: {
        c: { name: "rabbitmq.connection.count", agg: "last" },
        h: { name: "rabbitmq.channel.count", agg: "last" },
        q: { name: "rabbitmq.queue.count", agg: "last" },
      },
      unit: "number",
      build: (d, L) => [
        ...one(L("connections"), sumSeries(get(d, "c"))),
        ...one(L("commands"), sumSeries(get(d, "h"))),
        ...one(L("queries"), sumSeries(get(d, "q"))),
      ],
    },
    {
      id: "rabbitmqNodeMemory",
      queries: { u: { name: "rabbitmq.node.memory.used", agg: "avg" }, l: { name: "rabbitmq.node.memory.limit", agg: "last" } },
      unit: "bytes",
      alert: "u",
      build: (d, L) => [...one(L("used"), sumSeries(get(d, "u"))), ...one(L("limit"), sumSeries(get(d, "l")))],
    },
  ],
  // Elasticsearch and OpenSearch (§6.14): the cluster metrics come from the node that reports the health, the
  // rest from the local node.
  elasticsearch: [
    {
      id: "esClusterShards",
      queries: { s: { name: "elasticsearch.cluster.shards", agg: "last", groupBy: ["state"] } },
      unit: "number",
      stacked: true,
      order: ["active", "active_primary", "relocating", "initializing", "unassigned", "delayed_unassigned"],
      alert: "s",
      build: (d, L) => by(get(d, "s"), ["state"], L("active")),
    },
    {
      id: "esOperations",
      queries: { o: { name: "elasticsearch.node.operations.completed", agg: "rate", groupBy: ["operation"] } },
      unit: "number",
      order: ["query", "fetch", "index", "merge"],
      alert: "o",
      build: (d, L) => by(get(d, "o"), ["operation"], L("searched")),
    },
    {
      id: "esOperationTime",
      queries: { t: { name: "elasticsearch.node.operations.time", agg: "rate", groupBy: ["operation"] } },
      unit: "number",
      order: ["query", "fetch", "index", "merge"],
      build: (d, L) => by(get(d, "t"), ["operation"], L("waitTime")),
    },
    {
      id: "esDocuments",
      queries: { d: { name: "elasticsearch.node.documents", agg: "last", groupBy: ["state"] } },
      unit: "number",
      order: ["active", "deleted"],
      build: (d, L) => by(get(d, "d"), ["state"], L("documents")),
    },
    {
      id: "esHeap",
      queries: { u: { name: "jvm.memory.heap.used", agg: "avg" }, m: { name: "jvm.memory.heap.max", agg: "last" } },
      unit: "bytes",
      alert: "u",
      build: (d, L) => [...one(L("heapUsed"), sumSeries(get(d, "u"))), ...one(L("heapMax"), sumSeries(get(d, "m")))],
    },
    {
      id: "esThreadPool",
      queries: {
        q: { name: "elasticsearch.node.thread_pool.tasks.queued", agg: "last" },
        r: { name: "elasticsearch.node.thread_pool.tasks.rejected", agg: "rate" },
      },
      unit: "number",
      alert: "r",
      build: (d, L) => [...one(L("queued"), sumSeries(get(d, "q"))), ...one(L("rejected"), sumSeries(get(d, "r")))],
    },
  ],
  // IIS (§6.8): one resource per site; the charts sum all sites of the instance, or show the selected site
  // (panelResource). The breakdown charts group by the site resource attribute.
  iis: [
    {
      id: "iisRequestsBySite",
      queries: { r: { name: "iis.request.count", agg: "rate", groupBy: [IIS_SITE] } },
      unit: "number",
      siteBreakdown: true,
      alert: "r",
      build: (d, L) => by(topSeries(get(d, "r"), IIS_TOP_SITES), [IIS_SITE], L("site")),
    },
    {
      id: "iisNotFoundBySite",
      queries: { n: { name: "iis.request.not_found.count", agg: "rate", groupBy: [IIS_SITE] } },
      unit: "number",
      siteBreakdown: true,
      alert: "n",
      build: (d, L) => by(topSeries(get(d, "n"), IIS_TOP_SITES), [IIS_SITE], L("site")),
    },
    {
      id: "iisBytesBySite",
      // Sent and received summed per site.
      queries: { b: { name: "iis.network.io", agg: "rate", groupBy: [IIS_SITE] } },
      unit: "bytesPerSec",
      siteBreakdown: true,
      build: (d, L) => by(topSeries(get(d, "b"), IIS_TOP_SITES), [IIS_SITE], L("site")),
    },
    {
      id: "iisRequests",
      queries: { r: { name: "iis.request.count", agg: "rate", groupBy: ["request"] } },
      unit: "number",
      stacked: true,
      order: ["get", "post", "put", "delete", "head", "options", "trace"],
      alert: "r",
      build: (d, L) => by(get(d, "r"), ["request"], L("requests")),
    },
    {
      id: "iisConnections",
      queries: { c: { name: "iis.connection.active", agg: "last" } },
      unit: "number",
      alert: "c",
      build: (d, L) => one(L("connections"), sumSeries(get(d, "c"))),
    },
    {
      id: "iisConnectionAttempts",
      queries: { a: { name: "iis.connection.attempt.count", agg: "rate" }, n: { name: "iis.connection.anonymous", agg: "rate" } },
      unit: "number",
      build: (d, L) => [...one(L("attempts"), sumSeries(get(d, "a"))), ...one(L("anonymous"), sumSeries(get(d, "n")))],
    },
    {
      id: "iisNetworkIo",
      queries: { n: { name: "iis.network.io", agg: "rate", groupBy: ["direction"] } },
      unit: "bytesPerSec",
      order: ["sent", "received"],
      build: (d, L) => [
        ...one(L("sent"), sumSeries(pickSeries(get(d, "n"), "direction", ["sent"]))),
        ...one(L("received"), sumSeries(pickSeries(get(d, "n"), "direction", ["received"]))),
      ],
    },
    {
      id: "iisFiles",
      queries: { f: { name: "iis.network.file.count", agg: "rate", groupBy: ["direction"] } },
      unit: "number",
      build: (d, L) => [
        ...one(L("sent"), sumSeries(pickSeries(get(d, "f"), "direction", ["sent"]))),
        ...one(L("received"), sumSeries(pickSeries(get(d, "f"), "direction", ["received"]))),
      ],
    },
    {
      id: "iisNotFound",
      queries: { n: { name: "iis.request.not_found.count", agg: "rate" } },
      unit: "number",
      alert: "n",
      build: (d, L) => one(L("notFound"), sumSeries(get(d, "n"))),
    },
  ],
};
