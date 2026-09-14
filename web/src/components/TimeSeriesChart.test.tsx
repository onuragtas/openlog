import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import type { ChartSeriesInput } from "@/lib/series";
import { ThemeProvider } from "@/lib/theme";
import { stubLayout } from "@/test/layout";

interface FakePlot {
  data: unknown[][];
  series: { label?: string; show?: boolean }[];
  setData: ReturnType<typeof vi.fn>;
  setSize: ReturnType<typeof vi.fn>;
  setSeries: ReturnType<typeof vi.fn>;
  destroy: ReturnType<typeof vi.fn>;
}

const plots = vi.hoisted(() => [] as FakePlot[]);

vi.mock("uplot", () => ({
  default: class {
    data: unknown[][];
    series: { label?: string; show?: boolean }[];
    setData = vi.fn();
    setSize = vi.fn();
    setSeries = vi.fn();
    setCursor = vi.fn();
    destroy = vi.fn();
    constructor(opts: { series: { label?: string; show?: boolean }[] }, data: unknown[][]) {
      this.data = data;
      this.series = opts.series.map((s) => ({ ...s, show: s.show ?? true }));
      plots.push(this as unknown as FakePlot);
    }
  },
}));

const t0 = Date.UTC(2026, 8, 14, 10, 0, 0);
const series = (v: number, labels = ["a", "b"]): ChartSeriesInput[] =>
  labels.map((label, i) => ({
    label,
    points: [
      [t0, v + i],
      [t0 + 60_000, v + i + 1],
    ],
  }));

const wrap = (node: ReactNode) => <ThemeProvider>{node}</ThemeProvider>;

describe("TimeSeriesChart updates in place", () => {
  let layout: ReturnType<typeof stubLayout>;
  beforeEach(() => {
    plots.length = 0;
    layout = stubLayout({ viewportHeight: 200, width: 800, itemHeight: 200 });
  });
  afterEach(() => vi.restoreAllMocks());

  it("reuses the uPlot instance for data, size and visibility changes, rebuilds on structure, cleans up on unmount", async () => {
    const user = userEvent.setup();
    const { rerender, unmount } = render(wrap(<TimeSeriesChart series={series(1)} unit="percent" title="CPU" />));
    expect(plots).toHaveLength(1);
    const plot = plots[0]!;
    expect(plot.data[1]).toEqual([1, 2]);

    // New data (e.g. a refetch) → setData, no new instance.
    for (let v = 2; v < 12; v++) rerender(wrap(<TimeSeriesChart series={series(v)} unit="percent" title="CPU" />));
    expect(plots).toHaveLength(1);
    expect(plot.destroy).not.toHaveBeenCalled();
    expect(plot.setData).toHaveBeenLastCalledWith([expect.any(Array), [11, 12], [12, 13]]);

    // Container resize → setSize.
    act(() => layout.resize(500));
    expect(plot.setSize).toHaveBeenLastCalledWith({ width: 500, height: 200 });
    expect(plots).toHaveLength(1);

    // Legend toggle → setSeries.
    await user.click(screen.getByRole("button", { name: /a/, pressed: true }));
    expect(plot.setSeries).toHaveBeenCalledWith(1, { show: false });
    expect(plots).toHaveLength(1);

    // Different series labels change the plot structure → one rebuild.
    rerender(wrap(<TimeSeriesChart series={series(1, ["a", "b", "c"])} unit="percent" title="CPU" />));
    expect(plot.destroy).toHaveBeenCalledTimes(1);
    expect(plots).toHaveLength(2);

    unmount();
    expect(plots[1]!.destroy).toHaveBeenCalledTimes(1);
    expect(layout.connected()).toBe(0);
  });
});
