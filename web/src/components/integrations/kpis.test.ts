import { describe, expect, it } from "vitest";
import type { MetricSeries } from "@/api/types";
import { KPIS } from "./kpis";

const s = (attributes: Record<string, string>, points: [number, number][]): MetricSeries => ({ attributes, points }) as MetricSeries;
const kpi = (integration: keyof typeof KPIS, id: string) => KPIS[integration].find((k) => k.id === id)!;

describe("integration KPIs", () => {
  it("offers key figures for every integration with panels", () => {
    for (const id of ["nginx", "redis", "mysql", "postgresql"] as const) {
      expect(KPIS[id].length).toBeGreaterThanOrEqual(2);
      expect(new Set(KPIS[id].map((k) => k.id)).size).toBe(KPIS[id].length);
    }
  });

  it("reduces series to the latest value", () => {
    expect(kpi("nginx", "activeConnections").compute({ c: [s({ state: "active" }, [[1, 3], [2, 5]]), s({ state: "waiting" }, [[2, 50]])] })).toBe(5);
    expect(kpi("redis", "hitRatio").compute({ h: [s({}, [[1, 9], [2, 3]])], m: [s({}, [[1, 1], [2, 1]])] })).toBe(0.75);
    expect(kpi("mysql", "replicaLag").compute({ l: [] })).toBeNull();
  });

  it("computes PostgreSQL connection usage and transactions", () => {
    const backends = [s({ "resource.postgresql.database.name": "a" }, [[1, 10]]), s({ "resource.postgresql.database.name": "b" }, [[1, 30]])];
    expect(kpi("postgresql", "connectionUsage").compute({ b: backends, m: [s({}, [[1, 100]])] })).toBe(0.4);
    expect(kpi("postgresql", "connectionUsage").compute({ b: backends, m: [] })).toBeNull();
    expect(kpi("postgresql", "tps").compute({ c: [s({}, [[1, 4]])], r: [] })).toBe(4);
    expect(kpi("postgresql", "tps").compute({ c: [], r: [] })).toBeNull();
  });
});
