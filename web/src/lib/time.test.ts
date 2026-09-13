import { describe, expect, it } from "vitest";
import { parseDuration, parseTimeParam, parseTimestampNs, resolveRange, validateRangeSearch } from "./time";

const NOW = Date.UTC(2026, 8, 13, 10, 0, 0);

describe("parseDuration", () => {
  it.each([
    ["15m", 15 * 60_000],
    ["1h", 3_600_000],
    ["6h", 6 * 3_600_000],
    ["24h", 86_400_000],
    ["7d", 7 * 86_400_000],
    ["30s", 30_000],
    ["2w", 14 * 86_400_000],
  ])("%s", (v, ms) => expect(parseDuration(v)).toBe(ms));

  it.each(["", "0h", "1", "h", "1y", "-1h", "1.5h", " 1 h"])("rejects %j", (v) => expect(parseDuration(v)).toBeNull());
});

describe("parseTimeParam", () => {
  it("accepts unix milliseconds", () => expect(parseTimeParam("1757757600000")).toBe(1757757600000));
  it("accepts numbers", () => expect(parseTimeParam(42)).toBe(42));
  it("accepts RFC3339 with nanoseconds", () => expect(parseTimeParam("2026-09-13T10:00:00.123456789Z")).toBe(NOW + 123));
  it("accepts offsets", () => expect(parseTimeParam("2026-09-13T12:00:00+02:00")).toBe(NOW));
  it.each(["", "yesterday", "13/09/2026", "2026-13-45T00:00:00Z"])("rejects %j", (v) => expect(parseTimeParam(v)).toBeNull());
});

describe("resolveRange", () => {
  it("defaults to 1h", () => expect(resolveRange({}, NOW)).toEqual({ from: NOW - 3_600_000, to: NOW }));
  it("uses a relative range", () => expect(resolveRange({ range: "15m" }, NOW)).toEqual({ from: NOW - 900_000, to: NOW }));
  it("absolute from/to win over range", () => expect(resolveRange({ range: "7d", from: "1000", to: "2000" }, NOW)).toEqual({ from: 1000, to: 2000 }));
  it("ignores inverted absolute ranges", () => expect(resolveRange({ range: "6h", from: "2000", to: "1000" }, NOW)).toEqual({ from: NOW - 6 * 3_600_000, to: NOW }));
  it("falls back on garbage range", () => expect(resolveRange({ range: "abc" }, NOW)).toEqual({ from: NOW - 3_600_000, to: NOW }));
});

describe("validateRangeSearch", () => {
  it("keeps a valid relative range", () => expect(validateRangeSearch({ range: "24h" })).toEqual({ range: "24h" }));
  it("drops invalid values", () => expect(validateRangeSearch({ range: "soon", from: "x", to: 5 })).toEqual({}));
  it("keeps absolute ranges and drops range", () =>
    expect(validateRangeSearch({ range: "1h", from: 1000, to: "2026-09-13T10:00:00Z" })).toEqual({ from: "1000", to: "2026-09-13T10:00:00Z" }));
  it("requires both ends", () => expect(validateRangeSearch({ from: "1000" })).toEqual({}));
});

describe("parseTimestampNs", () => {
  it("is exact to the nanosecond", () => {
    const a = parseTimestampNs("2026-09-13T10:00:00.000000001Z")!;
    const b = parseTimestampNs("2026-09-13T10:00:00.000000002Z")!;
    expect(b - a).toBe(1n);
    expect(a).toBe(BigInt(NOW) * 1_000_000n + 1n);
  });
  it("pads short fractions", () => expect(parseTimestampNs("2026-09-13T10:00:00.5Z")).toBe(BigInt(NOW) * 1_000_000n + 500_000_000n));
  it("handles no fraction", () => expect(parseTimestampNs("2026-09-13T10:00:00Z")).toBe(BigInt(NOW) * 1_000_000n));
  it("rejects garbage", () => expect(parseTimestampNs("nope")).toBeNull());
});
