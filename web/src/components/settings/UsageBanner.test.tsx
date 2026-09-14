import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { server } from "@/mocks/server";
import { UsageBanner } from "./UsageBanner";

function renderBanner() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: UsageBanner });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe("UsageBanner", () => {
  it("warns about the metric furthest over its limit and links to the usage page", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderBanner();
    // Mock default organization: users 3 of 3 (exceeded), ingest 85 %.
    expect(await screen.findByText("Users is over your plan's limit (100%).")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View usage" })).toHaveAttribute("href", "/settings/usage");
  });

  it("reports blocked ingest", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    server.use(
      http.get("*/api/v1/usage/status", () =>
        HttpResponse.json({ level: "exceeded", ingest_blocked: true, saas_mode: true, metrics: [{ metric: "ingest_bytes", used: 2, limit: 1, percent: 200, level: "exceeded" }] }),
      ),
    );
    renderBanner();
    expect(await screen.findByText("Your monthly ingest quota is used up: new data is being rejected.")).toBeInTheDocument();
  });

  it("stays hidden within the limits", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // 2 GiB of 100, 1 host, 1 user
    renderBanner();
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
