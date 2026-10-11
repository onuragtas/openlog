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
import { RumFunnelTab } from "./RumFunnelTab";
import { RumPagesTab } from "./RumPagesTab";
import { RumReleasesTab } from "./RumReleasesTab";
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

  it("shows each vital's spread and how far the p75 is from the threshold", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumOverviewTab range={range} app={MOCK_RUM_APP} />);

    const cards = await screen.findAllByTestId("rum-vital");
    // One number is a verdict, three are a shape: p50/p75/p95 are all on the card.
    const lcp = within(cards[0]!);
    expect(lcp.getByText("1.62 s")).toBeInTheDocument();
    expect(lcp.getByText("3.48 s")).toBeInTheDocument();
    // The published boundaries, and the distance that decides the verdict.
    expect(lcp.getByText("Good ≤ 2.5 s · poor > 4 s")).toBeInTheDocument();
    expect(lcp.getByText("290 ms of headroom (2.5 s)")).toBeInTheDocument();
    // A poor vital is told how far over it is, against the boundary it has to get back under.
    expect(within(cards[2]!).getByText("0.06 over the threshold (0.25)")).toBeInTheDocument();
  });

  it("opens a page and asks for the vitals of that route alone", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<RumPagesTab range={range} app={MOCK_RUM_APP} sort="views" onSort={vi.fn()} />);

    const table = await screen.findByTestId("rum-pages");
    await user.click(within(table).getByRole("button", { name: "Details of /checkout" }));

    const vitals = await screen.findByTestId("rum-page-vitals");
    // The mock shop's LCP is good overall and poor on /checkout: the panel asked per route, not reused
    // the application's numbers.
    // Twice on the card: the headline p75 and the p75 cell of the spread.
    expect(within(vitals).getAllByText("4.3 s").length).toBe(2);
    expect(within(vitals).getAllByText("Poor").length).toBeGreaterThan(0);
    // The row's own figures come with it, including the slowest single load.
    expect(screen.getByText("9.1 s")).toBeInTheDocument();
  });

  it("lists versions with their crash-free rate, and marks a rollback", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumReleasesTab range={range} app={MOCK_RUM_APP} />);

    const table = await screen.findByTestId("rum-releases");
    const rows = within(table).getAllByTestId("rum-release-row");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByText("v1.4.2")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("99%")).toBeInTheDocument();
    // A version the trace retention no longer covers says so rather than reading as a perfect release.
    expect(within(rows[2]!).getByText("no sessions")).toBeInTheDocument();

    const deployments = screen.getByTestId("rum-deployments");
    expect(within(deployments).getByText("rollback")).toBeInTheDocument();
  });

  it("asks for a funnel once there are two steps, and shows what each one lost", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onChange = vi.fn();
    const { rerender } = renderWithClient(
      <RumFunnelTab range={range} app={MOCK_RUM_APP} steps={[]} funnelWindow="30m" onChange={onChange} />,
    );

    // One step is not a funnel, and the endpoint refuses it: nothing is asked until there are two.
    expect(screen.getByText("Enter at least two steps to see the funnel.")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Steps (in order, comma separated)"), "cart_viewed, order_placed");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    expect(onChange).toHaveBeenCalledWith({ steps: ["cart_viewed", "order_placed"] });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    rerender(
      <QueryClientProvider client={client}>
        <RumFunnelTab range={range} app={MOCK_RUM_APP} steps={["cart_viewed", "order_placed"]} funnelWindow="30m" onChange={onChange} />
      </QueryClientProvider>,
    );

    const funnel = await screen.findByTestId("rum-funnel");
    const steps = within(funnel).getAllByTestId("rum-funnel-step");
    expect(steps).toHaveLength(2);
    expect(within(steps[0]!).getByText("100%")).toBeInTheDocument();
    expect(within(steps[1]!).getByText("75%")).toBeInTheDocument();
    // The drop that matters is against the step before, which for the second step is the same 25%.
    expect(within(steps[1]!).getByText(/25% lost/)).toBeInTheDocument();
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
