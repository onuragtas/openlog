import { describe, expect, it } from "vitest";
import type { LogRecord } from "@/api/types";
import { concatLogPages, formatNsTimestamp } from "./logs";

function log(body: string): LogRecord {
  return {
    timestamp: "2026-09-13T10:00:00.000000000Z",
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

describe("concatLogPages", () => {
  it("keeps every row of every page in order (the cursor never repeats rows)", () => {
    expect(concatLogPages([{ logs: [log("a"), log("b")] }, { logs: [] }, { logs: [log("b"), log("c")] }]).map((l) => l.body)).toEqual(["a", "b", "b", "c"]);
    expect(concatLogPages([])).toEqual([]);
  });
});

describe("formatNsTimestamp", () => {
  it("formats epoch ns as RFC3339 with 9 digits", () => {
    expect(formatNsTimestamp(1_789_300_800_000_000_001n)).toBe("2026-09-13T12:00:00.000000001Z");
  });
});
