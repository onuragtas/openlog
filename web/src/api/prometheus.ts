// Prometheus targets (D-137): the latest `up` point of every scraped target, from the metrics explorer API.
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { MetricQueryResponse } from "./explorer";
import { PROM_FILTERS, PROM_GROUP_BY, prometheusTargets } from "@/lib/prometheus";

/** The window the card looks at: a target that sent no `up` in it is no longer scraped (removed or agent gone). */
const WINDOW_MS = 15 * 60_000;
/** Series the API returns at most (MetricQueryRequest.limit maximum). */
export const PROMETHEUS_MAX_TARGETS = 200;

export const prometheusTargetsQuery = () =>
  queryOptions({
    queryKey: ["prometheus", "targets"],
    queryFn: async ({ signal }) => {
      const to = Date.now();
      const body = { metric: "up", from: to - WINDOW_MS, to, filters: PROM_FILTERS, aggregation: "last" as const, group_by: [...PROM_GROUP_BY], step: "60s", limit: PROMETHEUS_MAX_TARGETS };
      const data = unwrap(await api.POST("/api/v1/metrics/query", { body, signal })) as MetricQueryResponse;
      return { targets: prometheusTargets(data.series), truncated: data.truncated };
    },
    refetchInterval: 60_000,
    // A tenant without the metric yet answers 404 (unknown metric): that is "no targets", not an error.
    retry: false,
  });

