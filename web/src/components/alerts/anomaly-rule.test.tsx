import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { changeType, draftToInput, emptyDraft, unitKindFor, validateDraft } from "@/lib/alerts";
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

describe("anomaly (baseline) alert rules", () => {
  beforeEach(() => resetMockAlerts());

  it("builds the metric and the APM baseline condition", () => {
    const d = { ...emptyDraft("anomaly"), name: "CPU baseline", metric: "system.cpu.utilization" };
    expect(validateDraft(d)).toEqual({});
    expect(draftToInput(d)).toMatchObject({
      type: "anomaly",
      interval_seconds: 60,
      condition: {
        signal: "metric", metric: "system.cpu.utilization", aggregation: "avg", group_by: ["host"], filters: [],
        window_seconds: 300, seasonality: "daily", lookback_days: 7, direction: "upper", sensitivity: 3,
        min_samples: 3, min_deviation: 0,
      },
    });
    // The APM signal sends the apm selector instead of the metric one (the server rejects a mix).
    const apm = { ...emptyDraft("anomaly"), name: "Checkout latency", signal: "apm" as const, metric: "p95_ms", service_name: " checkout ", group_by: [] };
    expect(validateDraft(apm)).toEqual({});
    const condition = draftToInput(apm).condition;
    expect(condition).toMatchObject({ signal: "apm", service_name: "checkout", metric: "p95_ms", min_requests: 0, seasonality: "daily" });
    expect(condition.aggregation).toBeUndefined();
    expect(condition.filters).toBeUndefined();
    // The deviation ratio is a plain number, not a percentage like other unit "1" signals.
    expect(unitKindFor("1", "anomaly")).toBe("number");
  });

  it("guards the season, the sample count and the sensitivity", () => {
    const d = { ...emptyDraft("anomaly"), name: "CPU baseline", metric: "system.cpu.utilization" };
    // 7 minutes does not divide a day, so the baseline windows would not cover the same slot.
    expect(validateDraft({ ...d, window_seconds: 420 }).window_seconds).toEqual({ key: "anomalySeason" });
    // A week of history gives one weekly sample, fewer than min_samples.
    expect(validateDraft({ ...d, seasonality: "weekly", lookback_days: 7 }).lookback_days).toEqual({ key: "anomalySamples", params: { count: 1 } });
    expect(validateDraft({ ...d, seasonality: "weekly", lookback_days: 28 })).toEqual({});
    expect(validateDraft({ ...d, sensitivity: "0.1" }).sensitivity).toEqual({ key: "range", params: { min: 0.5, max: 20 } });
    expect(validateDraft({ ...d, min_samples: "1" }).min_samples).toEqual({ key: "range", params: { min: 2, max: 50 } });
    expect(validateDraft({ ...d, metric: "" }).metric).toEqual({ key: "required" });
    expect(validateDraft({ ...d, signal: "apm", metric: "p95_ms" }).service_name).toEqual({ key: "required" });
    // Switching a metric rule to a baseline keeps only the filters the 1-minute rollup can evaluate.
    const metric = {
      ...emptyDraft("metric_threshold"),
      filters: [
        { field: "host.id", op: "eq" as const, values: "h1" },
        { field: "resource.env", op: "eq" as const, values: "prod" },
        { field: "attr.cpu.mode", op: "not_in" as const, values: "idle" },
      ],
    };
    expect(changeType(metric, "anomaly").filters.map((f) => f.field)).toEqual(["host.id", "attr.cpu.mode"]);
  });

  it("rule editor: baseline type with seasonality, direction and a deviation preview", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderUi(<RuleEditor onSaved={onSaved} />);

    await user.click(await screen.findByRole("radio", { name: /Baseline \(anomaly\)/ }));
    await user.type(screen.getByLabelText("Name"), "CPU baseline");
    await user.type(screen.getByLabelText("Metric"), "system.cpu.utilization");

    // A weekly season over the default week of history is too little: the lookback field says so.
    await user.selectOptions(screen.getByLabelText("Seasonality"), "weekly");
    expect(await screen.findByText("This lookback gives only 1 baseline samples.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Baseline lookback (days)"));
    await user.type(screen.getByLabelText("Baseline lookback (days)"), "28");
    expect(screen.queryByText("This lookback gives only 1 baseline samples.")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Alert when the signal is"), "both");
    await user.clear(screen.getByLabelText("Sensitivity (σ)"));
    await user.type(screen.getByLabelText("Sensitivity (σ)"), "4");

    const summary = await screen.findByTestId("preview-summary", {}, { timeout: 15_000 });
    expect(summary).toHaveTextContent(/Would have opened \d+ incidents? in this range\./);
    // The chart carries the deviation, not the raw signal, and says so.
    expect(screen.getByText("The chart shows the deviation, not the raw signal: the line at 1 is the edge of the baseline band.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Create rule" }));
    await vi.waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1), { timeout: 10_000 });
    expect(onSaved.mock.calls[0]![0]).toMatchObject({
      name: "CPU baseline",
      type: "anomaly",
      condition: {
        signal: "metric", metric: "system.cpu.utilization", seasonality: "weekly", lookback_days: 28,
        direction: "both", sensitivity: 4, min_samples: 3, window_seconds: 300,
      },
    });
  }, 40_000);
});
