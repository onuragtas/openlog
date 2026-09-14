import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { draftToInput, emptyDraft, validateDraft } from "@/lib/alerts";
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
import { resetMockAlerts } from "@/mocks/alerts";
import { ThemeProvider } from "@/lib/theme";
import { RuleEditor } from "./RuleEditor";

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

const QUERY = "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name";

describe("oql alert rules", () => {
  beforeEach(() => resetMockAlerts());

  it("builds the oql condition and validates the query restrictions", () => {
    const d = { ...emptyDraft("oql"), name: "Errors", query: QUERY, threshold: "10" };
    expect(validateDraft(d)).toEqual({});
    expect(draftToInput(d)).toMatchObject({
      type: "oql",
      interval_seconds: 60,
      condition: { query: QUERY, window_seconds: 300, operator: "gt", threshold: 10, recovery_threshold: null, missing_data: "keep" },
    });
    expect(validateDraft({ ...d, query: `${QUERY} TIMESERIES` }).query).toEqual({ key: "oqlQuery" });
    expect(validateDraft({ ...d, window_seconds: 30 }).window_seconds).toEqual({ key: "range", params: { min: 60, max: 21600 } });
  });

  it("rule editor: OQL type with query editor, preview and save", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderUi(<RuleEditor onSaved={onSaved} />);

    await user.click(await screen.findByRole("radio", { name: /OQL query/ }));
    await user.type(screen.getByLabelText("Name"), "Errors per service");
    const box = screen.getByRole("textbox", { name: "OQL query" });
    fireEvent.change(box, { target: { value: `${QUERY} SINCE 1 hour ago` } });
    expect(screen.getByTestId("oql-restrictions")).toHaveTextContent("Remove SINCE: the window sets the range.");
    fireEvent.change(box, { target: { value: QUERY } });
    expect(screen.queryByTestId("oql-restrictions")).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Alert when value is"), "gte");
    await user.type(screen.getByLabelText("Threshold"), "10");
    await user.selectOptions(screen.getByLabelText("When data is missing"), "ok");

    const summary = await screen.findByTestId("preview-summary", {}, { timeout: 15_000 });
    expect(summary).toHaveTextContent(/Would have opened \d+ incidents? in this range\./);

    await user.click(screen.getByRole("button", { name: "Create rule" }));
    await vi.waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1), { timeout: 10_000 });
    expect(onSaved.mock.calls[0]![0]).toMatchObject({
      name: "Errors per service",
      type: "oql",
      condition: { query: QUERY, window_seconds: 300, operator: "gte", threshold: 10, recovery_threshold: null, missing_data: "ok" },
    });
  }, 40_000);
});
