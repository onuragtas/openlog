// MSW handlers for the Kubernetes endpoints (docs/contracts/api.md "Kubernetes", semantic-conventions §7):
// cluster "prod-eu" (3 nodes, the shop namespace with a crash-looping payments deployment) and a small
// "staging" cluster that stopped reporting.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type {
  ApmServiceKubernetesPod,
  KubernetesCluster,
  KubernetesClusterDetail,
  KubernetesEvent,
  KubernetesNode,
  KubernetesPod,
  KubernetesPodDetail,
  KubernetesWorkload,
  KubernetesWorkloadDetail,
  PodPhase,
  WorkloadHealth,
} from "@/api/kubernetes";
import type { LogRecord } from "@/api/types";
import { authenticate } from "./account";
import { CONTAINER_IDS } from "./containers";
import { formatTs, HOST_IDS } from "./fixtures";

const API = "*/api/v1";

type Code = "invalid_argument" | "not_found";
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: code === "not_found" ? 404 : 400 });

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

export const CLUSTER_UIDS = { prod: "6f1d2c3b-0000-4a5b-8c9d-000000000001", staging: "6f1d2c3b-0000-4a5b-8c9d-000000000002" } as const;

export const POD_UIDS = {
  orders0: "a1000000-0000-4000-8000-000000000001",
  orders1: "a1000000-0000-4000-8000-000000000002",
  catalog0: "a1000000-0000-4000-8000-000000000003",
  catalog1: "a1000000-0000-4000-8000-000000000004",
  payments0: "a1000000-0000-4000-8000-000000000005",
  redis0: "a1000000-0000-4000-8000-000000000006",
  agent0: "a1000000-0000-4000-8000-000000000007",
  agent1: "a1000000-0000-4000-8000-000000000008",
  agent2: "a1000000-0000-4000-8000-000000000009",
  migrate0: "a1000000-0000-4000-8000-00000000000a",
  coredns0: "a1000000-0000-4000-8000-00000000000b",
  stagingApi0: "a1000000-0000-4000-8000-00000000000c",
} as const;

const MiB = 1024 ** 2;
const GiB = 1024 ** 3;

interface WorkloadSpec {
  cluster: keyof typeof CLUSTER_UIDS;
  namespace: string;
  kind: string;
  name: string;
  desired: number;
  ready: number;
  available: number;
  updated: number;
  hpa?: boolean;
}

const WORKLOADS: WorkloadSpec[] = [
  { cluster: "prod", namespace: "shop", kind: "Deployment", name: "orders", desired: 2, ready: 2, available: 2, updated: 2, hpa: true },
  { cluster: "prod", namespace: "shop", kind: "Deployment", name: "catalog", desired: 2, ready: 1, available: 1, updated: 2 },
  { cluster: "prod", namespace: "shop", kind: "Deployment", name: "payments", desired: 1, ready: 0, available: 0, updated: 1 },
  { cluster: "prod", namespace: "shop", kind: "StatefulSet", name: "redis", desired: 1, ready: 1, available: 1, updated: 1 },
  { cluster: "prod", namespace: "shop", kind: "Job", name: "migrate", desired: 1, ready: 0, available: 1, updated: 0 },
  { cluster: "prod", namespace: "shop", kind: "CronJob", name: "nightly-report", desired: 0, ready: 0, available: 0, updated: 0 },
  { cluster: "prod", namespace: "openlog", kind: "DaemonSet", name: "openlog-agent", desired: 3, ready: 3, available: 3, updated: 3 },
  { cluster: "prod", namespace: "kube-system", kind: "Deployment", name: "coredns", desired: 1, ready: 1, available: 1, updated: 1 },
  { cluster: "staging", namespace: "shop", kind: "Deployment", name: "api", desired: 1, ready: 1, available: 1, updated: 1 },
];

interface ContainerSpec {
  name: string;
  id: string;
  image: string;
  state: string;
  reason: string;
  ready: boolean;
  restarts: number;
  known: boolean;
}

