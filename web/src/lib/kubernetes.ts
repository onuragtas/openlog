// Kubernetes display helpers (docs/contracts/api.md "Kubernetes", semantic-conventions §7).
import type { KubernetesCluster, KubernetesNode, KubernetesPod, KubernetesPodContainer, PodTimeseries, WorkloadTimeseries } from "@/api/kubernetes";
import { parseTimeParam } from "./time";
import type { ChartSeriesInput } from "./series";

export const WORKLOAD_KINDS = ["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet"] as const;
export type WorkloadKind = (typeof WORKLOAD_KINDS)[number];
export const WORKLOAD_HEALTHS = ["healthy", "degraded", "unavailable", "unknown"] as const;
export type WorkloadHealthParam = (typeof WORKLOAD_HEALTHS)[number];
export const POD_PHASES = ["Pending", "Running", "Succeeded", "Failed", "Unknown"] as const;
export type PodPhaseParam = (typeof POD_PHASES)[number];

/** Search of links out of Kubernetes screens: keep only the time range. */
export const rangeOnly = (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never;

export type K8sVariant = "success" | "warning" | "destructive" | "secondary" | "muted";

export function workloadHealthVariant(health: string): K8sVariant {
  switch (health) {
    case "healthy":
      return "success";
    case "degraded":
      return "warning";
    case "unavailable":
      return "destructive";
  }
  return "muted";
}

/** Pod reasons that are not failures (terminated containers of finished pods). */
const BENIGN_REASONS = new Set(["Completed", "ContainerCreating", "PodInitializing"]);

export interface PodStatusDisplay {
  /** Kubernetes status text ("CrashLoopBackOff", "Running", …) or "notReporting" (translated by the caller) */
  label: string;
  notReporting: boolean;
  variant: K8sVariant;
  /** Running pod failing its readiness checks */
  notReady: boolean;
}

/**
 * Badge of a pod: the API's computed status (reason, else phase). Pods believed running or pending whose data
 * stopped arriving are "not reporting"; reasons such as CrashLoopBackOff are failures.
 */
export function podStatus(p: Pick<KubernetesPod, "status" | "phase" | "reason" | "ready" | "reporting">): PodStatusDisplay {
  const label = p.status || p.reason || p.phase || "Unknown";
  const active = p.phase === "Running" || p.phase === "Pending" || p.phase === "";
  if (!p.reporting && active) return { label, notReporting: true, variant: "muted", notReady: false };
  const notReady = p.phase === "Running" && !p.ready;
  if (p.reason && !BENIGN_REASONS.has(p.reason)) return { label, notReporting: false, variant: "destructive", notReady };
  switch (p.phase) {
    case "Running":
      return { label, notReporting: false, variant: p.ready ? "success" : "warning", notReady };
    case "Pending":
      return { label, notReporting: false, variant: "warning", notReady };
    case "Succeeded":
      return { label, notReporting: false, variant: "secondary", notReady };
    case "Failed":
      return { label, notReporting: false, variant: "destructive", notReady };
  }
  return { label, notReporting: false, variant: "muted", notReady };
}

export type NodeReadyKey = "ready" | "notReady" | "unknown" | "notReporting";

export function nodeStatus(n: Pick<KubernetesNode, "ready" | "reporting">): { key: NodeReadyKey; variant: K8sVariant } {
  if (!n.reporting) return { key: "notReporting", variant: "muted" };
  if (n.ready === "true") return { key: "ready", variant: "success" };
  if (n.ready === "false") return { key: "notReady", variant: "destructive" };
  return { key: "unknown", variant: "muted" };
}

/** Node conditions other than Ready that are currently true (pressures, network unavailable). */
export const nodeProblems = (n: Pick<KubernetesNode, "conditions">) => n.conditions.filter((c) => c.condition !== "Ready" && c.status === "true").map((c) => c.condition);

export function containerStateVariant(c: Pick<KubernetesPodContainer, "state" | "ready" | "reason">): K8sVariant {
  if (c.reason && !BENIGN_REASONS.has(c.reason)) return "destructive";
  if (c.state === "running") return c.ready ? "success" : "warning";
  if (c.state === "waiting") return "warning";
  if (c.state === "terminated") return "secondary";
  return "muted";
}

export const eventVariant = (type: string): K8sVariant => (type === "Warning" ? "warning" : "secondary");

/** CPU in cores: "250m" below one core, else "1.5". */
export function formatCores(v: number | null | undefined, locale?: string): string {
  if (v == null || !Number.isFinite(v)) return "–";
  if (v !== 0 && Math.abs(v) < 1) {
    const m = Math.round(v * 1000);
    return m === 0 ? "<1m" : `${m}m`;
  }
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(v);
}

/** used / total (0..1), null when either is unknown. */
export function usageRatio(used: number | null | undefined, total: number | null | undefined): number | null {
  if (used == null || total == null || !(total > 0)) return null;
  return used / total;
}

export const totalPods = (c: Pick<KubernetesCluster, "pods">) => Object.values(c.pods).reduce((a, b) => a + b, 0);

/** Selected cluster: the URL's when it exists, else the first reporting cluster, else the first one. */
export function pickCluster<T extends Pick<KubernetesCluster, "cluster_uid" | "reporting">>(clusters: T[], selected?: string): T | undefined {
  return clusters.find((c) => c.cluster_uid === selected) ?? clusters.find((c) => c.reporting) ?? clusters[0];
}

/** Namespaces of the selected cluster, or of all clusters, sorted and unique. */
export function clusterNamespaces(clusters: Pick<KubernetesCluster, "cluster_uid" | "namespaces">[], clusterUid?: string): string[] {
  const set = new Set<string>();
  for (const c of clusters) if (!clusterUid || c.cluster_uid === clusterUid) c.namespaces.forEach((n) => set.add(n));
  return [...set].sort((a, b) => a.localeCompare(b));
}

/** Number of list filters set (search excluded), for "no match" versus "empty" messages. */
export const hasFilters = (f: Record<string, unknown>) => Object.values(f).some((v) => v !== undefined && v !== "");

/** Readiness text "2/3", with desired 0 shown as "0/0". */
export const replicasText = (ready: number, desired: number) => `${ready}/${desired}`;

/** Unix ms of a timeseries bound (numbers or RFC3339 strings). */
export function boundMs(v: number | string | undefined): number | undefined {
  if (v === undefined) return undefined;
  if (typeof v === "number") return v;
  return parseTimeParam(v) ?? undefined;
}

export const timeseriesBounds = (ts: { from: number | string; to: number | string } | undefined) => ({ from: boundMs(ts?.from), to: boundMs(ts?.to) });

const pts = (p: number[][] | undefined) => (p ?? []).map((x) => [x[0]!, x[1]!] as [number, number]);

/** Chart series of the workload detail page; labels are translated by the caller. */
export function workloadChartSeries(series: WorkloadTimeseries["series"], labels: Record<"cpu" | "memory" | "ready" | "desired" | "restarts", string>) {
  const s = (label: string, p: number[][] | undefined): ChartSeriesInput => ({ label, points: pts(p) });
  return {
    cpu: [s(labels.cpu, series.cpu_usage)],
    memory: [s(labels.memory, series.memory_working_set)],
    replicas: [s(labels.ready, series.ready), s(labels.desired, series.desired)],
    restarts: [s(labels.restarts, series.restarts)],
  };
}

/** Chart series of the pod detail page; labels are translated by the caller. */
export function podChartSeries(series: PodTimeseries["series"], labels: Record<"cpu" | "memory" | "receive" | "transmit" | "restarts", string>) {
  const s = (label: string, p: number[][] | undefined): ChartSeriesInput => ({ label, points: pts(p) });
  return {
    cpu: [s(labels.cpu, series.cpu_usage)],
    memory: [s(labels.memory, series.memory_working_set)],
    network: [s(labels.receive, series.network_receive), s(labels.transmit, series.network_transmit)],
    restarts: [s(labels.restarts, series.restarts)],
  };
}

/** Short container id (12 hex characters, runtime prefix removed). */
export const shortId = (id: string) => id.replace(/^[a-z]+:\/\//, "").slice(0, 12);
