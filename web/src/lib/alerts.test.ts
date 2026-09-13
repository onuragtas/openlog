import { describe, expect, it } from "vitest";
import type { AlertRule, AlertRulePreview } from "@/api/alerts";
import {
  applyPrefill,
  canEditOwned,
  changeType,
  createAlertSearch,
  draftFromRule,
  draftToInput,
  emptyDraft,
  formatDurationShort,
  hasErrors,
  joinDuration,
  previewChartData,
  splitDuration,
  unitKindFor,
  validateDraft,
} from "./alerts";

describe("durations", () => {
  it("splits into the largest even unit and joins back", () => {
    expect(splitDuration(300)).toEqual({ value: 5, unit: "m" });
    expect(splitDuration(7200)).toEqual({ value: 2, unit: "h" });
    expect(splitDuration(90)).toEqual({ value: 90, unit: "s" });
    expect(splitDuration(0)).toEqual({ value: 0, unit: "m" });
    expect(joinDuration(5, "m")).toBe(300);
    expect(joinDuration(1.5, "h")).toBe(5400);
    expect(formatDurationShort(90)).toBe("1m 30s");
    expect(formatDurationShort(86400)).toBe("1d");
  });
});

describe("drafts", () => {
  it("converts a metric draft to the API input", () => {
    const d = emptyDraft();
    d.name = " High CPU ";
    d.metric = "system.cpu.utilization";
    d.threshold = "0,9";
    d.recovery_threshold = "0.8";
    d.series_aggregation = "sum";
    d.filters = [
      { field: "attr.cpu.mode", op: "not_in", values: "idle, steal" },
      { field: "host.name", op: "eq", values: "web-1, ignored" },
      { field: "", op: "eq", values: "x" },
    ];
    d.labels = [{ key: "team", value: "infra" }, { key: "", value: "dropped" }];
    const input = draftToInput(d);
    expect(input.name).toBe("High CPU");
    expect(input.condition).toMatchObject({
      metric: "system.cpu.utilization", threshold: 0.9, recovery_threshold: 0.8, series_aggregation: "sum", group_by: ["host"],
      filters: [{ field: "attr.cpu.mode", op: "not_in", values: ["idle", "steal"] }, { field: "host.name", op: "eq", values: ["web-1"] }],
    });
    expect(input.labels).toEqual({ team: "infra" });
    expect(input.condition.recovery_threshold).toBe(0.8);
  });

  it("round-trips a stored rule and switches types", () => {
    const rule = {
      id: "r1", name: "Errors", description: "", type: "log_match", severity: "critical", enabled: true, interval_seconds: 60,
      for_seconds: 120, recovery_for_seconds: 0, condition: { query: "timeout", severity_min: "ERROR", window_seconds: 300, operator: "gte", threshold: 5, recovery_threshold: null, group_by: ["host"], filters: [] },
      channel_ids: ["c1"], renotify_interval_seconds: 0, flapping: { enabled: true, transitions: 4, window_seconds: 3600, hold_seconds: 600 },
      runbook_url: "", labels: { team: "api" }, version: 3, created_by_user_id: "u1", created_by_email: "a@b", created_at: "", updated_at: "",
      status: { state: "ok", series_pending: 0, series_firing: 0, open_incidents: 0, last_evaluated_at: null, last_result: "", last_error: "", last_duration_ms: 0, next_evaluation_at: null, owner: null },
    } as unknown as AlertRule;
    const d = draftFromRule(rule);
    expect(d.threshold).toBe("5");
    expect(d.recovery_threshold).toBe("");
    const input = draftToInput(d);
    expect(input).toMatchObject({ type: "log_match", version: 3, for_seconds: 120, condition: { query: "timeout", severity_min: "ERROR", recovery_threshold: null } });
    const disc = changeType(d, "discovery");
    expect(disc.name).toBe("Errors");
    expect(disc.for_seconds).toBe(0);
    expect(disc.interval_seconds).toBe(300);
    expect(draftToInput(disc).condition).toEqual({ event: "service_disappeared", filters: [], match: "", window_seconds: 900, lookback_seconds: 86400 });
  });

  it("validates like the server", () => {
    const d = emptyDraft();
    expect(validateDraft(d)).toMatchObject({ name: { key: "required" }, metric: { key: "required" }, threshold: { key: "required" } });
    d.name = "x";
    d.metric = "m";
    d.threshold = "abc";
    expect(validateDraft(d).threshold).toEqual({ key: "number" });
    d.threshold = "0.9";
    d.recovery_threshold = "0.95";
    expect(validateDraft(d).recovery_threshold).toEqual({ key: "recoverySide" });
    d.recovery_threshold = "0.8";
    d.interval_seconds = 5;
    expect(validateDraft(d).interval_seconds).toEqual({ key: "range", params: { min: 10, max: 3600 } });
    d.interval_seconds = 60;
    d.renotify_interval_seconds = 60;
    d.labels = [{ key: "bad key", value: "v" }];
    d.filters = [{ field: "attr.cpu.mode", op: "in", values: " , " }];
    const e = validateDraft(d);
    expect(e.renotify_interval_seconds?.key).toBe("renotify");
    expect(e["labels.0"]?.key).toBe("labelKey");
    expect(e["filters.0"]?.key).toBe("filterValues");
    d.renotify_interval_seconds = 0;
    d.labels = [];
    d.filters = [];
    expect(hasErrors(validateDraft(d))).toBe(false);
    const apm = emptyDraft("apm");
    apm.name = "latency";
    apm.threshold = "500";
    expect(validateDraft(apm).service_name).toEqual({ key: "required" });
  });
});