interface PodSpec {
  uid: string;
  cluster: keyof typeof CLUSTER_UIDS;
  namespace: string;
  name: string;
  node: string;
  workloadKind: string;
  workloadName: string;
  phase: PodPhase;
  ready: boolean;
  reason: string;
  restarts: number;
  ip: string;
  cpu: number;
  memory: number;
  containers: ContainerSpec[];
  services?: string[];
}

const c = (name: string, image: string, over: Partial<ContainerSpec> = {}): ContainerSpec => ({
  name, id: "", image, state: "running", reason: "", ready: true, restarts: 0, known: false, ...over,
});

const PODS: PodSpec[] = [
  { uid: POD_UIDS.orders0, cluster: "prod", namespace: "shop", name: "orders-7d9f8b6c5-x2k4p", node: "node-a", workloadKind: "Deployment", workloadName: "orders", phase: "Running", ready: true, reason: "", restarts: 2, ip: "10.42.0.11", cpu: 0.012, memory: 18 * MiB,
    containers: [c("orders", "openlog-apmdemo/orders:1", { id: CONTAINER_IDS.orders, restarts: 2, known: true })], services: ["orders"] },
  { uid: POD_UIDS.orders1, cluster: "prod", namespace: "shop", name: "orders-7d9f8b6c5-q8w1z", node: "node-b", workloadKind: "Deployment", workloadName: "orders", phase: "Running", ready: true, reason: "", restarts: 0, ip: "10.42.1.12", cpu: 0.015, memory: 21 * MiB,
    containers: [c("orders", "openlog-apmdemo/orders:1", { id: "1a".repeat(32) })], services: ["orders"] },
  { uid: POD_UIDS.catalog0, cluster: "prod", namespace: "shop", name: "catalog-5c6d7e8f9-aa11b", node: "node-a", workloadKind: "Deployment", workloadName: "catalog", phase: "Running", ready: true, reason: "", restarts: 0, ip: "10.42.0.13", cpu: 0.024, memory: 64 * MiB,
    containers: [c("catalog", "openlog-apmdemo/catalog:1", { id: CONTAINER_IDS.catalog, known: true })], services: ["catalog"] },
  { uid: POD_UIDS.catalog1, cluster: "prod", namespace: "shop", name: "catalog-5c6d7e8f9-bb22c", node: "node-c", workloadKind: "Deployment", workloadName: "catalog", phase: "Running", ready: false, reason: "", restarts: 1, ip: "10.42.2.14", cpu: 0.004, memory: 40 * MiB,
    containers: [c("catalog", "openlog-apmdemo/catalog:1", { id: "2b".repeat(32), ready: false, restarts: 1 })], services: ["catalog"] },
  { uid: POD_UIDS.payments0, cluster: "prod", namespace: "shop", name: "payments-6b5a4c3d2-zz9yx", node: "node-b", workloadKind: "Deployment", workloadName: "payments", phase: "Running", ready: false, reason: "CrashLoopBackOff", restarts: 17, ip: "10.42.1.15", cpu: 0.001, memory: 12 * MiB,
    containers: [c("payments", "openlog-apmdemo/payments:1", { id: "3c".repeat(32), state: "waiting", reason: "CrashLoopBackOff", ready: false, restarts: 17 })] },
  { uid: POD_UIDS.redis0, cluster: "prod", namespace: "shop", name: "redis-0", node: "node-c", workloadKind: "StatefulSet", workloadName: "redis", phase: "Running", ready: true, reason: "", restarts: 0, ip: "10.42.2.16", cpu: 0.004, memory: 9 * MiB,
    containers: [c("redis", "redis:7-alpine", { id: CONTAINER_IDS.redis, known: true })] },
  { uid: POD_UIDS.migrate0, cluster: "prod", namespace: "shop", name: "migrate-4hx7t", node: "node-a", workloadKind: "Job", workloadName: "migrate", phase: "Succeeded", ready: false, reason: "", restarts: 0, ip: "", cpu: 0, memory: 0,
    containers: [c("migrate", "openlog-apmdemo/orders:1", { id: "4d".repeat(32), state: "terminated", reason: "Completed", ready: false })] },
  ...(["a", "b", "c"] as const).map((n, i): PodSpec => ({
    uid: [POD_UIDS.agent0, POD_UIDS.agent1, POD_UIDS.agent2][i]!, cluster: "prod", namespace: "openlog", name: `openlog-agent-${n}${n}${i}qz`, node: `node-${n}`, workloadKind: "DaemonSet", workloadName: "openlog-agent",
    phase: "Running", ready: true, reason: "", restarts: 0, ip: `10.0.0.${11 + i}`, cpu: 0.03, memory: 48 * MiB, containers: [c("agent", "ghcr.io/onuragtas/openlog-agent:0.4.0", { id: `5${i}`.repeat(32) })],
  })),
  { uid: POD_UIDS.coredns0, cluster: "prod", namespace: "kube-system", name: "coredns-5d78c9869d-k7l2m", node: "node-a", workloadKind: "Deployment", workloadName: "coredns", phase: "Running", ready: true, reason: "", restarts: 0, ip: "10.42.0.2", cpu: 0.003, memory: 16 * MiB,
    containers: [c("coredns", "registry.k8s.io/coredns/coredns:v1.11.1", { id: "6e".repeat(32) })] },
  { uid: POD_UIDS.stagingApi0, cluster: "staging", namespace: "shop", name: "api-6c7d8e9f0-m3n4o", node: "staging-1", workloadKind: "Deployment", workloadName: "api", phase: "Running", ready: true, reason: "", restarts: 0, ip: "10.43.0.5", cpu: 0.02, memory: 30 * MiB,
    containers: [c("api", "openlog-apmdemo/frontend:1", { id: "7f".repeat(32) })] },
];

