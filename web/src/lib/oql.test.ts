import { describe, expect, it } from "vitest";
import { alertQueryIssues, completionContext, eventTypeOf, hasTimeClause, hostMetricOql, lexOql, lineColumn, offsetOf, variablesOf } from "./oql";

const types = (q: string) => lexOql(q).filter((t) => t.type !== "whitespace").map((t) => [t.type, t.text]);

describe("lexOql", () => {
  it("classifies keywords, functions, attributes, strings, numbers, variables and operators", () => {
    expect(types("SELECT count(*) FROM Log WHERE severity = 'ERROR' AND host.name IN ({{host}}) SINCE 3 hours ago")).toEqual([
      ["keyword", "SELECT"],
      ["function", "count"],
      ["punctuation", "("],
      ["operator", "*"],
      ["punctuation", ")"],
      ["keyword", "FROM"],
      ["identifier", "Log"],
      ["keyword", "WHERE"],
      ["identifier", "severity"],
      ["operator", "="],
      ["string", "'ERROR'"],
      ["keyword", "AND"],
      ["identifier", "host.name"],
      ["keyword", "IN"],
      ["punctuation", "("],
      ["variable", "{{host}}"],
      ["punctuation", ")"],
      ["keyword", "SINCE"],
      ["number", "3"],
      ["unit", "hours"],
      ["keyword", "ago"],
    ]);
  });

  it("handles comments, escapes, backticks and case-insensitive keywords", () => {
    expect(types("select `my attr` -- trailing\nfrom Span // x")).toEqual([
      ["keyword", "select"],
      ["backtick", "`my attr`"],
      ["comment", "-- trailing"],
      ["keyword", "from"],
      ["identifier", "Span"],
      ["comment", "// x"],
    ]);
    expect(types("'it''s' \"a\\\"b\" 1.5e3 >= != <>")).toEqual([
      ["string", "'it''s'"],
      ["string", '"a\\"b"'],
      ["number", "1.5e3"],
      ["operator", ">="],
      ["operator", "!="],
      ["operator", "<>"],
    ]);
    // A function name without a call is an identifier; min as a unit.
    expect(types("max min(x) 5 min")).toEqual([
      ["identifier", "max"],
      ["function", "min"],
      ["punctuation", "("],
      ["identifier", "x"],
      ["punctuation", ")"],
      ["number", "5"],
      ["unit", "min"],
    ]);
  });

  it("covers the whole text without gaps (unterminated input too)", () => {
    const q = "SELECT count(*) FROM Log WHERE message = 'unterminated\nFACET `x";
    const toks = lexOql(q);
    expect(toks.map((t) => t.text).join("")).toBe(q);
    toks.forEach((t, i) => expect(t.from).toBe(i === 0 ? 0 : toks[i - 1]!.to));
  });
});

describe("structure helpers", () => {
  it("detects SINCE/UNTIL outside strings and comments", () => {
    expect(hasTimeClause("SELECT count(*) FROM Log SINCE 1 day ago")).toBe(true);
    expect(hasTimeClause("SELECT count(*) FROM Log until now")).toBe(true);
    expect(hasTimeClause("SELECT count(*) FROM Log WHERE message = 'SINCE' -- UNTIL")).toBe(false);
  });

  it("finds the event type and variables", () => {
    expect(eventTypeOf("SELECT count(*)\nFROM Transaction FACET name")).toBe("Transaction");
    expect(eventTypeOf("SELECT count(*) FROM")).toBeNull();
    expect(variablesOf("SELECT count(*) FROM Log WHERE host.name IN ({{host}}) AND service.name = {{ svc }} OR x = {{host}}")).toEqual(["host", "svc"]);
  });

  it("builds host metric chart queries", () => {
    expect(hostMetricOql("system.cpu.utilization", "h-1", ["cpu.mode"])).toBe(
      "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' AND host.id = 'h-1' FACET attributes['cpu.mode'] TIMESERIES AUTO",
    );
    expect(hostMetricOql("x", "it's")).toBe("SELECT average(value) FROM Metric WHERE metricName = 'x' AND host.id = 'it''s' TIMESERIES AUTO");
  });

  it("converts offsets and line/columns", () => {
    const q = "SELECT\n  count(*)";
    expect(lineColumn(q, 9)).toEqual({ line: 2, column: 3 });
    expect(offsetOf(q, 2, 3)).toBe(9);
    expect(offsetOf(q, 9, 99)).toBe(q.length);
  });
});

describe("completionContext", () => {
  it("suggests event types after FROM, functions in SELECT, attributes in WHERE/FACET and inside calls", () => {
    expect(completionContext("SELECT count(*) FROM Lo", 23)).toEqual({ kind: "eventType", from: 21, prefix: "Lo" });
    expect(completionContext("SELECT perc", 11)).toMatchObject({ kind: "function", prefix: "perc" });
    expect(completionContext("SELECT average(dur", 18)).toMatchObject({ kind: "attribute", prefix: "dur" });
    expect(completionContext("SELECT count(*) FROM Log WHERE sev", 34)).toMatchObject({ kind: "attribute", prefix: "sev" });
    expect(completionContext("SELECT count(*) FROM Log FACET service.na", 41)).toMatchObject({ kind: "attribute", prefix: "service.na" });
    expect(completionContext("SELECT count(*) FROM Log WHERE severity = 'ERROR' ", 50)).toMatchObject({ kind: "keyword", prefix: "" });
    expect(completionContext("SELECT count(*) FROM Log ", 25)).toMatchObject({ kind: "keyword" });
  });

  it("does not complete inside strings or comments", () => {
    expect(completionContext("SELECT count(*) FROM Log WHERE message = 'tim", 44)).toBeNull();
    expect(completionContext("SELECT count(*) FROM Log -- FAC", 31)).toBeNull();
  });
});

describe("alertQueryIssues", () => {
  it("accepts a single-column faceted query", () => {
    expect(alertQueryIssues("SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name")).toEqual([]);
    expect(alertQueryIssues("SELECT percentile(duration.ms, 95) FROM Transaction")).toEqual([]);
    expect(alertQueryIssues("SELECT filter(count(*), WHERE error = true) FROM Transaction")).toEqual([]);
  });

  it("reports columns, time clauses, compare, histogram and variables", () => {
    expect(alertQueryIssues("SELECT count(*), max(duration) FROM Span").map((i) => i.key)).toEqual(["columns"]);
    expect(alertQueryIssues("SELECT percentile(duration.ms, 50, 95) FROM Transaction").map((i) => i.key)).toEqual(["columns"]);
    expect(alertQueryIssues("SELECT histogram(duration.ms, 1000) FROM Span SINCE 1 hour ago UNTIL now TIMESERIES COMPARE WITH 1 day ago").map((i) => i.key).sort()).toEqual(
      ["compare", "histogram", "since", "timeseries", "until"],
    );
    expect(alertQueryIssues("SELECT count(*) FROM Log WHERE host.name = {{host}}").map((i) => i.key)).toEqual(["variables"]);
  });
});
