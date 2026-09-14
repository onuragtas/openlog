// Dashboards API: api.md "Dashboards" (OPENLOG_AUTH_MODE=postgres only; every path answers 404 in static mode).
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, ApiError, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type Dashboard = S["Dashboard"];
export type DashboardSummary = S["DashboardSummary"];
export type DashboardInput = S["DashboardInput"];
export type DashboardPage = S["DashboardPage"];
export type DashboardPageInput = S["DashboardPageInput"];
export type DashboardWidget = S["DashboardWidget"];
export type DashboardWidgetInput = S["DashboardWidgetInput"];
export type DashboardWidgetLayout = S["DashboardWidgetLayout"];
export type DashboardVariable = S["DashboardVariable"];
export type DashboardVisualization = S["DashboardVisualization"];
export type DashboardVisibility = S["DashboardVisibility"];
export type DashboardUnit = S["DashboardUnit"];
export type DashboardThreshold = S["DashboardThreshold"];
export type DashboardWidgetOptions = S["DashboardWidgetOptions"];
export type DashboardExport = S["DashboardExport"];
export type DashboardImport = S["DashboardImport"];

/** Dashboards are not available (static auth mode answers 404 for the whole API group). */
export function isDashboardsUnavailable(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

export const dashboardsQuery = (q = "") =>
  queryOptions({
    queryKey: ["dashboards", "list", q],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards", { params: { query: { q: q || undefined } }, signal })).dashboards,
    placeholderData: keepPreviousData,
    retry: false,
  });

export const dashboardQuery = (id: string) =>
  queryOptions({
    queryKey: ["dashboards", "detail", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/dashboards/{id}", { params: { path: { id } }, signal })),
    enabled: id !== "",
    retry: false,
  });

export async function createDashboard(input: DashboardInput): Promise<Dashboard> {
  return unwrap(await api.POST("/api/v1/dashboards", { body: input }));
}

/** Replaces the whole document; `input.version` must be the version that was read (409 when it changed). */
export async function updateDashboard(id: string, input: DashboardInput): Promise<Dashboard> {
  return unwrap(await api.PUT("/api/v1/dashboards/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteDashboard(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/dashboards/{id}", { params: { path: { id } } }));
}

export async function duplicateDashboard(id: string, name?: string): Promise<Dashboard> {
  return unwrap(await api.POST("/api/v1/dashboards/{id}/duplicate", { params: { path: { id } }, body: name ? { name } : {} }));
}

export async function exportDashboard(id: string): Promise<DashboardExport> {
  return unwrap(await api.GET("/api/v1/dashboards/{id}/export", { params: { path: { id } } }));
}

export async function importDashboard(doc: DashboardImport): Promise<Dashboard> {
  return unwrap(await api.POST("/api/v1/dashboards/import", { body: doc }));
}

export async function addDashboardWidget(id: string, widget: DashboardWidgetInput, pageId?: string): Promise<Dashboard> {
  return unwrap(await api.POST("/api/v1/dashboards/{id}/widgets", { params: { path: { id } }, body: { page_id: pageId, widget } }));
}
