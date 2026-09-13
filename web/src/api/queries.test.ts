import { describe, expect, it } from "vitest";
import { logAttrQuery, logsInfiniteQuery } from "./queries";

describe("log attribute filters", () => {
  it("maps filters to attr.<key> parameters and omits empty values", () => {
    expect(logAttrQuery({ source: "file", filePath: "/var/log/nginx/access.log", discoveryId: "", systemdUnit: undefined })).toEqual({
      "attr.openlog.log.source": "file",
      "attr.log.file.path": "/var/log/nginx/access.log",
      "attr.openlog.discovery.id": undefined,
      "attr.openlog.systemd.unit": undefined,
    });
    expect(Object.values(logAttrQuery(undefined)).every((v) => v === undefined)).toBe(true);
  });

  it("includes the filters in the query key", () => {
    const base = { range: { range: "1h" }, hostId: "h1" };
    const a = logsInfiniteQuery({ ...base, attrs: { discoveryId: "nginx" } }).queryKey;
    const b = logsInfiniteQuery({ ...base, attrs: { discoveryId: "redis" } }).queryKey;
    const c = logsInfiniteQuery(base).queryKey;
    expect(a).not.toEqual(b);
    expect(a).not.toEqual(c);
  });
});
