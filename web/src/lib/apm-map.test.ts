import { describe, expect, it } from "vitest";
import { FOCUS_ZOOM, initialMapView, layoutMap, mostConnected } from "./apm-map";

describe("layoutMap", () => {
  it("places callers left of callees and keeps nodes apart", () => {
    const nodes = [{ id: "frontend" }, { id: "orders" }, { id: "catalog" }, { id: "db:postgresql" }, { id: "db:redis" }, { id: "lonely" }];
    const edges = [
      { source: "frontend", target: "orders" },
      { source: "frontend", target: "catalog" },
      { source: "orders", target: "db:postgresql" },
      { source: "catalog", target: "db:redis" },
      { source: "orders", target: "missing" },
      { source: "orders", target: "orders" },
    ];
    const pos = layoutMap(nodes, edges);
    expect(pos.size).toBe(nodes.length);
    const x = (id: string) => pos.get(id)!.x;
    expect(x("frontend")).toBeLessThan(x("orders"));
    expect(x("orders")).toBeLessThan(x("db:postgresql"));
    expect(x("catalog")).toBeLessThan(x("db:redis"));
    const all = [...pos.values()];
    for (let i = 0; i < all.length; i++) {
      for (let j = i + 1; j < all.length; j++) {
        const overlap = Math.abs(all[i]!.x - all[j]!.x) < 220 && Math.abs(all[i]!.y - all[j]!.y) < 88;
        expect(overlap).toBe(false);
      }
    }
  });

  it("handles cycles and empty graphs", () => {
    expect(layoutMap([], []).size).toBe(0);
    const pos = layoutMap([{ id: "a" }, { id: "b" }], [{ source: "a", target: "b" }, { source: "b", target: "a" }]);
    expect(pos.size).toBe(2);
  });
});

describe("initial map view", () => {
  const star = () => {
    const nodes = [{ id: "hub" }, ...Array.from({ length: 12 }, (_, i) => ({ id: `leaf${i}` }))];
    const edges = nodes.slice(1).map((n, i) => (i % 2 ? { source: "hub", target: n.id } : { source: n.id, target: "hub" }));
    return { nodes, edges, positions: layoutMap(nodes, edges) };
  };

  it("finds the most connected node (first wins ties, self loops ignored)", () => {
    const { nodes, edges } = star();
    expect(mostConnected(nodes, edges)).toBe("hub");
    expect(mostConnected([{ id: "a" }, { id: "b" }], [{ source: "b", target: "b" }])).toBe("a");
    expect(mostConnected([], [])).toBeNull();
  });

  it("fits on wide canvases and when the graph is readable when fitted", () => {
    const { edges, positions } = star();
    expect(initialMapView({ positions, edges, width: 1200, height: 600, compact: false })).toEqual({ mode: "fit" });
    const small = layoutMap([{ id: "a" }, { id: "b" }], [{ source: "a", target: "b" }]);
    expect(initialMapView({ positions: small, edges: [], width: 700, height: 420, compact: true })).toEqual({ mode: "fit" });
    expect(initialMapView({ positions, edges, width: 0, height: 0, compact: true })).toEqual({ mode: "fit" });
  });

  it("centers on the focused or most connected node on a phone when fitting would be unreadable", () => {
    const { edges, positions } = star();
    const hub = initialMapView({ positions, edges, width: 340, height: 420, compact: true });
    expect(hub.mode).toBe("focus");
    if (hub.mode !== "focus") return;
    expect(hub.nodeId).toBe("hub");
    expect(hub.x).toBe(positions.get("hub")!.x + 110);
    expect(hub.y).toBe(positions.get("hub")!.y + 44);
    expect(hub.zoom).toBe(FOCUS_ZOOM);
    const leaf = initialMapView({ positions, edges, width: 340, height: 420, compact: true, focusId: "leaf3" });
    expect(leaf.mode === "focus" && leaf.nodeId).toBe("leaf3");
    const unknown = initialMapView({ positions, edges, width: 340, height: 420, compact: true, focusId: "gone" });
    expect(unknown.mode === "focus" && unknown.nodeId).toBe("hub");
  });
});
