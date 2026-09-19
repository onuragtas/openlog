import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { server } from "@/mocks/server";
import { DbQueriesTab } from "./DbQueriesTab";
import { DbSessionsTab } from "./DbSessionsTab";

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
    renderRouted(() => <DbQueriesTab instance="db1" range={{ range: "1h" }} sort="avg" q="" onSort={() => {}} onSearch={() => {}} onOpen={() => {}} />);
    const row = await screen.findByTestId("db-query");
    expect(row).toHaveTextContent("75 %");
    expect(row).toHaveTextContent("2/s");
    expect(sort).toBe("avg");
  });
});
