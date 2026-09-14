import { describe, expect, it } from "vitest";
import { dayTone, formatUptime, statusTone, visibleDays } from "./status-page";

describe("status page helpers", () => {
  it("formats uptime without rounding up to 100%", () => {
    expect(formatUptime(100)).toBe("100%");
    expect(formatUptime(99.999)).toBe("99.99%");
    expect(formatUptime(98.5)).toBe("98.50%");
    expect(formatUptime(null)).toBe("—");
    expect(formatUptime(99.5, "tr")).toBe("99,50%");
  });

  it("maps statuses to tones", () => {
    expect(statusTone("major_outage").dot).toBe("bg-red-600");
    expect(statusTone("unknown").dot).toBe("bg-muted-foreground");
    expect(dayTone("no_data")).toBe("bg-muted");
    expect(dayTone("outage")).toBe("bg-red-600");
  });

  it("keeps the most recent days on narrow screens", () => {
    const days = Array.from({ length: 90 }, (_, i) => i);
    expect(visibleDays(days, false)).toHaveLength(90);
    expect(visibleDays(days, true)).toEqual(days.slice(60));
  });
});
