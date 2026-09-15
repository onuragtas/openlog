import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { api, unwrap } from "@/api/client";
import type { FilterState } from "@/api/explorer";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_DASHBOARD_IDS } from "@/mocks/dashboards";
import { LogVolumeChart } from "./LogVolumeChart";

// uPlot needs matchMedia/canvas; charts have their own tests.
vi.mock("@/components/TimeSeriesChart", () => ({ TimeSeriesChart: () => null }));

function renderWithApp(ui: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const FILTER: FilterState = { filters: [{ key: "service.name", op: "=", value: "checkout" }], groups: [], q: "timeout" };
const noop = () => {};

describe("LogVolumeChart add to dashboard", { timeout: 20_000 }, () => {
  it("adds the volume chart as a stacked bar widget with the explorer conditions", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithApp(<LogVolumeChart range={{ range: "1h" }} filter={FILTER} groupBy="service.name" onGroupByChange={noop} onZoom={noop} />);

    await user.click(await screen.findByRole("button", { name: "Add to dashboard: Log volume by service.name" }));
    const query = "SELECT count(*) FROM Log WHERE event.name NOT LIKE 'openlog.inventory.%' AND message CONTAINS 'timeout' AND service.name = 'checkout' FACET service.name LIMIT 10 TIMESERIES AUTO";
    expect(await screen.findByText(query)).toBeInTheDocument();
    await user.selectOptions(await screen.findByLabelText("Dashboard"), MOCK_DASHBOARD_IDS.infra);
    await user.click(screen.getByRole("button", { name: "Add widget" }));
    expect(await screen.findByText(/^Added to /)).toBeInTheDocument();

    const d = unwrap(await api.GET("/api/v1/dashboards/{id}", { params: { path: { id: MOCK_DASHBOARD_IDS.infra } } }));
    const added = d.pages[0]!.widgets.at(-1)!;
    expect(added).toMatchObject({ title: "Log volume by service.name", visualization: "bar", query, options: { legend: true, stacked: true } });
  });

  it("is disabled for logs of an APM transaction", async () => {
    renderWithApp(
      <LogVolumeChart range={{ range: "1h" }} filter={FILTER} groupBy="~" onGroupByChange={noop} onZoom={noop} context={{ transaction: "GET /orders", transactionService: "checkout" }} />,
    );
    expect(await screen.findByRole("button", { name: "Add to dashboard: The APM transaction filter has no OQL equivalent" })).toBeDisabled();
  });
});
