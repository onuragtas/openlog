import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import type { KubernetesClusterDetail, KubernetesEvent, KubernetesNode, KubernetesPod, KubernetesPodContainer, KubernetesWorkload } from "@/api/kubernetes";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { POD_UIDS } from "@/mocks/kubernetes";
import { ClusterTiles, WorkloadsByKind } from "./ClusterOverview";
import { EventList } from "./EventList";
import { NodeTable, PodTable, WorkloadTable } from "./KubernetesTables";
import { PodContainers } from "./PodContainers";
import { ServiceKubernetesPods } from "./ServiceKubernetesPods";

function renderWithRouter(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  );
}

const now = new Date().toISOString();

function workload(over: Partial<KubernetesWorkload>): KubernetesWorkload {
  return {
    cluster_uid: "c1", cluster_name: "prod-eu", namespace: "shop", kind: "Deployment", name: "orders", uid: "w1", desired: 3, ready: 3, available: 3, updated: 3,
    health: "healthy", pods: 3, restarts: 2, cpu_usage: 0.25, memory_working_set: 64 * 1024 ** 2, cpu_sparkline: [[0, 0.2], [60_000, 0.25]], memory_sparkline: [],
    created_at: now, first_seen: now, last_seen: now, reporting: true,
    ...over,
  };
}

function pod(over: Partial<KubernetesPod>): KubernetesPod {
  return {
    cluster_uid: "c1", cluster_name: "prod-eu", namespace: "shop", pod_name: "orders-abc", pod_uid: "p1", node_name: "node-a", workload_kind: "Deployment", workload_name: "orders",
    phase: "Running", ready: true, reason: "", status: "Running", restarts: 0, pod_ip: "10.0.0.1", qos_class: "BestEffort", created_at: now, started_at: now,
    cpu_usage: 0.012, memory_working_set: 18 * 1024 ** 2, first_seen: now, last_seen: now, reporting: true,
    ...over,
  };
}

function node(over: Partial<KubernetesNode>): KubernetesNode {
  return {
    cluster_uid: "c1", cluster_name: "prod-eu", node_name: "node-a", node_uid: "n1", ready: "true", unschedulable: false, roles: ["control-plane"], kubelet_version: "v1.30.4",
    os_image: "Ubuntu", container_runtime: "containerd", internal_ip: "10.0.0.11", created_at: now, allocatable_cpu: 4, allocatable_memory: 8 * 1024 ** 3, allocatable_pods: 110,
    cpu_usage: 1, memory_working_set: 2 * 1024 ** 3, pods: 12, host_id: "h1", host_name: "web-1", first_seen: now, last_seen: now, reporting: true,
    conditions: [{ condition: "Ready", status: "true" }],
    ...over,
  };
}

describe("kubernetes tables", () => {
  it("lists workloads with health, replicas and usage, linking to the workload page", async () => {
    renderWithRouter(
      <WorkloadTable
        workloads={[workload({}), workload({ name: "payments", health: "unavailable", ready: 0, desired: 1, cpu_usage: null, memory_working_set: null }), workload({ kind: "CronJob", name: "report" })]}
        showCluster={false}
      />,
    );
    const table = await screen.findByTestId("k8s-workload-table");
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(4);
    expect(within(rows[1]!).getByRole("link", { name: "Open workload orders" })).toHaveAttribute("href", "/kubernetes/workloads/c1/shop/Deployment/orders");
    expect(within(rows[1]!).getByText("Healthy")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("3/3")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("250m")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("64.0 MiB")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Unavailable")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("0/1")).toBeInTheDocument();
    // CronJobs have no replicas.
    expect(within(rows[3]!).queryByText("3/3")).not.toBeInTheDocument();
  });

  it("lists pods with status, not-ready marker and workload link", async () => {
    renderWithRouter(<PodTable pods={[pod({}), pod({ pod_uid: "p2", pod_name: "payments-x", status: "CrashLoopBackOff", reason: "CrashLoopBackOff", ready: false, restarts: 17, workload_name: "payments" })]} />);
    const table = await screen.findByTestId("k8s-pod-table");
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]!).getByRole("link", { name: "Open pod orders-abc" })).toHaveAttribute("href", "/kubernetes/pods/p1");
    expect(within(rows[1]!).getByText("Running")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("CrashLoopBackOff")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Not ready")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("17")).toBeInTheDocument();
    expect(within(rows[2]!).getByRole("link", { name: "Open workload payments" })).toHaveAttribute("href", "/kubernetes/workloads/c1/shop/Deployment/payments");
  });

  it("lists nodes with readiness, usage against allocatable and the host link", async () => {
    renderWithRouter(
      <NodeTable
        nodes={[node({}), node({ node_name: "node-c", ready: "false", host_id: null, host_name: null, cpu_usage: null, conditions: [{ condition: "Ready", status: "false" }, { condition: "DiskPressure", status: "true" }] })]}
      />,
    );
    const table = await screen.findByTestId("k8s-node-table");
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]!).getByText("Ready")).toBeInTheDocument();
    expect(within(rows[1]!).getByRole("link", { name: "Open host web-1" })).toHaveAttribute("href", "/hosts/h1");
    expect(within(rows[1]!).getByText(/2\.0 GiB/)).toBeInTheDocument();
    // CPU 1 of 4 cores and memory 2 of 8 GiB.
    expect(within(rows[1]!).getAllByText(/25%/)).toHaveLength(2);
    expect(within(rows[2]!).getByText("Not ready")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("DiskPressure")).toBeInTheDocument();
    expect(within(rows[2]!).queryByRole("link")).not.toBeInTheDocument();
  });

  it("links event objects to pod and workload pages", async () => {
    const base: KubernetesEvent = { timestamp: now, type: "Warning", reason: "BackOff", message: "Back-off restarting", count: 5, namespace: "shop", object_kind: "Pod", object_name: "orders-abc", object_uid: "p1", source: "kubelet", cluster_uid: "c1", cluster_name: "prod-eu" };
    renderWithRouter(<EventList events={[base, { ...base, type: "Normal", reason: "ScalingReplicaSet", count: 1, object_kind: "Deployment", object_name: "orders", object_uid: "w1" }]} />);
    const table = await screen.findByTestId("k8s-events");
    expect(within(table).getByText("×5")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: /orders-abc/ })).toHaveAttribute("href", "/kubernetes/pods/p1");
    expect(within(table).getByRole("link", { name: /Deployment orders/ })).toHaveAttribute("href", "/kubernetes/workloads/c1/shop/Deployment/orders");
  });

  it("shows an empty state without events", async () => {
    renderWithRouter(<EventList events={[]} emptyText="Nothing here" />);
    expect(await screen.findByText("Nothing here")).toBeInTheDocument();
  });
});

