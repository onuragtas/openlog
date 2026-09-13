import { describe, expect, it } from "vitest";
import { compareVersions, formatWaves, isOpenRollout, parseWaves, rollbackCandidates, rolloutShares, statusTone, validClock, windowError } from "./fleet";

describe("parseWaves", () => {
  it.each([
    ["10, 50, 100", [10, 50, 100], null],
    ["1 10 100", [1, 10, 100], null],
    ["100", [100], null],
    ["10 → 50 → 100", [10, 50, 100], null],
    ["", [], "empty"],
    ["10, x, 100", null, "invalid"],
    ["0, 100", null, "invalid"],
    ["10, 101", null, "invalid"],
    ["2.5, 100", null, "invalid"],
    ["50, 10, 100", null, "order"],
    ["10, 10, 100", null, "order"],
    ["10, 50", null, "last"],
  ])("%j", (text, waves, error) => {
    const r = parseWaves(text);
    expect(r.error).toBe(error);
    if (waves) expect(r.waves).toEqual(waves);
  });

  it("limits the number of waves", () => {
    const many = Array.from({ length: 21 }, (_, i) => i + 80).join(",");
    expect(parseWaves(many).error).toBe("invalid");
    expect(formatWaves([10, 50, 100])).toBe("10, 50, 100");
  });
});

describe("maintenance windows", () => {
  it("validates clocks", () => {
    expect(validClock("02:00")).toBe(true);
    expect(validClock("24:00")).toBe(false);
    expect(validClock("24:00", true)).toBe(true);
    expect(validClock("2:00")).toBe(false);
    expect(validClock("23:60")).toBe(false);
    expect(windowError({ start: "22:00", end: "02:00" })).toBeNull();
    expect(windowError({ start: "02:00", end: "02:00" })).toBe("equal");
    expect(windowError({ start: "24:00", end: "02:00" })).toBe("clock");
  });
});

describe("versions", () => {
  it("orders by SemVer", () => {
    const sorted = ["0.10.0", "0.4.0", "0.4.0-beta.2", "0.4.0-beta.10", "v0.3.9", "0.4.0-alpha", "0.4.0+build"].sort(compareVersions);
    expect(sorted).toEqual(["v0.3.9", "0.4.0-alpha", "0.4.0-beta.2", "0.4.0-beta.10", "0.4.0", "0.4.0+build", "0.10.0"]);
    expect(compareVersions("0.4.0", "0.4.0+abc")).toBe(0);
    expect(compareVersions("junk", "0.1.0")).toBe(-1);
  });

  it("lists rollback candidates below the target", () => {
    expect(rollbackCandidates(["0.3.0", "0.4.0", "0.2.0", "dev", "0.3.0"], "0.4.0")).toEqual(["0.3.0", "0.2.0"]);
    expect(rollbackCandidates(["0.3.0"], null)).toEqual(["0.3.0"]);
  });
});

describe("rollouts", () => {
  it("computes progress shares", () => {
    expect(rolloutShares({ pending: 50, attempted: 52, succeeded: 45, failed: 3, rolled_back: 2 })).toEqual({
      total: 100,
      done: 50,
      succeeded: 45,
      failed: 5,
      pending: 50,
    });
    expect(rolloutShares({ pending: 0, attempted: 0, succeeded: 0, failed: 0, rolled_back: 0 }).total).toBe(0);
  });

  it("classifies states and statuses", () => {
    expect(isOpenRollout("halted")).toBe(true);
    expect(isOpenRollout("completed")).toBe(false);
    expect(statusTone("already_failed")).toBe("destructive");
    expect(statusTone("something_new")).toBe("muted");
  });
});
