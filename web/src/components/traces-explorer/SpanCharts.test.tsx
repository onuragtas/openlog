import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "i18next";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { api, unwrap } from "@/api/client";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_DASHBOARD_IDS } from "@/mocks/dashboards";
import { SpanCharts } from "./SpanCharts";

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

const FILTER = { filters: [{ key: "service.name", op: "=" as const, value: "checkout" }], groups: [] };
const noop = () => {};

describe("SpanCharts add to dashboard", { timeout: 20_000 }, () => {
  it("adds the span count chart with root-only and group-by to an existing dashboard", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithApp(<SpanCharts range={{ range: "1h" }} filter={FILTER} rootOnly groupBy="name" onGroupByChange={noop} onZoom={noop} />);

    await user.click(await screen.findByRole("button", { name: "Add to dashboard: Span count by name" }));
    const query = "SELECT count(*) FROM Span WHERE parent.id IS NULL AND service.name = 'checkout' FACET name LIMIT 10 TIMESERIES AUTO";
    expect(await screen.findByText(query)).toBeInTheDocument();
    await user.selectOptions(await screen.findByLabelText("Dashboard"), MOCK_DASHBOARD_IDS.infra);
    await user.click(screen.getByRole("button", { name: "Add widget" }));
    expect(await screen.findByText(/^Added to /)).toBeInTheDocument();

    const d = unwrap(await api.GET("/api/v1/dashboards/{id}", { params: { path: { id: MOCK_DASHBOARD_IDS.infra } } }));
    expect(d.pages[0]!.widgets.at(-1)).toMatchObject({ title: "Span count by name", visualization: "bar", query, options: { stacked: true } });
  });

  it("adds the duration percentiles to a new dashboard in milliseconds", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithApp(<SpanCharts range={{ range: "1h" }} filter={FILTER} rootOnly={false} groupBy="~" onGroupByChange={noop} onZoom={noop} />);

    await user.click(await screen.findByRole("button", { name: "Add to dashboard: Span duration" }));
    await user.click(await screen.findByRole("radio", { name: "A new dashboard" }));
    await user.type(screen.getByLabelText(i18n.t("dashboards.fields.name")), "Checkout spans");
    await user.click(screen.getByRole("button", { name: "Add widget" }));
    expect(await screen.findByText("Added to Checkout spans.")).toBeInTheDocument();

    const list = unwrap(await api.GET("/api/v1/dashboards", { params: { query: {} } })).dashboards;
    const created = unwrap(await api.GET("/api/v1/dashboards/{id}", { params: { path: { id: list.find((x) => x.name === "Checkout spans")!.id } } }));
    expect(created.pages[0]!.widgets[0]).toMatchObject({
      title: "Span duration",
      visualization: "line",
      unit: "ms",
      query: "SELECT percentile(duration.ms, 50, 95, 99) FROM Span WHERE service.name = 'checkout' TIMESERIES AUTO",
    });
  });
});
