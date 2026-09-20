// Key figures per integration for the fleet dashboard on the Integrations page (metric names: semantic-conventions
// §6.3–§6.5). Each KPI fetches a few metrics of one instance and reduces them to the latest value.
import type { UnitKind } from "@/lib/format";
import { hitRatio, iisPoolRows, lastValue, latestMax, pgCacheHitRatio, pickSeries, sumSeries, type IntegrationId } from "@/lib/integrations";
import { IIS_APP_POOL, IIS_POOLS_QUERY, percentToRatio, PG_DATABASE, type PanelData, type PanelQuery } from "./panels";

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
  | "cacheHit"
  | "batchRequests"
  | "deadlocks"
  | "notFound"
  | "bytesSent"
  | "poolsNotRunning"
  | "busyWorkers"
  | "evictions"
  | "sessions"
  | "serverErrors"
  | "downServers"
  | "messagesReady"
  | "publishRate"
  | "consumers"
  | "documents"
  | "searchRate"
  | "indexRate"
  | "heapUsage"
  | "unassignedShards";

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
  mssql: [
    { id: "connections", queries: { c: { name: "sqlserver.user.connection.count", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
    { id: "batchRequests", queries: { b: { name: "sqlserver.batch.request.rate", agg: "avg" } }, unit: "number", compute: (d) => last(d, "b") },
    {
      id: "cacheHit",
      queries: { h: { name: "sqlserver.page.buffer_cache.hit_ratio", agg: "avg" } },
      unit: "percent",
      compute: (d) => lastValue(percentToRatio(sumSeries(get(d, "h")))),
    },
    { id: "deadlocks", queries: { d: { name: "sqlserver.deadlock.count", agg: "rate" } }, unit: "number", compute: (d) => last(d, "d") },
  ],
  apache: [
    { id: "requests", queries: { r: { name: "apache.requests", agg: "rate" } }, unit: "number", compute: (d) => last(d, "r") },
    {
      id: "busyWorkers",
      queries: { w: { name: "apache.workers", agg: "last", groupBy: ["state"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "w"), "state", ["busy"]))),
    },
    { id: "bytesSent", queries: { t: { name: "apache.traffic", agg: "rate" } }, unit: "bytesPerSec", compute: (d) => last(d, "t") },
    { id: "activeConnections", queries: { c: { name: "apache.current_connections", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
  ],
  memcached: [
    { id: "ops", queries: { c: { name: "memcached.commands", agg: "rate", groupBy: ["command"] } }, unit: "number", compute: (d) => last(d, "c") },
    {
      id: "hitRatio",
      queries: { h: { name: "memcached.operation_hit_ratio", agg: "avg", groupBy: ["operation"] } },
      unit: "percent",
      compute: (d) => lastValue(percentToRatio(sumSeries(pickSeries(get(d, "h"), "operation", ["get"])))),
    },
    { id: "connections", queries: { c: { name: "memcached.connections.current", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
    { id: "memory", queries: { b: { name: "memcached.bytes", agg: "last" } }, unit: "bytes", compute: (d) => last(d, "b") },
    { id: "evictions", queries: { e: { name: "memcached.evictions", agg: "rate" } }, unit: "number", compute: (d) => last(d, "e") },
  ],
  haproxy: [
    { id: "requests", queries: { r: { name: "haproxy.requests.total", agg: "rate" } }, unit: "number", compute: (d) => last(d, "r") },
    { id: "sessions", queries: { s: { name: "haproxy.sessions.current", agg: "last" } }, unit: "number", compute: (d) => last(d, "s") },
    {
      id: "serverErrors",
      queries: { r: { name: "haproxy.responses.count", agg: "rate", groupBy: ["status_code"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "r"), "status_code", ["5xx"]))),
    },
    {
      // Every row reports 1 for its state, so the sum over "down" is how many backends and servers are down.
      id: "downServers",
      queries: { s: { name: "haproxy.status", agg: "last", groupBy: ["state"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "s"), "state", ["down"]))),
    },
  ],
  rabbitmq: [
    {
      id: "messagesReady",
      queries: { m: { name: "rabbitmq.message.current", agg: "last", groupBy: ["state"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "m"), "state", ["ready"]))),
    },
    { id: "publishRate", queries: { p: { name: "rabbitmq.message.published", agg: "rate" } }, unit: "number", compute: (d) => last(d, "p") },
    { id: "consumers", queries: { c: { name: "rabbitmq.consumer.count", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
    { id: "connections", queries: { c: { name: "rabbitmq.connection.count", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
  ],
  elasticsearch: [
    {
      id: "documents",
      queries: { d: { name: "elasticsearch.node.documents", agg: "last", groupBy: ["state"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "d"), "state", ["active"]))),
    },
    {
      id: "searchRate",
      queries: { o: { name: "elasticsearch.node.operations.completed", agg: "rate", groupBy: ["operation"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "o"), "operation", ["query"]))),
    },
    {
      id: "indexRate",
      queries: { o: { name: "elasticsearch.node.operations.completed", agg: "rate", groupBy: ["operation"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "o"), "operation", ["index"]))),
    },
    {
      id: "heapUsage",
      queries: { h: { name: "jvm.memory.heap.utilization", agg: "avg" } },
      unit: "percent",
      compute: (d) => lastValue(percentToRatio(sumSeries(get(d, "h")))),
    },
    {
      id: "unassignedShards",
      queries: { s: { name: "elasticsearch.cluster.shards", agg: "last", groupBy: ["state"] } },
      unit: "number",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "s"), "state", ["unassigned"]))),
    },
  ],
  iis: [
    { id: "requests", queries: { r: { name: "iis.request.count", agg: "rate" } }, unit: "number", compute: (d) => last(d, "r") },
    { id: "activeConnections", queries: { c: { name: "iis.connection.active", agg: "last" } }, unit: "number", compute: (d) => last(d, "c") },
    { id: "notFound", queries: { n: { name: "iis.request.not_found.count", agg: "rate" } }, unit: "number", compute: (d) => last(d, "n") },
    {
      id: "bytesSent",
      queries: { n: { name: "iis.network.io", agg: "rate", groupBy: ["direction"] } },
      unit: "bytesPerSec",
      compute: (d) => lastValue(sumSeries(pickSeries(get(d, "n"), "direction", ["sent"]))),
    },
    {
      id: "poolsNotRunning",
      queries: { p: IIS_POOLS_QUERY },
      unit: "number",
      compute: (d) => {
        const rows = iisPoolRows(get(d, "p"), IIS_APP_POOL);
        return rows.length === 0 ? null : rows.filter((r) => r.state !== "running").length;
      },
    },
  ],
};
