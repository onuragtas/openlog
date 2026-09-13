import { describe, expect, it } from "vitest";
import { axisTickLabels } from "./chart-axis";

const at = (iso: string) => Date.parse(iso) / 1000;

describe("axisTickLabels", () => {
  it("uses 24h time for tr and the locale's convention for en", () => {
    const splits = [at("2026-09-13T09:20:00Z"), at("2026-09-13T21:30:00Z")];
    expect(axisTickLabels(splits, 600, "tr", "UTC")).toEqual(["09:20", "21:30"]);
    const en = axisTickLabels(splits, 600, "en-US", "UTC");
    expect(en[0]).toMatch(/^09:20\sAM$/u);
    expect(en[1]).toMatch(/^09:30\sPM$/u);
  });

  it("adds the date to the first tick only when ticks span days", () => {
    const sameDay = axisTickLabels([at("2026-09-13T09:00:00Z"), at("2026-09-13T10:00:00Z")], 3600, "tr", "UTC");
    expect(sameDay.every((l) => !l.includes("\n"))).toBe(true);

    const labels = axisTickLabels([at("2026-09-13T22:00:00Z"), at("2026-09-14T00:00:00Z"), at("2026-09-14T02:00:00Z")], 7200, "en-US", "UTC");
    expect(labels[0]).toContain("\nSep 13");
    expect(labels.slice(1).every((l) => !l.includes("\n"))).toBe(true);
  });

  it("shows seconds for sub-minute increments and dates for day increments", () => {
    expect(axisTickLabels([at("2026-09-13T09:20:15Z")], 15, "tr", "UTC")).toEqual(["09:20:15"]);
    expect(axisTickLabels([at("2026-09-13T00:00:00Z"), at("2026-09-14T00:00:00Z")], 86_400, "en-US", "UTC")).toEqual(["Sep 13", "Sep 14"]);
  });

  it("handles no ticks", () => expect(axisTickLabels([], 60, "en")).toEqual([]));
});
