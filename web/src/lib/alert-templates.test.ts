import { describe, expect, it } from "vitest";
import type { AlertTemplate, AlertTemplateParam } from "@/api/alerts";
import { buildParams, editableParams, groupTemplates, initialValues, targetParams, templateLanguage, templateText, templateUsable } from "./alert-templates";

const label = { en: "x", tr: "x" };
const p = (key: string, kind: AlertTemplateParam["kind"], extra: Partial<AlertTemplateParam> = {}): AlertTemplateParam => ({ key, kind, required: false, default: "", label, ...extra });

const redisMemory: AlertTemplate = {
  id: "redis_memory_high",
  category: "integration",
  integration: "redis",
  rule_type: "metric_threshold",
  severity: "critical",
  reference_metric: "redis.maxmemory",
  name: { en: "Redis memory near maxmemory", tr: "Redis belleği maxmemory sınırına yakın" },
  description: label,
  params: [
    p("host_id", "host"),
    p("host_name", "text"),
    p("discovery_id", "text"),
    p("instance", "instance"),
    p("ratio", "number", { unit: "ratio", required: true, default: 0.9, min: 0.01, max: 1 }),
    p("window_seconds", "duration", { unit: "seconds", required: true, default: 300, min: 60, max: 21600 }),
  ],
};

describe("alert templates", () => {
  it("picks texts by language with English fallback", () => {
    expect(templateLanguage("tr-TR")).toBe("tr");
    expect(templateLanguage(undefined)).toBe("en");
    expect(templateText(redisMemory.name, "tr")).toBe("Redis belleği maxmemory sınırına yakın");
    expect(templateText({ en: "only en", tr: "" }, "tr")).toBe("only en");
  });

  it("fixes target parameters and hides helper keys", () => {
    const target = { hostId: "h1", hostName: "web-1", discoveryId: "redis", instance: "/usr/bin/redis-server" };
    expect(targetParams(redisMemory, target)).toEqual({ host_id: "h1", host_name: "web-1", discovery_id: "redis", instance: "/usr/bin/redis-server" });
    expect(editableParams(redisMemory, target).map((x) => x.key)).toEqual(["ratio", "window_seconds"]);
    // Without an instance the instance picker is hidden (fleet mode); the host stays editable.
    expect(editableParams(redisMemory, {}).map((x) => x.key)).toEqual(["host_id", "ratio", "window_seconds"]);
    expect(templateUsable(redisMemory, target)).toBe(true);
    expect(templateUsable(redisMemory, { hostId: "h1" })).toBe(false);
  });

  it("edits ratios as percent and validates ranges", () => {
    const params = editableParams(redisMemory, { instance: "i", hostId: "h", discoveryId: "d" });
    const values = initialValues(params);
    expect(values).toEqual({ ratio: "90", window_seconds: "300" });
    expect(buildParams(params, values)).toEqual({ params: { ratio: 0.9, window_seconds: 300 }, errors: {} });
    expect(buildParams(params, { ratio: "85,5", window_seconds: "600" }).params).toEqual({ ratio: 0.855, window_seconds: 600 });
    const bad = buildParams(params, { ratio: "150", window_seconds: "" });
    expect(bad.errors).toEqual({ ratio: { key: "range", min: 1, max: 100 }, window_seconds: { key: "required" } });
    expect(buildParams(params, { ratio: "abc", window_seconds: "60" }).errors.ratio).toEqual({ key: "number" });
  });

  it("groups by category and integration in catalog order", () => {
    const host = { ...redisMemory, id: "host_cpu_high", category: "host" as const, integration: undefined };
    const nginx = { ...redisMemory, id: "nginx_no_requests", integration: "nginx" };
    expect(groupTemplates([host, redisMemory, nginx, { ...redisMemory, id: "redis_evicted_keys" }]).map((g) => `${g.key}:${g.templates.length}`)).toEqual([
      "host:1",
      "integration:redis:2",
      "integration:nginx:1",
    ]);
  });
});
