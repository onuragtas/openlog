import { describe, expect, it } from "vitest";
import type { FilterState } from "@/api/explorer";
import { logsVolumeOql, spanCountOql, spanLatencyOql } from "./explorer-oql";

const logs = (p: Partial<FilterState>): FilterState => ({ filters: [], groups: [], q: "", ...p });
const INVENTORY = "event.name NOT LIKE 'openlog.inventory.%'";

describe("logsVolumeOql", () => {
  it("counts logs faceted by the group-by key, without inventory events", () => {
    expect(logsVolumeOql({ filter: logs({}) })).toEqual({ ok: true, query: `SELECT count(*) FROM Log WHERE ${INVENTORY} TIMESERIES AUTO` });
    expect(logsVolumeOql({ filter: logs({ filters: [{ key: "service.name", op: "=", value: "checkout" }] }), groupBy: "severity_text" })).toEqual({
      ok: true,
      query: `SELECT count(*) FROM Log WHERE ${INVENTORY} AND service.name = 'checkout' FACET severity LIMIT 10 TIMESERIES AUTO`,
    });
  });

  it("maps fields, map keys, body search and OR groups", () => {
    const r = logsVolumeOql({
      filter: logs({
        q: " timeout ",
        filters: [
          { key: "trace_id", op: "=", value: "ABC" },
          { key: "http.route", op: "exists" },
          { key: "resource.k8s.pod.name", op: "in", values: ["a", "b"] },
          { key: "user", op: "=", value: "x" },
        ],
        groups: [[{ key: "severity", op: "=", value: "ERROR" }], [{ key: "attributes.code", op: ">=", value: 500 }, { key: "body", op: "contains", value: "it's" }], []],
      }),
      groupBy: "attr.log.file.path",
    });
    expect(r).toEqual({
      ok: true,
      query:
        `SELECT count(*) FROM Log WHERE ${INVENTORY} AND message CONTAINS 'timeout' AND trace.id = 'abc' ` +
        "AND (attributes['http.route'] IS NOT NULL OR resource['http.route'] IS NOT NULL) AND resource['k8s.pod.name'] IN ('a', 'b') " +
        "AND ((attributes['user'] IS NOT NULL AND attributes['user'] = 'x') OR (attributes['user'] IS NULL AND resource['user'] = 'x')) " +
        "AND (severity = 'ERROR' OR (attributes['code'] >= 500 AND message CONTAINS 'it''s')) FACET attributes['log.file.path'] LIMIT 10 TIMESERIES AUTO",
    });
  });

  it("reports conditions without an OQL equivalent", () => {
    expect(logsVolumeOql({ filter: logs({ filters: [{ key: "body", op: "regex", value: "^x" }] }) })).toEqual({ ok: false, reason: "regex" });
    expect(logsVolumeOql({ filter: logs({ groups: [[{ key: "body.user.id", op: "=", value: "1" }]] }) })).toEqual({ ok: false, reason: "key" });
    expect(logsVolumeOql({ filter: logs({}), groupBy: "trace_flags" })).toEqual({ ok: false, reason: "key" });
    // A bare key reads the attribute, else the resource attribute: FACET takes one attribute, so it has no equivalent.
    expect(logsVolumeOql({ filter: logs({}), groupBy: "k8s.namespace.name" })).toEqual({ ok: false, reason: "key" });
    expect(spanCountOql({ filter: { filters: [], groups: [] }, rootOnly: false, groupBy: "k8s.namespace.name" })).toEqual({ ok: false, reason: "key" });
    expect(logsVolumeOql({ filter: logs({}), context: { transaction: "GET /", transactionService: "web" } })).toEqual({ ok: false, reason: "transaction" });
  });
});

describe("span chart OQL", () => {
  it("counts spans with root-only, boolean and nanosecond conditions", () => {
    const filter = {
      filters: [
        { key: "is_entry", op: "=" as const, value: "true" },
        { key: "duration_ns", op: ">" as const, value: 250_000_000 },
        { key: "status_code", op: "=" as const, value: "error" },
      ],
      groups: [],
    };
    expect(spanCountOql({ filter, rootOnly: true, groupBy: "attributes.http.route" })).toEqual({
      ok: true,
      query: "SELECT count(*) FROM Span WHERE parent.id IS NULL AND entry = true AND duration.ms > 250 AND status.code = 'error' FACET attributes['http.route'] LIMIT 10 TIMESERIES AUTO",
    });
    expect(spanCountOql({ filter: { filters: [], groups: [] }, rootOnly: false })).toEqual({ ok: true, query: "SELECT count(*) FROM Span TIMESERIES AUTO" });
  });

  it("builds the duration percentiles", () => {
    expect(spanLatencyOql({ filter: { filters: [{ key: "service.name", op: "=", value: "checkout" }], groups: [] }, rootOnly: false })).toEqual({
      ok: true,
      query: "SELECT percentile(duration.ms, 50, 95, 99) FROM Span WHERE service.name = 'checkout' TIMESERIES AUTO",
    });
  });

  it("reports conditions without an OQL equivalent", () => {
    const one = (f: FilterState["filters"][number]) => spanCountOql({ filter: { filters: [f], groups: [] }, rootOnly: false });
    expect(one({ key: "duration_ns", op: ">", value: "slow" })).toEqual({ ok: false, reason: "key" });
    expect(one({ key: "error", op: "=", value: "yes" })).toEqual({ ok: false, reason: "key" });
    expect(one({ key: "timestamp", op: ">", value: 1 })).toEqual({ ok: false, reason: "key" });
    expect(one({ key: "name", op: "not_regex", value: "^GET" })).toEqual({ ok: false, reason: "regex" });
  });
});
