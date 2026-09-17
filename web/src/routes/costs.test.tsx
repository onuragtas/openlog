import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { CostsView } from "./costs";

// uPlot needs matchMedia/canvas; the chart itself is covered by TimeSeriesChart.test.tsx.
vi.mock("@/components/TimeSeriesChart", () => ({
  TimeSeriesChart: ({ title }: { title: string }) => <div data-testid="chart">{title}</div>,
}));

function renderCosts() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <CostsView range={{ range: "24h" }} /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

beforeEach(async () => {
  await login(MOCK_EMAIL, MOCK_PASSWORD);
});

describe("Costs view", { timeout: 20_000 }, () => {
  it("shows the total, the run rate and the idle share as its own number", async () => {
    renderCosts();
    // mocks/costs.ts: total 12.8026, per hour 0.2667, idle 4.235 (33% of the total). The amounts are
    // scoped to their tile: the idle figure deliberately appears again in the breakdown legend.
    expect(within(await screen.findByTestId("cost-tile-total")).getByText("$12.80")).toBeInTheDocument();
    expect(within(screen.getByTestId("cost-tile-perHour")).getByText("$0.27")).toBeInTheDocument();
    const idle = screen.getByTestId("cost-tile-idle");
    expect(within(idle).getByText("$4.24")).toBeInTheDocument();
    expect(within(idle).getByText("33% of the total")).toBeInTheDocument();
  });

  it("never shows a cost without saying it is an estimate", async () => {
    renderCosts();
    expect(await screen.findByText(/Price table v1, prices from 2026-09-17/)).toBeInTheDocument();
    expect(screen.getByText(/ignore committed-use discounts/)).toBeInTheDocument();
  });

  it("names the hosts it could not price instead of counting them as free", async () => {
    renderCosts();
    expect(await screen.findByText(/1 host\(s\) could not be priced/)).toBeInTheDocument();
    // worker-1 is listed, marked as unpriced rather than as costing nothing.
    expect(screen.getByRole("link", { name: "worker-1" })).toBeInTheDocument();
    expect(screen.getByText("Not priced")).toBeInTheDocument();
  });

  it("breaks the total down into the four buckets, with idle kept separate", async () => {
    renderCosts();
    expect(await screen.findByText("Where the money goes")).toBeInTheDocument();
    expect(screen.getByText("Services")).toBeInTheDocument();
    expect(screen.getByText("Unlinked containers")).toBeInTheDocument();
    expect(screen.getByText("Outside containers")).toBeInTheDocument();
    expect(screen.getByText("Capacity nobody used. It is never spread over the services.")).toBeInTheDocument();
    // The bar is an exact decomposition, so it is labelled for screen readers too.
    expect(screen.getByRole("img", { name: /Idle: \$4\.24/ })).toBeInTheDocument();
  });

  it("lists cost by service and by host, linking each one", async () => {
    renderCosts();
    expect(await screen.findByRole("link", { name: "orders" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "catalog" })).toBeInTheDocument();

    const web = screen.getByRole("link", { name: "web-1" });
    expect(web.getAttribute("href")).toContain(`/hosts/`);
    expect(screen.getByText(/m5\.large/)).toBeInTheDocument();
    // db-1 has no cloud instance facts and is priced from vCPU and memory instead.
    expect(screen.getByRole("link", { name: "db-1" })).toBeInTheDocument();
    expect(screen.getByText(/Estimated from vCPU and memory/)).toBeInTheDocument();
  });

  it("draws the trend of total and idle cost", async () => {
    renderCosts();
    expect(await screen.findByTestId("chart")).toHaveTextContent("Cost over time");
  });
});
