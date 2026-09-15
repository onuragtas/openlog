import { describe, expect, it } from "vitest";
import type { MetricSeries } from "@/api/types";
import {
  decodeMetricQueries,
  encodeMetricQueries,
  evaluateFormula,
  metricOql,
  metricSeriesLabel,
  metricsSearchFromViewState,
  metricsViewState,
  newMetricQuery,
  nextQueryId,
  oqlAttribute,
  parseFormula,
  seriesStats,
  unitDisplay,
  type MetricQueryState,
} from "./metrics-explorer";

const gauge = { type: "gauge" as const, monotonic: false, temporality: "unspecified" as const, default_aggregation: "avg" as const };

describe("URL state", () => {
  it("round-trips queries and falls back to one empty query", () => {
    const qs: MetricQueryState[] = [
      { id: "A", metric: "http.server.duration", filters: [{ key: "service.name", op: "=", value: "api" }], groups: [], aggregation: "p95", groupBy: ["http.route"] },
      newMetricQuery("B", "x"),
    ];
    const enc = encodeMetricQueries(qs)!;
    expect(enc).toEqual([{ i: "A", m: "http.server.duration", f: { f: [["service.name", "=", "api"]] }, a: "p95", g: ["http.route"] }, { i: "B", m: "x" }]);
    expect(decodeMetricQueries(enc)).toEqual(qs);
    expect(decodeMetricQueries(JSON.stringify(enc))).toEqual(qs);
    expect(encodeMetricQueries([newMetricQuery("A")])).toBeUndefined();
    expect(decodeMetricQueries("nope")).toEqual([newMetricQuery("A")]);
  });

  it("drops invalid ids, duplicates and aggregations", () => {
    expect(decodeMetricQueries([{ i: "Z" }, { i: "B", a: "median", g: ["k", "k", 3] }, { i: "B", m: "dup" }])).toEqual([{ id: "B", metric: "", filters: [], groups: [], aggregation: undefined, groupBy: ["k"] }]);
    expect(nextQueryId([newMetricQuery("A"), newMetricQuery("C")])).toBe("B");
  });
});

describe("labels and saved views", () => {
  it("labels series by group-by values with or without the key prefix", () => {
    expect(metricSeriesLabel({ attributes: { "http.route": "/a", "service.name": "api" }, points: [] }, ["attributes.http.route", "service.name"], "all")).toBe("/a · api");
    expect(metricSeriesLabel({ attributes: {}, points: [] }, [], "all")).toBe("all");
  });

  it("round-trips the saved view state and validates it", () => {
    const search = { range: "24h", mq: [{ i: "A", m: "x", a: "max" as const }], formula: "A * 2" };
    expect(metricsSearchFromViewState(JSON.parse(JSON.stringify(metricsViewState(search))) as Record<string, unknown>)).toEqual(search);
    expect(metricsSearchFromViewState({ queries: "bad", formula: 5 })).toEqual({ mq: undefined, formula: undefined });
  });
});

describe("unitDisplay and seriesStats", () => {
  it("maps obvious units only", () => {
    expect(unitDisplay("system.memory.usage", "By", "avg")).toEqual({ kind: "bytes", scale: 1 });
    expect(unitDisplay("system.network.io", "By", "rate")).toEqual({ kind: "bytesPerSec", scale: 1 });
    expect(unitDisplay("system.cpu.utilization", "1", "avg")).toEqual({ kind: "percent", scale: 1 });
    expect(unitDisplay("process.threads", "1", "avg")).toEqual({ kind: "number", scale: 1 });
    expect(unitDisplay("disk.used", "%", "max")).toEqual({ kind: "percent", scale: 0.01 });
    expect(unitDisplay("http.server.duration", "ms", "p95")).toEqual({ kind: "ms", scale: 1 });
    expect(unitDisplay("http.server.duration", "ms", "count")).toEqual({ kind: "number", scale: 1 });
    expect(unitDisplay("queue.depth", "{message}", "last")).toEqual({ kind: "number", scale: 1 });
  });

  it("computes last, avg, min and max", () => {
    expect(seriesStats([[1, 2], [2, 4], [3, Number.NaN], [4, 0]])).toEqual({ last: 0, avg: 2, min: 0, max: 4 });
    expect(seriesStats([])).toEqual({ last: null, avg: null, min: null, max: null });
  });
});

describe("parseFormula", () => {
  it("parses precedence, parentheses, unary minus and query ids", () => {
    const r = parseFormula("A / (B + 2) * -100", ["A", "B"]);
    expect(r.ok && r.refs).toEqual(["A", "B"]);
    const env = { A: [{ attributes: {}, points: [[1, 10]] }], B: [{ attributes: {}, points: [[1, 3]] }] } as Record<string, MetricSeries[]>;
    expect(r.ok && evaluateFormula(r.ast, r.refs, env).series[0]!.points).toEqual([[1, -200]]);
    const lower = parseFormula("a*1.5e1", ["A"]);
    expect(lower.ok && evaluateFormula(lower.ast, lower.refs, env).series[0]!.points).toEqual([[1, 150]]);
  });

  it.each<[string, string]>([
    ["", "empty"],
    ["A +", "syntax"],
    ["(A", "syntax"],
    ["A B", "syntax"],
    ["A % 2", "syntax"],
    ["C * 2", "unknownQuery"],
    ["alert(1)", "unknownQuery"],
    ["A".repeat(300), "tooLong"],
  ])("rejects %j", (text, error) => {
    const r = parseFormula(text, ["A", "B"]);
    expect(r.ok).toBe(false);
    expect(!r.ok && r.error).toBe(error);
  });
});

