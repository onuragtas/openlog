// Metric correlation (docs/contracts/api.md "Metric correlation", D-146): what else changed in a window.
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type MetricCorrelation = S["MetricCorrelation"];
export type MetricCorrelations = S["MetricCorrelations"];

export interface CorrelateParams {
  from: string;
  to: string;
  hostId?: string;
  limit?: number;
}

export const correlateQuery = ({ from, to, hostId, limit }: CorrelateParams) =>
  queryOptions({
    queryKey: ["metrics", "correlate", from, to, hostId ?? "", limit ?? 0],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/metrics/correlate", {
          params: { query: { from, to, ...(hostId ? { host_id: hostId } : {}), ...(limit ? { limit } : {}) } },
          signal,
        }),
      ),
    enabled: from !== "" && to !== "",
    // The answer is about a window that has already happened, so it does not change under the reader.
    staleTime: 5 * 60_000,
  });
