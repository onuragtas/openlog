// Job monitoring (docs/contracts/api.md "Job monitoring", D-141). Query factories follow queries.ts;
// mutations are plain async functions for useMutation. The server enforces permissions.
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type JobMonitor = S["JobMonitor"];
export type JobMonitorInput = S["JobMonitorInput"];
export type JobMonitorState = S["JobMonitorState"];
export type JobScheduleKind = S["JobScheduleKind"];
export type JobRun = S["JobRun"];
export type JobRunStatus = S["JobRunStatus"];
export type JobSummary = S["JobSummary"];

/** A monitor's state changes when a job pings or the sweeper concludes a run, so once a minute is enough. */
export const JOBS_REFRESH_MS = 60_000;

export const jobMonitorsQuery = (withSummary = true) =>
  queryOptions({
    queryKey: ["jobs", "list", withSummary],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/jobs/monitors", { params: { query: { summary: withSummary } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: JOBS_REFRESH_MS,
  });

export const jobMonitorQuery = (id: string) =>
  queryOptions({
    queryKey: ["jobs", "monitor", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/jobs/monitors/{id}", { params: { path: { id } }, signal })),
    enabled: id !== "",
  });

/** The concluded runs of one monitor with the outcome summary over the range. */
export const jobRunsQuery = (id: string) =>
  queryOptions({
    queryKey: ["jobs", "runs", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/jobs/monitors/{id}/runs", { params: { path: { id } }, signal })),
    enabled: id !== "",
    placeholderData: keepPreviousData,
    refetchInterval: JOBS_REFRESH_MS,
  });

export async function createJobMonitor(input: JobMonitorInput): Promise<JobMonitor> {
  return unwrap(await api.POST("/api/v1/jobs/monitors", { body: input }));
}

export async function updateJobMonitor(id: string, input: JobMonitorInput): Promise<JobMonitor> {
  return unwrap(await api.PUT("/api/v1/jobs/monitors/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteJobMonitor(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/jobs/monitors/{id}", { params: { path: { id } } }));
}

/** A new ping token for a monitor whose URL leaked; the old URL stops working at once. */
export async function rotateJobMonitorToken(id: string): Promise<JobMonitor> {
  return unwrap(await api.POST("/api/v1/jobs/monitors/{id}/rotate", { params: { path: { id } } }));
}
