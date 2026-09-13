import { describe, expect, it } from "vitest";
import type { Span } from "@/api/types";
import { bigTrace, trace } from "@/mocks/fixtures";
import { layoutWaterfall, tickAnchor, tickCountForWidth, timeTicks } from "./waterfall";

const base = "2026-09-13T10:00:00";

function span(id: string, parent: string, startNs: number, durNs: number, service = "svc"): Span {
  const frac = String(startNs).padStart(9, "0");
  return {
    span_id: id,
    parent_span_id: parent,
    name: id,
    kind: "internal",
    service_name: service,
    start: `${base}.${frac}Z`,
    duration_ns: durNs,
    status_code: "unset",
    status_message: "",
    attributes: {},
    resource_attributes: {},
    events: [],
  };
}

describe("layoutWaterfall", () => {
  it("returns an empty layout for no spans", () => {
    expect(layoutWaterfall([])).toEqual({ rows: [], traceStartNs: 0n, totalNs: 0, services: [] });
  });

  it("orders depth-first by start time and computes depth", () => {
    // root 0..1000ns; a 100..400; a1 150..250; b 500..900 ; input shuffled
    const spans = [span("b", "root", 500, 400), span("a1", "a", 150, 100), span("root", "", 0, 1000), span("a", "root", 100, 300)];
    const { rows, totalNs } = layoutWaterfall(spans);
    expect(totalNs).toBe(1000);
    expect(rows.map((r) => [r.span.span_id, r.depth])).toEqual([
      ["root", 0],
      ["a", 1],
      ["a1", 2],
      ["b", 1],
    ]);
    const a = rows[1]!;
    expect(a.left).toBeCloseTo(0.1);
    expect(a.width).toBeCloseTo(0.3);
    expect(a.offsetNs).toBe(100);
    expect(rows[0]!.childCount).toBe(2);
  });

  it("uses nanosecond precision relative offsets", () => {
    const { rows } = layoutWaterfall([span("r", "", 1, 2), span("c", "r", 2, 1)]);
    expect(rows[1]!.offsetNs).toBe(1);
    expect(rows[1]!.left).toBeCloseTo(0.5);
  });

  it("treats spans with a missing parent as orphan roots", () => {
    const { rows } = layoutWaterfall([span("root", "", 0, 100), span("lost", "deadbeefdeadbeef", 50, 10)]);
    const lost = rows.find((r) => r.span.span_id === "lost")!;
    expect(lost.depth).toBe(0);
    expect(lost.orphan).toBe(true);
    expect(rows.find((r) => r.span.span_id === "root")!.orphan).toBe(false);
  });

  it("applies a minimum width and clamps to the right edge", () => {
    const { rows } = layoutWaterfall([span("root", "", 0, 1_000_000), span("tiny", "root", 999_999, 0)], { minWidth: 0.01 });
    const tiny = rows[1]!;
    expect(tiny.width).toBeGreaterThanOrEqual(0.01);
    expect(tiny.left).toBeCloseTo(0.999999);
  });

  it("survives parent cycles", () => {
    const { rows } = layoutWaterfall([span("x", "y", 0, 10), span("y", "x", 5, 10)]);
    expect(rows.map((r) => r.span.span_id).sort()).toEqual(["x", "y"]);
  });

  it("lays out the fixture trace", () => {
    const layout = layoutWaterfall(trace(Date.now()));
    expect(layout.rows).toHaveLength(11);
    expect(layout.rows[0]!.span.name).toBe("GET /api/cart");
    expect(layout.services).toEqual(["checkout", "frontend", "pricing", "recommendations"]);
    const deepest = Math.max(...layout.rows.map((r) => r.depth));
    expect(deepest).toBe(4);
    for (const r of layout.rows) {
      expect(r.left).toBeGreaterThanOrEqual(0);
      expect(r.left + r.width).toBeLessThanOrEqual(1.0000001);
    }
  });
});

describe("layoutWaterfall at scale", () => {
  it("lays out a 12k-span trace quickly and in DFS order", () => {
    const spans = bigTrace(Date.now());
    expect(spans).toHaveLength(12_000);
    const t0 = performance.now();
    const layout = layoutWaterfall(spans);
    expect(performance.now() - t0).toBeLessThan(2_000);
    expect(layout.rows).toHaveLength(12_000);
    expect(layout.rows[0]!.depth).toBe(0);
    expect(Math.max(...layout.rows.map((r) => r.depth))).toBe(2);
    // every child comes after its parent
    const pos = new Map(layout.rows.map((r, i) => [r.span.span_id, i]));
    for (const r of layout.rows) if (r.span.parent_span_id) expect(pos.get(r.span.parent_span_id)!).toBeLessThan(pos.get(r.span.span_id)!);
  });
});

describe("timeTicks", () => {
  it("spaces ticks evenly", () => expect(timeTicks(1000, 4)).toEqual([0, 250, 500, 750, 1000]));
  it("handles zero", () => expect(timeTicks(0)).toEqual([0]));

  it("chooses the tick count from the axis width", () => {
    expect(tickCountForWidth(0)).toBe(4); // unmeasured: desktop default
    expect(tickCountForWidth(100)).toBe(1); // phone: start and end only, no "0 ns" + "1.03" crowding
    expect(tickCountForWidth(170)).toBe(2);
    expect(tickCountForWidth(376)).toBe(4);
    expect(tickCountForWidth(2000)).toBe(4);
    expect(tickCountForWidth(40, 80, 4)).toBe(1);
    expect(timeTicks(4_130_000, tickCountForWidth(150))).toEqual([0, 4_130_000]);
  });

  it("anchors the first label at the start and the last at the end", () => {
    expect([0, 1, 2].map((i) => tickAnchor(i, 3))).toEqual(["start", "middle", "end"]);
    expect([0, 1].map((i) => tickAnchor(i, 2))).toEqual(["start", "end"]);
    expect(tickAnchor(0, 1)).toBe("start");
  });
});
