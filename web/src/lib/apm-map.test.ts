import { describe, expect, it } from "vitest";
import { layoutMap } from "./apm-map";

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
