// Vulnerabilities (docs/contracts/api.md "Vulnerabilities", D-142): read-only, like every other telemetry
// read. Query factories follow queries.ts.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import type { ListState } from "@/lib/list";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type VulnGroup = S["VulnGroup"];
export type VulnFinding = S["VulnFinding"];
export type VulnSeverity = S["VulnSeverity"];
export type VulnCatalogStatus = S["VulnCatalogStatus"];

/** The matcher runs hourly, so the screens refresh slowly. */
export const VULN_REFRESH_MS = 5 * 60_000;

export type VulnSort = "severity" | "advisory" | "score" | "hosts";

export const vulnerabilitiesQuery = (severity: VulnSeverity | undefined, list: ListState<VulnSort>) =>
  queryOptions({
    queryKey: ["vulnerabilities", "list", severity ?? "", list],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/vulnerabilities", { params: { query: { ...list, ...(severity ? { severity } : {}) } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: VULN_REFRESH_MS,
  });

export type VulnFindingSort = "severity" | "score" | "advisory" | "package" | "host" | "version" | "ecosystem";

export const vulnerabilityQuery = (id: string, list: ListState<VulnFindingSort>) =>
  queryOptions({
    queryKey: ["vulnerabilities", "one", id, list],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/vulnerabilities/{id}", { params: { path: { id }, query: list }, signal })),
    placeholderData: keepPreviousData,
    enabled: id !== "",
  });

export const hostVulnerabilitiesQuery = (hostId: string, list: ListState<VulnFindingSort>) =>
  queryOptions({
    queryKey: ["vulnerabilities", "host", hostId, list],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/hosts/{host_id}/vulnerabilities", { params: { path: { host_id: hostId }, query: list }, signal })),
    enabled: hostId !== "",
    placeholderData: keepPreviousData,
  });

/** What the catalog holds: an empty list means something different when the feed never synced. */
export const vulnerabilityCatalogQuery = () =>
  queryOptions({
    queryKey: ["vulnerabilities", "catalog"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/vulnerabilities/catalog/status", { signal })),
    retry: false,
  });
