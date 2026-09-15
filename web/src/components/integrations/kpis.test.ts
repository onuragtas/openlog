import { describe, expect, it } from "vitest";
import type { MetricSeries } from "@/api/types";
import { KPIS } from "./kpis";

const s = (attributes: Record<string, string>, points: [number, number][]): MetricSeries => ({ attributes, points }) as MetricSeries;
const kpi = (integration: keyof typeof KPIS, id: string) => KPIS[integration].find((k) => k.id === id)!;

describe("integration KPIs", () => {
  it("offers key figures for every integration with panels", () => {
    for (const id of ["nginx", "redis", "mysql", "postgresql", "mssql", "iis"] as const) {
      expect(KPIS[id].length).toBeGreaterThanOrEqual(2);
      expect(new Set(KPIS[id].map((k) => k.id)).size).toBe(KPIS[id].length);
    }
  });

  it("reduces series to the latest value", () => {
    expect(kpi("nginx", "activeConnections").compute({ c: [s({ state: "active" }, [[1, 3], [2, 5]]), s({ state: "waiting" }, [[2, 50]])] })).toBe(5);
    expect(kpi("redis", "hitRatio").compute({ h: [s({}, [[1, 9], [2, 3]])], m: [s({}, [[1, 1], [2, 1]])] })).toBe(0.75);
    expect(kpi("mysql", "replicaLag").compute({ l: [] })).toBeNull();
  });

  it("SQL Server cache hit ratio arrives in % and IIS bytes sent picks the sent direction", () => {
    expect(kpi("mssql", "cacheHit").compute({ h: [s({}, [[1, 50], [2, 99.5]])] })).toBeCloseTo(0.995);
    expect(kpi("mssql", "cacheHit").compute({ h: [] })).toBeNull();
    expect(kpi("mssql", "batchRequests").queries.b!.name).toBe("sqlserver.batch.request.rate");
    expect(kpi("mssql", "deadlocks").queries.d).toEqual({ name: "sqlserver.deadlock.count", agg: "rate" });
    expect(kpi("iis", "bytesSent").compute({ n: [s({ direction: "sent" }, [[1, 300]]), s({ direction: "received" }, [[1, 20]])] })).toBe(300);
    expect(kpi("iis", "requests").compute({ r: [s({}, [[1, 7]])] })).toBe(7);
  });

  it("counts IIS application pools that are not running", () => {
    const pool = (name: string, state: number) => s({ "resource.iis.application_pool": name }, [[1, 3], [2, state]]);
    expect(kpi("iis", "poolsNotRunning").queries.p).toEqual({ name: "iis.application_pool.state", agg: "last", groupBy: ["resource.iis.application_pool"] });
    expect(kpi("iis", "poolsNotRunning").compute({ p: [pool("DefaultAppPool", 3), pool("api", 6), pool("legacy", 5)] })).toBe(2);
    expect(kpi("iis", "poolsNotRunning").compute({ p: [] })).toBeNull();
  });

  it("computes PostgreSQL connection usage and transactions", () => {
    const backends = [s({ "resource.postgresql.database.name": "a" }, [[1, 10]]), s({ "resource.postgresql.database.name": "b" }, [[1, 30]])];
    expect(kpi("postgresql", "connectionUsage").compute({ b: backends, m: [s({}, [[1, 100]])] })).toBe(0.4);
    expect(kpi("postgresql", "connectionUsage").compute({ b: backends, m: [] })).toBeNull();
    expect(kpi("postgresql", "tps").compute({ c: [s({}, [[1, 4]])], r: [] })).toBe(4);
    expect(kpi("postgresql", "tps").compute({ c: [], r: [] })).toBeNull();
  });
});
