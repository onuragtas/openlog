// Query factories for the cost endpoints (docs/contracts/api.md "Costs", cost.md). Keys include the
// range spec; the window is resolved inside queryFn like every other factory (api/queries.ts).
//
// Every response carries a `pricing` object: the price table's version, date and caveat. The UI
// renders it next to the numbers — a cost estimate must never be mistaken for a bill.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type CostSummary = S["CostSummary"];
export type CostPricing = S["CostPricing"];
export type CostHost = S["CostHost"];
export type CostService = S["CostService"];
export type CostContainer = S["CostContainer"];
export type CostTrendPoint = S["CostTrendPoint"];
export type CostPriceTable = S["CostPriceTable"];
export type CostSource = S["CostSource"];

const REFRESH_MS = 60_000;

const rangeKey = (r: RangeSpec) => [r.range ?? "", r.from ?? "", r.to ?? ""];
const live = (r: RangeSpec) => (r.range || (!r.from && !r.to) ? REFRESH_MS : false);

function window(r: RangeSpec) {
  const { from, to } = resolveRange(r, Date.now());
  return { from: String(from), to: String(to) };
}

export const costSummaryQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["cost-summary", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/costs/summary", { params: { query: window(range) }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const costHostsQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["cost-hosts", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/costs/hosts", { params: { query: { ...window(range), limit: 200 } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const costServicesQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["cost-services", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/costs/services", { params: { query: { ...window(range), limit: 200 } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const costTrendQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["cost-trend", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/costs/trend", { params: { query: window(range) }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

/** One host's cost card (host detail page). */
export const costHostQuery = (hostId: string, range: RangeSpec) =>
  queryOptions({
    queryKey: ["cost-host", hostId, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/costs/hosts/{host_id}", { params: { path: { host_id: hostId }, query: window(range) }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
    // A host with no cost data is not an error worth retrying on the detail page.
    retry: false,
  });

/** Formats an amount in the price table's currency; falls back to USD when it is missing. */
export function formatMoney(value: number, currency: string | undefined, locale: string): string {
  const code = currency && /^[A-Z]{3}$/.test(currency) ? currency : "USD";
  // Small amounts need cents to be readable at all ($0.42/h); large ones do not ($1,204).
  const digits = Math.abs(value) < 100 ? 2 : 0;
  try {
    return new Intl.NumberFormat(locale, { style: "currency", currency: code, minimumFractionDigits: digits, maximumFractionDigits: digits }).format(value);
  } catch {
    return `${value.toFixed(digits)} ${code}`;
  }
}

/** Formats a 0..1 share as a percentage. */
export function formatShare(share: number, locale: string): string {
  const v = Number.isFinite(share) ? Math.max(0, Math.min(1, share)) : 0;
  return new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 }).format(v);
}
