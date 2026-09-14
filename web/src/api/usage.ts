// Usage, plans and quota status (docs/contracts/api.md "Usage and plans", docs/contracts/usage.md).
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type UsageOverview = S["UsageOverview"];
export type UsageDay = S["UsageDay"];
export type UsageTopEntry = S["UsageTopEntry"];
export type UsageStatus = S["UsageStatus"];
export type QuotaMetric = S["QuotaMetric"];
export type Plan = S["Plan"];
export type OrgPlan = S["OrgPlan"];
export type OrgPlanInput = S["OrgPlanInput"];
export type QuotaMetricName = QuotaMetric["metric"];
export type UsageSignal = "traces" | "logs" | "metrics";

/** "current", "previous" or "YYYY-MM". */
export type UsagePeriod = string;

export const usageOverviewQuery = (period: UsagePeriod) =>
  queryOptions({
    queryKey: ["usage", "overview", period],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/usage", { params: { query: { period } }, signal })),
  });

export const usageDailyQuery = (period: UsagePeriod) =>
  queryOptions({
    queryKey: ["usage", "daily", period],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/usage/daily", { params: { query: { period } }, signal })),
  });

export const usageTopQuery = (period: UsagePeriod, by: "service" | "host") =>
  queryOptions({
    queryKey: ["usage", "top", period, by],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/usage/top", { params: { query: { period, by, limit: 10 } }, signal })),
  });

/** Latest quota evaluation of the organization (banner). */
export const usageStatusQuery = () =>
  queryOptions({
    queryKey: ["usage", "status"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/usage/status", { signal })),
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
    retry: false,
  });

export const plansQuery = () =>
  queryOptions({
    queryKey: ["usage", "plans"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/plans", { signal })),
    staleTime: 5 * 60_000,
  });

/** Plan assignment of any organization (superadmin). */
export const orgPlanQuery = (org: string) =>
  queryOptions({
    queryKey: ["usage", "admin", "plan", org],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/admin/orgs/{org}/plan", { params: { path: { org } }, signal })),
  });

export async function putOrgPlan(org: string, body: OrgPlanInput): Promise<OrgPlan> {
  return unwrap(await api.PUT("/api/v1/admin/orgs/{org}/plan", { params: { path: { org } }, body }));
}

/** Downloads the invoice-period export (admins and owners). */
export async function downloadUsageExport(period: UsagePeriod, format: "csv" | "json", tenantId: string): Promise<void> {
  const res = await api.GET("/api/v1/usage/export", { params: { query: { period, format } }, parseAs: "blob" });
  const blob = unwrap(res) as Blob;
  if (typeof URL.createObjectURL !== "function") return; // tests (jsdom)
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `openlog-usage-${tenantId}-${period}.${format}`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/** Period choices: current, previous and the four months before. */
export function periodOptions(now = new Date()): UsagePeriod[] {
  const out: UsagePeriod[] = ["current", "previous"];
  for (let i = 2; i < 6; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    out.push(`${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`);
  }
  return out;
}

/** The metric with the highest percentage that is not ok, if any. */
export function worstMetric(metrics: readonly QuotaMetric[]): QuotaMetric | undefined {
  return metrics.filter((m) => m.level !== "ok").sort((a, b) => b.percent - a.percent)[0];
}