interface NodeSpec {
  cluster: keyof typeof CLUSTER_UIDS;
  name: string;
  ready: "true" | "false" | "unknown";
  roles: string[];
  cpu: number;
  memory: number;
  allocCpu: number;
  allocMemory: number;
  hostId?: string;
  hostName?: string;
  pressure?: string;
}

const NODES: NodeSpec[] = [
  { cluster: "prod", name: "node-a", ready: "true", roles: ["control-plane"], cpu: 0.8, memory: 2.1 * GiB, allocCpu: 4, allocMemory: 7.6 * GiB, hostId: HOST_IDS.web, hostName: "web-1" },
  { cluster: "prod", name: "node-b", ready: "true", roles: [], cpu: 1.4, memory: 5.9 * GiB, allocCpu: 4, allocMemory: 7.6 * GiB, pressure: "MemoryPressure" },
  { cluster: "prod", name: "node-c", ready: "false", roles: [], cpu: 0.2, memory: 1.2 * GiB, allocCpu: 2, allocMemory: 3.8 * GiB },
  { cluster: "staging", name: "staging-1", ready: "true", roles: ["control-plane"], cpu: 0.3, memory: 1.1 * GiB, allocCpu: 2, allocMemory: 3.8 * GiB },
];

const reportingCluster = (k: keyof typeof CLUSTER_UIDS) => k === "prod";
const clusterName = (k: keyof typeof CLUSTER_UIDS) => (k === "prod" ? "prod-eu" : "staging");
const clusterKey = (uid: string) => (Object.keys(CLUSTER_UIDS) as (keyof typeof CLUSTER_UIDS)[]).find((k) => CLUSTER_UIDS[k] === uid);

function window(url: URL): { from: number; to: number } | Response {
  const now = Date.now();
  const f = url.searchParams.get("from");
  const t = url.searchParams.get("to");
  const from = f ? Number(f) : now - 3_600_000;
  const to = t ? Number(t) : now;
  if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) return fail("invalid_argument", "from must be before to");
  return { from, to };
}

function wave(seed: number, from: number, to: number, n: number, base: number): [number, number][] {
  if (base === 0) return [];
  const step = (to - from) / n;
  return Array.from({ length: n }, (_, i) => [Math.round(from + i * step), base * (1 + 0.25 * Math.sin((i + seed) / 3))] as [number, number]);
}

const seedOf = (s: string) => [...s].reduce((a, ch) => a + ch.charCodeAt(0), 0) % 17;

