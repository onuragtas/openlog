import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { server } from "@/mocks/server";
import { AppDbQueries } from "./AppDbQueries";
import { DbActivityTab } from "./DbActivityTab";
import { DbInstances } from "./DbInstances";
import { DbQueriesTab } from "./DbQueriesTab";
import { DbQueryDetail } from "./DbQueryDetail";
import { DbSessionsTab } from "./DbSessionsTab";

// uPlot needs matchMedia/canvas; the chart itself is covered by TimeSeriesChart.test.tsx.
vi.mock("@/components/TimeSeriesChart", () => ({ TimeSeriesChart: ({ title }: { title: string }) => <div role="img" aria-label={title} /> }));

function renderRouted(ui: () => ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({ routeTree: createRootRoute({ component: ui }), history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const session = (id: string, blockedBy: string[], blocks: number, extra: Record<string, unknown> = {}) => ({
  session_id: id, state: "active", wait_type: "Lock", wait_event: "transactionid", db_name: "shop", user: "app", application: "api",
  client_address: "10.0.0.7", duration_ms: 1500, fingerprint: "42", text: "UPDATE orders SET amount = ? WHERE id = ?",
  blocking_session_ids: blockedBy, blocks, ...extra,
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

describe("database screens", () => {
  it("shows the blocking chain with the head first and opens a statement", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    let instance: string | null = null;
    server.use(
      http.get("*/api/v1/db/sessions", ({ request }) => {
        instance = new URL(request.url).searchParams.get("instance");
        return HttpResponse.json({
          sampled_at: "2026-09-19T10:00:00Z",
          sessions: [
            session("101", ["100"], 0),
            session("100", [], 1, { state: "idle in transaction", wait_type: "Client", wait_event: "ClientRead", fingerprint: "" }),
            session("200", [], 0, { wait_type: "", wait_event: "", text: "SELECT 1", fingerprint: "7" }),
          ],
        });
      }),
    );
    const onOpen = vi.fn();
    renderRouted(() => <DbSessionsTab instance="db1:5432" onOpenQuery={onOpen} />);
    const tree = await screen.findByTestId("db-blocking");
    const nodes = within(tree).getAllByTestId("db-blocking-node");
    expect(nodes.map((n) => n.getAttribute("data-session"))).toEqual(["100", "101"]);
    expect(nodes[0]).toHaveTextContent("blocks 1 session");
    expect(screen.getByText("CPU")).toBeInTheDocument(); // session 200 has no wait
    within(nodes[1]!).getByRole("button", { name: "UPDATE orders SET amount = ? WHERE id = ?" }).click();
    expect(onOpen).toHaveBeenCalledWith("42");
    expect(instance).toBe("db1:5432");
  });

  it("lists statements with their share of time and sends the sort", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    let sort: string | null = null;
    server.use(
      http.get("*/api/v1/db/queries", ({ request }) => {
        sort = new URL(request.url).searchParams.get("sort");
        return HttpResponse.json({
          total_time_ms: 1000,
          queries: [{ fingerprint: "1", query_id: "q", text: "SELECT * FROM orders WHERE id = ?", db_names: ["shop"], calls: 60, throughput: 2, total_time_ms: 750,
            avg_ms: 12.5, time_share: 0.75, rows: 60, rows_per_call: 1, rows_examined: 0, errors: 0, no_index_used: 0, blocks_hit: 10, blocks_read: 0, cache_hit_ratio: 1 }],
        });
      }),
    );
    renderRouted(() => <DbQueriesTab instance="db1" range={{ range: "1h" }} initialSort="avg" q="" onSearch={() => {}} onOpen={() => {}} />);
    const row = await screen.findByTestId("db-query");
    expect(row).toHaveTextContent("75 %");
    expect(row).toHaveTextContent("2/s");
    expect(sort).toBe("avg");
  });

  it("sorts statements on the server by their headings, and pages with offset", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const asked = spyOn("*/api/v1/db/queries", {
      total_time_ms: 1000,
      has_more: true,
      queries: [{ fingerprint: "1", query_id: "q", text: "SELECT 1", db_names: [], calls: 60, throughput: 2, total_time_ms: 750, avg_ms: 12.5, time_share: 0.75, rows: 60,
        rows_per_call: 1, rows_examined: 0, errors: 0, no_index_used: 0, blocks_hit: 10, blocks_read: 3, cache_hit_ratio: 1 }],
    });
    renderRouted(() => <DbQueriesTab instance="db1" range={{ range: "1h" }} q="" onSearch={() => {}} onOpen={() => {}} />);
    await screen.findByTestId("db-query");
    expect(last(asked)).toMatchObject({ sort: "time", dir: "desc", offset: "0" });

    fireEvent.click(screen.getByRole("button", { name: "Disk reads" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "reads", dir: "desc" }));
    fireEvent.click(screen.getByRole("button", { name: "Disk reads" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "reads", dir: "asc" }));
    fireEvent.click(screen.getByRole("button", { name: "Statement" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "statement", dir: "asc" }));

    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "statement", offset: "50", limit: "50" }));
    // A new order is a new list, so it starts over.
    fireEvent.click(screen.getByRole("button", { name: "Errors" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "errors", offset: "0" }));
  });

  it("sorts and pages the instances on the server, and keeps an empty page reachable", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const asked: Record<string, string>[] = [];
    server.use(
      http.get("*/api/v1/db/instances", ({ request }) => {
        const q = Object.fromEntries(new URL(request.url).searchParams);
        asked.push(q);
        // Past the last row: what the merged list answers when something ended while it was read.
        if (q.offset === "50") return HttpResponse.json({ instances: [], has_more: false });
        return HttpResponse.json({
          has_more: true,
          instances: [{ instance: "db1:5432", db_system: "postgresql", host_id: "h", host_name: "db1", server_address: "db1", server_port: 5432, calls: 1, throughput: 1,
            total_time_ms: 1, avg_ms: 1, statements: 1, errors: 0, avg_active_sessions: null, top_wait: "", last_seen: "2026-09-19T10:00:00Z" }],
        });
      }),
    );
    renderRouted(() => <DbInstances range={{ range: "1h" }} onOpen={() => {}} />);
    await screen.findByTestId("db-instance");
    fireEvent.click(screen.getByRole("button", { name: "Avg active sessions" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "aas", dir: "desc" }));
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last(asked)).toMatchObject({ sort: "aas", offset: "50" }));
    // The way back is still on the screen rather than an "empty" message that would strand the reader.
    await waitFor(() => expect(screen.queryByTestId("db-instance")).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: "Previous" }));
    await screen.findByTestId("db-instance");
  });

  it("reads an instance's waits and statements as lists of their own", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const waits = spyOn("*/api/v1/db/activity/waits", { has_more: true, waits: [{ type: "Lock", event: "transactionid", samples: 90, share: 0.9 }] });
    const queries = spyOn("*/api/v1/db/activity/queries", { has_more: true, queries: [{ fingerprint: "1", text: "SELECT 1", samples: 5, avg_active_sessions: 1, top_wait: "CPU" }] });
    renderRouted(() => <DbActivityTab instance="db1" range={{ range: "1h" }} onOpenQuery={() => {}} />);
    const waitTable = await screen.findByTestId("db-activity-waits");
    const queryTable = await screen.findByTestId("db-activity-queries");

    fireEvent.click(within(waitTable).getByRole("button", { name: "Wait event" }));
    await waitFor(() => expect(last(waits)).toMatchObject({ sort: "event", dir: "asc", instance: "db1" }));
    fireEvent.click(within(queryTable).getByRole("button", { name: "Top wait" }));
    await waitFor(() => expect(last(queries)).toMatchObject({ sort: "wait", dir: "asc" }));
    // Two lists, two pagers, and each moves its own.
    fireEvent.click(screen.getAllByRole("button", { name: "Next" })[1]!);
    await waitFor(() => expect(last(queries)).toMatchObject({ offset: "50" }));
    expect(last(waits)).toMatchObject({ offset: "0" });
  });

  it("sorts the sessions of a sample where it holds them, without touching the chains", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const asked: string[] = [];
    server.use(
      http.get("*/api/v1/db/sessions", ({ request }) => {
        asked.push(request.url);
        return HttpResponse.json({
          sampled_at: "2026-09-19T10:00:00Z",
          has_more: true,
          sessions: [
            session("101", ["100"], 0),
            session("100", [], 1),
            session("300", [], 0, { duration_ms: 10, wait_type: "", wait_event: "" }),
            session("200", [], 0, { duration_ms: 900, wait_type: "", wait_event: "" }),
          ],
        });
      }),
    );
    renderRouted(() => <DbSessionsTab instance="db1" onOpenQuery={() => {}} />);
    const table = await screen.findByTestId("db-other-sessions");
    const ids = () => within(table).getAllByTestId("db-other-session").map((r) => r.textContent?.match(/#(\d+)/)?.[1]);
    expect(ids()).toEqual(["200", "300"]);
    fireEvent.click(within(table).getByRole("button", { name: "Duration" }));
    expect(ids()).toEqual(["300", "200"]);
    fireEvent.click(within(table).getByRole("button", { name: "Session" }));
    expect(ids()).toEqual(["200", "300"]);
    expect(within(screen.getByTestId("db-blocking")).getAllByTestId("db-blocking-node")).toHaveLength(2);
    // A click asked the server nothing, and the screen says the sample was cut.
    expect(asked).toHaveLength(1);
    expect(screen.getByTestId("db-sessions-cut")).toHaveTextContent("4 longest");
  });

  it("sorts a statement's callers in the browser, and leaves its plans newest first", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const plan = (hash: string, current: boolean) => ({ plan_hash: hash, format: "text", plan: `plan ${hash}`, total_cost: 1, db_name: "shop", first_seen: "2026-09-19T10:00:00Z", last_seen: "2026-09-19T10:00:00Z", captures: 1, is_current: current, plan_change: current });
    server.use(
      http.get("*/api/v1/db/queries/:fp", () =>
        HttpResponse.json({
          from: "2026-09-19T09:00:00Z", to: "2026-09-19T10:00:00Z", step: "60s", db_system: "postgresql", points: [], waits: [],
          query: { fingerprint: "1", query_id: "q", text: "SELECT 1", db_names: [], calls: 1, throughput: 1, total_time_ms: 1, avg_ms: 1, time_share: 1, rows: 1, rows_per_call: 1, rows_examined: 0, errors: 0, no_index_used: 0, blocks_hit: 0, blocks_read: 0, cache_hit_ratio: null },
          plans: [plan("newest", true), plan("older", false)],
          callers: [
            { service_name: "billing", environment: "prod", calls: 5, avg_ms: 2, errors: 0 },
            { service_name: "api", environment: "prod", calls: 50, avg_ms: null, errors: 0 },
          ],
        }),
      ),
    );
    renderRouted(() => <DbQueryDetail instance="db1" fingerprint="1" range={{ range: "1h" }} />);
    const callers = await screen.findAllByTestId("db-caller");
    expect(callers[0]).toHaveTextContent("api");
    const card = screen.getByTestId("db-callers");
    fireEvent.click(within(card).getByRole("button", { name: "Client-side avg" }));
    // No figure sorts last in either direction, because a blank is not the smallest of anything.
    expect(within(card).getAllByTestId("db-caller")[0]).toHaveTextContent("billing");
    fireEvent.click(within(card).getByRole("button", { name: "Client-side avg" }));
    expect(within(card).getAllByTestId("db-caller")[1]).toHaveTextContent("api");
    expect(screen.getAllByTestId("db-plan")[0]).toHaveTextContent("plan newest");
  });

  it("lists what applications measured, and opens a statement's plan", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderRouted(() => <AppDbQueries range={{ range: "1h" }} q="" onSearch={() => {}} />);
    const table = await screen.findByTestId("app-db-queries");
    const rows = within(table).getAllByTestId("app-db-query");
    expect(within(rows[0]!).getByRole("link", { name: "orders" })).toBeInTheDocument();
    fireEvent.click(within(rows[0]!).getAllByRole("button")[0]!);
    const plans = await screen.findByTestId("app-db-plans");
    expect(within(plans).getByText(/Seq Scan on orders/)).toBeInTheDocument();
  });

  it("says where a plan comes from when there is none", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    server.use(http.get("*/api/v1/apm/db-queries/plans", () => HttpResponse.json({ plans: [] })));
    renderRouted(() => <AppDbQueries range={{ range: "1h" }} q="" onSearch={() => {}} />);
    const rows = await screen.findAllByTestId("app-db-query");
    fireEvent.click(within(rows[0]!).getAllByRole("button")[0]!);
    expect(await screen.findByText(/WithExplain/)).toBeInTheDocument();
  });
});
