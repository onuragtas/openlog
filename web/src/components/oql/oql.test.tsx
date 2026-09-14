import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";

// uPlot reads window.matchMedia when it loads; jsdom has none.
vi.hoisted(() => {
  if (typeof window !== "undefined" && !window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }),
    });
  }
});
import { ThemeProvider } from "@/lib/theme";
import { HISTORY_KEY } from "@/lib/query-history";
import { OqlEditor } from "./OqlEditor";
import { QueryConsole, type ConsoleView } from "./QueryConsole";

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

function EditorHarness({ onRun }: { onRun: () => void }) {
  const [value, setValue] = useState("");
  return <OqlEditor value={value} onChange={setValue} onRun={onRun} label="OQL query" />;
}

function ConsoleHarness({ onSearch }: { onSearch: (p: { q?: string; view?: ConsoleView }) => void }) {
  const [search, setSearch] = useState<{ q?: string; view?: ConsoleView }>({});
  return (
    <QueryConsole
      query={search.q}
      view={search.view}
      range={{ range: "1h" }}
      onSearchChange={(p) => {
        onSearch(p);
        setSearch((s) => ({ ...s, ...p }));
      }}
    />
  );
}

describe("OQL editor", () => {
  it("falls back to a textarea, shows validation errors with line and column, and runs on Ctrl+Enter", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const onRun = vi.fn();
    renderUi(<EditorHarness onRun={onRun} />);
    const box = await screen.findByRole("textbox", { name: "OQL query" });
    fireEvent.change(box, { target: { value: "SELECT count(*)\nFROM Lgo" } });
    const list = await screen.findByRole("list", { name: "Query problems" });
    expect(list).toHaveTextContent('Line 2, column 6: unknown event type "Lgo"');
    expect(box).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(box, { target: { value: "SELECT count(*) FROM Log WHERE sevrity = 'ERROR'" } });
    expect(await screen.findByText(/Line 1, column 32: unknown attribute "sevrity" for Log/)).toBeInTheDocument();

    fireEvent.change(box, { target: { value: "SELECT count(*) FROM Log WHERE http.route = '/x'" } });
    expect(await screen.findByText(/attribute http\.route is read from attributes/)).toBeInTheDocument();
    expect(box).not.toHaveAttribute("aria-invalid");

    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    expect(onRun).toHaveBeenCalledTimes(1);
  });
});

describe("query console", () => {
  it("runs a query, toggles chart/table, keeps history and explains errors", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSearch = vi.fn();
    renderUi(<ConsoleHarness onSearch={onSearch} />);

    expect(await screen.findByText("Examples")).toBeInTheDocument();
    const box = screen.getByRole("textbox", { name: "OQL query" });
    fireEvent.change(box, { target: { value: "SELECT count(*) FROM Log FACET severity" } });
    expect(screen.getByTestId("range-note")).toHaveTextContent("Uses the time picker range");
    await user.click(screen.getByRole("button", { name: "Run" }));
    expect(onSearch).toHaveBeenCalledWith({ q: "SELECT count(*) FROM Log FACET severity" });

    // Facets in chart view: horizontal bars.
    const bars = await screen.findByTestId("bar-list");
    expect(within(bars).getAllByRole("listitem")).toHaveLength(4);
    expect(screen.getByTestId("result-metadata")).toHaveTextContent(/rows read · \d+ ms · table logs/);

    await user.click(screen.getByRole("button", { name: "Table" }));
    expect(onSearch).toHaveBeenLastCalledWith({ view: "table" });
    const table = await screen.findByTestId("result-table");
    expect(within(table).getByRole("columnheader", { name: "severity" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "count(*)" })).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(5);
    expect(screen.getByRole("button", { name: "Table" })).toHaveAttribute("aria-pressed", "true");

    expect(within(screen.getByTestId("query-history")).getByText("SELECT count(*) FROM Log FACET severity")).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem(HISTORY_KEY) ?? "[]")).toEqual(["SELECT count(*) FROM Log FACET severity"]);

    // SINCE in the query: the picker range is not sent.
    fireEvent.change(box, { target: { value: "SELECT count(*) FROM Log SINCE 1 day ago COMPARE WITH 1 day ago" } });
    expect(screen.getByTestId("range-note")).toHaveTextContent("Uses the query's own SINCE/UNTIL");
    await user.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByRole("columnheader", { name: "count(*) (previous)" })).toBeInTheDocument();

    // 400: the API message with its position; 504: a friendly message.
    fireEvent.change(box, { target: { value: "SELECT count(*) FROM Lgo" } });
    await user.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByTestId("query-error")).toHaveTextContent('line 1, column 22: unknown event type "Lgo"');
    fireEvent.change(box, { target: { value: "SELECT count(*) FROM Log WHERE message = '__timeout__'" } });
    await user.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByText("The query timed out. Narrow the time range or add filters.")).toBeInTheDocument();
  }, 30_000);
});
