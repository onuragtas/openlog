import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_RUM_APP, MOCK_RUM_SESSION_ID } from "@/mocks/rum";
import { RumApps } from "./RumApps";
import { RumOverviewTab } from "./RumOverviewTab";
import { RumPagesTab } from "./RumPagesTab";
import { RumSessionsTab } from "./RumSessionsTab";

// uPlot needs matchMedia/canvas; the chart itself is covered by TimeSeriesChart.test.tsx.
vi.mock("@/components/TimeSeriesChart", () => ({
  TimeSeriesChart: ({ title, series }: { title: string; series?: { label: string }[] }) => (
    <div role="img" aria-label={title} data-series={series?.map((s) => s.label).join(",")} />
  ),
}));

const range = { range: "1h" };

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("RUM", () => {
  it("lists browser applications and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<RumApps range={range} onOpen={onOpen} />);

    const list = await screen.findByTestId("rum-apps");
    expect(within(list).getByText(MOCK_RUM_APP)).toBeInTheDocument();

    await user.click(within(list).getByRole("button", { name: MOCK_RUM_APP }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ app: MOCK_RUM_APP, environment: "production" }));
  });

  it("scores each vital on its p75 with the rating the server sent", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumOverviewTab range={range} app={MOCK_RUM_APP} />);

    const cards = await screen.findAllByTestId("rum-vital");
    expect(cards).toHaveLength(5);
    // The mock application loads fast but shifts badly: LCP is good, INP needs work, CLS is poor.
    expect(within(cards[0]!).getByText("LCP")).toBeInTheDocument();
    expect(within(cards[0]!).getByText("Good")).toBeInTheDocument();
    expect(within(cards[1]!).getByText("Needs work")).toBeInTheDocument();
    expect(within(cards[2]!).getByText("Poor")).toBeInTheDocument();
    // CLS is unitless, so its p75 is a score and not a duration.
    expect(within(cards[2]!).getByText("0.31")).toBeInTheDocument();
  });

  it("sorts pages by the measure the user picks", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSort = vi.fn();
    renderWithClient(<RumPagesTab range={range} app={MOCK_RUM_APP} sort="views" onSort={onSort} />);

    const table = await screen.findByTestId("rum-pages");
    const routes = within(table).getAllByTestId("rum-page-row");
    expect(routes).toHaveLength(3);
    expect(within(routes[0]!).getByText("/")).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Sort by"), "slowest");
    expect(onSort).toHaveBeenCalledWith("slowest");
  });

  it("lists sessions and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<RumSessionsTab range={range} app={MOCK_RUM_APP} onOpen={onOpen} />);

    const table = await screen.findByTestId("rum-sessions");
    expect(within(table).getAllByTestId("rum-session-row")).toHaveLength(2);

    await user.click(within(table).getByRole("button", { name: `Open session ${MOCK_RUM_SESSION_ID}` }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ session_id: MOCK_RUM_SESSION_ID }));
  });
});
