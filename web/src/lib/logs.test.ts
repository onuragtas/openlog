import { describe, expect, it } from "vitest";
import type { LogRecord } from "@/api/types";
import { formatNsTimestamp, mergeLogPages, nextLogCursor } from "./logs";

const ts = (sec: number, ns = 0) => `2026-09-13T10:00:${String(sec).padStart(2, "0")}.${String(ns).padStart(9, "0")}Z`;

function log(timestamp: string, body: string): LogRecord {
  return {
    timestamp,
    severity_text: "INFO",
    severity_number: 9,
    body,
    host_id: "h",
    service_name: "svc",
    trace_id: "",
    span_id: "",
    attributes: {},
    resource_attributes: {},
  };
}

describe("mergeLogPages", () => {
  it("drops boundary rows repeated by the next page (inclusive `to`)", () => {
    const p1 = [log(ts(9), "a"), log(ts(8), "b"), log(ts(7, 5), "c")];
    // to = ts(7,5): the server returns "c" again plus a sibling at the same instant
    const p2 = [log(ts(7, 5), "c"), log(ts(7, 5), "c2"), log(ts(6), "d")];
    expect(mergeLogPages([p1, p2]).map((l) => l.body)).toEqual(["a", "b", "c", "c2", "d"]);
  });

  it("keeps identical lines as often as a single page returned them", () => {
    const p1 = [log(ts(9), "x"), log(ts(5), "dup")];
    const p2 = [log(ts(5), "dup"), log(ts(5), "dup"), log(ts(4), "y")];
    expect(mergeLogPages([p1, p2]).map((l) => l.body)).toEqual(["x", "dup", "dup", "y"]);
  });

  it("ignores rows newer than the boundary and handles empty pages", () => {
    const p1 = [log(ts(9), "a"), log(ts(8), "b")];
    const p2 = [log(ts(9), "a"), log(ts(7), "c")];
    expect(mergeLogPages([p1, [], p2]).map((l) => l.body)).toEqual(["a", "b", "c"]);
    expect(mergeLogPages([])).toEqual([]);
  });

  it("compares with nanosecond precision", () => {
    const p1 = [log(ts(1, 200), "late")];
    const p2 = [log(ts(1, 200), "late"), log(ts(1, 100), "early")];
    expect(mergeLogPages([p1, p2]).map((l) => l.body)).toEqual(["late", "early"]);
  });
});

describe("nextLogCursor", () => {
  it("is undefined when the last page was not full", () => {
    expect(nextLogCursor([[log(ts(1), "a")]], 2)).toBeUndefined();
    expect(nextLogCursor([], 2)).toBeUndefined();
  });

  it("uses the oldest timestamp of a full page", () => {
    expect(nextLogCursor([[log(ts(9), "a"), log(ts(3, 42), "b")]], 2)).toBe(ts(3, 42));
  });

  it("steps back 1ns when a full page holds a single instant", () => {
    expect(nextLogCursor([[log(ts(3, 0), "a"), log(ts(3, 0), "b")]], 2)).toBe(ts(2, 999_999_999));
  });
});

describe("formatNsTimestamp", () => {
  it("formats epoch ns as RFC3339 with 9 digits", () => {
    expect(formatNsTimestamp(1_789_300_800_000_000_001n)).toBe("2026-09-13T12:00:00.000000001Z");
  });
});
