// Explorer API (api.md "Fields", D-118, D-119): attribute keys and values for query builders, structured log queries and
// volume, the metrics explorer and saved views. Query keys carry the range spec (not resolved timestamps) so relative
// ranges refetch with a fresh "now", like api/queries.ts.
import { infiniteQueryOptions, keepPreviousData, queryOptions } from "@tanstack/react-query";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, ApiError, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type FieldSignal = S["FieldSignal"];
export type FieldSource = S["FieldSource"];
export type FieldType = S["FieldType"];
export type FieldKey = S["FieldKey"];
export type FieldValue = S["FieldValue"];
export type FilterOp = S["FilterOp"];
export type QueryFilter = S["QueryFilter"];
export type LogQueryRow = S["LogQueryRow"];
export type LogsQueryRequest = S["LogsQueryRequest"];
export type LogsAggregateResponse = S["LogsAggregateResponse"];
export type MetricInfo = S["MetricInfo"];
export type MetricDetail = S["MetricDetail"];
export type MetricAggregation = S["MetricAggregation"];
export type MetricQueryResponse = S["MetricQueryResponse"];
export type SavedView = S["SavedView"];
export type SavedViewInput = S["SavedViewInput"];

/** Filter model shared by every explorer request: AND-ed `filters`, OR-ed AND-`groups`, body substring `q`. */
export interface FilterState {
  filters: QueryFilter[];
  groups: QueryFilter[][];
  q: string;
}

const rangeKey = (r: RangeSpec) => [r.range ?? "", r.from ?? "", r.to ?? ""];
const REFRESH_MS = 60_000;

/** Request body part of a filter state (empty parts omitted). */
export function filterBody(f: FilterState): Pick<LogsQueryRequest, "filters" | "groups" | "q"> {
  const groups = f.groups.filter((g) => g.length > 0);
  return { filters: f.filters.length ? f.filters : undefined, groups: groups.length ? groups : undefined, q: f.q.trim() || undefined };
}

export const fieldKeysQuery = (p: { signal: FieldSignal; range: RangeSpec; q?: string; metric?: string; limit?: number }) =>
  queryOptions({
    queryKey: ["fields", "keys", p.signal, ...rangeKey(p.range), p.q ?? "", p.metric ?? "", p.limit ?? 200],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(p.range, Date.now());
      return unwrap(
        await api.GET("/api/v1/fields/keys", {
          params: { query: { signal: p.signal, from: String(from), to: String(to), q: p.q || undefined, metric: p.metric || undefined, limit: p.limit } },
          signal,
        }),
      );
    },
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  });

export const fieldValuesQuery = (p: { signal: FieldSignal; key: string; range: RangeSpec; q?: string; metric?: string; filters?: QueryFilter[]; limit?: number; enabled?: boolean }) => {
  const filters = p.filters?.length ? JSON.stringify(p.filters) : undefined;
  return queryOptions({
    queryKey: ["fields", "values", p.signal, p.key, ...rangeKey(p.range), p.q ?? "", p.metric ?? "", filters ?? "", p.limit ?? 50],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(p.range, Date.now());
      return unwrap(
        await api.GET("/api/v1/fields/values", {
          params: { query: { signal: p.signal, key: p.key, from: String(from), to: String(to), q: p.q || undefined, metric: p.metric || undefined, filters, limit: p.limit } },
          signal,
        }),
      );
    },
    enabled: p.key !== "" && p.enabled !== false,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  });
};

export interface LogsExplorerRequest {
  range: RangeSpec;
  filter: FilterState;
  order: "asc" | "desc";
  columns: string[];
  limit?: number;
}

interface LogsExplorerPage {
  rows: LogQueryRow[];
  nextCursor: string | null;
  from: number;
  to: number;
}

interface LogsExplorerPageParam {
  from: number;
  to: number;
  cursor: string;
}

export const LOGS_EXPLORER_PAGE = 200;

