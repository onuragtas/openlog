import { describe, expect, it } from "vitest";
import type { Span } from "@/api/types";
import { expandAncestors, layoutWaterfall, matchRowIndexes, visibleRowIndexes, waterfallTree } from "./waterfall";

const t0 = Date.UTC(2026, 8, 14, 10, 0, 0);
const span = (id: string, parent: string, name: string, startMs: number, service = "svc"): Span => ({
  span_id: id,
  parent_span_id: parent,
  name,
  kind: "internal",
  service_name: service,
  start: new Date(t0 + startMs).toISOString(),
  duration_ns: 1_000_000,
  status_code: "unset",
  status_message: "",
  attributes: {},
  resource_attributes: {},
  events: [],
});

// root ─ a ─ a1
//      │   └ a2
//      └ b ─ b1
const spans = [span("r", "", "root", 0), span("a", "r", "GET /a", 1), span("a1", "a", "db query", 2), span("a2", "a", "cache", 3), span("b", "r", "GET /b", 4, "billing"), span("b1", "b", "db query", 5)];
const { rows } = layoutWaterfall(spans);
const ids = (idx: number[]) => idx.map((i) => rows[i]!.span.span_id);

describe("waterfallTree", () => {
  it("links parents and sibling positions", () => {
    expect(ids(rows.map((_, i) => i))).toEqual(["r", "a", "a1", "a2", "b", "b1"]);
    const tree = waterfallTree(rows);
    expect([...tree.parent]).toEqual([-1, 0, 1, 1, 0, 4]);
    expect([...tree.posInSet]).toEqual([1, 1, 1, 2, 2, 1]);
    expect([...tree.setSize]).toEqual([1, 2, 2, 2, 2, 1]);
  });
});

describe("visibleRowIndexes", () => {
  it("hides descendants of collapsed spans only", () => {
    expect(ids(visibleRowIndexes(rows, new Set()))).toEqual(["r", "a", "a1", "a2", "b", "b1"]);
    expect(ids(visibleRowIndexes(rows, new Set(["a"])))).toEqual(["r", "a", "b", "b1"]);
    expect(ids(visibleRowIndexes(rows, new Set(["r", "a"])))).toEqual(["r"]);
    // Collapsing a leaf changes nothing.
    expect(ids(visibleRowIndexes(rows, new Set(["a1"])))).toHaveLength(6);
  });

  it("stays linear for 100k rows", () => {
    const big = layoutWaterfall([span("r", "", "root", 0), ...Array.from({ length: 100_000 }, (_, i) => span(`c${i}`, "r", `child ${i}`, i % 1000))]);
    const start = performance.now();
    expect(visibleRowIndexes(big.rows, new Set()).length).toBe(100_001);
    expect(visibleRowIndexes(big.rows, new Set(["r"])).length).toBe(1);
    expect(waterfallTree(big.rows).setSize[1]).toBe(100_000);
    expect(performance.now() - start).toBeLessThan(1000);
  });
});

describe("matchRowIndexes / expandAncestors", () => {
  it("matches name, service and id case-insensitively", () => {
    expect(ids(matchRowIndexes(rows, " DB "))).toEqual(["a1", "b1"]);
    expect(ids(matchRowIndexes(rows, "billing"))).toEqual(["b"]);
    expect(ids(matchRowIndexes(rows, "a2"))).toEqual(["a2"]);
    expect(matchRowIndexes(rows, "  ")).toEqual([]);
  });

  it("expands collapsed ancestors of a match and keeps the set otherwise", () => {
    const tree = waterfallTree(rows);
    const collapsed = new Set(["r", "a", "b"]);
    expect([...expandAncestors(rows, tree, 2, collapsed)]).toEqual(["b"]);
    const none = new Set(["b"]);
    expect(expandAncestors(rows, tree, 2, none)).toBe(none);
  });
});
