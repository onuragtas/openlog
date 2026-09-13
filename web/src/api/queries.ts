// Query option factories: one per endpoint. Components call
// useQuery(xxxQuery(...)); keys are stable and include the *range spec*
// (not resolved timestamps) so relative ranges refetch with a fresh "now".
import { infiniteQueryOptions, keepPreviousData, queryOptions } from "@tanstack/react-query";
import { nextLogCursor } from "@/lib/logs";
import { parseTimestampNs, resolveRange, type RangeSpec } from "@/lib/time";
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
}

/** Query parameters for LogAttrFilters; empty values are omitted. */
export const logAttrQuery = (a?: LogAttrFilters) => ({
  "attr.openlog.log.source": a?.source || undefined,
  "attr.log.file.path": a?.filePath || undefined,
  "attr.openlog.discovery.id": a?.discoveryId || undefined,
  "attr.openlog.systemd.unit": a?.systemdUnit || undefined,
});

const logAttrKey = (a?: LogAttrFilters) => [a?.source ?? "", a?.filePath ?? "", a?.discoveryId ?? "", a?.systemdUnit ?? ""];

export interface LogsRequest {
  range: RangeSpec;
  hostId?: string;
  service?: string;
  q?: string;
  severity?: string;
  traceId?: string;
  attrs?: LogAttrFilters;
  limit?: number;
}

export const logsQuery = (r: LogsRequest) =>
  queryOptions({
    queryKey: ["logs", r.range.range ?? "", r.range.from ?? "", r.range.to ?? "", r.hostId ?? "", r.service ?? "", r.q ?? "", r.severity ?? "", r.traceId ?? "", ...logAttrKey(r.attrs), r.limit ?? 200],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(r.range, Date.now());
      return unwrap(
        await api.GET("/api/v1/logs", {
          params: {
            query: {
              from: String(from),
              to: String(to),
              host_id: r.hostId || undefined,
              service: r.service || undefined,
              q: r.q || undefined,
              severity_min: r.severity || undefined,
              trace_id: r.traceId || undefined,
              ...logAttrQuery(r.attrs),
              limit: r.limit ?? 200,
            },
          },
          signal,
        }),
      ).logs;
    },
    placeholderData: keepPreviousData,
  });

export const LOG_PAGE_SIZE = 200;

export interface LogPageParam {
  /** unix ms, fixed for all pages of one listing */
  from: number;
  /** unix ms (first page) or the RFC3339Nano cursor of the previous page's oldest row */
  to: string;
}

export interface LogPage {
  logs: LogRecord[];
  from: number;
}

/** Newest-first log listing with "load older" pages (cursor = oldest row's timestamp; see lib/logs.ts). */
export const logsInfiniteQuery = (r: LogsRequest) => {
  const limit = r.limit ?? LOG_PAGE_SIZE;
  return infiniteQueryOptions({
    queryKey: ["logs-pages", r.range.range ?? "", r.range.from ?? "", r.range.to ?? "", r.hostId ?? "", r.service ?? "", r.q ?? "", r.severity ?? "", r.traceId ?? "", ...logAttrKey(r.attrs), limit],
    initialPageParam: null as LogPageParam | null,
    queryFn: async ({ pageParam, signal }): Promise<LogPage> => {
      let page = pageParam;
      if (!page) {
        const { from, to } = resolveRange(r.range, Date.now());
        page = { from, to: String(to) };
      }
      const data = unwrap(
        await api.GET("/api/v1/logs", {
          params: {
            query: {
              from: String(page.from),
              to: page.to,
              host_id: r.hostId || undefined,
              service: r.service || undefined,
              q: r.q || undefined,
              severity_min: r.severity || undefined,
              trace_id: r.traceId || undefined,
              ...logAttrQuery(r.attrs),
              limit,
            },
          },
          signal,
        }),
      );
      return { logs: data.logs, from: page.from };
    },
    getNextPageParam: (last, all): LogPageParam | undefined => {
      const to = nextLogCursor(
        all.map((p) => p.logs),
        limit,
      );
      if (!to) return undefined;
      const toNs = parseTimestampNs(to);
      // The API rejects from >= to.
      if (toNs === null || toNs <= BigInt(last.from) * 1_000_000n) return undefined;
      return { from: last.from, to };
    },
    placeholderData: keepPreviousData,
  });
};

export const traceQuery = (traceId: string) =>
  queryOptions({
    queryKey: ["trace", traceId],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/traces/{trace_id}", { params: { path: { trace_id: traceId } }, signal })),
  });
