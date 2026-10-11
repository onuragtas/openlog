// Query factories for the real user monitoring endpoints (docs/contracts/rum.md §7, api.md "Real user
// monitoring"). Keys include the range spec; the window is resolved inside queryFn like every other factory
// (api/queries.ts). Browser errors deliberately have no endpoint of their own — they are APM error groups of
// the same application (rum.md §7), so the UI links to the error inbox instead.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type RumApp = S["RumApp"];
export type RumVital = S["RumVital"];
export type RumVitalName = RumVital["name"];
export type RumPage = S["RumPage"];
export type RumOverview = S["RumOverview"];
export type RumSession = S["RumSession"];
export type RumEvent = S["RumEvent"];
export type RumRelease = S["RumRelease"];
export type RumFunnelStep = S["RumFunnelStep"];

/** `slowest` orders by total time consumed, the same reasoning as APM's "most time consuming". */
export type RumPageSort = "views" | "slowest" | "avg";

const REFRESH_MS = 60_000;

const rangeKey = (r: RangeSpec) => [r.range ?? "", r.from ?? "", r.to ?? ""];

function window(r: RangeSpec) {
  const { from, to } = resolveRange(r, Date.now());
  return { from: String(from), to: String(to) };
}

const live = (r: RangeSpec) => (r.range || (!r.from && !r.to) ? REFRESH_MS : false);

export const rumAppsQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["rum", "apps", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/rum/apps", { params: { query: window(range) }, signal })).apps,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const rumOverviewQuery = (range: RangeSpec, app: string, environment?: string) =>
  queryOptions({
    queryKey: ["rum", "overview", app, environment ?? "", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/rum/overview", { params: { query: { app, environment, ...window(range) } }, signal })),
    enabled: app !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const rumPagesQuery = (range: RangeSpec, app: string, sort: RumPageSort, environment?: string) =>
  queryOptions({
    queryKey: ["rum", "pages", app, environment ?? "", sort, ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/rum/pages", { params: { query: { app, environment, sort, ...window(range) } }, signal })).pages,
    enabled: app !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/** The five vitals of the application, or of one route when `route` is given. */
export const rumVitalsQuery = (range: RangeSpec, app: string, route?: string, environment?: string) =>
  queryOptions({
    queryKey: ["rum", "vitals", app, environment ?? "", route ?? "", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/rum/vitals", { params: { query: { app, environment, route, ...window(range) } }, signal })),
    enabled: app !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const rumSessionsQuery = (range: RangeSpec, app: string, environment?: string) =>
  queryOptions({
    queryKey: ["rum", "sessions", app, environment ?? "", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/rum/sessions", { params: { query: { app, environment, ...window(range) } }, signal })).sessions,
    enabled: app !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/**
 * One session with its event timeline. The timeline comes from the stored spans, so it is bounded by the
 * 7-day trace retention: an older session still has its summary and trace links, but no events.
 */
export const rumSessionQuery = (range: RangeSpec, sessionId: string) =>
  queryOptions({
    queryKey: ["rum", "session", sessionId, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/rum/sessions/{session_id}", { params: { path: { session_id: sessionId }, query: window(range) }, signal })),
    enabled: sessionId !== "",
    placeholderData: keepPreviousData,
  });

/**
 * How each version of the application behaved, with the deployments that introduced them.
 *
 * The two halves see different distances and the screen has to say so: deployments come from the 30-day
 * version rollup, sessions are counted from the spans and so stop at the 7-day trace retention. A version
 * older than a week is therefore listed with no sessions against it, which is a gap in the data rather
 * than a quiet week.
 */
export const rumReleasesQuery = (range: RangeSpec, app: string, environment?: string) =>
  queryOptions({
    queryKey: ["rum", "releases", app, environment ?? "", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/rum/releases", { params: { query: { app, environment, ...window(range) } }, signal })),
    enabled: app !== "",
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/**
 * Of the sessions that did the first step, how many reached each of the next ones in order.
 *
 * Disabled until there are at least two steps: the endpoint requires them, and asking with one would be a
 * 400 on every keystroke while somebody types the funnel.
 */
export const rumFunnelQuery = (range: RangeSpec, app: string, steps: string[], funnelWindow: string, environment?: string) =>
  queryOptions({
    queryKey: ["rum", "funnel", app, environment ?? "", steps.join(">"), funnelWindow, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/rum/funnel", { params: { query: { app, environment, step: steps, window: funnelWindow, ...window(range) } }, signal })),
    enabled: app !== "" && steps.length >= 2,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });
