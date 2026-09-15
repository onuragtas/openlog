import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { getSupportSession, setSupportSession } from "@/api/supportSession";
import { SupportAccessSettings } from "@/components/settings/SupportAccessSettings";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockOperator, setMockOperator } from "@/mocks/operator";
import { addMockDeletionCertificate, resetMockPrivacy } from "@/mocks/privacy";
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
  resetMockPrivacy();
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

  it("offers only plans with trial_days in the trial picker and defaults the days to the plan's", async () => {
    const user = userEvent.setup();
    renderWith(() => <OperatorOrgDetail orgId="spammy" />);
    expect(await screen.findByRole("heading", { name: "Spammy Ltd" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Start trial" }));
    const picker = screen.getByLabelText("Trial plan");
    await waitFor(() => expect(Array.from((picker as HTMLSelectElement).options).map((o) => o.textContent)).toEqual(["", "Pro (14 days)"]));
    await user.selectOptions(picker, "pro");
    expect(screen.getByLabelText("Days")).toHaveValue(14);
  });

  it("schedules an immediate deletion only with a reason and the typed tenant id, then cancels it", async () => {
    const user = userEvent.setup();
    let body: unknown;
    server.events.on("request:start", async ({ request }) => {
      if (request.method === "POST" && request.url.endsWith("/deletion")) body = await request.clone().json();
    });
    renderWith(() => <OperatorOrgDetail orgId="spammy" />);
    expect(await screen.findByText("No deletion certificates for this tenant.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Schedule deletion" }));
    const form = screen.getByRole("form", { name: "Schedule deletion" });
    await user.type(within(form).getByLabelText("Reason (written to the organization's audit log)"), "fraud ticket 7");
    expect(within(form).getByRole("button", { name: "Schedule deletion" })).toBeEnabled();
    await user.click(within(form).getByLabelText("Delete immediately (skip the grace period)"));
    const submit = within(form).getByRole("button", { name: "Delete immediately" });
    expect(submit).toBeDisabled();
    await user.type(within(form).getByLabelText("Type the tenant id (spammy) to confirm"), "spam");
    expect(submit).toBeDisabled();
    await user.type(within(form).getByLabelText("Type the tenant id (spammy) to confirm"), "my");
    await user.click(submit);
    const pending = await screen.findByTestId("operator-deletion-pending");
    expect(pending).toHaveTextContent("Scheduled for deletion");
    expect(pending).toHaveTextContent("fraud ticket 7");
    expect(body).toEqual({ reason: "fraud ticket 7", immediate: true });
    server.events.removeAllListeners();

    await user.click(within(pending).getByRole("button", { name: "Cancel deletion" }));
    await user.click(within(pending).getByRole("button", { name: "Confirm cancellation" }));
    await waitFor(() => expect(screen.queryByTestId("operator-deletion-pending")).not.toBeInTheDocument());
    expect(await screen.findByText("Cancelled")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Schedule deletion" })).toBeInTheDocument();
  });

  it("lists the tenant's deletion certificates", async () => {
    addMockDeletionCertificate("spammy", {
      id: "cert-1", subject_type: "organization", subject_hash: "", initiator: "operator", requested_at: "2026-09-01T00:00:00Z",
      grace_ended_at: "2026-09-08T00:00:00Z", started_at: "2026-09-08T00:00:00Z", completed_at: "2026-09-08T01:00:00Z",
      postgres_rows: { dashboards: 3, memberships: 2 }, clickhouse_rows: { logs: 1000 }, verified: true,
    });
    addMockDeletionCertificate("other-tenant", {
      id: "cert-2", subject_type: "organization", subject_hash: "", initiator: "owner", requested_at: "2026-09-01T00:00:00Z",
      grace_ended_at: null, started_at: "2026-09-01T00:00:00Z", completed_at: "2026-09-01T01:00:00Z", postgres_rows: {}, clickhouse_rows: {}, verified: false,
    });
    renderWith(() => <OperatorOrgDetail orgId="spammy" />);
    const list = await screen.findByTestId("deletion-certificates");
    expect(list).toHaveTextContent("cert-1");
    expect(list).toHaveTextContent("verified");
    expect(list).toHaveTextContent("5 PostgreSQL rows · 1000 ClickHouse rows");
    expect(list).not.toHaveTextContent("cert-2");
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
