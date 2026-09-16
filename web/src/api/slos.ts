// Service level objectives (docs/contracts/slo.md, api.md "Service level objectives"). Query factories follow
// queries.ts; mutations are plain async functions for useMutation. The server enforces permissions.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type Slo = S["Slo"];
export type SloInput = S["SloInput"];
export type SloListItem = S["SloListItem"];
export type SloStatus = S["SloStatus"];
export type SloBudget = S["SloBudget"];
export type SloResults = S["SloResults"];
export type SloBurnWindowResult = S["SloBurnWindowResult"];
export type SloPoint = S["SloPoint"];
export type SloSliType = S["SloSliType"];

/** Budgets move slowly; the list and the detail refresh once a minute. */
export const SLO_REFRESH_MS = 60_000;

export const slosQuery = (withStatus = true) =>
  queryOptions({
    queryKey: ["slos", "list", withStatus],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/slos", { params: { query: { status: withStatus } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: SLO_REFRESH_MS,
  });

export const sloQuery = (id: string) =>
  queryOptions({
    queryKey: ["slos", "slo", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/slos/{id}", { params: { path: { id } }, signal })),
    enabled: id !== "",
  });

/** Status, burn windows and burndown series over the SLO's rolling window. */
export const sloResultsQuery = (id: string) =>
  queryOptions({
    queryKey: ["slos", "results", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/slos/{id}/results", { params: { path: { id } }, signal })),
    enabled: id !== "",
    placeholderData: keepPreviousData,
    refetchInterval: SLO_REFRESH_MS,
  });

export async function createSlo(input: SloInput): Promise<Slo> {
  return unwrap(await api.POST("/api/v1/slos", { body: input }));
}

export async function updateSlo(id: string, input: SloInput): Promise<Slo> {
  return unwrap(await api.PUT("/api/v1/slos/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteSlo(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/slos/{id}", { params: { path: { id } } }));
}
