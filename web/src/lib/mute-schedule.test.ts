import { describe, expect, it } from "vitest";
import { monthlyRRule, monthlySummary, normalizeDates, parseCalendarDates, parseMonthDays, parseMonthlyRRule } from "./mute-schedule";

describe("mute schedules", () => {
  it("parses day-of-month lists", () => {
    expect(parseMonthDays("15, 1 -1,1")).toEqual([-1, 1, 15]);
    expect(parseMonthDays("")).toBeNull();
    expect(parseMonthDays("0")).toBeNull();
    expect(parseMonthDays("32")).toBeNull();
    expect(parseMonthDays("1st")).toBeNull();
  });

  it("builds monthly rules the API accepts", () => {
    expect(monthlyRRule({ mode: "monthday", monthDays: "1, 15", ordinal: 1, day: "mon" })).toBe("FREQ=MONTHLY;BYMONTHDAY=1,15");
    expect(monthlyRRule({ mode: "monthday", monthDays: "x", ordinal: 1, day: "mon" })).toBeNull();
    expect(monthlyRRule({ mode: "weekday", monthDays: "", ordinal: -1, day: "fri" })).toBe("FREQ=MONTHLY;BYDAY=-1FR");
    expect(monthlyRRule({ mode: "weekday", monthDays: "", ordinal: 1, day: "weekday" })).toBe("FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1");
  });

  it("round-trips stored rules and rejects shapes the form cannot edit", () => {
    for (const r of ["FREQ=MONTHLY;BYMONTHDAY=-1,1,15", "FREQ=MONTHLY;BYDAY=2TU", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1"]) {
      expect(monthlyRRule(parseMonthlyRRule(r)!)).toBe(r);
    }
    expect(parseMonthlyRRule("FREQ=WEEKLY;BYDAY=MO")).toBeNull();
    expect(parseMonthlyRRule("FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13")).toBeNull();
    expect(parseMonthlyRRule("FREQ=MONTHLY;BYDAY=5MO")).toBeNull();
    expect(parseMonthlyRRule(null)).toBeNull();
    expect(monthlySummary("FREQ=MONTHLY;BYDAY=-1FR")).toEqual({ key: "nthDay", ordinal: -1, day: "fri" });
    expect(monthlySummary("FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1")).toEqual({ key: "nthWeekday", ordinal: 1 });
  });

  it("normalizes exception and calendar dates", () => {
    expect(normalizeDates(["2026-10-29", "20261029", "2026-02-30", " ", "2026-01-01"])).toEqual({ dates: ["2026-01-01", "2026-10-29"], invalid: ["2026-02-30"] });
    expect(parseCalendarDates("01-01\n--04-23, 2026-05-19\n13-01\n02-29")).toEqual({ dates: ["01-01", "02-29", "04-23", "2026-05-19"], invalid: ["13-01"] });
  });
});
