// How full the ClickHouse disks are and the levels they are reported at (docs/operations/disk-space.md).
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type DiskSpace = S["DiskSpace"];
export type DiskStatus = S["DiskStatus"];
export type DiskSpaceSettingsInput = S["DiskSpaceSettingsInput"];
export type DiskSpaceLevel = keyof S["DiskSpaceSettingsInput"];

/** The order the levels are shown in, which is also the order they take effect in. */
export const DISK_SPACE_LEVELS: readonly DiskSpaceLevel[] = ["warn_percent", "high_percent", "hysteresis"];

/** The last measurement and the levels in force. Admins and owners only; the server refuses anyone else. */
export const diskSpaceQuery = () =>
  queryOptions({
    queryKey: ["storage", "disk"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/storage/disk", { signal })),
    // The leader measures every few minutes, so re-asking on every mount only adds load. The banner renders on
    // every page, which is exactly where that would show.
    staleTime: 60_000,
  });

export async function putDiskSpaceSettings(body: DiskSpaceSettingsInput): Promise<DiskSpace> {
  return unwrap(await api.PUT("/api/v1/storage/disk/settings", { body }));
}

export async function deleteDiskSpaceSettings(): Promise<DiskSpace> {
  return unwrap(await api.DELETE("/api/v1/storage/disk/settings", {}));
}
