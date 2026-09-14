// Tail sampling policy API (docs/contracts/apm.md §4.2, docs/operations/tail-sampling.md). The server validates
// and enforces permissions; the UI only mirrors the rules.
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type TailSamplingPolicy = S["TailSamplingPolicy"];
export type TailSamplingRule = S["TailSamplingRule"];
export type TailSamplingPolicyState = S["TailSamplingPolicyState"];
export type TailSamplingPreview = S["TailSamplingPreview"];
export type TailSamplingRuleType = TailSamplingRule["type"];

export const RULE_TYPES: readonly TailSamplingRuleType[] = ["error", "latency", "service", "route", "attribute"];

export const tailSamplingQuery = () =>
  queryOptions({
    queryKey: ["tail-sampling"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/apm/sampling", { signal })),
  });

export async function putTailSampling(policy: TailSamplingPolicy, version: number): Promise<TailSamplingPolicyState> {
  return unwrap(await api.PUT("/api/v1/apm/sampling", { body: { policy, version } }));
}

export async function previewTailSampling(policy: TailSamplingPolicy, windowMinutes = 60): Promise<TailSamplingPreview> {
  return unwrap(await api.POST("/api/v1/apm/sampling/preview", { body: { policy, window_minutes: windowMinutes } }));
}

/** Keeps only the fields that belong to the rule's type (the server rejects unknown combinations). */
export function normalizeRule(r: TailSamplingRule): TailSamplingRule {
  const out: TailSamplingRule = { name: r.name.trim(), type: r.type };
  if (r.ratio !== undefined && r.ratio !== 1) out.ratio = r.ratio;
  switch (r.type) {
    case "error":
      if (r.service) out.service = r.service;
      break;
    case "latency":
      out.threshold_ms = r.threshold_ms;
      if (r.service) out.service = r.service;
      break;
    case "service":
      out.services = (r.services ?? []).map((s) => s.trim()).filter(Boolean);
      break;
    case "route":
      out.route = r.route;
      if (r.service) out.service = r.service;
      break;
    case "attribute":
      out.key = r.key;
      if (r.value) out.value = r.value;
      if (r.service) out.service = r.service;
      break;
  }
  return out;
}

export function normalizePolicy(p: TailSamplingPolicy): TailSamplingPolicy {
  return { ...p, rules: p.rules.map(normalizeRule) };
}
