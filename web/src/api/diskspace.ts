// How full the ClickHouse disks are and the levels they are reported at (docs/operations/disk-space.md).
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type DiskSpace = S["DiskSpace"];
export type DiskStatus = S["DiskStatus"];
export type DiskSpaceSettingsInput = S["DiskSpaceSettingsInput"];

/** The reporting levels. These delete nothing: they only decide when a disk is called filling or critical. */
export const DISK_ALERT_LEVELS = ["warn_percent", "high_percent", "hysteresis"] as const;
export type DiskAlertLevel = (typeof DISK_ALERT_LEVELS)[number];

/**
 * The deletion levels, deliberately a separate list from the reporting ones so no screen can present them as the
 * same kind of setting. shed_enabled is not here because it is a switch, not a number.
 */
export const DISK_SHED_LEVELS = ["shed_start_percent", "shed_stop_percent", "shed_min_partitions", "shed_max_drops_per_run"] as const;
export type DiskShedLevel = (typeof DISK_SHED_LEVELS)[number];

/** The order data is given up in when a disk fills; mirrors diskspace.ShedOrder. */
export const DISK_SHED_ORDER = [
  "profiles",
  "exemplars",
  "traces",
  "sessionSamples",
  "logs",
  "queryStats",
  "rawMetrics",
] as const;

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
