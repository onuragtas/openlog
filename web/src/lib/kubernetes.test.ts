import { describe, expect, it } from "vitest";
import type { KubernetesCluster } from "@/api/kubernetes";
import {
  boundMs,
  clusterNamespaces,
  containerStateVariant,
  formatCores,
  hasFilters,
  nodeProblems,
  nodeStatus,
  pickCluster,
  podChartSeries,
  podStatus,
  shortId,
  totalPods,
  usageRatio,
  workloadChartSeries,
  workloadHealthVariant,
} from "./kubernetes";

const pod = { status: "Running", phase: "Running", reason: "", ready: true, reporting: true };

function cluster(over: Partial<KubernetesCluster>): KubernetesCluster {
  return {
    cluster_uid: "u1", cluster_name: "prod", version: "v1.30.0", first_seen: "", last_seen: "", reporting: true, nodes: 3, nodes_ready: 3,
    pods: { Pending: 1, Running: 5, Succeeded: 2, Failed: 0, Unknown: 0 }, pods_not_ready: 0, workloads: 4, workloads_unhealthy: 0, namespaces: ["shop", "default"],
    ...over,
  };
}

describe("kubernetes status helpers", () => {
  it("maps workload health to badge variants", () => {
    expect(workloadHealthVariant("healthy")).toBe("success");
    expect(workloadHealthVariant("degraded")).toBe("warning");
    expect(workloadHealthVariant("unavailable")).toBe("destructive");
    expect(workloadHealthVariant("unknown")).toBe("muted");
    expect(workloadHealthVariant("")).toBe("muted");
  });

  it("derives the pod badge from status, reason, readiness and reporting", () => {
    expect(podStatus(pod)).toEqual({ label: "Running", notReporting: false, variant: "success", notReady: false });
    expect(podStatus({ ...pod, ready: false })).toMatchObject({ variant: "warning", notReady: true });
    expect(podStatus({ ...pod, status: "CrashLoopBackOff", reason: "CrashLoopBackOff", ready: false })).toMatchObject({ label: "CrashLoopBackOff", variant: "destructive", notReady: true });
    expect(podStatus({ ...pod, reporting: false })).toMatchObject({ notReporting: true, variant: "muted" });
    // Finished pods keep their phase when their data stops.
    expect(podStatus({ ...pod, status: "Succeeded", phase: "Succeeded", ready: false, reporting: false })).toMatchObject({ label: "Succeeded", notReporting: false, variant: "secondary", notReady: false });
    expect(podStatus({ ...pod, status: "Pending", phase: "Pending", ready: false })).toMatchObject({ variant: "warning", notReady: false });
    expect(podStatus({ ...pod, status: "Failed", phase: "Failed", ready: false })).toMatchObject({ variant: "destructive" });
    expect(podStatus({ ...pod, status: "", phase: "", reason: "" }).label).toBe("Unknown");
  });

  it("derives node readiness and pressure conditions", () => {
    expect(nodeStatus({ ready: "true", reporting: true })).toEqual({ key: "ready", variant: "success" });
    expect(nodeStatus({ ready: "false", reporting: true }).key).toBe("notReady");
    expect(nodeStatus({ ready: "unknown", reporting: true }).key).toBe("unknown");
    expect(nodeStatus({ ready: "true", reporting: false }).key).toBe("notReporting");
    expect(
      nodeProblems({
        conditions: [
          { condition: "Ready", status: "true" },
          { condition: "MemoryPressure", status: "true" },
          { condition: "DiskPressure", status: "false" },
        ],
      }),
    ).toEqual(["MemoryPressure"]);
  });

  it("maps container states", () => {
    expect(containerStateVariant({ state: "running", ready: true, reason: "" })).toBe("success");
    expect(containerStateVariant({ state: "running", ready: false, reason: "" })).toBe("warning");
    expect(containerStateVariant({ state: "waiting", ready: false, reason: "CrashLoopBackOff" })).toBe("destructive");
    expect(containerStateVariant({ state: "terminated", ready: false, reason: "Completed" })).toBe("secondary");
  });
});

describe("kubernetes formatting and filters", () => {
  it("formats CPU cores as millicores below one core", () => {
    expect(formatCores(0.25, "en")).toBe("250m");
    expect(formatCores(0.0001, "en")).toBe("<1m");
    expect(formatCores(1.5, "en")).toBe("1.5");
    expect(formatCores(0, "en")).toBe("0");
    expect(formatCores(null)).toBe("–");
    expect(usageRatio(2, 8)).toBe(0.25);
    expect(usageRatio(2, null)).toBeNull();
    expect(usageRatio(null, 8)).toBeNull();
  });

  it("picks the selected, else the first reporting cluster", () => {
    const a = cluster({ cluster_uid: "a", reporting: false });
    const b = cluster({ cluster_uid: "b" });
    expect(pickCluster([a, b], "a")).toBe(a);
    expect(pickCluster([a, b], "missing")).toBe(b);
    expect(pickCluster([a])).toBe(a);
    expect(pickCluster([])).toBeUndefined();
    expect(totalPods(b)).toBe(8);
  });

  it("lists namespaces of one or all clusters", () => {
    const cs = [cluster({ cluster_uid: "a", namespaces: ["shop", "kube-system"] }), cluster({ cluster_uid: "b", namespaces: ["shop", "billing"] })];
    expect(clusterNamespaces(cs)).toEqual(["billing", "kube-system", "shop"]);
    expect(clusterNamespaces(cs, "b")).toEqual(["billing", "shop"]);
    expect(hasFilters({ q: undefined, ns: "" })).toBe(false);
    expect(hasFilters({ q: undefined, ns: "shop" })).toBe(true);
  });

  it("builds chart series and bounds", () => {
    const w = workloadChartSeries(
      { cpu_usage: [[1, 0.1]], memory_working_set: [], ready: [[1, 2]], desired: [[1, 3]], restarts: [[1, 0]] },
      { cpu: "CPU", memory: "Memory", ready: "Ready", desired: "Desired", restarts: "Restarts" },
    );
    expect(w.replicas.map((s) => s.label)).toEqual(["Ready", "Desired"]);
    expect(w.cpu[0]!.points).toEqual([[1, 0.1]]);
    const p = podChartSeries(
      { cpu_usage: [], memory_working_set: [], network_receive: [[5, 10]], network_transmit: [[5, 20]], restarts: [] },
      { cpu: "CPU", memory: "Memory", receive: "Rx", transmit: "Tx", restarts: "Restarts" },
    );
    expect(p.network).toEqual([
      { label: "Rx", points: [[5, 10]] },
      { label: "Tx", points: [[5, 20]] },
    ]);
    expect(boundMs(1000)).toBe(1000);
    expect(boundMs("2026-09-14T10:00:00Z")).toBe(Date.parse("2026-09-14T10:00:00Z"));
    expect(boundMs(undefined)).toBeUndefined();
    expect(shortId("containerd://" + "ab".repeat(32))).toBe("abababababab");
  });
});
