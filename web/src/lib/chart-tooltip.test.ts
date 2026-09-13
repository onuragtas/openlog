import { describe, expect, it } from "vitest";
import { placeTooltip } from "./chart-tooltip";

const base = { width: 100, height: 60, areaWidth: 400, areaHeight: 200 };

describe("placeTooltip", () => {
  it("sits right of the cursor when there is room", () => {
    expect(placeTooltip({ ...base, x: 50, y: 40 })).toEqual({ left: 62, top: 30 });
  });

  it("flips left near the right edge", () => {
    expect(placeTooltip({ ...base, x: 350, y: 40 })).toEqual({ left: 238, top: 30 });
  });

  it("stays inside a plot narrower than the tooltip, using the axis room", () => {
    expect(placeTooltip({ ...base, width: 150, areaWidth: 220, x: 60, y: 0 })).toEqual({ left: 0, top: 0 });
    expect(placeTooltip({ ...base, width: 150, areaWidth: 220, x: 60, y: 0, leftRoom: 64 })).toEqual({ left: -64, top: 0 });
    expect(placeTooltip({ ...base, width: 400, areaWidth: 220, x: 100, y: 0, leftRoom: 64 })).toEqual({ left: -64, top: 0 });
  });

  it("keeps the tooltip above the bottom edge", () => {
    expect(placeTooltip({ ...base, x: 50, y: 190 })).toEqual({ left: 62, top: 140 });
  });
});