function health(w: WorkloadSpec): WorkloadHealth {
  if (!reportingCluster(w.cluster)) return "unknown";
  if (w.kind === "CronJob") return "healthy";
  if (w.kind === "Job") return w.updated > 0 && w.available < w.desired ? "degraded" : "healthy";
  if (w.available >= w.desired) return "healthy";
  return w.available === 0 ? "unavailable" : "degraded";
}

function pod(p: PodSpec): KubernetesPod {
  const now = Date.now();
  const reporting = reportingCluster(p.cluster);
  return {
    cluster_uid: CLUSTER_UIDS[p.cluster],
    cluster_name: clusterName(p.cluster),
    namespace: p.namespace,
    pod_name: p.name,
    pod_uid: p.uid,
    node_name: p.node,
    workload_kind: p.workloadKind,
    workload_name: p.workloadName,
    phase: p.phase,
    ready: p.ready,
    reason: p.reason,
    status: p.reason || p.phase,
    restarts: p.restarts,
    pod_ip: p.ip,
    qos_class: p.namespace === "kube-system" ? "Burstable" : "BestEffort",
    created_at: formatTs(now - 2 * 86_400_000),
    started_at: formatTs(now - 2 * 86_400_000 + 5_000),
    cpu_usage: reporting && p.phase === "Running" ? p.cpu : null,
    memory_working_set: reporting && p.phase === "Running" ? p.memory : null,
    first_seen: formatTs(now - 2 * 86_400_000),
    last_seen: formatTs(reporting ? now - 20_000 : now - 40 * 60_000),
    reporting,
  };
}

const podsOf = (w: WorkloadSpec) => PODS.filter((p) => p.cluster === w.cluster && p.namespace === w.namespace && p.workloadKind === w.kind && p.workloadName === w.name);

function workload(w: WorkloadSpec, from: number, to: number): KubernetesWorkload {
  const now = Date.now();
  const pods = podsOf(w);
  const running = pods.filter((p) => p.phase === "Running");
  const cpu = running.reduce((a, p) => a + p.cpu, 0);
  const mem = running.reduce((a, p) => a + p.memory, 0);
  const reporting = reportingCluster(w.cluster);
  return {
    cluster_uid: CLUSTER_UIDS[w.cluster],
    cluster_name: clusterName(w.cluster),
    namespace: w.namespace,
    kind: w.kind,
    name: w.name,
    uid: `w-${w.namespace}-${w.name}`,
    desired: w.desired,
    ready: w.ready,
    available: w.available,
    updated: w.updated,
    health: health(w),
    pods: pods.length,
    restarts: pods.reduce((a, p) => a + p.restarts, 0),
    cpu_usage: reporting && running.length ? cpu : null,
    memory_working_set: reporting && running.length ? mem : null,
    cpu_sparkline: reporting ? wave(seedOf(w.name), from, to, 30, cpu) : [],
    memory_sparkline: reporting ? wave(seedOf(w.name) + 3, from, to, 30, mem) : [],
    created_at: formatTs(now - 10 * 86_400_000),
    first_seen: formatTs(now - 10 * 86_400_000),
    last_seen: formatTs(reporting ? now - 20_000 : now - 40 * 60_000),
    reporting,
  };
}

function node(n: NodeSpec): KubernetesNode {
  const now = Date.now();
  const reporting = reportingCluster(n.cluster);
  const status = (v: boolean) => (v ? "true" : "false") as "true" | "false";
  return {
    cluster_uid: CLUSTER_UIDS[n.cluster],
    cluster_name: clusterName(n.cluster),
    node_name: n.name,
    node_uid: `n-${n.name}`,
    ready: n.ready,
    unschedulable: false,
    roles: n.roles,
    kubelet_version: "v1.30.4+k3s1",
    os_image: "Ubuntu 24.04.1 LTS",
    container_runtime: "containerd://1.7.20",
    internal_ip: `10.0.0.${11 + NODES.indexOf(n)}`,
    created_at: formatTs(now - 30 * 86_400_000),
    allocatable_cpu: n.allocCpu,
    allocatable_memory: n.allocMemory,
    allocatable_pods: 110,
    cpu_usage: reporting && n.ready === "true" ? n.cpu : null,
    memory_working_set: reporting && n.ready === "true" ? n.memory : null,
    pods: PODS.filter((p) => p.cluster === n.cluster && p.node === n.name && (p.phase === "Running" || p.phase === "Pending")).length,
    host_id: n.hostId ?? null,
    host_name: n.hostName ?? null,
    first_seen: formatTs(now - 30 * 86_400_000),
    last_seen: formatTs(reporting ? now - 20_000 : now - 40 * 60_000),
    reporting,
    conditions: [
      { condition: "Ready", status: n.ready },
      { condition: "MemoryPressure", status: status(n.pressure === "MemoryPressure") },
      { condition: "DiskPressure", status: "false" },
    ],
  };
}

