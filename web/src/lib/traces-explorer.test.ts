import { describe, expect, it } from "vitest";
import type { SpanQueryRow } from "@/api/explorer";
import {
  DEFAULT_SPAN_COLUMNS,
  formatSpanDuration,
  isDefaultSpanColumns,
  latencySeries,
  normalizeSpanColumns,
  requestSpanColumns,
  searchFromTracesViewState,
  spanCellValue,
  spanRecordFields,
  tracesViewStateFromSearch,
} from "./traces-explorer";

const row = (p: Partial<SpanQueryRow> = {}): SpanQueryRow => ({
  id: "1-a",
  timestamp: "2026-09-15T10:00:00.000000000Z",
  trace_id: "4bf92f3577b34da6a3ce929d0e0e4736",
  span_id: "a1a1a1a1a1a1a1a1",
  parent_span_id: "",
  name: "GET /api/cart",
  kind: "server",
  status_code: "error",
  status_message: "boom",
  service_name: "frontend",
  host_id: "h-1",
  duration_ns: 312_400_000,
  duration_ms: 312.4,
  is_entry: true,
  is_error: true,
  http_status_code: 503,
  transaction_name: "GET /api/cart",
  fields: { "attributes.http.route": "/api/cart" },
  attributes: { "http.route": "/api/cart", "db.system": "redis" },
  resource_attributes: { "k8s.pod.name": "web-1", "db.system": "pg" },
  ...p,
});

describe("traces explorer columns", () => {
  it("normalizes columns with span defaults", () => {
    expect(normalizeSpanColumns([])).toEqual([...DEFAULT_SPAN_COLUMNS]);
    expect(normalizeSpanColumns(["name", "timestamp", "name", "duration_ms"])).toEqual(["timestamp", "name", "duration_ms"]);
    expect(isDefaultSpanColumns([...DEFAULT_SPAN_COLUMNS])).toBe(true);
    expect(requestSpanColumns(["timestamp", "name", "attributes.http.route", "http.route"])).toEqual(["attributes.http.route", "http.route"]);
  });

  it("reads cell values from the row, requested fields and the attribute maps", () => {
    const r = row();
    expect(spanCellValue(r, "duration_ms")).toBe("312.4");
    expect(spanCellValue(r, "http.status_code")).toBe("503");
    expect(spanCellValue(row({ http_status_code: 0 }), "http.status_code")).toBeUndefined();
    expect(spanCellValue(r, "error")).toBe("true");
    expect(spanCellValue(r, "attributes.http.route")).toBe("/api/cart");
    expect(spanCellValue(r, "resource.k8s.pod.name")).toBe("web-1");
    // Bare keys: the span attribute, else the resource attribute (as the API).
    expect(spanCellValue(r, "db.system")).toBe("redis");
    expect(spanCellValue(r, "k8s.pod.name")).toBe("web-1");
    expect(spanCellValue(r, "missing")).toBeUndefined();
  });

  it("lists detail fields by source", () => {
    const fields = spanRecordFields(row({ parent_span_id: "" }));
    expect(fields.find((f) => f.key === "parent_span_id")).toBeUndefined();
    expect(fields.find((f) => f.key === "status_message")).toEqual({ key: "status_message", value: "boom", source: "field" });
    expect(fields.filter((f) => f.source === "attribute").map((f) => f.key)).toEqual(["attributes.db.system", "attributes.http.route"]);
    expect(fields.filter((f) => f.source === "resource").map((f) => f.key)).toEqual(["resource.db.system", "resource.k8s.pod.name"]);
  });

  it("formats durations", () => {
    expect(formatSpanDuration(0.25, "en")).toBe("250 µs");
    expect(formatSpanDuration(3.456, "en")).toBe("3.46 ms");
    expect(formatSpanDuration(312.44, "en")).toBe("312.4 ms");
    expect(formatSpanDuration(2500, "en")).toBe("2.5 s");
  });
});

describe("traces explorer charts and views", () => {
  it("maps latency percentiles to chart series", () => {
    const s = latencySeries({ latency: { p50: [[1, 10]], p95: [[1, 40]], p99: [] } });
    expect(s).toEqual([
      { label: "p50", points: [[1, 10]] },
      { label: "p95", points: [[1, 40]] },
      { label: "p99", points: [] },
    ]);
  });

  it("round-trips saved view state and validates untrusted state", () => {
    const search = {
      range: "6h",
      f: { f: [["service.name", "=", "checkout"] as ["service.name", "=", string]], g: undefined },
      cols: ["timestamp", "name", "duration_ms"],
      sort: "duration" as const,
      root: true,
      gb: "status_code",
    };
    const state = tracesViewStateFromSearch(search, search.cols, "status_code");
    expect(state).toMatchObject({ filters: [{ key: "service.name", op: "=", value: "checkout" }], sort: "duration", root_only: true, range: "6h", group_by: "status_code" });
    expect(searchFromTracesViewState(state)).toEqual({ range: "6h", f: { f: [["service.name", "=", "checkout"]] }, cols: ["timestamp", "name", "duration_ms"], order: undefined, sort: "duration", root: true, gb: "status_code" });
    expect(searchFromTracesViewState({ filters: [{ key: "x", op: "drop table" }], columns: 42, sort: "random", root_only: "yes", group_by: 5 })).toEqual({
      f: undefined,
      cols: undefined,
      order: undefined,
      sort: undefined,
      root: undefined,
      gb: undefined,
    });
  });
});
