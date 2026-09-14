// Public status page and its incidents (docs/contracts/api.md "Status page").
import { queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type StatusPage = S["StatusPage"];
export type StatusIncident = S["StatusIncident"];
export type StatusIncidentInput = S["StatusIncidentInput"];
export const STATUS_COMPONENTS = ["ingest", "query_api", "alerting", "processing"] as const;
export const INCIDENT_STATUSES = {
  incident: ["investigating", "identified", "monitoring", "resolved"],
  maintenance: ["scheduled", "in_progress", "completed"],
} as const;
export const IMPACTS = ["none", "minor", "major", "critical"] as const;

export const statusPageQuery = () =>
  queryOptions({
    queryKey: ["status-page"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/status", { signal })),
    refetchInterval: 60_000,
  });

export const statusIncidentsQuery = () =>
  queryOptions({
    queryKey: ["status-page", "admin", "incidents"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/admin/status/incidents", { signal })).incidents,
  });

export async function createStatusIncident(body: StatusIncidentInput): Promise<StatusIncident> {
  return unwrap(await api.POST("/api/v1/admin/status/incidents", { body })).incident;
}

export async function addStatusIncidentUpdate(id: string, status: string, message: string): Promise<StatusIncident> {
  return unwrap(await api.POST("/api/v1/admin/status/incidents/{id}/updates", { params: { path: { id } }, body: { status, message } })).incident;
}

export async function deleteStatusIncident(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/admin/status/incidents/{id}", { params: { path: { id } } }));
}
