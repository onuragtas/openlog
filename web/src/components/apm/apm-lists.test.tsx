// The sort and the page of the APM lists: a heading and Next have to reach the server, because a browser can only sort the page it
// was sent (api.md "Lists"). A list held whole in the browser only has to reorder its rows.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { useState, type ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import type { ApmAgentStatus } from "@/api/apm";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockApm } from "@/mocks/apm";
import { server } from "@/mocks/server";
import { agentRows } from "@/lib/agent-versions";
import { ApmServicesView } from "@/routes/apm";
import { AgentVersionsTable } from "./AgentVersions";
import { DatabasesTab } from "./DatabasesTab";
import { DeploymentsCard } from "./Deployments";
import { DEFAULT_INBOX_FILTERS, ErrorInbox, type ErrorInboxFilterValues } from "./errors/ErrorInbox";
import { ServiceMap } from "./ServiceMap";
import { TracesTab, type TraceFilters } from "./TracesTab";
import { TransactionsTab } from "./TransactionsTab";

// uPlot needs matchMedia and a canvas, and a transaction's charts are not what this is about.
vi.mock("@/components/TimeSeriesChart", () => ({ TimeSeriesChart: () => null }));

// React Flow measures a real layout, and the connections table beside it is what this is about.
vi.mock("@xyflow/react", () => ({
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  MarkerType: { ArrowClosed: "arrowclosed" },
  Panel: () => null,
  Position: { Left: "left", Right: "right", Top: "top", Bottom: "bottom" },
  ReactFlow: () => null,
}));

vi.mock("@/lib/theme", async (original) => ({ ...(await original<object>()), useTheme: () => ({ resolved: "light" }) }));

const scope = { service: "orders", namespace: "shop", environment: "prod" };
const range = { range: "1h" };

