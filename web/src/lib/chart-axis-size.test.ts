import { describe, expect, it } from "vitest";
import { axisLabelWidth } from "./chart-axis";

describe("axisLabelWidth", () => {
  const measure = (s: string) => s.length * 6;

  it("fits the widest formatted label", () => {
    expect(axisLabelWidth(["0 B", "476.8 MiB", "953.7 MiB"], measure)).toBe(54);
  });

  it("measures each line of multi-line labels and skips empty values", () => {
    expect(axisLabelWidth(["12:00\n13 Sep", null, "", undefined], measure)).toBe(36);
    expect(axisLabelWidth(null, measure)).toBe(0);
  });
});
