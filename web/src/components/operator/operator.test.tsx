import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { getSupportSession, setSupportSession } from "@/api/supportSession";
import { SupportAccessSettings } from "@/components/settings/SupportAccessSettings";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockOperator, setMockOperator } from "@/mocks/operator";
import { server } from "@/mocks/server";
import { OperatorConsole } from "./OperatorConsole";
import { OperatorOrgDetail } from "./OperatorOrgDetail";
import { SaaSBanner } from "./SaaSBanner";

// The plan editor (UsageSettings) pulls in uPlot, which needs matchMedia/canvas; charts have their own tests.
vi.mock("@/components/TimeSeriesChart", () => ({ TimeSeriesChart: () => null }));

function renderWith(Component: () => ReactElement | null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: Component });
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

describe("operator console", () => {
  it("lists organizations and resolves a flag with a note", async () => {
    const user = userEvent.setup();
    renderWith(OperatorConsole);
    expect(await screen.findByRole("link", { name: "Spammy Ltd" })).toBeInTheDocument();
    expect(screen.getByText("3 organizations")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Flagged" }));
    expect(await screen.findByText("Many hosts in a new organization")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    const confirm = screen.getByRole("button", { name: "Confirm" });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText("Reason (written to the organization's audit log)"), "known customer onboarding");
    await user.click(confirm);
    expect(await screen.findByText("No open flags.")).toBeInTheDocument();
  });

  it("hides the console from non-operators", async () => {
    setMockOperator(false);
    renderWith(OperatorConsole);
    expect(await screen.findByText("This page is only available to openlog operators.")).toBeInTheDocument();
  });

  it("suspends an organization only with a reason", async () => {
    const user = userEvent.setup();
    let body: unknown;
    server.events.on("request:start", async ({ request }) => {
      if (request.url.endsWith("/suspend")) body = await request.clone().json();
    });
    renderWith(() => <OperatorOrgDetail orgId="spammy" />);
    expect(await screen.findByRole("heading", { name: "Spammy Ltd" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Suspend" }));
    await user.type(screen.getByLabelText("Reason (written to the organization's audit log)"), "abuse ticket 42");
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    expect(await screen.findByRole("button", { name: "Unsuspend" })).toBeInTheDocument();
    expect(body).toEqual({ reason: "abuse ticket 42" });
    server.events.removeAllListeners();
  });

  it("opens a support view only after the owner granted access", async () => {
    renderWith(() => <OperatorOrgDetail orgId="default" />);
    expect(await screen.findByRole("heading", { name: "Default" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open support view" })).toBeDisabled();
  });
});

describe("support access and SaaS banners", () => {
  it("lets an owner grant and revoke support access", async () => {
    const user = userEvent.setup();
    renderWith(SupportAccessSettings);
    expect(await screen.findByText("Support access is not allowed.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Allow for 24 hours" }));
    expect(await screen.findByText(/Support access is allowed until/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Revoke access" }));
    await user.click(screen.getByRole("button", { name: "Confirm revoke" }));
    expect(await screen.findByText("Support access is not allowed.")).toBeInTheDocument();
  });

  it("shows the suspension banner", async () => {
    server.use(
      http.get("*/api/v1/orgs/current/saas", () =>
        HttpResponse.json({ saas_mode: true, suspended: true, trial: null, support_access: null, support_session: null, can_manage_support_access: true }),
      ),
    );
    renderWith(SaaSBanner);
    expect(await screen.findByRole("alert")).toHaveTextContent("This organization is suspended");
  });

  it("shows the support view banner and exits it", async () => {
    const user = userEvent.setup();
    setSupportSession({ id: "ss-1", org_id: "o", org_name: "Acme", expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    renderWith(SaaSBanner);
    expect(await screen.findByTestId("support-banner")).toHaveTextContent("Support view of Acme (read-only)");
    await user.click(screen.getByRole("button", { name: "Exit support view" }));
    await waitFor(() => expect(getSupportSession()).toBeNull());
  });
});
