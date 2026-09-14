// Query factories for the Kubernetes endpoints (docs/contracts/api.md "Kubernetes", semantic-conventions §7).
// Keys include the range spec; the window is resolved inside queryFn like every other factory (api/queries.ts,
// api/containers.ts).
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import type { ServiceScope } from "@/lib/apm";
import { resolveRange, type RangeSpec } from "@/lib/time";
import { api, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type KubernetesCluster = S["KubernetesCluster"];
export type KubernetesClusterDetail = S["KubernetesClusterDetail"];
export type KubernetesKindHealth = S["KubernetesWorkloadKindCount"];
export type KubernetesEvent = S["KubernetesEvent"];
export type KubernetesNode = S["KubernetesNode"];
export type KubernetesWorkload = S["KubernetesWorkload"];
export type KubernetesHpa = S["KubernetesHPA"];
export type KubernetesWorkloadDetail = S["KubernetesWorkloadDetail"];
export type KubernetesPod = S["KubernetesPod"];
export type KubernetesPodContainer = S["KubernetesPodContainer"];
export type KubernetesPodDetail = S["KubernetesPodDetail"];
export type WorkloadTimeseries = S["KubernetesWorkloadTimeseries"];
export type PodTimeseries = S["KubernetesPodTimeseries"];
export type ApmServiceKubernetesPod = S["KubernetesServicePod"];
export type KubernetesWorkloadKind = S["KubernetesWorkloadKind"];
export type WorkloadHealth = S["KubernetesWorkloadHealth"];
export type PodPhase = S["KubernetesPodPhase"];
export type KubernetesEventType = S["KubernetesEventType"];

/** Identity of a workload (detail route parameters). */
export interface WorkloadRef {
  clusterUid: string;
  namespace: string;
  kind: string;
  name: string;
}

export interface WorkloadFilters {
  clusterUid?: string;
  namespace?: string;
  kind?: string;
  health?: string;
  q?: string;
}

export interface PodFilters {
  clusterUid?: string;
  namespace?: string;
  node?: string;
  workloadKind?: string;
  workloadName?: string;
  phase?: string;
  q?: string;
}

export interface EventFilters {
  clusterUid?: string;
  namespace?: string;
  type?: string;
  objectKind?: string;
  objectName?: string;
  objectUid?: string;
  limit?: number;
}

const REFRESH_MS = 60_000;

const rangeKey = (r: RangeSpec) => [r.range ?? "", r.from ?? "", r.to ?? ""];
const live = (r: RangeSpec) => (r.range || (!r.from && !r.to) ? REFRESH_MS : false);

function window(r: RangeSpec) {
  const { from, to } = resolveRange(r, Date.now());
  return { from: String(from), to: String(to) };
}

/** Empty filter values mean "no filter". */
const opt = (v?: string) => v || undefined;
/** URL filter values are validated by the router; the API answers 400 for anything else. */
const kindOpt = (v?: string) => opt(v) as KubernetesWorkloadKind | undefined;

const workloadPath = (w: WorkloadRef) => ({ cluster_uid: w.clusterUid, namespace: w.namespace, kind: w.kind as KubernetesWorkloadKind, name: w.name });

// openapi-fetch widens the MetricPoint tuples (sparklines, series) to number[]; the schema types are exact.

export const kubernetesClustersQuery = (range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-clusters", ...rangeKey(range)],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/kubernetes/clusters", { params: { query: window(range) }, signal })).clusters,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesClusterQuery = (clusterUid: string, range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-cluster", clusterUid, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/kubernetes/clusters/{cluster_uid}", { params: { path: { cluster_uid: clusterUid }, query: window(range) }, signal })) as KubernetesClusterDetail,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
    enabled: clusterUid !== "",
  });

