import { describe, expect, it } from "vitest";
import { allFiltersIgnored, filtersForFacets, MAX_FILTERS, removeFilter, sanitizeFiltersSearch, toggleFilters } from "./dashboard-filters";

describe("sanitizeFiltersSearch", () => {
  it("accepts JSON strings and arrays of valid filters", () => {
    expect(sanitizeFiltersSearch('[{"attribute":"host.name","value":"web 1","event_type":"Log"}]')).toEqual([{ attribute: "host.name", value: "web 1", event_type: "Log" }]);
    expect(sanitizeFiltersSearch([{ attribute: "attributes['http.route']", value: 200 }])).toEqual([{ attribute: "attributes['http.route']", value: "200" }]);
    expect(sanitizeFiltersSearch([{ attribute: "`weird name`", value: "" }])).toEqual([{ attribute: "`weird name`", value: "" }]);
  });

  it("drops invalid, duplicate and excess entries", () => {
    expect(sanitizeFiltersSearch("not json")).toBeUndefined();
    expect(sanitizeFiltersSearch({ attribute: "a", value: "b" })).toBeUndefined();
    expect(
      sanitizeFiltersSearch([
        { attribute: "host.name OR 1=1", value: "x" },
        { attribute: "host.name", value: "a\nb" },
        { attribute: "host.name", value: "x", event_type: "Log; DROP" },
        { attribute: "host.name", value: "x" },
        null,
      ]),
    ).toEqual([{ attribute: "host.name", value: "x" }]);
    const many = Array.from({ length: 15 }, (_, i) => ({ attribute: "host.name", value: `h${i}` }));
    expect(sanitizeFiltersSearch(many)).toHaveLength(MAX_FILTERS);
  });
});

describe("toggleFilters", () => {
  const a = { attribute: "host.name", value: "web-1" };
  const b = { attribute: "service.name", value: "checkout" };
  it("adds the facets of a row and removes them when clicked again", () => {
    const one = toggleFilters(undefined, [a]);
    expect(one).toEqual([a]);
    const two = toggleFilters(one, [a, b]);
    expect(two).toEqual([a, b]);
    expect(toggleFilters(two, [a, b])).toBeUndefined();
    expect(toggleFilters(two, [b])).toEqual([a]);
  });

  it("keeps the newest filters within the limit and ignores invalid ones", () => {
    let list: ReturnType<typeof toggleFilters>;
    for (let i = 0; i < 12; i++) list = toggleFilters(list, [{ attribute: "host.name", value: `h${i}` }]);
    expect(list).toHaveLength(MAX_FILTERS);
    expect(list![0]!.value).toBe("h2");
    expect(toggleFilters([a], [{ attribute: "bad attr!", value: "x" }])).toEqual([a]);
  });

  it("removes one filter by index", () => {
    expect(removeFilter([a, b], 0)).toEqual([b]);
    expect(removeFilter([a], 0)).toBeUndefined();
  });
});

describe("filtersForFacets", () => {
  it("pairs facet names with the clicked values", () => {
    expect(filtersForFacets(["host.name", "severity"], ["web-1", "ERROR"], "Log")).toEqual([
      { attribute: "host.name", value: "web-1", event_type: "Log" },
      { attribute: "severity", value: "ERROR", event_type: "Log" },
    ]);
    expect(filtersForFacets(["host.name"], [])).toEqual([]);
  });

  it("detects widgets the filters do not apply to", () => {
    expect(allFiltersIgnored([{ attribute: "host.name", value: "x" }], ["host.name"])).toBe(true);
    expect(allFiltersIgnored([{ attribute: "host.name", value: "x" }, { attribute: "severity", value: "E" }], ["host.name"])).toBe(false);
    expect(allFiltersIgnored(undefined, ["host.name"])).toBe(false);
  });
});
