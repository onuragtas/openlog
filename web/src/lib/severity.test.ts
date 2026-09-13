import { describe, expect, it } from "vitest";
import { severityLabel } from "./severity";

describe("severityLabel", () => {
  it("prefers the severity text", () => {
    expect(severityLabel("ERROR", 17)).toBe("ERROR");
    expect(severityLabel("warning", 0)).toBe("warning");
  });

  it("names the OpenTelemetry number range when there is no text", () => {
    expect(severityLabel("", 1)).toBe("TRACE");
    expect(severityLabel("", 9)).toBe("INFO");
    expect(severityLabel(null, 10)).toBe("INFO2");
    expect(severityLabel(undefined, 13)).toBe("WARN");
    expect(severityLabel(" ", 24)).toBe("FATAL4");
  });

  it("has no label for unspecified severity (number 0) or out-of-range numbers", () => {
    expect(severityLabel("", 0)).toBeNull();
    expect(severityLabel("", 25)).toBeNull();
    expect(severityLabel("", -1)).toBeNull();
  });
});
