// Log tabs on the Logs Explorer (D-122): pre-explorer tab parameters become chips; top values shares.
import { describe, expect, it } from "vitest";
import { isFilterableValue, legacyTabFilters, normalizeColumns, valueShare } from "./logs-explorer";

describe("legacyTabFilters", () => {
  it("maps host tab source filters and severity to conditions", () => {
    expect(legacyTabFilters({ severity: "warn", source: "file", file: " /var/log/nginx/error.log ", discovery: "nginx", unit: "nginx.service" })).toEqual([
      { key: "severity_number", op: ">=", value: 13 },
      { key: "attributes.openlog.log.source", op: "=", value: "file" },
      { key: "attributes.log.file.path", op: "=", value: "/var/log/nginx/error.log" },
      { key: "attributes.openlog.discovery.id", op: "=", value: "nginx" },
      { key: "attributes.openlog.systemd.unit", op: "=", value: "nginx.service" },
    ]);
  });

  it("maps the container stream and ignores empty or invalid values", () => {
    expect(legacyTabFilters({ stream: "stderr", severity: "17" })).toEqual([
      { key: "severity_number", op: ">=", value: 17 },
      { key: "attributes.log.iostream", op: "=", value: "stderr" },
    ]);
    expect(legacyTabFilters({ severity: "loud", file: "  " })).toEqual([]);
  });
});

describe("explorer helpers", () => {
  it("computes shares and value limits", () => {
    expect(valueShare(5, 20)).toBe(25);
    expect(valueShare(3, 0)).toBe(0);
    expect(valueShare(30, 20)).toBe(100);
    expect(isFilterableValue("x".repeat(1024))).toBe(true);
    expect(isFilterableValue("é".repeat(513))).toBe(false);
  });

  it("normalizes columns with explorer-specific defaults", () => {
    expect(normalizeColumns([], ["timestamp", "name"])).toEqual(["timestamp", "name"]);
    expect(normalizeColumns(["body"], ["timestamp", "name"])).toEqual(["timestamp", "body"]);
  });
});
