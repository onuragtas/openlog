import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

export type VersionInfo = components["schemas"]["VersionInfo"];
export type UpdateRequest = components["schemas"]["UpdateRequest"];

/** Poll interval while a check/update request or an update is in progress (and while reconnecting). */
export const FOLLOW_REFRESH_MS = 2_000;
const IDLE_REFRESH_MS = 60 * 60_000;

/** An update request waits for or is handled by openlog-updater, or the updater is updating. */
export function updateInProgress(v: VersionInfo | undefined): boolean {
  if (!v) return false;
  const state = v.update_requests?.latest?.state;
  return state === "pending" || state === "running" || v.updater?.state === "updating";
}

/**
 * GET /api/v1/version. The backend refreshes the release check at most daily; poll rarely, except
 * while an update request is in progress or `follow` is set (the page follows an update it started,
 * including while the server restarts).
 */
/**
 * Whether the update request followId is still being followed: until it finished and the server
 * answers again (serverAnswers false while the api restarts during the update).
 */
export function followingRequest(v: VersionInfo | undefined, followId: string | null, serverAnswers: boolean): boolean {
  if (followId === null) return false;
  const latest = v?.update_requests?.latest;
  const finished =
    serverAnswers && latest?.id === followId && latest.state !== "pending" && latest.state !== "running" && v?.updater?.state !== "updating";
  return !finished;
}

export const versionQuery = ({ followId = null }: { followId?: string | null } = {}) =>
  queryOptions({
    queryKey: ["version"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/version", { signal })),
    staleTime: 15 * 60_000,
    refetchInterval: (q) =>
      followingRequest(q.state.data, followId, q.state.status !== "error") || updateInProgress(q.state.data)
        ? FOLLOW_REFRESH_MS
        : IDLE_REFRESH_MS,
  });

/** POST /api/v1/version/check: runs the release check now and asks openlog-updater to check. */
export async function requestUpdateCheck(): Promise<VersionInfo> {
  return unwrap(await api.POST("/api/v1/version/check"));
}

/** POST /api/v1/version/update: asks openlog-updater to install targetVersion now. */
export async function requestUpdateApply(targetVersion: string, ignoreMaintenanceWindow: boolean): Promise<UpdateRequest> {
  return unwrap(
    await api.POST("/api/v1/version/update", { body: { target_version: targetVersion, ignore_maintenance_window: ignoreMaintenanceWindow } }),
  );
}
