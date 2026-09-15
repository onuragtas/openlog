import { describe, expect, it } from "vitest";
import type { LogQueryRow } from "@/api/explorer";
import {
  cellValue,
  DEFAULT_COLUMNS,
  jsonBody,
  legacyFilters,
  moveColumn,
  normalizeColumns,
  parseGoDuration,
  recordFields,
  requestColumns,
  searchFromViewState,
  storedColumns,
  storeColumns,
  storedWidths,
  toggleColumn,
  valueFilter,
  viewStateFromSearch,
  volumeSeries,
} from "./logs-explorer";

const row: LogQueryRow = {
  id: "r1",
  timestamp: "2026-09-15T10:00:00.000000001Z",
  observed_timestamp: "2026-09-15T10:00:00.1Z",
  severity_text: "ERROR",
  severity_number: 17,
  body: '{"msg":"boom","user":{"id":7}}',
  service_name: "checkout",
  host_id: "h-1",
  host_name: "web-1",
  trace_id: "",
  span_id: "",
  fields: { "http.route": "/pay", "body.msg": "boom" },
  attributes: { "http.route": "/pay", "http.status_code": "500" },
  resource_attributes: { "k8s.pod.name": "checkout-7f", "service.name": "checkout" },
};

describe("columns", () => {
  it("normalizes untrusted column lists and pins timestamp first", () => {
    expect(normalizeColumns(undefined)).toEqual([...DEFAULT_COLUMNS]);
    expect(normalizeColumns(["body", "timestamp", "body", "", 3, " service.name "])).toEqual(["timestamp", "body", "service.name"]);
    expect(normalizeColumns("a,b")).toEqual(["timestamp", "a", "b"]);
    expect(normalizeColumns(Array.from({ length: 80 }, (_, i) => `k${i}`))).toHaveLength(50);
  });

  it("moves and toggles columns without unpinning timestamp", () => {
    const cols = ["timestamp", "a", "b", "c"];
    expect(moveColumn(cols, 3, 1)).toEqual(["timestamp", "c", "a", "b"]);
    expect(moveColumn(cols, 1, 0)).toEqual(cols);
    expect(moveColumn(cols, 0, 2)).toEqual(cols);
    expect(toggleColumn(cols, "b")).toEqual(["timestamp", "a", "c"]);
    expect(toggleColumn(cols, "d")).toEqual([...cols, "d"]);
    expect(toggleColumn(cols, "timestamp")).toEqual(cols);
    expect(requestColumns(["timestamp", "service.name", "http.route", "body.msg"])).toEqual(["http.route", "body.msg"]);
  });

  it("reads cell values from row fields, requested fields and the attribute maps", () => {
    expect(cellValue(row, "service.name")).toBe("checkout");
    expect(cellValue(row, "severity_number")).toBe("17");
    expect(cellValue(row, "body.msg")).toBe("boom");
    expect(cellValue(row, "attributes.http.status_code")).toBe("500");
    expect(cellValue(row, "resource.k8s.pod.name")).toBe("checkout-7f");
    expect(cellValue(row, "k8s.pod.name")).toBe("checkout-7f");
    expect(cellValue(row, "missing")).toBeUndefined();
  });

  it("lists record fields with sources and parses JSON bodies", () => {
    const keys = recordFields(row).map((f) => `${f.source}:${f.key}`);
    expect(keys).toContain("field:host.name");
    expect(keys).toContain("attribute:attributes.http.status_code");
    expect(keys).toContain("resource:resource.k8s.pod.name");
    expect(keys).toContain("body:body.msg");
    expect(recordFields(row).find((f) => f.key === "body.user")?.value).toBe('{"id":7}');
    expect(keys).not.toContain("field:trace_id");
    expect(jsonBody(row.body)).toEqual({ msg: "boom", user: { id: 7 } });
    expect(jsonBody("plain text")).toBeNull();
    expect(jsonBody("{broken")).toBeNull();
    expect(jsonBody("[1]")).toBeNull();
  });

  it("builds filter in/out conditions", () => {
    expect(valueFilter("severity_number", "17", false)).toEqual({ key: "severity_number", op: "=", value: 17 });
    expect(valueFilter("attributes.code", "500", true, "string")).toEqual({ key: "attributes.code", op: "!=", value: "500" });
  });

  it("stores columns and widths per viewer and survives broken storage", () => {
    storeColumns(["timestamp", "body"]);
    expect(storedColumns()).toEqual(["timestamp", "body"]);
    localStorage.setItem("openlog.logsExplorer.widths", '{"body":300,"x":5,"y":"wide"}');
    expect(storedWidths()).toEqual({ body: 300 });
    localStorage.setItem("openlog.logsExplorer.columns", "{oops");
    expect(storedColumns()).toBeNull();
  });
});

