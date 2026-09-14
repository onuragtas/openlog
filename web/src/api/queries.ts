// Query option factories: one per endpoint. Components call
// useQuery(xxxQuery(...)); keys are stable and include the *range spec*
// (not resolved timestamps) so relative ranges refetch with a fresh "now".
import { infiniteQueryOptions, keepPreviousData, queryOptions } from "@tanstack/react-query";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { Aggregation, LogRecord, MetricSeriesResponse } from "./types";

const REFRESH_MS = 60_000;

export const hostsQuery = () =>
  queryOptions({
    queryKey: ["hosts"],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/hosts", { params: { query: { limit: 1000 } }, signal })).hosts,
    refetchInterval: REFRESH_MS,
  });

export const hostQuery = (hostId: string) =>
  queryOptions({
    queryKey: ["host", hostId],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/hosts/{host_id}", { params: { path: { host_id: hostId } }, signal })),
  });

export interface MetricRequest {
  hostId: string;
  name: string;
  range: RangeSpec;
  agg?: Aggregation;
  groupBy?: string[];
  /** Exact-match resource attribute filters (`resource.<key>`; allowlisted keys, see api.md), e.g. one integration instance. */
  resource?: Record<string, string>;
}

const resourceEntries = (r?: Record<string, string>) => Object.entries(r ?? {}).sort(([a], [b]) => a.localeCompare(b));

export const metricQuery = (r: MetricRequest) =>
  queryOptions({
    queryKey: [
      "metric", r.hostId, r.name, r.range.range ?? "", r.range.from ?? "", r.range.to ?? "", r.agg ?? "", r.groupBy?.join(",") ?? "",
      ...(r.resource ? [JSON.stringify(resourceEntries(r.resource))] : []),
    ],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(r.range, Date.now());
      // `resource.<key>` parameters are sent as flat query keys (openapi-fetch would serialize an object as deepObject).
      const resource = Object.fromEntries(resourceEntries(r.resource).map(([k, v]) => [`resource.${k}`, v]));
      // openapi-fetch widens the MetricPoint tuple to number[]; the schema type is exact.
      const data = unwrap(
        await api.GET("/api/v1/hosts/{host_id}/metrics", {
          params: {
            path: { host_id: r.hostId },
            query: { name: r.name, from: String(from), to: String(to), agg: r.agg, group_by: r.groupBy?.join(",") || undefined, ...(resource as object) },
          },
          signal,
        }),
      ) as MetricSeriesResponse;
      return { ...data, from, to };
    },
    placeholderData: keepPreviousData,
    refetchInterval: r.range.range ? REFRESH_MS : false,
  });

export const inventoryQuery = (hostId: string) =>
  queryOptions({
    queryKey: ["inventory", hostId],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/hosts/{host_id}/inventory", { params: { path: { host_id: hostId } }, signal })),
  });

export const servicesQuery = (hostId: string) =>
  queryOptions({
    queryKey: ["services", hostId],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/hosts/{host_id}/services", { params: { path: { host_id: hostId } }, signal })),
  });

export const inventorySearchQuery = (category: string, q: string) =>
  queryOptions({
    queryKey: ["inventory-search", category, q],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/inventory/search", {
          params: { query: { category, q: q || undefined, limit: 1000 } },
          signal,
        }),
      ).items,
    enabled: category !== "",
  });

/** Exact-match log attribute filters (`attr.<key>` on GET /api/v1/logs; allowlisted keys, see api.md). */
export interface LogAttrFilters {
  /** openlog.log.source: file | journald */
  source?: string;
  /** log.file.path */
  filePath?: string;
  /** openlog.discovery.id (rule id, e.g. nginx) */
  discoveryId?: string;
  /** openlog.systemd.unit */
  systemdUnit?: string;
  /** log.iostream of container logs: stdout | stderr */
  stream?: string;
}

/** Query parameters for LogAttrFilters; empty values are omitted. */
export const logAttrQuery = (a?: LogAttrFilters) => ({
  "attr.openlog.log.source": a?.source || undefined,
  "attr.log.file.path": a?.filePath || undefined,
  "attr.openlog.discovery.id": a?.discoveryId || undefined,
  "attr.openlog.systemd.unit": a?.systemdUnit || undefined,
  "attr.log.iostream": a?.stream || undefined,
});

