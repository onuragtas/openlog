import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

export type VersionInfo = components["schemas"]["VersionInfo"];

/** GET /api/v1/version. The backend refreshes the release check at most daily; poll rarely. */
export const versionQuery = () =>
  queryOptions({
    queryKey: ["version"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/version", { signal })),
    staleTime: 15 * 60_000,
    refetchInterval: 60 * 60_000,
  });
