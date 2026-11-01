import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_RUM_APP, MOCK_RUM_SESSION_ID } from "@/mocks/rum";
import { server } from "@/mocks/server";
import { RumAnalyticsTab } from "./RumAnalyticsTab";
import { RumApps } from "./RumApps";
import { RumOverviewTab } from "./RumOverviewTab";
import { RumCrashesTab } from "./RumCrashesTab";
import { RumFunnelTab } from "./RumFunnelTab";
import { RumPagesTab } from "./RumPagesTab";
import { RumReleasesTab } from "./RumReleasesTab";
import { RumSessionDetail } from "./RumSessionDetail";
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

// The two detail panels link onwards with TanStack's <Link>, which reads the router out of context and
// throws without one. A memory router is what the rest of the suite gives such components.
function renderWithRouter(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  );
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

/** Answers a list with this body and keeps what each request asked. */
function spyOn(url: string, body: Record<string, unknown>) {
  const asked: Record<string, string>[] = [];
  server.use(
    http.get(url, ({ request }) => {
      asked.push(Object.fromEntries(new URL(request.url).searchParams));
      return HttpResponse.json(body as never);
    }),
  );
  return asked;
}

const last = <T,>(items: T[]) => items[items.length - 1];

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
    // CLS is unitless, so its p75 is a score and not a duration. Twice on the card: the headline and the
    // p75 cell of the spread.
    expect(within(cards[2]!).getAllByText("0.31")).toHaveLength(2);
  });

  it("sorts pages on the server by their headings, and pages with offset", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const page = { route: "/", views: 1, avg_ms: 1, p50_ms: 1, p75_ms: 1, p95_ms: 1, max_ms: 1, ttfb_avg_ms: 1, lcp_p75: 1, errors: 0 };
    const asked = spyOn("*/api/v1/rum/pages", { pages: [page], has_more: true });
    renderWithClient(<RumPagesTab range={range} app={MOCK_RUM_APP} facets={{}} onFacets={vi.fn()} />);

    await screen.findByTestId("rum-page-row");
    expect(last(asked)).toMatchObject({ sort: "views", dir: "desc", offset: "0" });
    expect(screen.queryByLabelText("Sort by")).toBeNull();

    // What the dropdown offered is all on a heading now, and what the server can't order is not pretending.
    await user.click(screen.getByRole("button", { name: "Total time" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "slowest", dir: "desc" }));
    await user.click(screen.getByRole("button", { name: "Average" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "avg", dir: "desc" }));
    await user.click(screen.getByRole("button", { name: "Route" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "route", dir: "asc" }));
    expect(screen.queryByRole("button", { name: "p75" })).toBeNull();
    expect(screen.queryByRole("button", { name: "LCP p75" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "route", offset: "50" }));
  });

  it("opens the pages on the sort a link asked for", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const asked = spyOn("*/api/v1/rum/pages", { pages: [], has_more: false });
    renderWithClient(<RumPagesTab range={range} app={MOCK_RUM_APP} initialSort="slowest" facets={{}} onFacets={vi.fn()} />);
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "slowest", dir: "desc" }));
  });

  it("sorts and pages browser applications on the server", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const asked = spyOn("*/api/v1/rum/apps", { apps: [{ app: "a", environment: "", views: 1, sessions: 1, errors: 0, last_seen: "" }], has_more: true });
    renderWithClient(<RumApps range={range} onOpen={vi.fn()} />);
    await screen.findByTestId("rum-app-row");
    await user.click(screen.getByRole("button", { name: "Errors" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "errors", dir: "desc" }));
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "errors", offset: "50" }));
  });

  it("sorts and pages sessions on the server, one person's as well", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const session = { session_id: MOCK_RUM_SESSION_ID, app: MOCK_RUM_APP, environment: "", started_at: "", ended_at: "", duration_ms: 1, page_views: 1, errors: 0, entry_route: "/", exit_route: "/",
      device_type: "", browser_name: "", browser_version: "", os_name: "", trace_id: "", user_id: "", country: "" };
    const asked = spyOn("*/api/v1/rum/sessions", { sessions: [session], has_more: true });
    renderWithClient(<RumSessionsTab range={range} app={MOCK_RUM_APP} onOpen={vi.fn()} facets={{}} onFacets={vi.fn()} userId="u1" onUser={vi.fn()} />);
    await screen.findByTestId("rum-session-row");
    await user.click(screen.getByRole("button", { name: "Duration" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "duration", dir: "desc", user_id: "u1" }));
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "duration", offset: "50", user_id: "u1" }));
  });

  it("sorts and pages versions on the server, and keeps the deployments", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const release = { version: "1.0", sessions: 1, error_sessions: 0, crashed_sessions: 0, capturing_sessions: 0, error_free_rate: 1, session_crash_free_rate: null, crash_free_rate: 1, first_seen: "", last_seen: "" };
    const deployment = { timestamp: "", t: 1, service_namespace: "", environment: "", version: "1.0", previous_version: "", initial: true, rollback: false };
    const asked = spyOn("*/api/v1/rum/releases", { releases: [release], deployments: [deployment], has_more: true });
    renderWithClient(<RumReleasesTab range={range} app={MOCK_RUM_APP} />);
    await screen.findByTestId("rum-release-row");
    await user.click(screen.getByRole("button", { name: "Crash-free" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "crash_free", dir: "desc" }));
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "crash_free", offset: "50" }));
    expect(screen.getByTestId("rum-deployments")).toBeInTheDocument();
  });

  it("sorts an analytics list in the browser, with what only the previous period had still reachable", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    // Sixty current names, then what stopped altogether: the server adds those at the end with a zero (compareRows).
    const events = Array.from({ length: 60 }, (_, i) => ({ name: `e${String(i).padStart(2, "0")}`, count: 1000 - i, sessions: i + 1, users: 1, previous_count: 1, previous_sessions: 1, previous_users: 1 }));
    events.push({ name: "stopped", count: 0, sessions: 0, users: 0, previous_count: 5000, previous_sessions: 900, previous_users: 500 });
    const asked = spyOn("*/api/v1/analytics/events", { events });
    renderWithClient(<RumAnalyticsTab app={MOCK_RUM_APP} />);
    const table = await screen.findByTestId("analytics-events");
    const names = () => within(table).getAllByTestId("analytics-events-row").map((r) => within(r).getByRole("rowheader").textContent);
    expect(names()).toHaveLength(50);
    expect(names()[0]).toBe("e00");
    expect(names()).not.toContain("stopped");
    const section = table.closest("div")!;

    // It is on the last page of the largest first, at its zero, with its fall still showing.
    await user.click(within(section).getByRole("button", { name: "Next" }));
    expect(names()).toHaveLength(11);
    expect(names()[10]).toBe("stopped");
    expect(within(table).getAllByTestId("analytics-events-row")[10]).toHaveTextContent("−5,000");

    // A new order goes back to the first page, where the zero now leads.
    await user.click(within(table).getByRole("button", { name: "Count" }));
    expect(names()[0]).toBe("stopped");
    await user.click(within(table).getByRole("button", { name: "Event" }));
    await user.click(within(section).getByRole("button", { name: "Next" }));
    expect(names()).toContain("stopped");
    // Sorting and paging asked the server nothing, and it was not told how.
    expect(asked).toHaveLength(1);
    expect(asked[0]!.sort).toBeUndefined();
    expect(asked[0]!.offset).toBeUndefined();
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
    renderWithRouter(<RumPagesTab range={range} app={MOCK_RUM_APP} facets={{}} onFacets={vi.fn()} />);

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
    expect(within(rows[0]!).getByText("99.6%")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("98.8%")).toBeInTheDocument();
    // A version with no sessions in the rollup says so rather than reading as a perfect release.
    expect(within(rows[2]!).getByText("no sessions")).toBeInTheDocument();

    const deployments = screen.getByTestId("rum-deployments");
    expect(within(deployments).getByText("rollback")).toBeInTheDocument();
  });

  it("lists what crashed, with its stack", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumCrashesTab range={range} app={MOCK_RUM_APP} />);

    const crash = await screen.findByTestId("rum-crash");
    expect(within(crash).getByText("IllegalStateException")).toBeInTheDocument();
    expect(within(crash).getByText("Uncaught exception")).toBeInTheDocument();
    expect(within(crash).getByText("at a.b.c(SourceFile:1)")).toBeInTheDocument();
  });

  it("does not claim a crash-free rate for a version that cannot report crashes", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumReleasesTab range={range} app={MOCK_RUM_APP} />);

    const rows = await screen.findAllByTestId("rum-release-row");
    expect(within(rows[0]!).getByText("99.6%")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("—")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("84.1%")).toBeInTheDocument();
  });

  it("shows active users and a retention table", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumAnalyticsTab app={MOCK_RUM_APP} />);

    expect(await screen.findByText("DAU / MAU")).toBeInTheDocument();
    const table = await screen.findByTestId("analytics-retention");
    // The share is written in the cell, so the tint is never the only way to read it.
    expect(within(table).getAllByText("100%").length).toBe(6);
    expect(within(table).getByText("Week 0")).toBeInTheDocument();
    expect(within(await screen.findByTestId("analytics-events")).getByText("checkout_started")).toBeInTheDocument();
    // An empty source is a direct visit, and it has to say so rather than show a blank row.
    expect(within(await screen.findByTestId("analytics-sources")).getByText("(direct)")).toBeInTheDocument();
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

  it("narrows the pages to one kind of visitor", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onFacets = vi.fn();
    const { rerender } = renderWithClient(
      <RumPagesTab range={range} app={MOCK_RUM_APP} facets={{}} onFacets={onFacets} />,
    );
    await screen.findByTestId("rum-pages");

    await user.selectOptions(screen.getByLabelText("Device"), "mobile");
    expect(onFacets).toHaveBeenCalledWith({ device_type: "mobile" });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    rerender(
      <QueryClientProvider client={client}>
        <RumPagesTab range={range} app={MOCK_RUM_APP} facets={{ device_type: "mobile" }} onFacets={onFacets} />
      </QueryClientProvider>,
    );

    // The mock shop is slower on phones: the numbers are the device's, not the application's. Awaited
    // rather than read at once, because the table keeps the previous rows while the narrowed ones load.
    const table = await screen.findByTestId("rum-pages");
    expect(await within(table).findByText("2.36 s")).toBeInTheDocument();
  });

  it("follows one identity from the session it was seen on", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithRouter(<RumSessionDetail range={range} sessionId={MOCK_RUM_SESSION_ID} />);

    // The identity is the detail's answer alone -- the list reads a rollup that does not carry it --
    // and the link to the rest of that person's visits starts here rather than from a search box.
    expect(await screen.findByText("acct_8f3a2b")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sessions of this user" })).toBeInTheDocument();
  });

  it("lists the sessions of one identity, and says whose they are", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onUser = vi.fn();
    renderWithClient(
      <RumSessionsTab
        range={range}
        app={MOCK_RUM_APP}
        onOpen={vi.fn()}
        facets={{}}
        onFacets={vi.fn()}
        userId="acct_8f3a2b"
        onUser={onUser}
      />,
    );

    const table = await screen.findByTestId("rum-sessions");
    expect(within(table).getAllByTestId("rum-session-row")).toHaveLength(1);
    expect(screen.getByText("Sessions of acct_8f3a2b")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "All sessions" }));
    expect(onUser).toHaveBeenCalledWith(undefined);
  });

  it("lists sessions and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<RumSessionsTab range={range} app={MOCK_RUM_APP} onOpen={onOpen} facets={{}} onFacets={vi.fn()} onUser={vi.fn()} />);

    const table = await screen.findByTestId("rum-sessions");
    expect(within(table).getAllByTestId("rum-session-row")).toHaveLength(2);

    await user.click(within(table).getByRole("button", { name: `Open session ${MOCK_RUM_SESSION_ID}` }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ session_id: MOCK_RUM_SESSION_ID }));
  });

  it("shows how far each number moved, as a count and a share", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<RumAnalyticsTab app={MOCK_RUM_APP} />);
    const totals = await screen.findByTestId("analytics-totals");
    await waitFor(() => expect(totals).toHaveTextContent("+200(+20%)"));
    // A fall, and a rise from nothing, which has no percentage.
    expect(totals).toHaveTextContent("−100(−25%)");
    expect(totals).toHaveTextContent("+900(new)");
    expect(await screen.findByTestId("analytics-compare-range")).toHaveTextContent("2026-09-06");
  });
});