describe("prefill from host charts", () => {
  it("builds CPU busy-time alerts for one host", () => {
    const s = createAlertSearch({ metric: "system.cpu.utilization", hostId: "h-1", hostName: "web-1", agg: "avg" });
    expect(s).toMatchObject({ exclude: "attr.cpu.mode=idle", seriesAgg: "sum", groupBy: "host" });
    const d = applyPrefill(s);
    expect(d).toMatchObject({ type: "metric_threshold", metric: "system.cpu.utilization", aggregation: "avg", series_aggregation: "sum", name: "system.cpu.utilization on web-1" });
    expect(d.filters).toEqual([{ field: "host.id", op: "eq", values: "h-1" }, { field: "attr.cpu.mode", op: "not_in", values: "idle" }]);
    const other = applyPrefill(createAlertSearch({ metric: "system.disk.io", hostId: "h-2", agg: "rate" }));
    expect(other.filters).toHaveLength(1);
    expect(other.series_aggregation).toBe("");
    expect(applyPrefill({ type: "bogus", agg: "median" })).toMatchObject({ type: "metric_threshold", aggregation: "avg" });
  });
});

describe("permissions", () => {
  it("lets members edit only their own rules", () => {
    expect(canEditOwned("admin", "someone", "me")).toBe(true);
    expect(canEditOwned("member", "me", "me")).toBe(true);
    expect(canEditOwned("member", "someone", "me")).toBe(false);
    expect(canEditOwned("member", null, "me")).toBe(false);
    expect(canEditOwned("viewer", "me", "me")).toBe(false);
  });
});

describe("preview chart data", () => {
  const preview: AlertRulePreview = {
    from: "2026-09-13T09:00:00.000000000Z", to: "2026-09-13T10:00:00.000000000Z", step_seconds: 60, operator: "gt", threshold: 0.9,
    recovery_threshold: 0.8, unit: "1", truncated: false, approximate: false,
    series: [
      { key: "host.id=b", labels: { "host.id": "b", "host.name": "db-1" }, points: [[1, 0.2], [2, null]], transitions: [], incidents: [] },
      {
        key: "host.id=a", labels: { "host.id": "a", "host.name": "web-1" }, points: [[1, 0.95], [2, 0.97]],
        transitions: [{ at: "2026-09-13T09:10:00.000000000Z", state: "firing", value: 0.95 }, { at: "2026-09-13T09:20:00.000000000Z", state: "ok", value: 0.5 }],
        incidents: [{ opened_at: "2026-09-13T09:10:00.000000000Z", resolved_at: "2026-09-13T09:20:00.000000000Z", peak: 0.97 }, { opened_at: "2026-09-13T09:50:00.000000000Z", resolved_at: null, peak: 0.93 }],
      },
    ],
  };

  it("puts firing series first, drops null points and derives bands and markers", () => {
    const data = previewChartData(preview, "value", 1);
    expect(data.series).toEqual([{ label: "web-1", points: [[1, 0.95], [2, 0.97]] }]);
    expect(data.hiddenSeries).toBe(1);
    expect(data.incidents).toBe(2);
    expect(data.bands).toEqual([
      { from: Date.parse("2026-09-13T09:10:00Z"), to: Date.parse("2026-09-13T09:20:00Z") },
      { from: Date.parse("2026-09-13T09:50:00Z"), to: Date.parse("2026-09-13T10:00:00Z") },
    ]);
    expect(data.fires).toEqual([Date.parse("2026-09-13T09:10:00Z")]);
    expect(previewChartData(preview, "value").series[1]).toEqual({ label: "db-1", points: [[1, 0.2]] });
  });

  it("maps units", () => {
    expect(unitKindFor("1", "metric_threshold")).toBe("percent");
    expect(unitKindFor("1", "apm", "apdex")).toBe("number");
    expect(unitKindFor("ms", "apm", "p95_ms")).toBe("ms");
    expect(unitKindFor("{records}", "log_match")).toBe("number");
  });
});