function events(now: number): KubernetesEvent[] {
  const prod = { cluster_uid: CLUSTER_UIDS.prod, cluster_name: "prod-eu" };
  const e = (ago: number, type: string, reason: string, message: string, count: number, ns: string, kind: string, name: string, uid: string, source: string): KubernetesEvent => ({
    timestamp: formatTs(now - ago), type, reason, message, count, namespace: ns, object_kind: kind, object_name: name, object_uid: uid, source, ...prod,
  });
  return [
    e(30_000, "Warning", "BackOff", "Back-off restarting failed container payments in pod payments-6b5a4c3d2-zz9yx", 17, "shop", "Pod", "payments-6b5a4c3d2-zz9yx", POD_UIDS.payments0, "kubelet"),
    e(90_000, "Warning", "Unhealthy", "Readiness probe failed: HTTP probe failed with statuscode: 503", 6, "shop", "Pod", "catalog-5c6d7e8f9-bb22c", POD_UIDS.catalog1, "kubelet"),
    e(4 * 60_000, "Warning", "NodeNotReady", "Node node-c status is now: NodeNotReady", 1, "", "Node", "node-c", "n-node-c", "node-controller"),
    e(6 * 60_000, "Normal", "ScalingReplicaSet", "Scaled up replica set orders-7d9f8b6c5 to 2", 1, "shop", "Deployment", "orders", "w-shop-orders", "deployment-controller"),
    e(8 * 60_000, "Normal", "Pulled", "Container image \"openlog-apmdemo/orders:1\" already present on machine", 1, "shop", "Pod", "orders-7d9f8b6c5-x2k4p", POD_UIDS.orders0, "kubelet"),
    e(20 * 60_000, "Normal", "Completed", "Job completed", 1, "shop", "Job", "migrate", "w-shop-migrate", "job-controller"),
  ];
}

function clusterSummary(k: keyof typeof CLUSTER_UIDS): KubernetesCluster {
  const now = Date.now();
  const reporting = reportingCluster(k);
  const pods = PODS.filter((p) => p.cluster === k);
  const nodes = NODES.filter((n) => n.cluster === k);
  const ws = WORKLOADS.filter((w) => w.cluster === k);
  const phases: Record<PodPhase, number> = { Pending: 0, Running: 0, Succeeded: 0, Failed: 0, Unknown: 0 };
  pods.forEach((p) => phases[p.phase]++);
  return {
    cluster_uid: CLUSTER_UIDS[k],
    cluster_name: clusterName(k),
    version: "v1.30.4+k3s1",
    first_seen: formatTs(now - 30 * 86_400_000),
    last_seen: formatTs(reporting ? now - 15_000 : now - 40 * 60_000),
    reporting,
    nodes: nodes.length,
    nodes_ready: nodes.filter((n) => n.ready === "true").length,
    pods: phases,
    pods_not_ready: pods.filter((p) => p.phase === "Running" && !p.ready).length,
    workloads: ws.length,
    workloads_unhealthy: ws.filter((w) => ["degraded", "unavailable"].includes(health(w))).length,
    namespaces: [...new Set([...pods.map((p) => p.namespace), ...ws.map((w) => w.namespace)])].sort(),
  };
}