describe("kubernetes overview and pod parts", () => {
  const detail: KubernetesClusterDetail = {
    cluster_uid: "c1", cluster_name: "prod-eu", version: "v1.30.4", first_seen: now, last_seen: now, reporting: true, nodes: 3, nodes_ready: 2,
    pods: { Pending: 1, Running: 9, Succeeded: 1, Failed: 0, Unknown: 0 }, pods_not_ready: 2, workloads: 8, workloads_unhealthy: 2, namespaces: ["shop"],
    workloads_by_kind: [{ kind: "Deployment", total: 4, healthy: 2, degraded: 1, unavailable: 1, unknown: 0 }],
    warning_events: [], cpu_usage: 2.4, memory_working_set: 9 * 1024 ** 3, allocatable_cpu: 10, allocatable_memory: 19 * 1024 ** 3,
  };

  it("shows cluster KPI tiles", async () => {
    renderWithRouter(<ClusterTiles cluster={detail} />);
    expect(within(await screen.findByTestId("tile-nodes")).getByText("2/3")).toBeInTheDocument();
    const pods = screen.getByTestId("tile-pods");
    expect(within(pods).getByText("11")).toBeInTheDocument();
    expect(within(pods).getByText("9 running · Pending 1 · Succeeded 1")).toBeInTheDocument();
    expect(within(screen.getByTestId("tile-not-ready")).getByText("2")).toBeInTheDocument();
    expect(within(screen.getByTestId("tile-workloads")).getByText("of 8 workloads")).toBeInTheDocument();
    expect(screen.getByText("of 10 allocatable · 24%")).toBeInTheDocument();
  });

  it("links workload health counts to the filtered workload list", async () => {
    renderWithRouter(<WorkloadsByKind cluster={detail} />);
    const table = await screen.findByTestId("k8s-workloads-by-kind");
    const unavailable = within(table).getAllByRole("link").find((a) => a.getAttribute("href")?.includes("health=unavailable"));
    expect(unavailable).toBeDefined();
    expect(unavailable!.getAttribute("href")).toContain("kind=Deployment");
    expect(unavailable!.getAttribute("href")).toContain("cluster=c1");
  });

  it("links known containers of a pod to the container page", async () => {
    const ct = (over: Partial<KubernetesPodContainer>): KubernetesPodContainer => ({
      name: "app", container_id: "containerd://" + "ab".repeat(32), image: "shop/app:1", ready: true, restarts: 0, state: "running", reason: "", known: true, host_id: "h1",
      cpu_usage: 0.1, memory_working_set: 1024 ** 2, cpu_request: 0.1, cpu_limit: 0.5, memory_request: null, memory_limit: 256 * 1024 ** 2, ...over,
    });
    renderWithRouter(<PodContainers pod={{ containers: [ct({}), ct({ name: "sidecar", container_id: "cd".repeat(32), known: false, state: "waiting", reason: "CrashLoopBackOff", ready: false, cpu_request: null, cpu_limit: null, memory_limit: null })] }} />);
    const table = await screen.findByTestId("k8s-pod-containers");
    expect(within(table).getByRole("link", { name: "Open container app" })).toHaveAttribute("href", `/containers/${"ab".repeat(32)}`);
    expect(within(table).getByText("100m / 500m")).toBeInTheDocument();
    expect(within(table).getByText("– / 256.0 MiB")).toBeInTheDocument();
    expect(within(table).getByText("sidecar")).toHaveAttribute("title", "No infra agent data for this container");
    expect(within(table).getByText("CrashLoopBackOff")).toBeInTheDocument();
  });

  it("shows the Kubernetes pods of an APM service", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithRouter(<ServiceKubernetesPods scope={{ service: "orders" }} range={{ range: "1h" }} />);
    const dd = await screen.findByTestId("service-k8s-pods");
    expect(within(dd).getByRole("link", { name: "Open pod orders-7d9f8b6c5-x2k4p" })).toHaveAttribute("href", `/kubernetes/pods/${POD_UIDS.orders0}`);
    expect(within(dd).getAllByRole("link")).toHaveLength(2);
  });
});