const logAttrKey = (a?: LogAttrFilters) => [a?.source ?? "", a?.filePath ?? "", a?.discoveryId ?? "", a?.systemdUnit ?? "", a?.stream ?? ""];

export interface LogsRequest {
  range: RangeSpec;
  hostId?: string;
  service?: string;
  q?: string;
  severity?: string;
  traceId?: string;
  /** log records of one span (16 hex) */
  spanId?: string;
  /** with transactionService: log records of the traces of that transaction */
  transaction?: string;
  transactionService?: string;
  /** container logs (resource attribute container.id) */
  containerId?: string;
  /** Kubernetes pod logs (resource attribute k8s.pod.uid) */
  k8sPodUid?: string;
  attrs?: LogAttrFilters;
  limit?: number;
}

const logsKey = (r: LogsRequest) => [
  r.range.range ?? "", r.range.from ?? "", r.range.to ?? "", r.hostId ?? "", r.service ?? "", r.q ?? "", r.severity ?? "", r.traceId ?? "",
  r.spanId ?? "", r.transaction ?? "", r.transactionService ?? "", r.containerId ?? "", r.k8sPodUid ?? "", ...logAttrKey(r.attrs),
];

/** Filter parameters of GET /api/v1/logs (without the window, limit and cursor). */
const logsFilterQuery = (r: LogsRequest) => {
  // The API requires transaction and transaction_service together.
  const txn = r.transaction && r.transactionService ? { transaction: r.transaction, transaction_service: r.transactionService } : {};
  return {
    host_id: r.hostId || undefined,
    service: r.service || undefined,
    q: r.q || undefined,
    severity_min: r.severity || undefined,
    trace_id: r.traceId || undefined,
    span_id: r.spanId || undefined,
    container_id: r.containerId || undefined,
    k8s_pod_uid: r.k8sPodUid || undefined,
    ...txn,
    ...logAttrQuery(r.attrs),
  };
};

export const logsQuery = (r: LogsRequest) =>
  queryOptions({
    queryKey: ["logs", ...logsKey(r), r.limit ?? 200],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(r.range, Date.now());
      return unwrap(
        await api.GET("/api/v1/logs", {
          params: { query: { from: String(from), to: String(to), ...logsFilterQuery(r), limit: r.limit ?? 200 } },
          signal,
        }),
      ).logs;
    },
    placeholderData: keepPreviousData,
  });

export const LOG_PAGE_SIZE = 200;

export interface LogPageParam {
  /** unix ms, resolved once for the first page and kept for all pages of one listing */
  from: number;
  to: number;
  /** next_cursor of the previous page */
  cursor: string;
}

export interface LogPage {
  logs: LogRecord[];
  nextCursor: string | null;
  from: number;
  to: number;
}

/** Newest-first log listing with "load older" pages (opaque server cursor, same filters and window on every page). */
export const logsInfiniteQuery = (r: LogsRequest) => {
  const limit = r.limit ?? LOG_PAGE_SIZE;
  return infiniteQueryOptions({
    queryKey: ["logs-pages", ...logsKey(r), limit],
    initialPageParam: null as LogPageParam | null,
    queryFn: async ({ pageParam, signal }): Promise<LogPage> => {
      const win = pageParam ?? resolveRange(r.range, Date.now());
      const data = unwrap(
        await api.GET("/api/v1/logs", {
          params: {
            query: { from: String(win.from), to: String(win.to), ...logsFilterQuery(r), limit, cursor: pageParam?.cursor },
          },
          signal,
        }),
      );
      return { logs: data.logs, nextCursor: data.next_cursor ?? null, from: win.from, to: win.to };
    },
    getNextPageParam: (last): LogPageParam | undefined => (last.nextCursor ? { from: last.from, to: last.to, cursor: last.nextCursor } : undefined),
    placeholderData: keepPreviousData,
  });
};

export const traceQuery = (traceId: string) =>
  queryOptions({
    queryKey: ["trace", traceId],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/traces/{trace_id}", { params: { path: { trace_id: traceId } }, signal })),
  });
