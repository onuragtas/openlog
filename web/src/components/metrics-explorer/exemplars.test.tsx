import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import type { MetricExemplar } from "@/api/explorer";
import { ExemplarsPanel } from "./ExemplarsPanel";

// Metric exemplars (D-130): the list under the chart is the clickable, keyboard-reachable path from a metric to the
// trace behind it, so each row must link to its trace (and span, when the exemplar has one).

/** Renders ui with /traces/$traceId registered, so Link resolves to a real href. */
function renderWithRoutes(ui: ReactNode) {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => ui });
  const traceRoute = createRoute({ getParentRoute: () => rootRoute, path: "/traces/$traceId", component: () => null });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, traceRoute]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  return render(<RouterProvider router={router} />);
}

const TRACE_A = "5b8efff798038103d269b633813fc60c";
const TRACE_B = "2f1e4d6c8a0b9c7d5e3f1a2b4c6d8e00";

const exemplar = (over: Partial<MetricExemplar> = {}): MetricExemplar => ({
  timestamp: "2026-09-17T11:42:13.512000000Z",
  value: 50,
  trace_id: TRACE_A,
  span_id: "eee19b7ec3c1b174",
  service_name: "checkout",
  attributes: { "http.route": "/pay" },
  filtered_attributes: { "http.status_code": "500" },
  ...over,
});

describe("ExemplarsPanel", () => {
  it("links each exemplar to its trace, with the span when there is one", async () => {
    renderWithRoutes(
      <ExemplarsPanel
        exemplars={[exemplar(), exemplar({ trace_id: TRACE_B, span_id: "", service_name: "payments" })]}
        total={2}
        truncated={false}
        unit="number"
        scale={1}
      />,
    );
    const rows = await screen.findAllByTestId("metric-exemplar");
    expect(rows).toHaveLength(2);

    const first = within(rows[0]!).getByRole("link");
    expect(first).toHaveAttribute("href", expect.stringContaining(TRACE_A));
    expect(first).toHaveAttribute("href", expect.stringContaining("span=eee19b7ec3c1b174"));
    expect(first).toHaveAccessibleName(`Open trace ${TRACE_A}`);

    // No span id: the link still opens the trace, without a span in the search.
    const second = within(rows[1]!).getByRole("link");
    expect(second).toHaveAttribute("href", expect.stringContaining(TRACE_B));
    expect(second.getAttribute("href")).not.toContain("span=");
    expect(within(rows[1]!).getByText("payments")).toBeInTheDocument();
  });

  it("scales values the same way the chart does, so a row matches its dot", async () => {
    // A "%" metric is drawn as a percentage with scale 0.01: 50 becomes 0.5 and renders as 50%.
    renderWithRoutes(<ExemplarsPanel exemplars={[exemplar({ value: 50 })]} total={1} truncated={false} unit="percent" scale={0.01} />);
    expect(await screen.findByText("50%")).toBeInTheDocument();
  });

  it("reports that more exemplars exist than are listed", async () => {
    renderWithRoutes(<ExemplarsPanel exemplars={[exemplar()]} total={342} truncated unit="number" scale={1} />);
    expect(await screen.findByRole("note")).toHaveTextContent("342");
  });

  it("collapses long lists behind a toggle", async () => {
    const many = Array.from({ length: 14 }, (_, i) => exemplar({ trace_id: String(i + 1).padStart(32, "0") }));
    renderWithRoutes(<ExemplarsPanel exemplars={many} total={14} truncated={false} unit="number" scale={1} />);
    expect(await screen.findAllByTestId("metric-exemplar")).toHaveLength(10);

    await userEvent.click(screen.getByRole("button", { expanded: false }));
    expect(await screen.findAllByTestId("metric-exemplar")).toHaveLength(14);
  });
});
