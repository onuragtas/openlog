// OQL API: docs/contracts/oql.md, api.md "Query language (OQL)". Query factories follow queries.ts.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type OqlResult = S["OqlResult"];
export type OqlColumn = S["OqlColumn"];
export type OqlRow = S["OqlRow"];
export type OqlSeries = S["OqlSeries"];
export type OqlPoint = S["OqlPoint"];
export type OqlHistogramBucket = S["OqlHistogramBucket"];
export type OqlCompare = S["OqlCompare"];
export type OqlMetadata = S["OqlMetadata"];
export type OqlValidation = S["OqlValidation"];
export type OqlDiagnostic = S["OqlDiagnostic"];
export type OqlSchema = S["OqlSchema"];
export type OqlVariables = S["OqlVariables"];
export type OqlEventType = NonNullable<NonNullable<import("./schema.gen").operations["getOqlSchema"]["parameters"]["query"]>["event_type"]>;

export interface OqlRunSpec {
  query: string;
  /** Time range sent as from/to; null = the query's own SINCE/UNTIL. */
  range: RangeSpec | null;
  variables?: OqlVariables;
  /** Dashboard cross-widget filters (oql.md §7). */
  filters?: S["DashboardFilter"][];
  /** Changes on every explicit run so the console re-executes identical queries. */
  runId?: number;
}

const rangeKey = (r: RangeSpec | null) => (r ? [r.range ?? "", r.from ?? "", r.to ?? ""].join("|") : "query");

/** Runs an OQL query. Relative ranges are resolved when the query runs, so refetches move the window. */
export const oqlQuery = (spec: OqlRunSpec, opts: { refetchMs?: number | false; enabled?: boolean } = {}) =>
  queryOptions({
    queryKey: [
      "oql",
      "query",
      spec.query,
      rangeKey(spec.range),
      spec.variables ? JSON.stringify(spec.variables) : "",
      spec.runId ?? 0,
      spec.filters?.length ? JSON.stringify(spec.filters) : "",
    ],
    queryFn: async ({ signal }) => {
      const body: S["OqlQueryRequest"] = { query: spec.query };
      if (spec.range) {
        const r = resolveRange(spec.range, Date.now());
        body.from = String(Math.floor(r.from));
        body.to = String(Math.floor(r.to));
      }
      if (spec.variables && Object.keys(spec.variables).length > 0) body.variables = spec.variables;
      if (spec.filters && spec.filters.length > 0) body.filters = spec.filters;
      // openapi-fetch widens the [ms, value] point tuple; the schema type is exact.
      return unwrap(await api.POST("/api/v1/query", { body, signal })) as OqlResult;
    },
    enabled: (opts.enabled ?? true) && spec.query.trim() !== "",
    refetchInterval: opts.refetchMs ?? false,
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: 30_000,
  });

/** Parses and plans a query without reading data (errors with positions). */
export const oqlValidateQuery = (query: string, variables?: OqlVariables) =>
  queryOptions({
    queryKey: ["oql", "validate", query, variables ? JSON.stringify(variables) : ""],
    queryFn: async ({ signal }) => unwrap(await api.POST("/api/v1/query/validate", { body: { query, variables }, signal })),
    enabled: query.trim() !== "",
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: 5 * 60_000,
  });

/** Event types, attributes, functions and keywords; with an event type also its frequent map keys and metric names. */
export const oqlSchemaQuery = (eventType?: OqlEventType | null) =>
  queryOptions({
    queryKey: ["oql", "schema", eventType ?? ""],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/query/schema", { params: { query: { event_type: eventType ?? undefined } }, signal })),
    staleTime: 10 * 60_000,
    retry: false,
  });