function clusterDetail(k: keyof typeof CLUSTER_UIDS): KubernetesClusterDetail {
  const base = clusterSummary(k);
  const ws = WORKLOADS.filter((w) => w.cluster === k);
  const kinds = [...new Set(ws.map((w) => w.kind))];
  const nodes = NODES.filter((n) => n.cluster === k && n.ready === "true");
  const reporting = base.reporting;
  return {
    ...base,
    workloads_by_kind: kinds.map((kind) => {
      const of = ws.filter((w) => w.kind === kind).map(health);
      return { kind, total: of.length, healthy: of.filter((h) => h === "healthy").length, degraded: of.filter((h) => h === "degraded").length, unavailable: of.filter((h) => h === "unavailable").length, unknown: of.filter((h) => h === "unknown").length };
    }),
    warning_events: k === "prod" ? events(Date.now()).filter((e) => e.type === "Warning") : [],
    cpu_usage: reporting ? nodes.reduce((a, n) => a + n.cpu, 0) : null,
    memory_working_set: reporting ? nodes.reduce((a, n) => a + n.memory, 0) : null,
    allocatable_cpu: NODES.filter((n) => n.cluster === k).reduce((a, n) => a + n.allocCpu, 0),
    allocatable_memory: NODES.filter((n) => n.cluster === k).reduce((a, n) => a + n.allocMemory, 0),
  };
}

function podDetail(p: PodSpec): KubernetesPodDetail {
  const n = NODES.find((x) => x.cluster === p.cluster && x.name === p.node);
  const running = p.phase === "Running" && reportingCluster(p.cluster);
  return {
    ...pod(p),
    containers: p.containers.map((ct) => ({
      name: ct.name,
      container_id: ct.id,
      image: ct.image,
      ready: ct.ready,
      restarts: ct.restarts,
      state: ct.state,
      reason: ct.reason,
      known: ct.known,
      host_id: ct.known ? HOST_IDS.web : null,
      cpu_usage: running ? p.cpu : null,
      memory_working_set: running ? p.memory : null,
      cpu_request: p.namespace === "kube-system" ? 0.1 : null,
      cpu_limit: null,
      memory_request: p.namespace === "kube-system" ? 70 * MiB : null,
      memory_limit: p.namespace === "kube-system" ? 170 * MiB : p.workloadName === "payments" ? 128 * MiB : null,
    })),
    labels: p.workloadKind === "Job" ? {} : { app: p.workloadName, "app.kubernetes.io/name": p.workloadName, "app.kubernetes.io/instance": p.namespace },
    services: (p.services ?? []).map((s) => ({ service_name: s, service_namespace: "shop", deployment_environment: "prod" })),
    host_id: n?.hostId ?? null,
    host_name: n?.hostName ?? null,
  };
}

/** Pod container log records merged into GET /logs (resource attribute k8s.pod.uid, semantic-conventions §7.2). */
export function kubernetesLogs(now: number): LogRecord[] {
  const out: LogRecord[] = [];
  const lines: [string, string, number][] = [
    ["listening on :8080", "INFO", 9],
    ["payment provider timeout after 5s", "ERROR", 17],
    ["GET /healthz 200 0.4ms", "INFO", 9],
  ];
  for (const p of PODS.filter((x) => x.cluster === "prod" && x.namespace === "shop" && x.phase === "Running")) {
    lines.forEach(([body, sev, num], i) => {
      out.push({
        timestamp: formatTs(now - (i + 1) * 70_000 - seedOf(p.name) * 1000),
        severity_text: sev,
        severity_number: num,
        body: `${p.workloadName}: ${body}`,
        host_id: "",
        service_name: "",
        trace_id: "",
        span_id: "",
        attributes: { "openlog.log.source": "container", "log.iostream": num >= 17 ? "stderr" : "stdout" },
        resource_attributes: {
          "k8s.cluster.name": clusterName(p.cluster),
          "k8s.namespace.name": p.namespace,
          "k8s.pod.name": p.name,
          "k8s.pod.uid": p.uid,
          "k8s.container.name": p.containers[0]!.name,
          "k8s.node.name": p.node,
        },
      });
    });
  }
  return out;
}

