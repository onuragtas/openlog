// Integration settings API: docs/contracts/api.md "Integration settings". Settings reach the host's
// agent with its next sync (releases-updates.md §3); `host.revision` vs `host.applied_revision` tells
// whether the agent applied the current set. The server enforces permissions.
import { queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type IntegrationSetting = S["IntegrationSetting"];
export type IntegrationSettingInput = S["IntegrationSettingInput"];
export type IntegrationSettingsList = S["IntegrationSettingsList"];
export type IntegrationSettingsHost = S["IntegrationSettingsHost"];
export type IntegrationName = S["IntegrationName"];

/** Poll interval while the agent has not applied the current settings yet. */
export const PENDING_REFRESH_MS = 10_000;

export function isApplyPending(host: IntegrationSettingsHost | null | undefined): boolean {
  return !!host && !host.remote_config_disabled && host.revision !== host.applied_revision;
}

export const integrationSettingsQuery = (hostId: string) =>
  queryOptions({
    queryKey: ["integration-settings", hostId],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/integrations/settings", { params: { query: { host_id: hostId } }, signal })),
    refetchInterval: (q) => (isApplyPending(q.state.data?.host) ? PENDING_REFRESH_MS : false),
  });

export async function createIntegrationSetting(body: IntegrationSettingInput): Promise<IntegrationSetting> {
  return unwrap(await api.POST("/api/v1/integrations/settings", { body }));
}

export async function updateIntegrationSetting(id: string, body: IntegrationSettingInput): Promise<IntegrationSetting> {
  return unwrap(await api.PUT("/api/v1/integrations/settings/{id}", { params: { path: { id } }, body }));
}

export async function deleteIntegrationSetting(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/integrations/settings/{id}", { params: { path: { id } } }));
}
