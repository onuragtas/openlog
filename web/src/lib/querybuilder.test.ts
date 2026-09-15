import { describe, expect, it } from "vitest";
import type { QueryFilter } from "@/api/explorer";
import {
  addOrGroup,
  conditionCount,
  decodeFilterState,
  encodeFilterState,
  formatFilter,
  makeFilter,
  normalizeFilterState,
  opsForType,
  parseCondition,
  parseFilterText,
  QB_LIMITS,
  sanitizeFilter,
} from "./querybuilder";

describe("parseCondition", () => {
  it.each<[string, QueryFilter]>([
    ["service.name=api", { key: "service.name", op: "=", value: "api" }],
    ["service.name = api", { key: "service.name", op: "=", value: "api" }],
    ["service.name == api", { key: "service.name", op: "=", value: "api" }],
    ["service.name!=api", { key: "service.name", op: "!=", value: "api" }],
    ["service.name <> api", { key: "service.name", op: "!=", value: "api" }],
    ["http.status_code >= 500", { key: "http.status_code", op: ">=", value: 500 }],
    ["http.status_code<400", { key: "http.status_code", op: "<", value: 400 }],
    ["duration > abc", { key: "duration", op: ">", value: "abc" }],
    ["severity_text IN (ERROR, WARN)", { key: "severity_text", op: "in", values: ["ERROR", "WARN"] }],
    ["severity_text in ERROR,WARN", { key: "severity_text", op: "in", values: ["ERROR", "WARN"] }],
    ["severity_text NOT IN (ERROR, 'a, b')", { key: "severity_text", op: "not_in", values: ["ERROR", "a, b"] }],
    ["severity_text not_in (DEBUG)", { key: "severity_text", op: "not_in", values: ["DEBUG"] }],
    ["k8s.pod.name exists", { key: "k8s.pod.name", op: "exists" }],
    ["k8s.pod.name NOT EXISTS", { key: "k8s.pod.name", op: "not_exists" }],
    ["k8s.pod.name is null", { key: "k8s.pod.name", op: "not_exists" }],
    ['body contains "timeout"', { key: "body", op: "contains", value: "timeout" }],
    ['body not contains "connection reset"', { key: "body", op: "not_contains", value: "connection reset" }],
    ["body contains connection reset", { key: "body", op: "contains", value: "connection reset" }],
    ["url like /api/%", { key: "url", op: "like", value: "/api/%" }],
    ["url NOT LIKE '%health%'", { key: "url", op: "not_like", value: "%health%" }],
    ["user.agent =~ ^curl", { key: "user.agent", op: "regex", value: "^curl" }],
    ["user.agent !~ bot$", { key: "user.agent", op: "not_regex", value: "bot$" }],
    ['msg = "say \\"hi\\""', { key: "msg", op: "=", value: 'say "hi"' }],
    ["`weird key` = x", { key: "weird key", op: "=", value: "x" }],
    ["attributes.http.route = /checkout", { key: "attributes.http.route", op: "=", value: "/checkout" }],
  ])("%s", (text, expected) => {
    expect(parseCondition(text)).toEqual(expected);
  });

  it.each(["timeout", "service.name", "service.name =", "a in ()", "a in (x", 'a = "unterminated', "k8s.pod.name exists now", "= x", "a = \"x\" trailing", "keyinside x"])(
    "rejects %s",
    (text) => {
      expect(parseCondition(text)).toBeNull();
    },
  );

  it("enforces value limits", () => {
    expect(parseCondition(`a = ${"x".repeat(QB_LIMITS.valueBytes + 1)}`)).toBeNull();
    expect(parseCondition(`a in (${Array.from({ length: QB_LIMITS.values + 1 }, (_, i) => i).join(",")})`)).toBeNull();
  });
});

describe("parseFilterText", () => {
  it("splits top-level AND into several conditions", () => {
    expect(parseFilterText('service.name=api AND body contains "a and b" and x exists')).toEqual({
      kind: "filters",
      filters: [
        { key: "service.name", op: "=", value: "api" },
        { key: "body", op: "contains", value: "a and b" },
        { key: "x", op: "exists" },
      ],
    });
  });

  it("falls back to a body search when anything does not parse", () => {
    expect(parseFilterText("connection timeout")).toEqual({ kind: "text", q: "connection timeout" });
    expect(parseFilterText('"a=b"')).toEqual({ kind: "text", q: "a=b" });
    expect(parseFilterText("service.name=api AND oops")).toEqual({ kind: "text", q: "service.name=api AND oops" });
    expect(parseFilterText("   ")).toBeNull();
  });
});

