import { describe, expect, it } from "vitest";
import type { MetricSeries } from "@/api/types";
import {
  alignSeries,
  dataStartHint,
  defaultVisibility,
  fromMetricSeries,
  lastValue,
  legendValues,
  seriesLabel,
  stackBands,
  stackVisible,
  visibleMax,
  yRange,
} from "./series";

describe("seriesLabel", () => {
  it("joins values of the requested keys", () => {
    expect(seriesLabel({ "system.device": "sda", "disk.io.direction": "read" }, ["disk.io.direction"])).toBe("read");
    expect(seriesLabel({ b: "2", a: "1" })).toBe("1 · 2");
    expect(seriesLabel({})).toBe("");
  });
});

describe("fromMetricSeries", () => {
  it("uses fallback label for attribute-less series", () => {
    const api: MetricSeries[] = [
      { attributes: {}, points: [[1000, 1]] },
      { attributes: { "cpu.mode": "user" }, points: [[1000, 0.2]] },
    ];
    expect(fromMetricSeries(api, { keys: ["cpu.mode"], fallbackLabel: "load" }).map((s) => s.label)).toEqual(["load", "user"]);
  });
});

describe("alignSeries", () => {
  const a = { label: "a", points: [[1000, 1], [2000, 2], [4000, 4]] as [number, number][] };
  const b = { label: "b", points: [[2000, 20], [3000, 30]] as [number, number][] };

  it("aligns on the union of timestamps in seconds with null gaps", () => {
    const d = alignSeries([a, b]);
    expect(d.xs).toEqual([1, 2, 3, 4]);
    expect(d.ys).toEqual([
      [1, 2, null, 4],
      [null, 20, 30, null],
    ]);
    expect(d.drawn).toBe(d.ys);
    expect(d.labels).toEqual(["a", "b"]);
  });

  it("stacks cumulatively and keeps gaps", () => {
    const d = alignSeries([a, b], { stacked: true });
    expect(d.drawn).toEqual([
      [1, 2, null, 4],
      [null, 22, 30, null],
    ]);
    // raw values stay available for tooltips
    expect(d.ys[1]).toEqual([null, 20, 30, null]);
  });

  it("orders series by preferred order then alphabetically", () => {
    const mk = (label: string) => ({ label, points: [[1000, 1]] as [number, number][] });
    const d = alignSeries([mk("zeta"), mk("idle"), mk("alpha"), mk("user")], { order: ["user", "idle"] });
    expect(d.labels).toEqual(["user", "idle", "alpha", "zeta"]);
  });

  it("drops non-finite values", () => {
    const d = alignSeries([{ label: "x", points: [[1000, Number.NaN], [2000, 5]] }]);
    expect(d.ys).toEqual([[null, 5]]);
  });

  it("handles empty input", () => {
    expect(alignSeries([])).toEqual({ xs: [], ys: [], drawn: [], labels: [] });
    expect(alignSeries([{ label: "x", points: [] }]).xs).toEqual([]);
  });

  it("unsorted input points are sorted", () => {
    const d = alignSeries([{ label: "x", points: [[3000, 3], [1000, 1]] }]);
    expect(d.xs).toEqual([1, 3]);
    expect(d.ys).toEqual([[1, 3]]);
  });
});

describe("CPU series selection", () => {
  const modes = ["user", "system", "iowait", "idle"];
  const ys = [
    [0.2, 0.3],
    [0.1, 0.05],
    [null, 0.05],
    [0.7, 0.6],
  ];

  it("hides idle by default", () => {
    expect(defaultVisibility(modes, ["idle"])).toEqual([true, true, true, false]);
    expect(defaultVisibility(modes)).toEqual([true, true, true, true]);
  });

  it("stacks only visible series; hidden ones sit at the running total", () => {
    const visible = [true, true, true, false];
    const drawn = stackVisible(ys, visible);
    expect(drawn[0]).toEqual([0.2, 0.3]);
    expect(drawn[1]![0]).toBeCloseTo(0.3);
    expect(drawn[1]![1]).toBeCloseTo(0.35);
    expect(drawn[2]![0]).toBeNull(); // gap kept
    expect(drawn[2]![1]).toBeCloseTo(0.4);
    expect(drawn[3]![1]).toBeCloseTo(0.4); // idle hidden: zero height
    // hiding a lower series drops it from the stack
    const noUser = stackVisible(ys, [false, true, true, false]);
    expect(noUser[0]).toEqual([0, 0]);
    expect(noUser[1]).toEqual([0.1, 0.05]);
  });

  it("scales y to the visible non-idle max, capped at 100%", () => {
    const visible = [true, true, true, false];
    const max = visibleMax(stackVisible(ys, visible), visible)!;
    expect(max).toBeCloseTo(0.4);
    const [lo, hi] = yRange(0, max, { yCap: 1 });
    expect(lo).toBe(0);
    expect(hi).toBeCloseTo(0.45); // 0.44 rounded up to a 5% step
    // with idle enabled the stack reaches 100% and the cap holds
    const all = [true, true, true, true];
    expect(yRange(0, visibleMax(stackVisible(ys, all), all), { yCap: 1 })[1]).toBe(1);
  });

  it("keeps fixed yMax behaviour and handles no visible data", () => {
    expect(yRange(0, 0.4, { yMax: 1 })).toEqual([0, 1]);
    expect(yRange(null, null, { yCap: 1 })).toEqual([0, 1]);
    expect(visibleMax(ys, [false, false, false, false])).toBeNull();
    expect(yRange(0, 8)).toEqual([0, 8 * 1.1]);
  });
});

describe("legendValues", () => {
  const data = { xs: [1, 2, 3], ys: [[1, 2, null], [null, null, 30]] };

  it("shows the latest non-null value per series when not hovering", () => {
    expect(legendValues(data, null)).toEqual({ time: 3, values: [2, 30] });
    expect(legendValues(data, undefined).values).toEqual([2, 30]);
    expect(lastValue([null, null])).toBeNull();
  });

  it("shows raw values at the hovered index", () => {
    expect(legendValues(data, 0)).toEqual({ time: 1, values: [1, null] });
    expect(legendValues(data, 99).time).toBe(3); // out of range → latest
  });
});

describe("dataStartHint", () => {
  const hour = 3_600_000;
  it("flags data starting well after the range start", () => {
    expect(dataStartHint(55 * 60_000, 0, hour)).toBe(55 * 60_000);
  });
  it("ignores small gaps and missing ranges", () => {
    expect(dataStartHint(60_000, 0, hour)).toBeNull(); // within 10% / 2 min
    expect(dataStartHint(5 * 60_000, 0, hour)).toBeNull(); // 5 min < 6 min (10%)
    expect(dataStartHint(100, undefined, hour)).toBeNull();
  });
});

describe("stackBands", () => {
  it("fills each series down to the previous one", () => {
    expect(stackBands(3)).toEqual([{ series: [3, 2] }, { series: [2, 1] }]);
    expect(stackBands(1)).toEqual([]);
  });
});
