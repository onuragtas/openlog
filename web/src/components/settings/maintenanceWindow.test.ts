import { describe, expect, it } from "vitest";
import { formatDays, formatWindowsLocal, formatWindowsUtc } from "./maintenanceWindow";

describe("maintenance window formatting", () => {
  it("formats days and ranges", () => {
    expect(formatDays(["sat", "sun"])).toBe("Sat, Sun");
    expect(formatDays(["mon", "tue", "wed", "thu", "fri"])).toBe("Mon–Fri");
    expect(formatDays(["sat", "sun", "mon"])).toBe("Sat–Mon");
    expect(formatDays(["wed", "mon"])).toBe("Mon, Wed");
    expect(formatDays([])).toBe("Every day");
    expect(
      formatWindowsUtc([
        { days: ["sat", "sun"], start: "02:00", end: "05:00" },
        { days: [], start: "23:30", end: "00:30" },
      ]),
    ).toBe("Sat, Sun 02:00–05:00; Every day 23:30–00:30");
  });

  it("shifts windows to local time, including the day", () => {
    const w = [{ days: ["sat", "sun"] as ("sat" | "sun")[], start: "02:00", end: "05:00" }];
    expect(formatWindowsLocal(w, 0)).toBeNull();
    expect(formatWindowsLocal(w, 180)).toBe("Sat, Sun 05:00–08:00");
    expect(formatWindowsLocal(w, -180)).toBe("Fri, Sat 23:00–02:00");
    expect(formatWindowsLocal([{ days: ["fri"], start: "22:30", end: "24:00" }], 120)).toBe("Sat 00:30–02:00");
    expect(formatWindowsLocal([{ days: [], start: "01:00", end: "02:00" }], 330)).toBe("Every day 06:30–07:30");
  });
});
