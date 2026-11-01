// Query factories for database query performance monitoring (docs/contracts/db-monitoring.md §5, D-138). Keys
// include the range spec; the window is resolved inside queryFn like every other factory (api/queries.ts). A
// database instance is addressed by its service.instance.id, a statement by its fingerprint.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import type { ListState } from "@/lib/list";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type DbInstance = S["DbInstance"];
export type DbQuery = S["DbQuery"];
export type DbPlan = S["DbPlan"];
export type DbWait = S["DbWait"];
export type DbQueryDetail = S["DbQueryDetail"];
export type DbActivity = S["DbActivity"];
export type DbSession = S["DbSession"];
export type DbActivityQuery = S["DbActivityQuery"];
export type DbInstanceSort = "time" | "calls" | "avg" | "aas" | "errors" | "statements" | "instance" | "host" | "wait" | "last_seen";
export type DbQuerySort = "time" | "calls" | "avg" | "rows" | "rows_per_call" | "errors" | "reads" | "statement";
export type DbWaitSort = "samples" | "type" | "event";
export type DbActivityQuerySort = "samples" | "statement" | "wait";

const REFRESH_MS = 60_000;
const rangeKey = (r: RangeSpec) => [r.range ?? "", r.from ?? "", r.to ?? ""];

function window(r: RangeSpec) {
  const { from, to } = resolveRange(r, Date.now());
  return { from: String(from), to: String(to) };
}

const live = (r: RangeSpec) => (r.range || (!r.from && !r.to) ? REFRESH_MS : false);

export const dbInstancesQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["db", "instances", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/db/instances", { params: { query: window(range) }, signal })).instances,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/** One page of the instances, for the list. Anything that needs to find an instance by name reads dbInstancesQuery, which is all of them. */
export const dbInstancesPageQuery = (range: RangeSpec, list: ListState<DbInstanceSort>) =>
  queryOptions({
    queryKey: ["db", "instances-page", ...rangeKey(range), list],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/db/instances", { params: { query: { ...window(range), ...list } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const dbQueriesQuery = (p: { instance: string; range: RangeSpec; list: ListState<DbQuerySort>; db?: string; q?: string }) =>
  queryOptions({
    queryKey: ["db", "queries", p.instance, p.list, p.db ?? "", p.q ?? "", ...rangeKey(p.range)],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/db/queries", {
          params: { query: { ...window(p.range), ...p.list, instance: p.instance, db: p.db || undefined, q: p.q || undefined } },
          signal,
        }),
      ),
    placeholderData: keepPreviousData,
    refetchInterval: live(p.range),
  });

export const dbQueryDetailQuery = (p: { instance: string; fingerprint: string; range: RangeSpec }) =>
  queryOptions({
    queryKey: ["db", "query", p.instance, p.fingerprint, ...rangeKey(p.range)],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/db/queries/{fingerprint}", {
          params: { path: { fingerprint: p.fingerprint }, query: { ...window(p.range), instance: p.instance } },
          signal,
        }),
      ),
    placeholderData: keepPreviousData,
    refetchInterval: live(p.range),
  });

export const dbActivityQuery = (p: { instance: string; range: RangeSpec }) =>
  queryOptions({
    queryKey: ["db", "activity", p.instance, ...rangeKey(p.range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/db/activity", { params: { query: { ...window(p.range), instance: p.instance } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(p.range),
  });

/** The wait events of an instance, as a list of their own: sorting them should not read the charts again. */
export const dbActivityWaitsQuery = (instance: string, range: RangeSpec, list: ListState<DbWaitSort>) =>
  queryOptions({
    queryKey: ["db", "activity-waits", instance, ...rangeKey(range), list],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/db/activity/waits", { params: { query: { ...window(range), ...list, instance } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const dbActivityQueriesQuery = (instance: string, range: RangeSpec, list: ListState<DbActivityQuerySort>) =>
  queryOptions({
    queryKey: ["db", "activity-queries", instance, ...rangeKey(range), list],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/db/activity/queries", { params: { query: { ...window(range), ...list, instance } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/** The latest session sample; refreshed every sampling interval while the tab is open. */
export const dbSessionsQuery = (instance: string, at?: number) =>
  queryOptions({
    queryKey: ["db", "sessions", instance, at ?? "now"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/db/sessions", { params: { query: { instance, at } }, signal })),
    refetchInterval: at ? false : 10_000,
  });

/** Server-side matches of an APM database statement (the link from APM's Databases tab). */
export const dbLookupQuery = (p: { dbSystem: string; statement: string; range: RangeSpec; enabled: boolean }) =>
  queryOptions({
    queryKey: ["db", "lookup", p.dbSystem, p.statement, ...rangeKey(p.range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/db/lookup", { params: { query: { ...window(p.range), db_system: p.dbSystem, statement: p.statement } }, signal })).matches,
    enabled: p.enabled,
    staleTime: 60_000,
  });

export type AppDbQuery = S["ApmDbQuery"];
export type AppDbPlan = S["ApmDbQueryPlan"];
export type AppDbSort = "time" | "calls" | "slowest" | "errors" | "max" | "statement" | "database";

/** The statements of every service, as the applications measured them (apm.md §7.1). */
export const appDbQueriesQuery = (range: RangeSpec, list: ListState<AppDbSort>, q: string) =>
  queryOptions({
    queryKey: ["apm", "db-queries", ...rangeKey(range), list, q],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/apm/db-queries", { params: { query: { ...window(range), ...list, q: q || undefined } }, signal })),
    refetchInterval: live(range),
    placeholderData: keepPreviousData,
  });

export const appDbPlansQuery = (range: RangeSpec, dbSystem: string, statement: string) =>
  queryOptions({
    queryKey: ["apm", "db-query-plans", ...rangeKey(range), dbSystem, statement],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/apm/db-queries/plans", { params: { query: { ...window(range), db_system: dbSystem, statement } }, signal })).plans,
  });
