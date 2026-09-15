// "Add data" page (routes/add-data.tsx): GET /api/v1/onboarding and the verification polls. The license key a user
// pastes or creates for an install is never sent here; it only lives in component state.
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

export type Onboarding = components["schemas"]["Onboarding"];

export const onboardingQuery = () =>
  queryOptions({
    queryKey: ["onboarding"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/onboarding", { signal })),
    staleTime: 5 * 60_000,
  });

/** Poll interval of the "Waiting for data…" step. */
export const VERIFY_POLL_MS = 5_000;
/** After this long without data the verification step opens the troubleshooting tips (it keeps polling). */
export const VERIFY_TIMEOUT_MS = 5 * 60_000;

/**
 * What already reported when the flow started (host ids, cluster uids or APM service names), so the verification step
 * recognizes the new entity. Fetched once per flow (`startedAt`).
 */
export const baselineQuery = (kind: "host" | "kubernetes" | "apm", startedAt: number) =>
  queryOptions({
    queryKey: ["onboarding", "baseline", kind, startedAt],
    queryFn: async ({ signal }): Promise<string[]> => {
      const to = Date.now();
      const from = String(to - 30 * 60_000);
      if (kind === "host") return unwrap(await api.GET("/api/v1/hosts", { params: { query: { limit: 1000 } }, signal })).hosts.map((h) => h.host_id);
      if (kind === "kubernetes") {
        return unwrap(await api.GET("/api/v1/kubernetes/clusters", { params: { query: { from, to: String(to) } }, signal })).clusters.map((c) => c.cluster_uid);
      }
      return unwrap(await api.GET("/api/v1/apm/services", { params: { query: { from, to: String(to) } }, signal })).services.map((s) => s.service_name);
    },
    staleTime: Infinity,
    gcTime: 60 * 60_000,
    retry: 2,
  });

export const verifyHostsQuery = (intervalMs = VERIFY_POLL_MS) =>
  queryOptions({
    queryKey: ["onboarding", "verify", "hosts"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/hosts", { params: { query: { limit: 1000 } }, signal })).hosts,
    refetchInterval: intervalMs,
    staleTime: 0,
  });

export const verifyApmServicesQuery = (intervalMs = VERIFY_POLL_MS) =>
  queryOptions({
    queryKey: ["onboarding", "verify", "apm"],
    queryFn: async ({ signal }) => {
      const to = Date.now();
      const from = to - 30 * 60_000;
      return unwrap(await api.GET("/api/v1/apm/services", { params: { query: { from: String(from), to: String(to) } }, signal })).services;
    },
    refetchInterval: intervalMs,
    staleTime: 0,
  });

export const verifyKubernetesQuery = (intervalMs = VERIFY_POLL_MS) =>
  queryOptions({
    queryKey: ["onboarding", "verify", "kubernetes"],
    queryFn: async ({ signal }) => {
      const to = Date.now();
      const from = to - 30 * 60_000;
      return unwrap(await api.GET("/api/v1/kubernetes/clusters", { params: { query: { from: String(from), to: String(to) } }, signal })).clusters;
    },
    refetchInterval: intervalMs,
    staleTime: 0,
  });

export interface VerifyLogsFilter {
  /** service.name of OTLP senders */
  service?: string;
  /** openlog.log.source of infra agent records: file, journald, unified_log (macOS), windows_event_log or container */
  source?: "file" | "journald" | "unified_log" | "windows_event_log" | "container";
}

/** Newest log record matching the filter since `since` (unix ms). */
export const verifyLogsQuery = (filter: VerifyLogsFilter, since: number, intervalMs = VERIFY_POLL_MS) =>
  queryOptions({
    queryKey: ["onboarding", "verify", "logs", filter.service ?? "", filter.source ?? "", since],
    queryFn: async ({ signal }) => {
      const to = Math.max(Date.now(), since + 1);
      // `attr.<key>` filters are flat query keys (openapi-fetch would serialize an object as deepObject).
      const query = {
        from: String(since),
        to: String(to),
        limit: 1,
        ...(filter.service ? { service: filter.service } : {}),
        ...(filter.source ? { "attr.openlog.log.source": filter.source } : {}),
      };
      return unwrap(await api.GET("/api/v1/logs", { params: { query: query as { from: string; to: string; limit: number } }, signal })).logs;
    },
    refetchInterval: intervalMs,
    staleTime: 0,
  });