/** POST /api/v1/logs/query pages: the window is resolved once and the same body is resent with the cursor. */
export const logsExplorerQuery = (r: LogsExplorerRequest) => {
  const body = { ...filterBody(r.filter), order: r.order, columns: r.columns, include_record: true, limit: r.limit ?? LOGS_EXPLORER_PAGE };
  return infiniteQueryOptions({
    queryKey: ["logs-explorer", ...rangeKey(r.range), JSON.stringify(body)],
    initialPageParam: null as LogsExplorerPageParam | null,
    queryFn: async ({ pageParam, signal }): Promise<LogsExplorerPage> => {
      const win = pageParam ?? resolveRange(r.range, Date.now());
      const data = unwrap(await api.POST("/api/v1/logs/query", { body: { ...body, from: win.from, to: win.to, cursor: pageParam?.cursor }, signal }));
      return { rows: data.rows, nextCursor: data.next_cursor ?? null, from: win.from, to: win.to };
    },
    getNextPageParam: (last): LogsExplorerPageParam | undefined => (last.nextCursor ? { from: last.from, to: last.to, cursor: last.nextCursor } : undefined),
    placeholderData: keepPreviousData,
  });
};

export const logsAggregateQuery = (r: { range: RangeSpec; filter: FilterState; groupBy?: string }) => {
  const body = { ...filterBody(r.filter), group_by: r.groupBy || undefined, limit: 10 };
  return queryOptions({
    queryKey: ["logs-aggregate", ...rangeKey(r.range), JSON.stringify(body)],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(r.range, Date.now());
      const data = unwrap(await api.POST("/api/v1/logs/aggregate", { body: { ...body, from, to }, signal })) as LogsAggregateResponse;
      return { ...data, from, to };
    },
    placeholderData: keepPreviousData,
  });
};

export const metricsListQuery = (p: { range: RangeSpec; q?: string }) =>
  queryOptions({
    queryKey: ["metrics-explorer", "list", ...rangeKey(p.range), p.q ?? ""],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(p.range, Date.now());
      return unwrap(await api.GET("/api/v1/metrics", { params: { query: { from: String(from), to: String(to), q: p.q || undefined } }, signal }));
    },
    placeholderData: keepPreviousData,
  });

export const metricDetailQuery = (name: string, range: RangeSpec) =>
  queryOptions({
    queryKey: ["metrics-explorer", "detail", name, ...rangeKey(range)],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(range, Date.now());
      return unwrap(await api.GET("/api/v1/metrics/{name}", { params: { path: { name }, query: { from: String(from), to: String(to) } }, signal }));
    },
    enabled: name !== "",
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
    staleTime: 60_000,
  });

export interface MetricExplorerRequest {
  metric: string;
  range: RangeSpec;
  filter: Pick<FilterState, "filters" | "groups">;
  aggregation?: MetricAggregation;
  groupBy: string[];
}

export const metricExplorerSeriesQuery = (r: MetricExplorerRequest) => {
  const groups = r.filter.groups.filter((g) => g.length > 0);
  const body = {
    metric: r.metric,
    filters: r.filter.filters.length ? r.filter.filters : undefined,
    groups: groups.length ? groups : undefined,
    aggregation: r.aggregation,
    group_by: r.groupBy.length ? r.groupBy : undefined,
    limit: 50,
  };
  return queryOptions({
    queryKey: ["metrics-explorer", "query", ...rangeKey(r.range), JSON.stringify(body)],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(r.range, Date.now());
      const data = unwrap(await api.POST("/api/v1/metrics/query", { body: { ...body, from, to }, signal })) as MetricQueryResponse;
      return { ...data, from, to };
    },
    enabled: r.metric !== "",
    placeholderData: keepPreviousData,
    refetchInterval: r.range.range ? REFRESH_MS : false,
  });
};

/** Saved views need PostgreSQL auth mode: the list answers 404 otherwise and the feature is hidden. */
export function isSavedViewsUnavailable(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

export const savedViewsQuery = (signal: SavedView["signal"]) =>
  queryOptions({
    queryKey: ["saved-views", signal],
    queryFn: async ({ signal: abort }) => unwrap(await api.GET("/api/v1/saved-views", { params: { query: { signal } }, signal: abort })).views,
    retry: false,
    staleTime: 30_000,
  });

export async function createSavedView(input: SavedViewInput): Promise<SavedView> {
  return unwrap(await api.POST("/api/v1/saved-views", { body: input }));
}

export async function updateSavedView(id: string, input: SavedViewInput): Promise<SavedView> {
  return unwrap(await api.PUT("/api/v1/saved-views/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteSavedView(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/saved-views/{id}", { params: { path: { id } } }));
}