describe("legacy parameters", () => {
  it("maps the pre-explorer /logs parameters to conditions", () => {
    expect(legacyFilters({ severity: "warn", service: "api", host: "h-1", trace: "ABC", span: "DEF" })).toEqual([
      { key: "severity_number", op: ">=", value: 13 },
      { key: "service.name", op: "=", value: "api" },
      { key: "host.id", op: "=", value: "h-1" },
      { key: "trace_id", op: "=", value: "abc" },
      { key: "span_id", op: "=", value: "def" },
    ]);
    expect(legacyFilters({ severity: "17" })).toEqual([{ key: "severity_number", op: ">=", value: 17 }]);
    expect(legacyFilters({ severity: "loud" })).toEqual([]);
  });
});

describe("volumeSeries", () => {
  it("parses Go durations", () => {
    expect(parseGoDuration("60s")).toBe(60_000);
    expect(parseGoDuration("1m30s")).toBe(90_000);
    expect(parseGoDuration("1h")).toBe(3_600_000);
    expect(parseGoDuration("500ms")).toBe(500);
    expect(parseGoDuration("5x")).toBeNull();
    expect(parseGoDuration("")).toBeNull();
  });

  it("fills empty buckets with zero on the grid of the returned points", () => {
    const res = {
      step: "60s",
      series: [
        { group: "ERROR", other: false, total: 3, points: [[180_000, 2], [360_000, 1]] as [number, number][] },
        { group: "", other: true, total: 1, points: [[240_000, 1]] as [number, number][] },
      ],
    };
    const out = volumeSeries(res, 150_000, 400_000, (s) => (s.other ? "other" : s.group));
    expect(out[0]).toEqual({ label: "ERROR", points: [[120_000, 0], [180_000, 2], [240_000, 0], [300_000, 0], [360_000, 1]] });
    expect(out[1]!.points.map(([, v]) => v)).toEqual([0, 0, 1, 0, 0]);
  });

  it("keeps the points as they are without a usable step", () => {
    const res = { step: "?", series: [{ group: "a", other: false, total: 1, points: [[1, 1]] as [number, number][] }] };
    expect(volumeSeries(res, 0, 10, (s) => s.group)).toEqual([{ label: "a", points: [[1, 1]] }]);
  });
});

describe("saved view state", () => {
  it("round-trips the URL state", () => {
    const search = { range: "6h", q: "timeout", f: { f: [["service.name", "=", "api"]] as [string, "=", string][] }, cols: ["timestamp", "body", "http.route"], order: "asc" as const, gb: "service.name" };
    const state = viewStateFromSearch(search, search.cols, "service.name");
    expect(state).toEqual({ filters: [{ key: "service.name", op: "=", value: "api" }], groups: [], q: "timeout", columns: search.cols, order: "asc", group_by: "service.name", range: "6h" });
    expect(searchFromViewState(JSON.parse(JSON.stringify(state)) as Record<string, unknown>)).toEqual(search);
  });

  it("validates untrusted state", () => {
    expect(searchFromViewState({ filters: "x", columns: 7, order: "sideways", group_by: 1, from: "a", to: "b" })).toEqual({
      q: undefined,
      f: undefined,
      cols: undefined,
      order: undefined,
      gb: undefined,
    });
  });
});