const matches = (text: string[], q: string | null) => {
  const terms = (q ?? "").toLowerCase().split(/\s+/).filter(Boolean);
  const hay = text.join(" ").toLowerCase();
  return terms.every((t) => hay.includes(t));
};

const lim = (url: URL, def = 100) => Math.min(Number(url.searchParams.get("limit") ?? def) || def, 1000);

export const kubernetesHandlers = [
  http.get(`${API}/kubernetes/clusters`, authed(({ request }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    return HttpResponse.json({ clusters: (Object.keys(CLUSTER_UIDS) as (keyof typeof CLUSTER_UIDS)[]).map(clusterSummary) });
  })),

  http.get(`${API}/kubernetes/clusters/:uid`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const k = clusterKey(String(params.uid));
    return k ? HttpResponse.json(clusterDetail(k)) : fail("not_found", "cluster not found");
  })),

  http.get(`${API}/kubernetes/nodes`, authed(({ request }) => {
    const url = new URL(request.url);
    const p = url.searchParams;
    const all = NODES.filter((n) => (!p.get("cluster_uid") || CLUSTER_UIDS[n.cluster] === p.get("cluster_uid")) && matches([n.name, ...n.roles], p.get("q"))).map(node);
    return HttpResponse.json({ nodes: all.slice(0, lim(url)), total: all.length });
  })),

  http.get(`${API}/kubernetes/workloads`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const p = url.searchParams;
    const kind = p.get("kind");
    if (kind && !["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet", "Pod"].includes(kind)) return fail("invalid_argument", "kind must be one of Deployment, StatefulSet, DaemonSet, Job, CronJob, ReplicaSet");
    const hl = p.get("health");
    if (hl && !["healthy", "degraded", "unavailable", "unknown"].includes(hl)) return fail("invalid_argument", "health must be one of healthy, degraded, unavailable, unknown");
    const all = WORKLOADS.filter(
      (x) =>
        (!p.get("cluster_uid") || CLUSTER_UIDS[x.cluster] === p.get("cluster_uid")) &&
        (!p.get("namespace") || x.namespace === p.get("namespace")) &&
        (!kind || x.kind === kind) &&
        (!hl || health(x) === hl) &&
        matches([x.name, x.namespace], p.get("q")),
    ).map((x) => workload(x, w.from, w.to));
    return HttpResponse.json({ workloads: all.slice(0, lim(url)), total: all.length, step: "120s" });
  })),

  http.get(`${API}/kubernetes/workloads/:uid/:ns/:kind/:name/timeseries`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const spec = WORKLOADS.find((x) => CLUSTER_UIDS[x.cluster] === params.uid && x.namespace === params.ns && x.kind === params.kind && x.name === params.name);
    if (!spec) return fail("not_found", "workload not found");
    const d = workload(spec, w.from, w.to);
    const n = 60;
    const seed = seedOf(spec.name);
    const flat = (v: number) => wave(seed, w.from, w.to, n, 1).map(([t]) => [t, v] as [number, number]);
    return HttpResponse.json({
      step: "60s",
      from: w.from,
      to: w.to,
      series: {
        cpu_usage: wave(seed, w.from, w.to, n, d.cpu_usage ?? 0),
        memory_working_set: wave(seed + 3, w.from, w.to, n, d.memory_working_set ?? 0),
        ready: flat(spec.ready),
        desired: flat(spec.desired),
        restarts: flat(d.restarts),
      },
    });
  })),

  http.get(`${API}/kubernetes/workloads/:uid/:ns/:kind/:name`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const spec = WORKLOADS.find((x) => CLUSTER_UIDS[x.cluster] === params.uid && x.namespace === params.ns && x.kind === params.kind && x.name === params.name);
    if (!spec) return fail("not_found", "workload not found");
    const detail: KubernetesWorkloadDetail = {
      ...workload(spec, w.from, w.to),
      pod_list: podsOf(spec).map(pod),
      hpa: spec.hpa ? { name: spec.name, min_replicas: 2, max_replicas: 6, current_replicas: spec.ready, desired_replicas: spec.desired } : null,
      attributes: { "openlog.k8s.workload.kind": spec.kind, "openlog.k8s.workload.name": spec.name, "k8s.namespace.name": spec.namespace },
    };
    return HttpResponse.json(detail);
  })),

  http.get(`${API}/kubernetes/pods`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const p = url.searchParams;
    const all = PODS.filter(
      (x) =>
        (!p.get("cluster_uid") || CLUSTER_UIDS[x.cluster] === p.get("cluster_uid")) &&
        (!p.get("namespace") || x.namespace === p.get("namespace")) &&
        (!p.get("node") || x.node === p.get("node")) &&
        (!p.get("workload_kind") || x.workloadKind === p.get("workload_kind")) &&
        (!p.get("workload_name") || x.workloadName === p.get("workload_name")) &&
        (!p.get("phase") || x.phase === p.get("phase")) &&
        matches([x.name, x.namespace, x.ip, x.node], p.get("q")),
    ).map(pod);
    return HttpResponse.json({ pods: all.slice(0, lim(url)), total: all.length });
  })),

  http.get(`${API}/kubernetes/pods/:uid/timeseries`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const spec = PODS.find((x) => x.uid === params.uid);
    if (!spec) return fail("not_found", "pod not found");
    const n = 60;
    const seed = seedOf(spec.name);
    const running = spec.phase === "Running";
    return HttpResponse.json({
      step: "60s",
      from: w.from,
      to: w.to,
      series: {
        cpu_usage: running ? wave(seed, w.from, w.to, n, spec.cpu) : [],
        memory_working_set: running ? wave(seed + 3, w.from, w.to, n, spec.memory) : [],
        network_receive: running ? wave(seed + 5, w.from, w.to, n, 3100) : [],
        network_transmit: running ? wave(seed + 7, w.from, w.to, n, 1900) : [],
        restarts: wave(seed, w.from, w.to, n, 1).map(([t], i) => [t, Math.round((spec.restarts * (i + 1)) / n)]),
      },
    });
  })),

  http.get(`${API}/kubernetes/pods/:uid/events`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    return HttpResponse.json({ events: events(Date.now()).filter((e) => e.object_uid === params.uid) });
  })),

  http.get(`${API}/kubernetes/pods/:uid`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const spec = PODS.find((x) => x.uid === params.uid);
    return spec ? HttpResponse.json(podDetail(spec)) : fail("not_found", "pod not found");
  })),

  http.get(`${API}/kubernetes/events`, authed(({ request }) => {
    const url = new URL(request.url);
    const w = window(url);
    if (w instanceof Response) return w;
    const p = url.searchParams;
    const list = events(Date.now()).filter(
      (e) =>
        (!p.get("cluster_uid") || e.cluster_uid === p.get("cluster_uid")) &&
        (!p.get("namespace") || e.namespace === p.get("namespace")) &&
        (!p.get("type") || e.type === p.get("type")) &&
        (!p.get("object_kind") || e.object_kind === p.get("object_kind")) &&
        (!p.get("object_name") || e.object_name === p.get("object_name")) &&
        (!p.get("object_uid") || e.object_uid === p.get("object_uid")) &&
        (!p.get("reason") || e.reason === p.get("reason")),
    );
    return HttpResponse.json({ events: list.slice(0, lim(url)) });
  })),

  http.get(`${API}/apm/services/:service/kubernetes`, authed(({ request, params }) => {
    const w = window(new URL(request.url));
    if (w instanceof Response) return w;
    const pods: ApmServiceKubernetesPod[] = PODS.filter((p) => p.services?.includes(String(params.service))).map((p) => ({
      cluster_uid: CLUSTER_UIDS[p.cluster],
      cluster_name: clusterName(p.cluster),
      namespace: p.namespace,
      pod_name: p.name,
      pod_uid: p.uid,
      workload_kind: p.workloadKind,
      workload_name: p.workloadName,
      node_name: p.node,
      phase: p.phase,
      ready: p.ready,
      reporting: reportingCluster(p.cluster),
    }));
    return HttpResponse.json({ pods });
  })),
];
