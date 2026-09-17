// Cloud connections (docs/contracts/api.md "Cloud connections", D-135). Query factories follow queries.ts;
// mutations are plain async functions for useMutation. The server enforces permissions, and a credential is
// never part of a response: `credentials_set` is all the UI ever learns about a stored secret.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type CloudConnection = S["CloudConnection"];
export type CloudConnectionInput = S["CloudConnectionInput"];
export type CloudConnectionList = S["CloudConnectionList"];
export type CloudScopeStatus = S["CloudScopeStatus"];
export type CloudProvider = S["CloudProvider"];
export type CloudProviderName = S["CloudProviderName"];
export type CloudProviderCatalog = S["CloudProviderCatalog"];
export type CloudCredentials = S["CloudCredentials"];
export type CloudCredentialField = S["CloudCredentialField"];
export type CloudRun = S["CloudRun"];
export type CloudRunList = S["CloudRunList"];
export type CloudServiceRun = S["CloudServiceRun"];
export type CloudTestRequest = S["CloudTestRequest"];
export type CloudTestResult = S["CloudTestResult"];

/** A connection polls at most once a minute, so the list and the detail refresh once a minute. */
export const CLOUD_REFRESH_MS = 60_000;

/** The provider catalog is compiled into the binary, so it is cached for the session. */
export const cloudProvidersQuery = () =>
  queryOptions({
    queryKey: ["cloud", "providers"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/cloud/providers", { signal })),
    staleTime: 60 * 60_000,
  });

export const cloudConnectionsQuery = () =>
  queryOptions({
    queryKey: ["cloud", "connections"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/cloud/connections", { signal })),
    placeholderData: keepPreviousData,
    refetchInterval: CLOUD_REFRESH_MS,
  });

export const cloudConnectionQuery = (id: string) =>
  queryOptions({
    queryKey: ["cloud", "connection", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/cloud/connections/{id}", { params: { path: { id } }, signal })),
    enabled: id !== "",
  });

/** The recent polls of a connection; `scope` limits them to one region, subscription or project. */
export const cloudRunsQuery = (id: string, scope?: string) =>
  queryOptions({
    queryKey: ["cloud", "runs", id, scope ?? ""],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/cloud/connections/{id}/runs", { params: { path: { id }, query: { scope } }, signal })),
    enabled: id !== "",
    placeholderData: keepPreviousData,
    refetchInterval: CLOUD_REFRESH_MS,
  });

export async function createCloudConnection(input: CloudConnectionInput): Promise<CloudConnection> {
  return unwrap(await api.POST("/api/v1/cloud/connections", { body: input }));
}

export async function updateCloudConnection(id: string, input: CloudConnectionInput): Promise<CloudConnection> {
  return unwrap(await api.PUT("/api/v1/cloud/connections/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteCloudConnection(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/cloud/connections/{id}", { params: { path: { id } } }));
}

/**
 * Makes one provider call and stores nothing. A credential the provider rejects comes back as
 * `{ok: false, error}` with HTTP 200, so the form shows it inline instead of as a request failure.
 */
export async function testCloudConnection(body: CloudTestRequest): Promise<CloudTestResult> {
  return unwrap(await api.POST("/api/v1/cloud/connections/test", { body }));
}
