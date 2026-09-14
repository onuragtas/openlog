import { describe, expect, it } from "vitest";
import type { OqlResult } from "@/api/oql";
import { barItems, consoleVisualization, deltaPercent, heatmapModel, pieSlices, seriesLabel, summarize, thresholdState, toChartData } from "./oql-result";

function result(p: Partial<OqlResult>): OqlResult {
  return {
    kind: "single",
    event_type: "Log",
    columns: [{ name: "count(*)", function: "count", type: "number" }],
    facets: [],
    rows: [],
    series: [],
    buckets: [],
    compare: null,
    metadata: { from: "", to: "", bucket_seconds: null, rollup: false, table: "logs", rows_read: 0, bytes_read: 0, elapsed_ms: 1, queries: 1, facet_limit: 10, truncated: false, warnings: [] },
    ...p,
  };
}

const two = [
  { name: "count(*)", function: "count", type: "number" as const },
  { name: "p95", function: "percentile", type: "number" as const },
];

describe("oql-result", () => {
  it("labels series by facets and column", () => {
    expect(seriesLabel(two, ["api", "web-1"], 1)).toBe("api · web-1 · p95");
    expect(seriesLabel(two.slice(0, 1), ["api"], 0)).toBe("api");
    expect(seriesLabel(two, [], 0)).toBe("count(*)");
  });

  it("converts timeseries with compare into chart series, previous ones dashed", () => {
    const r = result({
      kind: "timeseries",
      facets: ["service.name"],
      series: [{ facets: ["api"], column: 0, points: [[1000, 1], [2000, null]] }],
      compare: { offset_seconds: 86400, rows: [], buckets: [], series: [{ facets: ["api"], column: 0, points: [[1000, 3], [2000, 4]] }] },
    });
    const d = toChartData(r, "previous");
    expect(d.series.map((s) => s.label)).toEqual(["api", "api (previous)"]);
    expect(d.dashed).toEqual(["api (previous)"]);
    expect(d.series[0]!.points).toEqual([[1000, 1], [2000, null]]);
  });

  it("summarizes timeseries as totals for counts and last values otherwise, with compare", () => {
    const r = result({
      kind: "timeseries",
      columns: two,
      series: [
        { facets: ["api"], column: 0, points: [[1, 2], [2, 3]] },
        { facets: ["api"], column: 1, points: [[1, 10], [2, 12], [3, null]] },
      ],
      compare: { offset_seconds: 60, rows: [], buckets: [], series: [{ facets: ["api"], column: 0, points: [[1, 4]] }] },
    });
    expect(summarize(r)).toEqual([{ facets: ["api"], values: [5, 12], previous: [4, null] }]);
  });

  it("matches compare rows by facets and computes deltas", () => {
    const r = result({
      kind: "facets",
      rows: [{ facets: ["a"], values: [10] }, { facets: ["b"], values: [5] }],
      compare: { offset_seconds: 60, series: [], buckets: [], rows: [{ facets: ["b"], values: [10] }] },
    });
    expect(summarize(r).map((x) => x.previous)).toEqual([[null], [10]]);
    expect(deltaPercent(5, 10)).toBe(-50);
    expect(deltaPercent(5, 0)).toBeNull();
    expect(deltaPercent("x", 1)).toBeNull();
  });

  it("evaluates thresholds in both directions", () => {
    const up = [{ value: 100, severity: "warning" as const }, { value: 200, severity: "critical" as const }];
    expect(thresholdState(50, up)).toBeNull();
    expect(thresholdState(100, up)).toBe("warning");
    expect(thresholdState(250, up)).toBe("critical");
    const down = [{ value: 0.5, severity: "warning" as const }, { value: 0.2, severity: "critical" as const }];
    expect(thresholdState(0.9, down)).toBeNull();
    expect(thresholdState(0.4, down)).toBe("warning");
    expect(thresholdState(0.2, down)).toBe("critical");
    expect(thresholdState(null, up)).toBeNull();
  });

  it("builds bar items, pie slices and heatmaps", () => {
    const facets = result({ kind: "facets", rows: [{ facets: ["a"], values: [30] }, { facets: ["b"], values: [10] }, { facets: ["c"], values: [0] }] });
    expect(barItems(facets)).toEqual([{ label: "a", value: 30, previous: null }, { label: "b", value: 10, previous: null }, { label: "c", value: 0, previous: null }]);
    expect(pieSlices(facets, "other")).toEqual([{ label: "a", value: 30, fraction: 0.75 }, { label: "b", value: 10, fraction: 0.25 }]);
    expect(pieSlices(result({ kind: "facets", rows: [1, 2, 3].map((n) => ({ facets: [`f${n}`], values: [n] })) }), "other", 2).map((s) => s.label)).toEqual(["f3", "f2", "other"]);

    const hist = result({ kind: "histogram", buckets: [{ from: 0, to: 25, count: 17 }, { from: 25, to: 50, count: 3 }] });
    expect(barItems(hist)).toEqual([{ label: "0–25", value: 17, previous: null }, { label: "25–50", value: 3, previous: null }]);
    expect(heatmapModel(hist)).toMatchObject({ max: 17, rows: [{ cells: [17, 3] }], columns: [{ label: "0–25" }, { label: "25–50" }] });

    const ts = result({
      kind: "timeseries",
      series: [
        { facets: ["x"], column: 0, points: [[1000, 1], [2000, 5]] },
        { facets: ["y"], column: 0, points: [[2000, 2]] },
      ],
    });
    expect(heatmapModel(ts)).toEqual({
      rows: [{ label: "x", cells: [1, 5] }, { label: "y", cells: [null, 2] }],
      columns: [{ key: "1000", time: 1000 }, { key: "2000", time: 2000 }],
      max: 5,
    });
    expect(heatmapModel(facets)).toBeNull();
  });

  it("chooses console visualizations", () => {
    expect(consoleVisualization("timeseries", "chart")).toBe("line");
    expect(consoleVisualization("single", "chart")).toBe("billboard");
    expect(consoleVisualization("histogram", "chart")).toBe("bar");
    expect(consoleVisualization("timeseries", "table")).toBe("table");
  });
});
