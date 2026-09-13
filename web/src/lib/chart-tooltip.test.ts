import { describe, expect, it } from "vitest";
import { placeTooltip, TOUCH_MOUSE_SUPPRESS_MS, TOUCH_TOOLTIP_IDLE_MS, TouchTooltipState } from "./chart-tooltip";

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

describe("TouchTooltipState", () => {
  it("opens on tap and toggles closed when the same point is tapped again", () => {
    const s = new TouchTooltipState();
    expect(s.down(5, 100, 0)).toBe(true);
    expect(s.openIdx).toBe(5);
    expect(s.down(5, 101, 500)).toBe(false);
    expect(s.openIdx).toBeNull();
    expect(s.down(5, 100, 900)).toBe(true);
  });

  it("moves to another point on tap or horizontal drag, ignoring tap jitter", () => {
    const s = new TouchTooltipState();
    s.down(5, 100, 0);
    expect(s.down(9, 180, 100)).toBe(true);
    expect(s.openIdx).toBe(9);
    expect(s.move(9, 183, 120)).toBeUndefined();
    expect(s.move(12, 230, 140)).toBe(12);
    expect(s.openIdx).toBe(12);
    // A toggle-closing tap with a little jitter stays closed.
    s.down(12, 230, 1000);
    expect(s.move(12, 232, 1010)).toBeUndefined();
    expect(s.openIdx).toBeNull();
  });

  it("does not open on a tap without a data point", () => {
    const s = new TouchTooltipState();
    expect(s.down(null, 10, 0)).toBe(false);
  });

  it("closes (outside tap, scroll) and reports whether it was open", () => {
    const s = new TouchTooltipState();
    expect(s.close()).toBe(false);
    s.down(1, 10, 0);
    expect(s.close()).toBe(true);
    expect(s.openIdx).toBeNull();
  });

  it("expires after the idle timeout and suppresses compatibility mouse events right after a touch", () => {
    const s = new TouchTooltipState();
    expect(s.suppressMouse(0)).toBe(false);
    s.down(1, 10, 1000);
    expect(s.suppressMouse(1000 + TOUCH_MOUSE_SUPPRESS_MS - 1)).toBe(true);
    expect(s.suppressMouse(1000 + TOUCH_MOUSE_SUPPRESS_MS)).toBe(false);
    expect(TOUCH_TOOLTIP_IDLE_MS).toBe(4000);
    expect(s.idle(1000 + TOUCH_TOOLTIP_IDLE_MS - 1)).toBe(false);
    s.move(2, 60, 3000); // dragging keeps it alive
    expect(s.idle(1000 + TOUCH_TOOLTIP_IDLE_MS)).toBe(false);
    expect(s.idle(3000 + TOUCH_TOOLTIP_IDLE_MS)).toBe(true);
  });
});