function renderRouted(ui: () => ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({ routeTree: createRootRoute({ component: ui }), history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

/** Records the query of every request to a path, and answers it from a function. */
function recording(path: string, body: () => Record<string, unknown>) {
  const seen: URLSearchParams[] = [];
  server.use(
    http.get(path, ({ request }) => {
      seen.push(new URL(request.url).searchParams);
      return HttpResponse.json(body());
    }),
  );
  return { last: () => seen[seen.length - 1]!, seen };
}

const heading = (table: HTMLElement, name: string | RegExp) => within(table).getByRole("button", { name });
const next = () => screen.getByRole("button", { name: "Next" });

beforeEach(async () => {
  await login(MOCK_EMAIL, MOCK_PASSWORD);
});

afterEach(() => resetMockApm());

describe("lists sorted and paged by the server", () => {
  it("transactions send the column, the direction and the page", async () => {
    const tx = { transaction_type: "web", transaction_name: "GET /a", time_consumed_ms: 1, time_share: 1, max_ms: 1, requests: 1, throughput: 1, errors: 0, error_rate: 0, avg_ms: 1, p50_ms: 1, p95_ms: 1, p99_ms: 1, apdex: 1 };
    const api = recording("*/api/v1/apm/services/orders/transactions", () => ({ transactions: [tx], apdex_t_ms: 500, has_more: true }));
    const onSort = vi.fn();
    const user = userEvent.setup();
    renderRouted(() => <TransactionsTab scope={scope} range={range} sort="time" onSort={onSort} onSelect={() => {}} onShowTraces={() => {}} />);
    const table = await screen.findByTestId("transactions-table");
    expect(api.last().get("sort")).toBe("time");
    expect(api.last().get("dir")).toBe("desc");

    await user.click(heading(table, "p95"));
    await waitFor(() => expect(api.last().get("sort")).toBe("slowest"));
    expect(api.last().get("dir")).toBe("desc");
    expect(onSort).toHaveBeenLastCalledWith("slowest");

    await user.click(heading(table, "p95"));
    await waitFor(() => expect(api.last().get("dir")).toBe("asc"));

    await user.click(next());
    await waitFor(() => expect(api.last().get("offset")).toBe("50"));

    // A new order is a new list.
    await user.click(heading(table, "Transaction"));
    await waitFor(() => expect(api.last().get("sort")).toBe("name"));
    expect(api.last().get("dir")).toBe("asc");
    expect(api.last().get("offset")).toBe("0");
  });

  it("a service's databases send the column, the direction and the page", async () => {
    const row = { db_system: "postgresql", db_name: "orders", db_operation: "SELECT", statement: "SELECT 1", calls: 1, throughput: 1, errors: 0, error_rate: 0, avg_ms: 1, p95_ms: 1, max_ms: 1, time_consumed_ms: 1, time_share: 1 };
    const api = recording("*/api/v1/apm/services/orders/databases", () => ({ queries: [row], has_more: true }));
    const user = userEvent.setup();
    renderRouted(() => <DatabasesTab scope={scope} range={range} sort="time" onSort={() => {}} />);
    const table = await screen.findByTestId("db-queries");
    await user.click(heading(table, "Error rate"));
    await waitFor(() => expect(api.last().get("sort")).toBe("error_rate"));
    expect(api.last().get("dir")).toBe("desc");
    await user.click(next());
    await waitFor(() => expect(api.last().get("offset")).toBe("50"));
    // A p95 is worked out after the LIMIT, so its heading promises nothing.
    expect(within(table).queryByRole("button", { name: "p95" })).not.toBeInTheDocument();
  });

  it("traces sort only by what the endpoint can, and page", async () => {
    const row = { trace_id: "a".repeat(32), span_id: "b".repeat(16), timestamp: "2026-09-15T12:00:00Z", duration_ms: 5, is_error: false, http_status_code: 200, service_name: "orders", service_namespace: "shop", environment: "prod", transaction_type: "web", transaction_name: "GET /a" };
    const api = recording("*/api/v1/apm/traces", () => ({ traces: [row], has_more: true }));
    const onChange = vi.fn();
    const user = userEvent.setup();
    function Harness() {
      const [filters, setFilters] = useState<TraceFilters>({});
      return <TracesTab scope={scope} range={range} filters={filters} onChange={(f) => { onChange(f); setFilters(f); }} />;
    }
    renderRouted(() => <Harness />);
    const table = await screen.findByTestId("trace-results");
    await user.click(heading(table, "Duration"));
    await waitFor(() => expect(api.last().get("sort")).toBe("duration"));
    expect(api.last().get("dir")).toBe("desc");
    expect(onChange).toHaveBeenLastCalledWith({ qsort: "duration" });
    await user.click(next());
    await waitFor(() => expect(api.last().get("offset")).toBe("50"));
    expect(within(table).queryByRole("button", { name: "Status" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Transaction" })).not.toBeInTheDocument();
  });

  it("the error inbox sends the column, the direction and the page, on a filter back to the first", async () => {
    const group = { group_id: "a".repeat(16), service_name: "orders", service_namespace: "shop", environment: "prod", error_type: "TypeError", message: "m", count: 1, total_count: 1, first_seen: "2026-09-15T12:00:00Z", last_seen: "2026-09-15T12:00:00Z", last_trace_id: "", last_span_name: "", sparkline: [], status: "unresolved", assignee: null, resolved_at: null, resolved_in_version: "", resolved_by_email: "", regressed_at: null, regression_count: 0, comment_count: 0, updated_at: null, updated_by_email: "" };
    const api = recording("*/api/v1/apm/services/orders/errors", () => ({ groups: [group], step: "60s", counts: { unresolved: 1, resolved: 0, ignored: 0 }, truncated: true, capped: false, has_more: true, workflow: true }));
    const user = userEvent.setup();
    const seenSort: string[] = [];
    function Harness() {
      const [filters, setFilters] = useState<ErrorInboxFilterValues>(DEFAULT_INBOX_FILTERS);
      return <ErrorInbox range={range} scope={scope} filters={filters} onFilters={(p) => { if (p.sort) seenSort.push(p.sort); setFilters((f) => ({ ...f, ...p })); }} onOpen={() => {}} />;
    }
    renderRouted(() => <Harness />);
    const table = await screen.findByTestId("error-groups");
    expect(api.last().get("sort")).toBe("count");
    // A page cut short is not the notice about the candidates being capped.
    expect(screen.queryByText("Showing a subset: narrow the range or the filters.")).not.toBeInTheDocument();

    await user.click(heading(table, "First seen"));
    await waitFor(() => expect(api.last().get("sort")).toBe("first_seen"));
    expect(api.last().get("dir")).toBe("desc");
    expect(seenSort).toEqual(["first_seen"]);

    await user.click(next());
    await waitFor(() => expect(api.last().get("offset")).toBe("50"));

    await user.click(screen.getByRole("button", { name: /^Resolved/ }));
    await waitFor(() => expect(api.last().get("status")).toBe("resolved"));
    await waitFor(() => expect(api.last().get("offset")).toBe("0"));
  });

  it("the services list sends the column, the direction and the page, and the search", async () => {
    const svc = { service_name: "orders", service_namespace: "shop", environment: "prod", language: "go", version: "1", last_seen: "2026-09-15T12:00:00Z", apdex_t_ms: 500, requests: 1, throughput: 1, errors: 0, error_rate: 0, avg_ms: 1, p50_ms: 1, p95_ms: 1, p99_ms: 1, apdex: 1, sparkline: [] };
    const api = recording("*/api/v1/apm/services", () => ({ services: [svc], step: "60s", has_more: true, environments: ["prod", "staging"] }));
    const user = userEvent.setup();
    function Harness() {
      const [q, setQ] = useState("");
      return <ApmServicesView range={range} q={q} onQ={setQ} onEnv={() => {}} />;
    }
    renderRouted(() => <Harness />);
    const table = await screen.findByTestId("apm-services");
    expect(api.last().get("sort")).toBe("name");
    expect(api.last().get("dir")).toBe("asc");
    // The choices come from the answer, whatever the page holds.
    expect(screen.getByRole("option", { name: "staging" })).toBeInTheDocument();

    await user.click(heading(table, "p95"));
    await waitFor(() => expect(api.last().get("sort")).toBe("p95"));
    expect(api.last().get("dir")).toBe("desc");

    await user.click(next());
    await waitFor(() => expect(api.last().get("offset")).toBe("50"));

    await user.type(screen.getByLabelText("Filter services"), "ord");
    await waitFor(() => expect(api.last().get("q")).toBe("ord"));
    await waitFor(() => expect(api.last().get("offset")).toBe("0"));
    // The search waits for the typing to stop.
    expect(api.seen.filter((p) => p.get("q") === "o")).toHaveLength(0);
  });
});

describe("lists the browser holds whole", () => {
  it("deployments reorder without asking again", async () => {
    const d = (t: number, version: string) => ({ timestamp: "2026-09-15T12:00:00Z", t, service_namespace: "shop", environment: "prod", version, previous_version: "", initial: false, rollback: false });
    const api = recording("*/api/v1/apm/services/orders/deployments", () => ({ deployments: [d(1000, "1.10.0"), d(3000, "1.2.0"), d(2000, "1.9.0")], gap_seconds: 300 }));
    const user = userEvent.setup();
    renderRouted(() => <DeploymentsCard scope={scope} range={range} />);
    const card = await screen.findByTestId("deployments");
    const versions = () => within(card).getAllByRole("button", { name: /^Compare/ }).map((b) => b.getAttribute("aria-label"));
    await within(card).findAllByRole("button", { name: /^Compare/ });
    expect(versions()[0]).toContain("1.2.0"); // the newest first, as it has always been

    await user.click(heading(card, "Version"));
    // Numbers in a version sort as numbers: 1.9 comes before 1.10.
    expect(versions().map((v) => v!.match(/\d+\.\d+\.\d+/)![0])).toEqual(["1.2.0", "1.9.0", "1.10.0"]);
    expect(api.seen).toHaveLength(1);
  });

  it("agent versions sort by status worst first, and page", async () => {
    const seen = "2026-09-15T12:00:00.000000000Z";
    const version = (i: number, status: ApmAgentStatus) => ({ version: `0.1.${i}`, status, instances: i, spans: 1, last_seen: seen });
    const rows = agentRows([
      {
        service_name: "catalog", service_namespace: "shop", environment: "prod", status: "unsupported",
        agents: [{ kind: "php", distro_name: "openlog-php", sdk_name: "", sdk_language: "php", status: "unsupported", instances: 1, last_seen: seen, versions_truncated: false, instrumentation_modules: [], upgrade: null,
          versions: [version(1, "ok"), version(2, "unsupported"), version(3, "outdated"), ...Array.from({ length: 30 }, (_, i) => version(10 + i, "ok"))] }],
      },
    ]);
    const user = userEvent.setup();
    render(<AgentVersionsTable rows={rows} />);
    const table = screen.getByTestId("agent-versions");
    await user.click(heading(table, "Status"));
    const cells = () => within(table).getAllByRole("row").slice(1).map((r) => r.textContent);
    expect(cells()[0]).toContain("0.1.2");
    expect(cells()[1]).toContain("0.1.3");
    expect(cells()).toHaveLength(33);

    await user.selectOptions(screen.getByRole("combobox", { name: "Rows per page" }), "25");
    expect(cells()).toHaveLength(25);
    await user.click(next());
    expect(cells()).toHaveLength(8);
  });
});

describe("the service map's connections", () => {
  it("sort in the browser and page", async () => {
    const edge = (i: number) => ({ id: `e${i}`, source: "service:a||", target: `db:t${String(i).padStart(2, "0")}`, target_type: "db", calls: i, throughput: i, errors: 0, error_rate: 0, avg_ms: i, p95_ms: i });
    const nodes = [{ id: "service:a||", type: "service", name: "a", service_namespace: "", environment: "", requests: 1, throughput: 1, error_rate: 0, avg_ms: 1, p95_ms: 1, apdex: 1, host_count: 0, container_count: 0 }];
    const user = userEvent.setup();
    renderRouted(() => <ServiceMap data={{ nodes, edges: Array.from({ length: 60 }, (_, i) => edge(i)) } as never} onOpenService={() => {}} />);
    const table = await screen.findByTestId("map-connections");
    const rows = () => within(table).getAllByRole("row").slice(1);
    // The busiest first, and no longer every one of them on one screen.
    expect(rows()).toHaveLength(50);
    expect(rows()[0]).toHaveTextContent("t59");

    await user.click(heading(table, "To"));
    expect(rows()[0]).toHaveTextContent("t00");

    await user.click(next());
    expect(rows()).toHaveLength(10);
  });
});