export const kubernetesNodesQuery = (range: RangeSpec, f: { clusterUid?: string; q?: string } = {}) =>
  queryOptions({
    queryKey: ["k8s-nodes", ...rangeKey(range), f.clusterUid ?? "", f.q ?? ""],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/kubernetes/nodes", { params: { query: { ...window(range), cluster_uid: opt(f.clusterUid), q: opt(f.q), limit: 1000 } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesWorkloadsQuery = (range: RangeSpec, f: WorkloadFilters = {}) =>
  queryOptions({
    queryKey: ["k8s-workloads", ...rangeKey(range), f.clusterUid ?? "", f.namespace ?? "", f.kind ?? "", f.health ?? "", f.q ?? ""],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/kubernetes/workloads", {
          params: {
            query: {
              ...window(range),
              cluster_uid: opt(f.clusterUid),
              namespace: opt(f.namespace),
              kind: kindOpt(f.kind),
              health: opt(f.health) as WorkloadHealth | undefined,
              q: opt(f.q),
              limit: 1000,
            },
          },
          signal,
        }),
      ) as { workloads: KubernetesWorkload[]; total: number; step: string },
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesWorkloadQuery = (w: WorkloadRef, range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-workload", w.clusterUid, w.namespace, w.kind, w.name, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}", { params: { path: workloadPath(w), query: window(range) }, signal }),
      ) as KubernetesWorkloadDetail,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesWorkloadTimeseriesQuery = (w: WorkloadRef, range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-workload-timeseries", w.clusterUid, w.namespace, w.kind, w.name, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}/timeseries", { params: { path: workloadPath(w), query: window(range) }, signal }),
      ) as WorkloadTimeseries,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesPodsQuery = (range: RangeSpec, f: PodFilters = {}) =>
  queryOptions({
    queryKey: ["k8s-pods", ...rangeKey(range), f.clusterUid ?? "", f.namespace ?? "", f.node ?? "", f.workloadKind ?? "", f.workloadName ?? "", f.phase ?? "", f.q ?? ""],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/kubernetes/pods", {
          params: {
            query: {
              ...window(range),
              cluster_uid: opt(f.clusterUid),
              namespace: opt(f.namespace),
              node: opt(f.node),
              workload_kind: kindOpt(f.workloadKind),
              workload_name: opt(f.workloadName),
              phase: opt(f.phase) as PodPhase | undefined,
              q: opt(f.q),
              limit: 1000,
            },
          },
          signal,
        }),
      ),
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesPodQuery = (podUid: string, range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-pod", podUid, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/kubernetes/pods/{pod_uid}", { params: { path: { pod_uid: podUid }, query: window(range) }, signal })) as KubernetesPodDetail,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesPodTimeseriesQuery = (podUid: string, range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-pod-timeseries", podUid, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/kubernetes/pods/{pod_uid}/timeseries", { params: { path: { pod_uid: podUid }, query: window(range) }, signal })) as PodTimeseries,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesEventsQuery = (range: RangeSpec, f: EventFilters = {}) =>
  queryOptions({
    queryKey: ["k8s-events", ...rangeKey(range), f.clusterUid ?? "", f.namespace ?? "", f.type ?? "", f.objectKind ?? "", f.objectName ?? "", f.objectUid ?? "", f.limit ?? 100],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/kubernetes/events", {
          params: {
            query: {
              ...window(range),
              cluster_uid: opt(f.clusterUid),
              namespace: opt(f.namespace),
              type: opt(f.type) as KubernetesEventType | undefined,
              object_kind: opt(f.objectKind),
              object_name: opt(f.objectName),
              object_uid: opt(f.objectUid),
              limit: f.limit ?? 100,
            },
          },
          signal,
        }),
      ).events,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const kubernetesPodEventsQuery = (podUid: string, range: RangeSpec) =>
  queryOptions({
    queryKey: ["k8s-pod-events", podUid, ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/kubernetes/pods/{pod_uid}/events", { params: { path: { pod_uid: podUid }, query: window(range) }, signal })).events,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });

export const apmServiceKubernetesQuery = (s: ServiceScope, range: RangeSpec) =>
  queryOptions({
    queryKey: ["apm-service-kubernetes", s.service, s.namespace ?? " ", s.environment ?? " ", ...rangeKey(range)],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/apm/services/{service_name}/kubernetes", {
          params: { path: { service_name: s.service }, query: { namespace: s.namespace, environment: s.environment, ...window(range) } },
          signal,
        }),
      ).pods,
    placeholderData: keepPreviousData,
    refetchInterval: live(range),
  });
