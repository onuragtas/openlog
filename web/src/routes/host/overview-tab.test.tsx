import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { setSupportSession } from "@/api/supportSession";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockOperator, setMockOrgSuspended } from "@/mocks/operator";
import { MetricChartCard, OVERVIEW_CHARTS } from "./overview-tab";

// uPlot needs matchMedia/canvas; the chart itself is covered by TimeSeriesChart.test.tsx.
vi.mock("@/components/TimeSeriesChart", () => ({
  TimeSeriesChart: ({ title }: { title: string }) => <div data-testid="chart">{title}</div>,
}));

const HOST = "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b"; // web-1 in mocks/fixtures.ts

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <MetricChartCard hostId={HOST} range={{ range: "1h" }} def={OVERVIEW_CHARTS[0]!} canAlert /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

beforeEach(async () => {
  resetMockOperator();
  setSupportSession(null);
  await login(MOCK_EMAIL, MOCK_PASSWORD);
});
afterEach(() => {
  resetMockOperator();
});

describe("host overview chart shortcuts", { timeout: 20_000 }, () => {
  it("active organization: create alert is a link", async () => {
    renderCard();
    const link = await screen.findByRole("link", { name: "Create alert from this metric: CPU utilization by mode" });
    expect(link.getAttribute("href")).toContain("/alerts/rules/new");
    expect(screen.getByRole("button", { name: /Add to dashboard/ })).toBeEnabled();
  });

  it("suspended organization: create alert and add to dashboard are disabled with the reason", async () => {
    setMockOrgSuspended("default", true);
    renderCard();
    const alert = await screen.findByRole("button", { name: "Create alert from this metric: CPU utilization by mode" });
    expect(alert).toBeDisabled();
    expect(alert.closest("[data-testid=read-only-guard]")).toHaveAttribute("title", expect.stringContaining("suspended"));
    expect(alert.closest("fieldset")).toHaveAccessibleDescription(/suspended/);
    expect(screen.queryByRole("link", { name: /Create alert/ })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: /Add to dashboard/ })).toBeDisabled());
  });
});
