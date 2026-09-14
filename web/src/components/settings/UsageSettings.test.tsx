import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { periodOptions, worstMetric } from "@/api/usage";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { resetMockUsage } from "@/mocks/usage";
import { UsageSettings } from "./UsageSettings";

// uPlot needs matchMedia/canvas; the chart itself is covered by TimeSeriesChart.test.tsx.
vi.mock("@/components/TimeSeriesChart", () => ({
  TimeSeriesChart: ({ title, series }: { title: string; series?: { label: string }[] }) => (
    <div role="img" aria-label={title} data-series={series?.map((s) => s.label).join(",")} />
  ),
}));

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("UsageSettings", () => {
  beforeEach(() => resetMockUsage());

  it("shows usage against the plan limits with projection, breakdown and top services", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<UsageSettings />);

    const ingest = await screen.findByRole("progressbar", { name: "Data ingest" });
    expect(ingest).toHaveAttribute("aria-valuenow", "85");
    expect(screen.getByText(/85(\.0)? GiB of 100(\.0)? GiB/)).toBeInTheDocument();
    expect(screen.getByText(/Projected at period end: 170(\.0)? GiB \(170% of the limit\)/)).toBeInTheDocument();
    expect(screen.getAllByText("Near limit").length).toBeGreaterThan(0); // ingest 85 %
    expect(screen.getByRole("progressbar", { name: "Users" })).toHaveAttribute("aria-valuenow", "100");
    expect(screen.getByText("Over limit")).toBeInTheDocument();
    expect(screen.getByText("Free")).toBeInTheDocument();
    expect(screen.getByText("Limits enforced")).toBeInTheDocument();

    expect(await screen.findByText("checkout")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Group by"), "Hosts");
    expect(await screen.findByText("web-1")).toBeInTheDocument();

    // Admins and owners can export.
    expect(screen.getByRole("button", { name: "Export CSV" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Export JSON" }));
  });

  it("lets operators assign a plan and overrides", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<UsageSettings />);
    const section = (await screen.findByRole("heading", { name: "Plan assignment (operator)" })).closest("section")!;
    const overrides = within(section).getByLabelText("Overrides (JSON)");
    await user.clear(overrides);
    await user.type(overrides, "[[1]"); // "[[" types a literal "["
    await user.click(within(section).getByRole("button", { name: "Save plan" }));
    expect(await within(section).findByText("Overrides must be a JSON object.")).toBeInTheDocument();

    await user.clear(overrides);
    await user.selectOptions(within(section).getByLabelText("Plan"), "Pro");
    await user.click(within(section).getByRole("button", { name: "Save plan" }));
    await waitFor(() => expect(screen.getByText(/85(\.0)? GiB of 1000(\.0)? GiB/)).toBeInTheDocument());
    expect(screen.getAllByText("Pro").length).toBeGreaterThan(1); // plan badge and the selected option
  });

  it("shows query limits with their sources and lets owners override them", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<UsageSettings />);
    const section = (await screen.findByRole("heading", { name: "Query limits" })).closest("section")!;
    const rows = within(section).getByTestId("query-limit-max_rows_to_read");
    expect(await within(section).findAllByText("Server default")).toHaveLength(3);

    await user.type(within(rows).getByRole("textbox"), "5000");
    await user.click(within(section).getByRole("button", { name: "Save limits" }));
    expect(await within(rows).findByText("Organization")).toBeInTheDocument();
    expect(within(section).getByText(/All servers apply it within 30 s/)).toBeInTheDocument();

    await user.type(within(within(section).getByTestId("query-limit-max_bytes_to_read")).getByRole("textbox"), "-1");
    await user.click(within(section).getByRole("button", { name: "Save limits" }));
    expect(await within(section).findByRole("alert")).toHaveTextContent("whole numbers");

    await user.click(within(section).getByRole("button", { name: "Use plan and server defaults" }));
    await waitFor(() => expect(within(section).getAllByText("Server default")).toHaveLength(3));
  });

  it("hides export for viewers", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // viewer there
    renderWithClient(<UsageSettings />);
    expect(await screen.findByRole("progressbar", { name: "Data ingest" })).toHaveAttribute("aria-valuenow", "2");
    expect(screen.queryByRole("button", { name: "Export CSV" })).not.toBeInTheDocument();
  });
});

describe("usage helpers", () => {
  it("offers current, previous and four earlier months", () => {
    expect(periodOptions(new Date(Date.UTC(2026, 0, 15)))).toEqual(["current", "previous", "2025-11", "2025-10", "2025-09", "2025-08"]);
  });

  it("picks the metric furthest over its limit", () => {
    const m = worstMetric([
      { metric: "hosts", used: 4, limit: 5, percent: 80, level: "warning" },
      { metric: "users", used: 4, limit: 3, percent: 133, level: "exceeded" },
      { metric: "ingest_bytes", used: 1, limit: 0, percent: 0, level: "ok" },
    ]);
    expect(m?.metric).toBe("users");
  });
});
