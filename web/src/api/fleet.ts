// Fleet (agent updates) API: docs/contracts/api.md "Fleet (agent updates)". Query factories follow
// queries.ts; mutations are plain async functions for useMutation. The server enforces permissions.
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type FleetPolicy = S["FleetPolicy"];
export type FleetPolicyInput = S["FleetPolicyInput"];
export type FleetSummary = S["FleetSummary"];
export type FleetHost = S["FleetHost"];
export type FleetRollout = S["FleetRollout"];
export type FleetOverride = S["FleetOverride"];
export type MaintenanceWindow = S["MaintenanceWindow"];
export type FleetMode = S["FleetMode"];
export type FleetTarget = S["FleetTarget"];
export type FleetChannel = S["FleetChannel"];
export type FleetUpdateState = S["FleetUpdateState"];
export type FleetRolloutState = S["FleetRolloutState"];
export type FleetHostStatus = FleetHost["status"];

/** While a rollout is active the page follows it closely; otherwise it refreshes slowly. */
export const ACTIVE_REFRESH_MS = 3_000;
const IDLE_REFRESH_MS = 30_000;

export const fleetSummaryQuery = () =>
  queryOptions({
    queryKey: ["fleet", "summary"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/fleet/summary", { signal })),
    refetchInterval: (q) => (q.state.data?.current_rollout?.state === "active" ? ACTIVE_REFRESH_MS : IDLE_REFRESH_MS),
  });

export const fleetPolicyQuery = () =>
  queryOptions({
    queryKey: ["fleet", "policy"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/fleet/policy", { signal })),
  });

export const fleetRolloutsQuery = () =>
  queryOptions({
    queryKey: ["fleet", "rollouts"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/fleet/rollouts", { params: { query: { limit: 10 } }, signal })).rollouts,
    refetchInterval: IDLE_REFRESH_MS,
  });

export interface FleetHostFilter {
  q?: string;
  version?: string;
  state?: string;
}

export const FLEET_HOSTS_PAGE = 500;

export const fleetHostsQuery = (f: FleetHostFilter) =>
  infiniteQueryOptions({
    queryKey: ["fleet", "hosts", f.q ?? "", f.version ?? "", f.state ?? ""],
    queryFn: async ({ pageParam, signal }) =>
      unwrap(
        await api.GET("/api/v1/fleet/hosts", {
          params: {
            query: {
              q: f.q || undefined,
              version: f.version || undefined,
              state: (f.state || undefined) as FleetUpdateState | undefined,
              limit: FLEET_HOSTS_PAGE,
              cursor: pageParam || undefined,
            },
          },
          signal,
        }),
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: IDLE_REFRESH_MS,
  });

export async function saveFleetPolicy(policy: FleetPolicyInput): Promise<FleetPolicy> {
  return unwrap(await api.PUT("/api/v1/fleet/policy", { body: policy }));
}

export async function setHostOverride(hostId: string, action: "hold" | "pin", version?: string): Promise<FleetOverride> {
  return unwrap(await api.PUT("/api/v1/fleet/hosts/{host_id}/override", { params: { path: { host_id: hostId } }, body: { action, version } }));
}

export async function clearHostOverride(hostId: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/fleet/hosts/{host_id}/override", { params: { path: { host_id: hostId } } }));
}

export async function pauseRollout(id: string): Promise<FleetRollout> {
  return unwrap(await api.POST("/api/v1/fleet/rollouts/{id}/pause", { params: { path: { id } } }));
}

export async function resumeRollout(id: string): Promise<FleetRollout> {
  return unwrap(await api.POST("/api/v1/fleet/rollouts/{id}/resume", { params: { path: { id } } }));
}

export async function rollbackFleet(toVersion: string): Promise<FleetRollout> {
  return unwrap(await api.POST("/api/v1/fleet/rollback", { body: { to_version: toVersion } }));
}
