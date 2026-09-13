import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import type { Container } from "@/api/containers";
import { containerStatus, groupByComposeService, memoryRatio } from "@/lib/containers";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { CONTAINER_IDS } from "@/mocks/containers";
import { ContainerServices } from "./ContainerServices";
import { ContainerGroups, ContainerTable } from "./ContainerTable";
import { ServiceContainers } from "./ServiceContainers";

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

function mk(over: Partial<Container>): Container {
  return {
    container_id: "ab".repeat(32), name: "shop-orders-1", image_name: "openlog-apmdemo/orders", image_tags: ["1"], runtime: "docker",
    host_id: "h1", host_name: "web-1", compose_project: "shop", compose_service: "orders", k8s_pod_name: "", k8s_namespace_name: "", k8s_container_name: "",
    state: "running", health: "", started_at: null, restart_count: 0, first_seen: "2026-09-14T09:00:00.000000000Z", last_seen: "2026-09-14T10:00:00.000000000Z",
    reporting: true, cpu_utilization: 0.1, memory_usage: 50 * 1024 ** 2, memory_limit: 200 * 1024 ** 2, cpu_sparkline: [[0, 0.1], [60_000, 0.2]], memory_sparkline: [],
    ...over,
  };
}

describe("container helpers", () => {
  it("derives the status badge from state, health and reporting", () => {
    expect(containerStatus({ state: "running", health: "healthy", reporting: true })).toEqual({ key: "running", variant: "success" });
    expect(containerStatus({ state: "running", health: "unhealthy", reporting: true }).key).toBe("unhealthy");
    expect(containerStatus({ state: "running", health: "", reporting: false }).key).toBe("notReporting");
    expect(containerStatus({ state: "exited", health: "", reporting: false }).key).toBe("exited");
    expect(containerStatus({ state: "", health: "", reporting: true }).key).toBe("unknown");
    expect(memoryRatio(50, 200)).toBe(0.25);
    expect(memoryRatio(50, 0)).toBeNull();
  });

  it("groups by compose service with standalone containers last", () => {
    const groups = groupByComposeService([
      mk({ container_id: "1", name: "stray", compose_project: "", compose_service: "" }),
      mk({ container_id: "2", cpu_utilization: 0.1 }),
      mk({ container_id: "3", name: "shop-orders-2", cpu_utilization: 0.2, memory_usage: 10 }),
      mk({ container_id: "4", name: "shop-orders-3", state: "exited", reporting: false, cpu_utilization: null, memory_usage: null }),
    ]);
    expect(groups.map((g) => g.key)).toEqual(["shop/orders", ""]);
    expect(groups[0]!.containers).toHaveLength(3);
    expect(groups[0]!.running).toBe(2);
    expect(groups[0]!.cpu).toBeCloseTo(0.3);
  });
});

describe("container components", () => {
  it("lists containers with state, image, CPU and memory, linking to the detail page", async () => {
    renderWithRouter(
      <ContainerTable
        containers={[
          mk({ health: "healthy" }),
          mk({ container_id: "cd".repeat(32), name: "", image_name: "redis", image_tags: [], health: "unhealthy", compose_service: "redis" }),
          mk({ container_id: "ef".repeat(32), name: "migrate", state: "exited", reporting: false, cpu_utilization: null, memory_usage: null }),
        ]}
      />,
    );
    const table = await screen.findByTestId("container-table");
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(4);
    expect(within(rows[1]!).getByRole("link", { name: "Open container shop-orders-1" })).toHaveAttribute("href", `/containers/${"ab".repeat(32)}`);
    expect(within(rows[1]!).getByText("Running")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("10%")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("50.0 MiB")).toBeInTheDocument();
    // Without a name the short id is shown.
    expect(within(rows[2]!).getByRole("link", { name: "Open container cdcdcdcdcdcd" })).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Unhealthy")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("Exited")).toBeInTheDocument();
    expect(within(rows[3]!).getAllByText("–").length).toBeGreaterThan(0);
  });

  it("groups rows by compose service", async () => {
    renderWithRouter(<ContainerGroups containers={[mk({}), mk({ container_id: "2", compose_project: "", compose_service: "", name: "stray" })]} />);
    const groups = await screen.findByTestId("container-groups");
    expect(within(groups).getByRole("heading", { name: "orders · shop" })).toBeInTheDocument();
    expect(within(groups).getByRole("heading", { name: "Standalone containers" })).toBeInTheDocument();
    expect(within(groups).getAllByText("1 of 1 running")).toHaveLength(2);
  });

  it("shows a service's containers, marking containers without infra agent data", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithRouter(<ServiceContainers scope={{ service: "orders" }} range={{ range: "1h" }} />);
    const dd = await screen.findByTestId("service-containers");
    expect(await within(dd).findByRole("link", { name: "Open container shop-orders-1" })).toHaveAttribute("href", `/containers/${CONTAINER_IDS.orders}`);
    expect(within(dd).getByText("orders-canary")).toHaveAttribute("title", "no infra agent data for this container");
  });

  it("lists the APM services of a container with RED metrics", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithRouter(<ContainerServices containerId={CONTAINER_IDS.orders} range={{ range: "1h" }} />);
    const table = await screen.findByTestId("container-services");
    expect(within(table).getByRole("link", { name: /orders/ })).toBeInTheDocument();
    expect(within(table).getByText("190 rpm")).toBeInTheDocument();
    expect(within(table).getByText("1.2%")).toBeInTheDocument();
  });
});
