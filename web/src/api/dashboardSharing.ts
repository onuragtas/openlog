// Dashboard version history, sharing settings, share links, scheduled reports and the public share link endpoints
// (api.md "Dashboards" › "Version history" … "Scheduled reports"; D-086, D-087).
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { Dashboard } from "./dashboards";
import type { OqlResult } from "./oql";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type DashboardSettings = S["DashboardSettings"];
export type DashboardVersion = S["DashboardVersion"];
export type DashboardVersionList = S["DashboardVersionList"];
export type DashboardVersionDetail = S["DashboardVersionDetail"];
export type DashboardDiff = S["DashboardDiff"];
export type DashboardShare = S["DashboardShare"];
export type DashboardShareInput = S["DashboardShareInput"];
export type DashboardShareCreated = S["DashboardShareCreated"];
export type DashboardReport = S["DashboardReport"];
export type DashboardReportInput = S["DashboardReportInput"];
export type SharedDashboard = S["SharedDashboard"];
export type SharedDashboardWidget = S["SharedDashboardWidget"];

export const dashboardSettingsQuery = () =>
  queryOptions({
    queryKey: ["dashboards", "settings"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards/settings", { signal })),
    retry: false,
  });

export async function updateDashboardSettings(body: S["DashboardSettingsInput"]): Promise<DashboardSettings> {
  return unwrap(await api.PUT("/api/v1/dashboards/settings", { body }));
}

export const dashboardVersionsQuery = (id: string) =>
  queryOptions({
    queryKey: ["dashboards", "versions", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards/{id}/versions", { params: { path: { id } }, signal })),
    retry: false,
  });

export const dashboardVersionQuery = (id: string, version: number | null) =>
  queryOptions({
    queryKey: ["dashboards", "versions", id, version],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards/{id}/versions/{version}", { params: { path: { id, version: version ?? 0 } }, signal })),
    enabled: version !== null,
    placeholderData: keepPreviousData,
    retry: false,
  });

/** Saves a stored version as the new current version; `current` is the version that was read (409 when it changed). */
export async function restoreDashboardVersion(id: string, version: number, current: number): Promise<Dashboard> {
  return unwrap(await api.POST("/api/v1/dashboards/{id}/versions/{version}/restore", { params: { path: { id, version } }, body: { version: current } }));
}

export const dashboardSharesQuery = (id: string) =>
  queryOptions({
    queryKey: ["dashboards", "shares", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards/{id}/shares", { params: { path: { id } }, signal })),
    retry: false,
  });

export async function createDashboardShare(id: string, body: DashboardShareInput): Promise<DashboardShareCreated> {
  return unwrap(await api.POST("/api/v1/dashboards/{id}/shares", { params: { path: { id } }, body }));
}

export async function revokeDashboardShare(id: string, shareId: string): Promise<DashboardShare> {
  return unwrap(await api.DELETE("/api/v1/dashboards/{id}/shares/{share_id}", { params: { path: { id, share_id: shareId } } }));
}

export const dashboardReportsQuery = (id: string) =>
  queryOptions({
    queryKey: ["dashboards", "reports", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards/{id}/reports", { params: { path: { id } }, signal })).reports,
    retry: false,
  });

export async function createDashboardReport(id: string, body: DashboardReportInput): Promise<DashboardReport> {
  return unwrap(await api.POST("/api/v1/dashboards/{id}/reports", { params: { path: { id } }, body }));
}

export async function updateDashboardReport(id: string, reportId: string, body: DashboardReportInput): Promise<DashboardReport> {
  return unwrap(await api.PUT("/api/v1/dashboards/{id}/reports/{report_id}", { params: { path: { id, report_id: reportId } }, body }));
}

export async function deleteDashboardReport(id: string, reportId: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/dashboards/{id}/reports/{report_id}", { params: { path: { id, report_id: reportId } } }));
}

// ---- public share link endpoints (no session) ----

export const sharedDashboardQuery = (token: string) =>
  queryOptions({
    queryKey: ["shared-dashboard", token],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/public/dashboards/{token}", { params: { path: { token } }, signal })),
    retry: false,
    staleTime: 5 * 60_000,
  });

/** Result of a shared widget; relative ranges refresh every minute. */
export const sharedWidgetResultQuery = (token: string, widgetId: string, refetchMs: number | false) =>
  queryOptions({
    queryKey: ["shared-dashboard", token, "widget", widgetId],
    // openapi-fetch widens the [ms, value] point tuple; the schema type is exact.
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/public/dashboards/{token}/widgets/{widget_id}/result", { params: { path: { token, widget_id: widgetId } }, signal })) as OqlResult,
    refetchInterval: refetchMs,
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: 30_000,
  });

/** Absolute URL of a share link page for copying. */
export function shareUrl(path: string, origin: string = globalThis.location?.origin ?? ""): string {
  return `${origin}${path}`;
}
