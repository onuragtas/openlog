import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { OS_HOST_IDS } from "@/mocks/fixtures";
import { IisAppPoolsCard, IisSiteSelector } from "./integrations";

// uPlot needs matchMedia/canvas; the chart itself is covered by TimeSeriesChart.test.tsx.
vi.mock("@/components/TimeSeriesChart", () => ({
  TimeSeriesChart: ({ title }: { title: string }) => <div data-testid="chart">{title}</div>,
}));

const inst = { hostId: OS_HOST_IDS.win, discoveryId: "iis", instance: "W3SVC" };
const range = { range: "1h" };

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("IIS panel (win-iis-1 mock)", () => {
  it("lists application pools with translated, colored states", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<IisAppPoolsCard inst={inst} range={range} />);
    const rows = await screen.findAllByTestId("iis-app-pool");
    expect(rows.map((r) => [within(r).getAllByRole("cell")[0]!.textContent, r.querySelector("[data-state]")!.textContent, r.querySelector("[data-state]")!.getAttribute("data-slot")])).toEqual([
      ["api", "Shutdown pending", "badge"],
      ["DefaultAppPool", "Running", "badge"],
      ["legacy-reports", "Disabled", "badge"],
    ]);
    expect(rows[2]!.querySelector("[data-state]")!.className).toContain("destructive");
    expect(rows[1]!.querySelector("time")).not.toBeNull();
  });

  it("Turkish state names", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("tr");
    renderWithClient(<IisAppPoolsCard inst={inst} range={range} />);
    expect(await screen.findByText("Uygulama havuzları")).toBeInTheDocument();
    expect(await screen.findByText("Çalışıyor")).toBeInTheDocument();
    expect(screen.getByText("Devre dışı")).toBeInTheDocument();
  });

  it("offers all sites plus each site from the data and reports the choice", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const onChange = vi.fn();
    const user = userEvent.setup();
    renderWithClient(<IisSiteSelector inst={inst} range={range} onChange={onChange} />);
    const select = screen.getByLabelText("Site");
    await waitFor(() => expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["All sites", "api", "Default Web Site"]));
    await user.selectOptions(select, "Default Web Site");
    expect(onChange).toHaveBeenLastCalledWith("Default Web Site");
    await user.selectOptions(select, "");
    expect(onChange).toHaveBeenLastCalledWith(undefined);
  });
});
