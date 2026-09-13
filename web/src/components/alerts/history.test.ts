import { describe, expect, it } from "vitest";
import type { AlertRuleEvaluations } from "@/api/alerts";
import { historyChartData } from "@/lib/alert-history";
import { changeType, draftToInput, emptyDraft, validateDraft } from "@/lib/alerts";

describe("apm_no_data drafts", () => {
  it("converts to the condition of alerting.md §2.7", () => {
    const d = { ...changeType(emptyDraft("apm"), "apm_no_data"), name: "checkout silent", service_name: " checkout ", group_by: ["environment"] };
    expect(d.window_seconds).toBe(600);
    expect(draftToInput(d).condition).toEqual({ service_name: "checkout", environment: null, group_by: ["environment"], window_seconds: 600, lookback_seconds: 86400 });
    expect(validateDraft(d)).toEqual({});
    expect(validateDraft({ ...d, lookback_seconds: 600 }).lookback_seconds?.key).toBe("range");
  });
});

describe("historyChartData", () => {
  const h: AlertRuleEvaluations = {
    from: "2026-09-13T09:00:00.000000000Z",
    to: "2026-09-13T10:00:00.000000000Z",
    step_seconds: 60,
    truncated: false,
    evaluations: [
      { at: "2026-09-13T09:01:00Z", firing_series: 0, evaluations: 6, errors: 0, duration_ms: 4, max_duration_ms: 9 },
      { at: "2026-09-13T09:02:00Z", firing_series: 1, evaluations: 6, errors: 1, duration_ms: 5, max_duration_ms: 7 },
      { at: "2026-09-13T09:03:00Z", firing_series: 1, evaluations: 6, errors: 0, duration_ms: 6, max_duration_ms: 6 },
    ],
    series: [{ series_key: "host.id=a", labels: { "host.id": "a", "host.name": "web-1" }, points: [[1, 0.9, "firing"], [2, null, "ok"]] }],
  };

  it("merges consecutive firing buckets into bands and drops null points", () => {
    const d = historyChartData(h, "value");
    expect(d.series).toEqual([{ label: "web-1", points: [[1, 0.9]] }]);
    expect(d.bands).toEqual([{ from: Date.parse("2026-09-13T09:01:00Z"), to: Date.parse("2026-09-13T09:03:00Z") }]);
    expect(d.evaluations).toBe(18);
    expect(d.errors).toBe(1);
    expect(d.lastDurationMs).toBe(6);
  });
});
