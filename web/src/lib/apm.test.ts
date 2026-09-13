import { describe, expect, it } from "vitest";
import {
  apdexBadgeVariant,
  apdexLevel,
  bucketIndex,
  formatApdex,
  formatMs,
  formatRate,
  formatRpm,
  latencySeries,
  metricPoints,
  parseAttrFilter,
  parseServiceNodeId,
  serviceNodeId,
  shapeHistogram,
  stackLines,
  type PointLike,
} from "./apm";

const point = (t: number, over: Partial<PointLike> = {}): PointLike => ({
  t,
  requests: 10,
  throughput: 10,
  errors: 1,
  error_rate: 0.1,
  avg_ms: 12,
  p50_ms: 8,
  p95_ms: 40,
  p99_ms: 90,
  apdex: 0.9,
  ...over,
});

describe("formatting", () => {
  it("formats durations, rates and apdex", () => {
    expect(formatMs(0.4213, "en")).toBe("0.42 ms");
    expect(formatMs(5.26, "en")).toBe("5.3 ms");
    expect(formatMs(123.4, "en")).toBe("123 ms");
    expect(formatMs(1240, "en")).toBe("1.24 s");
    expect(formatMs(150_000, "en")).toBe("2.5 min");
    expect(formatMs(null)).toBe("–");
    expect(formatRpm(12.345, "en")).toBe("12.3 rpm");
    expect(formatRpm(0.5, "en")).toBe("0.5 rpm");
    expect(formatRate(0.0052, "en")).toBe("0.52%");
    expect(formatRate(0.123, "en")).toBe("12.3%");
    expect(formatRate(0, "en")).toBe("0%");
    expect(formatRate(0.5, "tr")).toBe("50%");
    expect(formatApdex(0.9, "en")).toBe("0.90");
    expect(formatApdex(null)).toBe("–");
  });

  it("maps apdex to levels and badge variants", () => {
    expect([0.99, 0.9, 0.75, 0.55, 0.2, null].map(apdexLevel)).toEqual(["excellent", "good", "fair", "poor", "unacceptable", "none"]);
    expect(apdexBadgeVariant("good")).toBe("success");
    expect(apdexBadgeVariant("poor")).toBe("warning");
    expect(apdexBadgeVariant("unacceptable")).toBe("destructive");
    expect(apdexBadgeVariant("none")).toBe("muted");
  });
});

describe("series", () => {
  it("skips null buckets and builds latency series", () => {
    const pts = [point(1000), point(2000, { p95_ms: null, apdex: null }), point(3000)];
    expect(metricPoints(pts, "apdex")).toEqual([
      [1000, 0.9],
      [3000, 0.9],
    ]);
    const lat = latencySeries(pts);
    expect(lat.map((s) => s.label)).toEqual(["p50", "p95", "p99"]);
    expect(lat[1]!.points).toHaveLength(2);
  });
});

describe("histogram", () => {
  const bin = (b: number, count: number) => ({ from_ms: 2 ** ((b - 1) / 8), to_ms: 2 ** (b / 8), count });

  it("recovers bucket indexes", () => {
    expect(bucketIndex(bin(0, 1))).toBe(0);
    expect(bucketIndex(bin(54, 1))).toBe(54);
    expect(bucketIndex(bin(-20, 1))).toBe(-20);
  });

  it("fills gaps and merges into at most maxBins bars", () => {
    const shaped = shapeHistogram([bin(10, 5), bin(12, 3)]);
    expect(shaped.map((b) => b.count)).toEqual([5, 0, 3]);
    const wide = shapeHistogram([bin(0, 1), bin(99, 2)], 10);
    expect(wide.length).toBeLessThanOrEqual(10);
    expect(wide.reduce((s, b) => s + b.count, 0)).toBe(3);
    expect(wide[0]!.from_ms).toBeCloseTo(2 ** (-1 / 8));
    expect(wide[wide.length - 1]!.to_ms).toBeCloseTo(2 ** (99 / 8));
    expect(shapeHistogram([])).toEqual([]);
  });
});

describe("stack traces", () => {
  it("marks application frames", () => {
    const lines = stackLines(
      "TypeError: boom\n    at /app/node_modules/express/lib/router/layer.js:95:5\n    at flaky (/app/server.js:42:17)\n",
    );
    expect(lines).toEqual([
      { text: "TypeError: boom", inApp: false, frame: false },
      { text: "    at /app/node_modules/express/lib/router/layer.js:95:5", inApp: false, frame: true },
      { text: "    at flaky (/app/server.js:42:17)", inApp: true, frame: true },
    ]);
    const goLines = stackLines("main.loadInventory(0x1)\n\t/app/main.go:88 +0x2a\nruntime.goexit()\n\t/usr/local/go/src/runtime/asm.s:1 +0x1");
    expect(goLines.filter((l) => l.inApp).map((l) => l.text.trim())).toEqual(["main.loadInventory(0x1)", "/app/main.go:88 +0x2a"]);
  });
});

describe("identifiers and filters", () => {
  it("round-trips service node ids", () => {
    const id = serviceNodeId("orders", "shop", "prod");
    expect(id).toBe("service:orders|shop|prod");
    expect(parseServiceNodeId(id)).toEqual({ name: "orders", namespace: "shop", environment: "prod" });
    expect(parseServiceNodeId("db:redis")).toBeNull();
  });

  it("parses attribute filters", () => {
    expect(parseAttrFilter("http.route=/orders/{id}, user.id = 42\nbad\n=x\ny=")).toEqual([
      ["http.route", "/orders/{id}"],
      ["user.id", "42"],
    ]);
    expect(parseAttrFilter(undefined)).toEqual([]);
  });
});