describe("evaluateFormula", () => {
  const s = (attributes: Record<string, string>, points: [number, number][]): MetricSeries => ({ attributes, points });
  const run = (text: string, data: Record<string, MetricSeries[]>) => {
    const r = parseFormula(text, Object.keys(data));
    if (!r.ok) throw new Error(r.error);
    return evaluateFormula(r.ast, r.refs, data);
  };

  it("aligns timestamps and leaves gaps for missing points and division by zero", () => {
    const out = run("A / B", { A: [s({}, [[1, 1], [2, 2], [3, 3]])], B: [s({}, [[1, 2], [2, 0]])] });
    expect(out.series).toEqual([{ attributes: {}, points: [[1, 0.5]] }]);
  });

  it("matches grouped series by attributes and broadcasts single series", () => {
    const a = [s({ host: "a" }, [[1, 4]]), s({ host: "b" }, [[1, 9]]), s({ host: "c" }, [[1, 1]])];
    const b = [s({ host: "b" }, [[1, 3]]), s({ host: "a" }, [[1, 2]])];
    expect(run("A / B", { A: a, B: b }).series).toEqual([
      { attributes: { host: "a" }, points: [[1, 2]] },
      { attributes: { host: "b" }, points: [[1, 3]] },
    ]);
    expect(run("A * B", { A: a, B: [s({}, [[1, 10]])] }).series.map((x) => x.points[0]![1])).toEqual([40, 90, 10]);
    expect(run("A - B", { A: a, B: [s({ zone: "x" }, [[1, 1]]), s({ zone: "y" }, [[1, 1]])] })).toEqual({ series: [], unmatched: true });
    expect(run("A + 1", { A: [] })).toEqual({ series: [], unmatched: false });
  });
});

describe("metricOql", () => {
  const q = (p: Partial<MetricQueryState>): MetricQueryState => ({ ...newMetricQuery("A", "system.cpu.utilization"), ...p });

  it("maps keys", () => {
    expect(oqlAttribute("service.name")).toBe("service.name");
    expect(oqlAttribute("metric.name")).toBe("metricName");
    expect(oqlAttribute("attributes.cpu.mode")).toBe("attributes['cpu.mode']");
    expect(oqlAttribute("cpu.mode")).toBe("attributes['cpu.mode']");
    expect(oqlAttribute("resource.k8s.pod.name")).toBe("resource['k8s.pod.name']");
    expect(oqlAttribute("attributes.it's")).toBe("attributes['it''s']");
  });

  it("builds filters, OR groups and facets", () => {
    const r = metricOql(
      q({
        filters: [
          { key: "host.id", op: "=", value: "h-1" },
          { key: "cpu.mode", op: "not_in", values: ["idle", "steal"] },
          { key: "resource.k8s.pod.name", op: "exists" },
          { key: "attributes.path", op: "contains", value: "api" },
          { key: "value", op: ">=", value: 0.5 },
        ],
        groups: [[{ key: "a", op: "like", value: "x%" }, { key: "b", op: "not_exists" }], [{ key: "c", op: "!=", value: true }]],
        groupBy: ["cpu.mode", "resource.host.name"],
      }),
      gauge,
    );
    expect(r).toEqual({
      ok: true,
      query:
        "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' AND host.id = 'h-1' AND attributes['cpu.mode'] NOT IN ('idle', 'steal') AND resource['k8s.pod.name'] IS NOT NULL AND attributes['path'] LIKE '%api%' AND value >= 0.5 AND ((attributes['a'] LIKE 'x%' AND attributes['b'] IS NULL) OR attributes['c'] != true) FACET attributes['cpu.mode'], resource['host.name'] LIMIT 50 TIMESERIES AUTO",
    });
  });

  it("maps aggregations per metric type", () => {
    const agg = (aggregation: MetricQueryState["aggregation"], meta: Parameters<typeof metricOql>[1] = gauge) => {
      const r = metricOql(q({ aggregation }), meta);
      return r.ok ? r.query.slice(7, r.query.indexOf(" FROM")) : r.reason;
    };
    expect(agg(undefined)).toBe("average(value)");
    expect(agg("last")).toBe("latest(value)");
    expect(agg("count")).toBe("count(*)");
    expect(agg("max")).toBe("max(value)");
    const cumulative = { type: "sum" as const, monotonic: true, temporality: "cumulative" as const, default_aggregation: "rate" as const };
    expect(agg("rate", cumulative)).toBe("aggregation");
    expect(agg("increase", { ...cumulative, temporality: "delta" })).toBe("sum(value)");
    expect(agg("rate", { ...cumulative, temporality: "delta" })).toBe("rate(sum(value), 1 second)");
    const histogram = { type: "histogram" as const, monotonic: false, temporality: "cumulative" as const, default_aggregation: "p95" as const };
    expect(agg(undefined, histogram)).toBe("aggregation");
    expect(agg("count", histogram)).toBe("sum(count)");
  });

  it("refuses what OQL cannot express", () => {
    expect(metricOql(q({ filters: [{ key: "a", op: "regex", value: "^x" }] }), gauge)).toEqual({ ok: false, reason: "regex" });
    expect(metricOql(q({ groups: [[{ key: "a", op: "exists" }], [{ key: "b", op: "not_regex", value: "y" }]] }), gauge)).toEqual({ ok: false, reason: "regex" });
    expect(metricOql(q({ metric: "" }), gauge)).toEqual({ ok: false, reason: "noMetric" });
  });
});