describe("formatFilter", () => {
  it.each<QueryFilter>([
    { key: "service.name", op: "=", value: "api" },
    { key: "severity_text", op: "in", values: ["ERROR", "WARN"] },
    { key: "severity_text", op: "not_in", values: ["a, b", 'q"x'] },
    { key: "http.status_code", op: ">=", value: 500 },
    { key: "k8s.pod.name", op: "not_exists" },
    { key: "body", op: "contains", value: "connection reset" },
    { key: "weird key", op: "!=", value: "" },
    { key: "u", op: "not_regex", value: "^a(b)$" },
  ])("round-trips %j", (f) => {
    expect(parseCondition(formatFilter(f))).toEqual(f);
  });

  it("reads like the query language", () => {
    expect(formatFilter({ key: "severity_text", op: "in", values: ["ERROR", "WARN"] })).toBe("severity_text IN (ERROR, WARN)");
    expect(formatFilter({ key: "body", op: "contains", value: "time out" })).toBe('body CONTAINS "time out"');
  });
});

describe("opsForType and makeFilter", () => {
  it("offers numeric comparisons only for numbers and text operators only for strings", () => {
    expect(opsForType("number")).toContain(">=");
    expect(opsForType("number")).not.toContain("contains");
    expect(opsForType("string")).toContain("regex");
    expect(opsForType("string")).not.toContain(">");
    expect(opsForType("bool")).toEqual(["=", "!=", "in", "not_in", "exists", "not_exists"]);
    expect(opsForType(undefined)).toHaveLength(16);
  });

  it("builds values per operator", () => {
    expect(makeFilter("a", ">", ["1.5"])).toEqual({ key: "a", op: ">", value: 1.5 });
    expect(makeFilter("a", "=", ["1.5"])).toEqual({ key: "a", op: "=", value: "1.5" });
    expect(makeFilter("a", "in", ["x", "y"])).toEqual({ key: "a", op: "in", values: ["x", "y"] });
    expect(makeFilter("a", "exists", ["ignored"])).toEqual({ key: "a", op: "exists" });
  });
});

describe("groups", () => {
  const a: QueryFilter = { key: "a", op: "=", value: "1" };
  const b: QueryFilter = { key: "b", op: "=", value: "2" };
  const c: QueryFilter = { key: "c", op: "exists" };

  it("turns the current filters into the first OR alternative", () => {
    const s = addOrGroup({ filters: [a, b], groups: [], q: "x" }, [c]);
    expect(s).toEqual({ filters: [], groups: [[a, b], [c]], q: "x" });
    expect(addOrGroup(s, [a]).groups).toHaveLength(3);
    expect(conditionCount(s)).toBe(3);
  });

  it("normalizes a single group into filters and drops empty groups", () => {
    expect(normalizeFilterState({ filters: [a], groups: [[], [b], []], q: "" })).toEqual({ filters: [a, b], groups: [], q: "" });
    expect(addOrGroup({ filters: [], groups: [], q: "" }, [c])).toEqual({ filters: [c], groups: [], q: "" });
  });
});

describe("URL encoding", () => {
  it("round-trips through the compact form", () => {
    const s = { filters: [{ key: "x", op: "exists" as const }], groups: [[{ key: "a", op: "in" as const, values: ["1", 2] }], [{ key: "b", op: ">" as const, value: 3 }]], q: "boom" };
    const enc = encodeFilterState(s)!;
    expect(enc).toEqual({ f: [["x", "exists"]], g: [[["a", "in", ["1", 2]]], [["b", ">", 3]]], q: "boom" });
    expect(decodeFilterState(enc)).toEqual(s);
    expect(decodeFilterState(JSON.stringify(enc))).toEqual(s);
    expect(encodeFilterState({ filters: [], groups: [[]], q: " " })).toBeUndefined();
  });

  it("accepts the API shape (saved views)", () => {
    expect(decodeFilterState({ filters: [{ key: "a", op: "=", value: "1" }], groups: [], q: "z" })).toEqual({ filters: [{ key: "a", op: "=", value: "1" }], groups: [], q: "z" });
  });

  it("drops invalid input instead of failing", () => {
    expect(decodeFilterState("{not json")).toEqual({ filters: [], groups: [], q: "" });
    expect(decodeFilterState([1, 2])).toEqual({ filters: [], groups: [], q: "" });
    expect(decodeFilterState({ f: [["a", "bogus", "1"], ["", "=", "1"], ["b", "in", []], ["c", "=", { x: 1 }], ["d", "=", "ok"]], q: 5 })).toEqual({
      filters: [{ key: "d", op: "=", value: "ok" }],
      groups: [],
      q: "",
    });
    expect(sanitizeFilter({ key: "k", op: "in", value: "one" })).toEqual({ key: "k", op: "in", values: ["one"] });
    expect(sanitizeFilter(["k", ">", Number.NaN])).toBeNull();
  });

  it("enforces the condition and group limits", () => {
    const many = Array.from({ length: 80 }, (_, i) => ["k", "=", String(i)]);
    expect(decodeFilterState({ f: many }).filters).toHaveLength(QB_LIMITS.conditions);
    const groups = Array.from({ length: 15 }, () => [["k", "exists"]]);
    expect(decodeFilterState({ g: groups }).groups).toHaveLength(QB_LIMITS.groups);
  });
});
