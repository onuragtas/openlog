import { describe, expect, it } from "vitest";
import { buildOql, builderIssue, defaultBuilderState, measureExpression, oqlKey, type BuilderState } from "./oql-builder";

const state = (p: Partial<BuilderState> = {}): BuilderState => ({ ...defaultBuilderState(), ...p });

describe("oqlKey", () => {
  it("maps prefixed keys to map lookups and leaves attributes alone", () => {
    expect(oqlKey("service.name")).toBe("service.name");
    expect(oqlKey("attributes.http.route")).toBe("attributes['http.route']");
    expect(oqlKey("attr.http.route")).toBe("attributes['http.route']");
    expect(oqlKey("resource.k8s.pod.name")).toBe("resource['k8s.pod.name']");
    expect(oqlKey("attributes.it's")).toBe("attributes['it''s']");
  });
});

describe("buildOql", () => {
  it("counts events of the chosen type over time", () => {
    expect(buildOql(state())).toBe("SELECT count(*) FROM Log TIMESERIES AUTO");
    expect(buildOql(state({ timeseries: false }))).toBe("SELECT count(*) FROM Log");
  });

  it("adds conditions, grouping and the facet limit", () => {
    const q = buildOql(
      state({
        conditions: [
          { key: "severity", op: "=", value: "ERROR" },
          { key: "attributes.http.route", op: "contains", value: "/api" },
          { key: "message", op: "is_not_null", value: "" },
        ],
        groupBy: ["service.name", "resource.host.arch"],
        limit: 5,
      }),
    );
    expect(q).toBe(
      "SELECT count(*) FROM Log WHERE severity = 'ERROR' AND attributes['http.route'] CONTAINS '/api' AND message IS NOT NULL " +
        "FACET service.name, resource['host.arch'] LIMIT 5 TIMESERIES AUTO",
    );
  });

  it("writes numbers and booleans unquoted, strings quoted", () => {
    const q = buildOql(
      state({
        eventType: "Span",
        conditions: [
          { key: "duration.ms", op: ">", value: "250" },
          { key: "error", op: "=", value: "true" },
          { key: "name", op: "=", value: "GET /it's" },
        ],
      }),
    );
    expect(q).toBe("SELECT count(*) FROM Span WHERE duration.ms > 250 AND error = true AND name = 'GET /it''s' TIMESERIES AUTO");
  });

  it("builds percentiles and metric queries", () => {
    expect(measureExpression("percentile", "duration.ms")).toBe("percentile(duration.ms, 50, 95, 99)");
    expect(buildOql(state({ eventType: "Transaction", measure: "percentile", attribute: "duration.ms", groupBy: ["transaction.name"] }))).toBe(
      "SELECT percentile(duration.ms, 50, 95, 99) FROM Transaction FACET transaction.name LIMIT 10 TIMESERIES AUTO",
    );
    expect(buildOql(state({ eventType: "Metric", measure: "average", attribute: "value", metricName: "system.cpu.utilization", groupBy: ["host.name"] }))).toBe(
      "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' FACET host.name LIMIT 10 TIMESERIES AUTO",
    );
  });

  it("reports what is still missing instead of producing a broken query", () => {
    expect(builderIssue(state({ measure: "average" }))).toBe("attribute");
    expect(buildOql(state({ measure: "average" }))).toBe("");
    expect(builderIssue(state({ eventType: "Metric", measure: "average", attribute: "value" }))).toBe("metric");
    expect(builderIssue(state())).toBeNull();
  });

  it("skips empty conditions and group-by entries", () => {
    expect(buildOql(state({ conditions: [{ key: "severity", op: "=", value: "  " }], groupBy: [""] }))).toBe("SELECT count(*) FROM Log TIMESERIES AUTO");
  });
});
