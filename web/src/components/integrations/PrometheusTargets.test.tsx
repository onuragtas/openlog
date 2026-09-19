import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { server } from "@/mocks/server";
import { PrometheusTargetsCard } from "./PrometheusTargets";

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: PrometheusTargetsCard });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe("PrometheusTargetsCard", () => {
  it("lists targets with the down ones first and asks for the up series of scraped targets", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    let body: Record<string, unknown> | undefined;
    const now = Date.now();
    server.use(
      http.post("*/api/v1/metrics/query", async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          metric: { name: "up", type: "gauge", unit: "1", temporality: "unspecified", monotonic: false },
          aggregation: "last",
          step: "60s",
          truncated: false,
          series: [
            { attributes: { "host.id": "h1", "host.name": "web-1", "service.name": "node", "resource.service.instance.id": "127.0.0.1:9100", "resource.openlog.scrape.source": "static" }, points: [[now - 60_000, 1]] },
            { attributes: { "host.id": "h1", "host.name": "web-1", "service.name": "api", "resource.service.instance.id": "172.17.0.2:9090", "resource.openlog.scrape.source": "container" }, points: [[now - 60_000, 0]] },
          ],
        });
      }),
    );
    renderCard();
    const card = await screen.findByTestId("prometheus-targets");
    expect(await within(card).findByTestId("prometheus-summary")).toHaveTextContent("1 down");
    const rows = within(card).getAllByTestId("prometheus-target");
    expect(rows.map((r) => r.getAttribute("data-up"))).toEqual(["false", "true"]);
    expect(rows[0]).toHaveTextContent("api");
    expect(rows[0]).toHaveTextContent("Container label");
    expect(within(rows[1]!).getByRole("link", { name: "web-1" })).toHaveAttribute("href", "/hosts/h1");
    expect(body).toMatchObject({ metric: "up", aggregation: "last", filters: [{ key: "resource.openlog.integration.id", op: "=", value: "prometheus" }] });
  });

  it("stays hidden while nothing is scraped", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    server.use(http.post("*/api/v1/metrics/query", () => HttpResponse.json({ code: "not_found", message: "metric not found in the time range" }, { status: 404 })));
    renderCard();
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByTestId("prometheus-targets")).not.toBeInTheDocument();
  });
});
