// Synthetic monitoring (docs/contracts/api.md "Synthetic monitoring", D-132). Query factories follow
// queries.ts; mutations are plain async functions for useMutation. The server enforces permissions.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type SyntheticCheck = S["SyntheticCheck"];
export type SyntheticCheckInput = S["SyntheticCheckInput"];
export type SyntheticCheckListItem = S["SyntheticCheckListItem"];
export type SyntheticLocationStatus = S["SyntheticLocationStatus"];
export type SyntheticSummary = S["SyntheticSummary"];
export type SyntheticPoint = S["SyntheticPoint"];
export type SyntheticFailure = S["SyntheticFailure"];
export type SyntheticResults = S["SyntheticResults"];
export type SyntheticAssertionType = S["SyntheticAssertionType"];

/** A check runs at most every 30 s, so the list and the detail refresh once a minute. */
export const SYNTHETICS_REFRESH_MS = 60_000;

export const syntheticChecksQuery = (withSummary = true) =>
  queryOptions({
    queryKey: ["synthetics", "list", withSummary],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/synthetics/checks", { params: { query: { summary: withSummary } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: SYNTHETICS_REFRESH_MS,
  });

export const syntheticCheckQuery = (id: string) =>
  queryOptions({
    queryKey: ["synthetics", "check", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/synthetics/checks/{id}", { params: { path: { id } }, signal })),
    enabled: id !== "",
  });

/** Uptime, latency percentiles, the series behind them and the recent failures. */
export const syntheticResultsQuery = (id: string) =>
  queryOptions({
    queryKey: ["synthetics", "results", id],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/synthetics/checks/{id}/results", { params: { path: { id } }, signal })),
    enabled: id !== "",
    placeholderData: keepPreviousData,
    refetchInterval: SYNTHETICS_REFRESH_MS,
  });

export async function createSyntheticCheck(input: SyntheticCheckInput): Promise<SyntheticCheck> {
  return unwrap(await api.POST("/api/v1/synthetics/checks", { body: input }));
}

export async function updateSyntheticCheck(id: string, input: SyntheticCheckInput): Promise<SyntheticCheck> {
  return unwrap(await api.PUT("/api/v1/synthetics/checks/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteSyntheticCheck(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/synthetics/checks/{id}", { params: { path: { id } } }));
}
