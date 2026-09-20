// Query factories for the continuous profiling endpoints (docs/contracts/profiles.md §5, api.md "Continuous
// profiling"). Keys include the range spec; the window is resolved inside queryFn like every other factory
// (api/queries.ts).
//
// A profile type is never guessed. Nanoseconds and bytes do not add up, so the flame and function queries
// stay disabled until a service *and* a type are chosen, and the services query is what offers the types.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type ProfileService = S["ProfileService"];
export type ProfileFunction = S["ProfileFunction"];
export type FlameNode = S["FlameNode"];

const REFRESH_MS = 60_000;

const rangeKey = (r: RangeSpec) => [r.range ?? "", r.from ?? "", r.to ?? ""];

function window(r: RangeSpec) {
  const { from, to } = resolveRange(r, Date.now());
  return { from: String(from), to: String(to) };
}

const live = (r: RangeSpec) => (r.range || (!r.from && !r.to) ? REFRESH_MS : false);

/** What has been profiled in the range: one entry per service, environment and profile type. */
export const profileServicesQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["profiles", "services", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/profiles/services", { params: { query: window(range) }, signal })).services,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/** The flame graph tree, with the unit the profile itself declared so the chart never hard-codes "ns". */
export const profileFlameQuery = (range: RangeSpec, service: string, type: string, environment?: string, host?: string) =>
  queryOptions({
    queryKey: ["profiles", "flame", service, type, environment ?? "", host ?? "", ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/profiles/flame", { params: { query: { service, type, environment, host, ...window(range) } }, signal })),
    enabled: service !== "" && type !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/** Functions ranked by self time — the first question asked of a profile. */
export const profileFunctionsQuery = (range: RangeSpec, service: string, type: string, environment?: string, host?: string) =>
  queryOptions({
    queryKey: ["profiles", "functions", service, type, environment ?? "", host ?? "", ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/profiles/functions", { params: { query: { service, type, environment, host, ...window(range) } }, signal })),
    enabled: service !== "" && type !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });
