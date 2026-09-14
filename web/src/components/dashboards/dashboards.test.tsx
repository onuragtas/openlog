import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";

// Controllable media queries: `media.belowLg` switches the dashboard grid between stacked and desktop layouts.
const media = vi.hoisted(() => {
  const state = { belowLg: false };
  if (typeof window !== "undefined") {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: (query: string) => ({
        matches: query.includes("max-width") ? state.belowLg : false,
        media: query,
        addEventListener() {},
        removeEventListener() {},
        addListener() {},
        removeListener() {},
      }),
    });
  }
  return state;
});
import { MOCK_DASHBOARD_IDS, resetMockDashboards } from "@/mocks/dashboards";
import { resetMockDashboardSharing } from "@/mocks/dashboardSharing";
import { ThemeProvider } from "@/lib/theme";
import { DashboardsList } from "./DashboardsList";
import { DashboardView, type DashboardViewSearch } from "./DashboardView";
import { SharedDashboardView } from "./SharedDashboardView";

function renderUi(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router as never} />
      </QueryClientProvider>
    </ThemeProvider>,
  );
}

function ViewHarness({ onSearch, initial = {} }: { onSearch?: (p: DashboardViewSearch) => void; initial?: DashboardViewSearch }) {
  const [search, setSearch] = useState<DashboardViewSearch>(initial);
  return (
    <DashboardView
      dashboardId={MOCK_DASHBOARD_IDS.infra}
      search={search}
      range={{ range: "1h" }}
      onSearchChange={(p) => {
        onSearch?.(p);
        setSearch((s) => ({ ...s, ...p }));
      }}
    />
  );
}

// The card title is the first heading of a widget (markdown widgets add their own headings below it).
const widgetTitles = () => screen.getAllByTestId("dashboard-widget").map((w) => within(w).getAllByRole("heading")[0]!.textContent);

describe("dashboards", () => {
  beforeEach(() => {
    resetMockDashboards();
    resetMockDashboardSharing();
  });
  afterEach(() => {
    media.belowLg = false;
  });

  it("phone/tablet: stacked widgets of several visualizations with the variables bar", async () => {
    media.belowLg = true;
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSearch = vi.fn();
    renderUi(<ViewHarness onSearch={onSearch} />);

    expect(await screen.findByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeInTheDocument();
    expect(screen.getByTestId("dashboard-grid-stacked")).toBeInTheDocument();
    expect(screen.queryByTestId("dashboard-grid-layout")).not.toBeInTheDocument();
    // Reading order: rows first, then columns.
    expect(widgetTitles()).toEqual(["Log volume", "p95 latency", "About", "Logs by host", "Logs by severity", "Slowest transactions", "Requests by service", "Errors by service"]);

    // Billboard with threshold state, compare delta, table, markdown (safe link), pie and heatmap.
    const values = await screen.findAllByTestId("billboard-value");
    expect(values[0]!.textContent).toMatch(/\d/);
    expect(await screen.findByText(/previous:/)).toBeInTheDocument();
    const table = await screen.findByTestId("result-table");
    expect(within(table).getByRole("columnheader", { name: "transaction.name" })).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(5);
    const link = screen.getByRole("link", { name: "openlog on GitHub" });
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(within(screen.getAllByTestId("dashboard-widget")[2]!).getByText("all hosts").tagName).toBe("STRONG");
    expect(await screen.findByTestId("pie-chart")).toBeInTheDocument();
    expect(await screen.findByTestId("heatmap")).toBeInTheDocument();

    // Variables: the host query variable offers the first facet values; picking one goes to the URL.
    const bar = screen.getByRole("group", { name: "Dashboard variables" });
    await user.click(within(bar).getByRole("button", { name: /Host/ }));
    await user.click(await screen.findByRole("checkbox", { name: "web-1" }));
    expect(onSearch).toHaveBeenLastCalledWith({ vars: { host: ["web-1"] } });
    await waitFor(() => expect(within(bar).getByRole("button", { name: /Host/ })).toHaveTextContent("web-1"));

    // Pages
    await user.click(screen.getByRole("tab", { name: "Latency" }));
    expect(onSearch).toHaveBeenLastCalledWith({ page: "p0000000-0000-4000-8000-000000000002" });
    await waitFor(() => expect(widgetTitles()).toEqual(["Average latency by service", "Latency distribution"]));
  }, 30_000);

  it("desktop: react-grid-layout grid, edit mode with the widget editor preview and save", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<ViewHarness />);

    const grid = await screen.findByTestId("dashboard-grid-layout", {}, { timeout: 10_000 });
    expect(grid.querySelectorAll(".react-grid-item")).toHaveLength(8);
    expect(screen.queryByRole("button", { name: /Actions for/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByRole("button", { name: "Done" })).toHaveAttribute("aria-pressed", "true");
    expect(await screen.findByRole("button", { name: "Actions for Log volume" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Add widget" }));
    const dialog = await screen.findByRole("dialog", { name: "Add widget" });
    await user.type(within(dialog).getByLabelText("Title"), "Errors");
    await user.selectOptions(within(dialog).getByLabelText("Visualization"), "billboard");
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Query" }), { target: { value: "SELECT count(*) FROM Log WHERE severity = 'ERROR'" } });
    await user.click(within(dialog).getByRole("button", { name: "Add threshold" }));
    fireEvent.change(within(dialog).getByLabelText("Threshold 1 value"), { target: { value: "1" } });
    await user.selectOptions(within(dialog).getByLabelText("Threshold 1 severity"), "critical");

    const preview = within(dialog).getByTestId("widget-preview");
    const value = await within(preview).findByTestId("billboard-value", {}, { timeout: 5_000 });
    expect(value.textContent).toMatch(/\d/);
    await waitFor(() => expect(within(preview).getByText("Critical")).toBeInTheDocument());

    await user.click(within(dialog).getByRole("button", { name: "Save widget" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Add widget" })).not.toBeInTheDocument(), { timeout: 5_000 });
    await waitFor(() => expect(screen.getAllByTestId("dashboard-widget")).toHaveLength(9));
    expect(widgetTitles()).toContain("Errors");
  }, 30_000);

  it("shows a reload hint when the dashboard changed meanwhile (409)", async () => {
    media.belowLg = true;
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<ViewHarness initial={{ edit: true }} />);
    await user.click(await screen.findByRole("button", { name: "Actions for About" }));
    // Someone else saves first.
    const { api } = await import("@/api/client");
    const current = (await api.GET("/api/v1/dashboards/{id}", { params: { path: { id: MOCK_DASHBOARD_IDS.infra } } })).data!;
    await api.PUT("/api/v1/dashboards/{id}", { params: { path: { id: current.id } }, body: { name: current.name, version: current.version, pages: current.pages, variables: current.variables } });
    await user.click(await screen.findByRole("button", { name: "Duplicate" }));
    expect(await screen.findByTestId("dashboard-conflict")).toHaveTextContent("This dashboard was changed meanwhile");
    await user.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(screen.queryByTestId("dashboard-conflict")).not.toBeInTheDocument());
  }, 30_000);

  it("lists dashboards as cards on phones and a table on desktop; duplicates and deletes", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const { unmount } = renderUi(<DashboardsList onSearchChange={() => undefined} onOpenDashboard={onOpen} />);
    // Grace's private dashboard is not visible.
    expect(await screen.findAllByTestId("dashboard-row")).toHaveLength(2);
    expect(screen.getByRole("columnheader", { name: "Name" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Duplicate Checkout service" }));
    expect(await screen.findByText("Checkout service (copy)")).toBeInTheDocument();
    const copyRow = screen.getByText("Checkout service (copy)").closest("tr")!;
    await user.click(within(copyRow).getByRole("button", { name: "Delete" }));
    await user.click(within(copyRow).getByRole("button", { name: "Confirm delete" }));
    await waitFor(() => expect(screen.queryByText("Checkout service (copy)")).not.toBeInTheDocument());
    unmount();

    media.belowLg = true;
    renderUi(<DashboardsList onSearchChange={() => undefined} onOpenDashboard={onOpen} />);
    expect(await screen.findByTestId("dashboard-cards")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  }, 30_000);

  it("cross-widget filters: a clicked facet value becomes a removable filter chip", async () => {
    media.belowLg = true;
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSearch = vi.fn();
    renderUi(<ViewHarness onSearch={onSearch} />);

    const table = await screen.findByTestId("result-table");
    const facet = (await within(table).findAllByTestId("facet-filter"))[0]!;
    const value = facet.textContent!;
    await user.click(facet);
    expect(onSearch).toHaveBeenLastCalledWith({ filters: [{ attribute: "transaction.name", value, event_type: "Transaction" }] });

    const bar = await screen.findByRole("group", { name: "Dashboard filters" });
    expect(within(bar).getByTestId("filter-chip")).toHaveTextContent(`transaction.name = ${value}`);
    // Clicking the same value again removes it (toggle); the chip's button removes it too.
    await user.click(within(bar).getByRole("button", { name: `Remove filter transaction.name = ${value}` }));
    expect(onSearch).toHaveBeenLastCalledWith({ filters: undefined });
    await waitFor(() => expect(screen.queryByRole("group", { name: "Dashboard filters" })).not.toBeInTheDocument());

    // Edit mode does not turn values into filters.
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await waitFor(() => expect(within(screen.getByTestId("result-table")).queryByTestId("facet-filter")).not.toBeInTheDocument());
  }, 30_000);

  it("version history: shows what a version changed and restores it as a new version", async () => {
    media.belowLg = true;
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<ViewHarness />);

    await user.click(await screen.findByRole("button", { name: "Version history" }));
    const sheet = await screen.findByRole("dialog", { name: "Version history" });
    const items = await within(sheet).findAllByTestId("version-item");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("Version 3");
    expect(items[0]).toHaveTextContent("Current");

    await user.click(items[1]!);
    const detail = await within(sheet).findByTestId("version-detail");
    expect(await within(detail).findByText(/oldest kept version/)).toBeInTheDocument();
    // What restoring it would change: the draft name and one widget fewer.
    expect(within(detail).getByText("Name changed")).toBeInTheDocument();
    expect(within(detail).getByText(/1 widget removed: Errors by service/)).toBeInTheDocument();
    await user.click(within(detail).getByRole("button", { name: "Restore this version" }));
    await user.click(within(detail).getByRole("button", { name: "Save version 2 as a new version" }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Version history" })).not.toBeInTheDocument());
    expect(await screen.findByRole("heading", { level: 1, name: "Infrastructure overview (draft)" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByTestId("dashboard-widget")).toHaveLength(7));
  }, 30_000);

  it("share links: an admin enables sharing, creates a link shown once, and the public view renders it read-only", async () => {
    media.belowLg = true;
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const { unmount } = renderUi(<ViewHarness initial={{ vars: { host: ["web-1"] } }} />);

    await user.click(await screen.findByRole("button", { name: "Share links" }));
    const sheet = await screen.findByRole("dialog", { name: "Share links" });
    expect(await within(sheet).findByTestId("share-disabled")).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Enable share links" }));
    const form = await within(sheet).findByTestId("share-create");
    expect(within(form).getByText("Locked variables: Host = web-1")).toBeInTheDocument();
    await user.type(within(form).getByLabelText("Label"), "Office TV");
    await user.selectOptions(within(form).getByLabelText("Expires after"), "30");
    await user.click(within(form).getByRole("button", { name: "Create link" }));

    const url = (await within(sheet).findByTestId("share-url")) as HTMLInputElement;
    expect(url.value).toMatch(/\/shared\/dashboards\/olds_[A-Za-z0-9_-]{43}$/);
    const item = await within(sheet).findByTestId("share-item");
    expect(item).toHaveTextContent("Office TV");
    expect(item).toHaveTextContent("Active");
    const token = url.value.split("/").pop()!;
    unmount();

    renderUi(<SharedDashboardView token={token} />);
    expect(await screen.findByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeInTheDocument();
    expect(screen.getByText("Shared dashboard (read-only)")).toBeInTheDocument();
    expect(screen.getByTestId("shared-variables")).toHaveTextContent("Host: web-1");
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect((await screen.findAllByTestId("billboard-value"))[0]!.textContent).toMatch(/\d/);
  }, 30_000);

  it("the public view explains an invalid link", async () => {
    renderUi(<SharedDashboardView token="olds_revoked" />);
    expect(await screen.findByRole("heading", { name: "Link not available" })).toBeInTheDocument();
  });
});
