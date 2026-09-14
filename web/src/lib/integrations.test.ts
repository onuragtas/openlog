import { describe, expect, it } from "vitest";
import type { MetricSeries } from "@/api/types";
import { PANELS } from "@/components/integrations/panels";
import { applyPrefill, parsePrefillFilters } from "./alerts";
import {
  combinePoints,
  differencePoints,
  filterIntegrationRows,
  hasPanel,
  hitRatio,
  instanceAlertSearch,
  instanceLabel,
  instanceResourceFilter,
  integrationForRule,
  integrationOf,
  latestMax,
  pgCacheHitRatio,
  splitServiceKey,
  sumSeries,
  summarizeIntegrations,
  topByLast,
  type InstanceRef,
} from "./integrations";

const ms = (s: number) => s * 1000;
const ref: InstanceRef = { hostId: "h1", discoveryId: "redis", instance: "/usr/bin/redis-server" };

describe("instance identity", () => {
  it("splits service keys at the first colon only", () => {
    expect(splitServiceKey("redis:/usr/bin/redis-server")).toEqual({ discoveryId: "redis", instance: "/usr/bin/redis-server" });
    expect(splitServiceKey("mysql:tcp:127.0.0.1:3306")).toEqual({ discoveryId: "mysql", instance: "tcp:127.0.0.1:3306" });
    expect(splitServiceKey("nocolon")).toBeNull();
    expect(splitServiceKey(":x")).toBeNull();
  });

  it("names instances by their command, keeping the executable path secondary", () => {
    expect(instanceLabel({ command: "redis-server", instance: "/usr/bin/redis-check-rdb" })).toEqual({ primary: "redis-server", secondary: "/usr/bin/redis-check-rdb" });
    // Multi-call binaries: the invoked path replaces the resolved executable as the secondary line.
    expect(instanceLabel({ command: "redis-server", instance: "/usr/bin/redis-check-rdb", displayInstance: "/usr/bin/redis-server" })).toEqual({ primary: "redis-server", secondary: "/usr/bin/redis-server" });
    expect(instanceLabel({ command: " nginx ", instance: "nginx" })).toEqual({ primary: "nginx", secondary: undefined });
    expect(instanceLabel({ command: "redis-server" })).toEqual({ primary: "redis-server", secondary: undefined });
    // Older agents omit command: the instance stays the name.
    expect(instanceLabel({ instance: "/usr/sbin/nginx" })).toEqual({ primary: "/usr/sbin/nginx" });
    expect(instanceLabel({ command: "  ", instance: "/usr/sbin/nginx" })).toEqual({ primary: "/usr/sbin/nginx" });
    expect(instanceLabel({})).toEqual({ primary: "" });
  });

  it("filters metrics by discovery id and instance resource attributes", () => {
    expect(instanceResourceFilter(ref)).toEqual({ "openlog.discovery.id": "redis", "openlog.discovery.instance": "/usr/bin/redis-server" });
  });

  it("normalizes integration objects and decides panel availability", () => {
    expect(integrationOf(undefined)).toEqual({ status: "not_available", id: undefined, error: undefined, hint: undefined, endpoint: undefined });
    expect(integrationOf({ integration: { id: "redis", status: "weird" as never } }).status).toBe("not_available");
    expect(hasPanel({ integration: { id: "redis", status: "needs_configuration" } })).toBe(true);
    // Docker's panel shows engine reachability (no metrics).
    expect(hasPanel({ integration: { id: "docker", status: "enabled" } })).toBe(true);
    expect(hasPanel({ integration: { id: "docker", status: "not_available" } })).toBe(false);
    expect(hasPanel({ integration: { status: "not_available" } })).toBe(false);
    expect(integrationForRule("mariadb")).toBe("mysql");
    expect(integrationForRule("postgresql")).toBe("postgresql");
    expect(integrationForRule("sshd")).toBeUndefined();
  });
});

describe("point arithmetic", () => {
  it("joins on timestamps and drops unmatched or null results", () => {
    expect(combinePoints([[1, 2], [2, 4], [3, 6]], [[2, 1], [3, 0]], (a, b) => (b === 0 ? null : a / b))).toEqual([[2, 4]]);
  });

  it("computes hit ratios, skipping idle buckets", () => {
    expect(hitRatio([[ms(10), 90], [ms(20), 0], [ms(30), 3]], [[ms(10), 10], [ms(20), 0], [ms(30), 1]])).toEqual([[ms(10), 0.9], [ms(30), 0.75]]);
  });

  it("clamps differences at zero (accepted − handled, offset lag)", () => {
    expect(differencePoints([[1, 10], [2, 5]], [[1, 7], [2, 6]])).toEqual([[1, 3], [2, 0]]);
  });

  it("sums series per timestamp with an optional attribute filter", () => {
    const series: MetricSeries[] = [
      { attributes: { kind: "waits" }, points: [[2, 1], [1, 2]] },
      { attributes: { kind: "time" }, points: [[1, 100]] },
      { attributes: { kind: "waits" }, points: [[1, 3]] },
    ];
    expect(sumSeries(series)).toEqual([[1, 105], [2, 1]]);
    expect(sumSeries(series, (a) => a.kind === "waits")).toEqual([[1, 5], [2, 1]]);
  });

  it("computes the PostgreSQL cache hit ratio from blocks_read sources", () => {
    const blocks: MetricSeries[] = [
      { attributes: { source: "heap_hit" }, points: [[1, 80]] },
      { attributes: { source: "idx_hit" }, points: [[1, 15]] },
      { attributes: { source: "heap_read" }, points: [[1, 4]] },
      { attributes: { source: "toast_read" }, points: [[1, 1]] },
    ];
    expect(pgCacheHitRatio(blocks)).toEqual([[1, 0.95]]);
  });

  it("ranks series by their latest value", () => {
    const series: MetricSeries[] = [
      { attributes: { t: "a" }, points: [[1, 5], [2, 1]] },
      { attributes: { t: "b" }, points: [[1, 2], [2, 9]] },
      { attributes: { t: "c" }, points: [] },
    ];
    expect(topByLast(series, 5).map((r) => [r.labels.t, r.value])).toEqual([["b", 9], ["a", 1]]);
    expect(latestMax(series)).toBe(9);
    expect(latestMax([])).toBeNull();
  });
});

describe("panel charts", () => {
  const L = (k: string) => k;
  it("nginx accepted vs handled adds a dropped series", () => {
    const chart = PANELS.nginx.find((c) => c.id === "nginxAcceptedHandled")!;
    const out = chart.build({ a: [{ attributes: {}, points: [[1, 10], [2, 12]] }], h: [{ attributes: {}, points: [[1, 9], [2, 12]] }] }, L as never);
    expect(out.map((s) => s.label)).toEqual(["accepted", "handled", "dropped"]);
    expect(out[2]!.points).toEqual([[1, 1], [2, 0]]);
  });

  it("redis memory hides maxmemory when unlimited (0)", () => {
    const chart = PANELS.redis.find((c) => c.id === "redisMemory")!;
    const used = [{ attributes: {}, points: [[1, 5]] as [number, number][] }];
    expect(chart.build({ u: used, r: [], m: [{ attributes: {}, points: [[1, 0]] }] }, L as never).map((s) => s.label)).toEqual(["used"]);
    expect(chart.build({ u: used, r: [], m: [{ attributes: {}, points: [[1, 100]] }] }, L as never).map((s) => s.label)).toEqual(["used", "max"]);
  });

  it("labels PostgreSQL backends by the grouped database resource attribute", () => {
    const chart = PANELS.postgresql.find((c) => c.id === "pgBackends")!;
    const out = chart.build(
      { b: [{ attributes: { "resource.postgresql.database.name": "app" }, points: [[1, 3]] }], m: [{ attributes: {}, points: [[1, 100]] }] },
      L as never,
    );
    expect(out.map((s) => s.label)).toEqual(["app", "maxConnections"]);
  });
});

describe("overview", () => {
  const items = [
    { host_id: "h2", host_name: "db-1", key: "postgresql:/usr/lib/postgresql/16/bin/postgres", data: { rule_id: "postgresql", name: "PostgreSQL", instance: "/usr/lib/postgresql/16/bin/postgres", integration: { id: "postgresql", status: "needs_configuration", hint: "x" } } },
    { host_id: "h1", host_name: "web-1", key: "nginx:/usr/sbin/nginx", data: { rule_id: "nginx", name: "NGINX", instance: "/usr/sbin/nginx", integration: { id: "nginx", status: "enabled", endpoint: "http://127.0.0.1/nginx_status" } } },
    { host_id: "h1", host_name: "web-1", key: "sshd:/usr/sbin/sshd", data: { rule_id: "sshd", integration: { status: "not_available" } } },
    { host_id: "h1", host_name: "web-1", key: "redis:/usr/bin/redis-check-rdb", data: { rule_id: "redis", name: "Redis", instance: "/usr/bin/redis-check-rdb", command: "redis-server", integration: { id: "redis", status: "enabled" } } },
    { host_id: "h1", host_name: "web-1", key: "docker:/usr/bin/dockerd", data: { rule_id: "docker", name: "Docker", instance: "/usr/bin/dockerd", integration: { id: "docker", status: "error", error: "socket missing" } } },
  ];

  it("lists services with an integration id, counts statuses and marks panels", () => {
    const { rows, counts } = summarizeIntegrations(items);
    expect(rows.map((r) => `${r.hostName}/${r.name}/${r.panel}`)).toEqual(["db-1/PostgreSQL/true", "web-1/Docker/true", "web-1/NGINX/true", "web-1/Redis/true"]);
    expect(counts).toEqual({ enabled: 2, needs_configuration: 1, error: 1, not_available: 0 });
    expect(rows.find((r) => r.name === "Redis")).toMatchObject({ command: "redis-server", instance: "/usr/bin/redis-check-rdb" });
    expect(rows.find((r) => r.name === "NGINX")!.command).toBeUndefined();
  });

  it("filters rows by status and text", () => {
    const { rows } = summarizeIntegrations(items);
    expect(filterIntegrationRows(rows, "enabled", "").map((r) => r.name)).toEqual(["NGINX", "Redis"]);
    expect(filterIntegrationRows(rows, undefined, "redis-server").map((r) => r.name)).toEqual(["Redis"]);
    expect(filterIntegrationRows(rows, undefined, "db-1 postgres").map((r) => r.name)).toEqual(["PostgreSQL"]);
    expect(filterIntegrationRows(rows, undefined, "nginx_status").map((r) => r.name)).toEqual(["NGINX"]);
  });
});

describe("alert prefill", () => {
  it("prefills chart alerts with the instance filters only", () => {
    const d = applyPrefill(instanceAlertSearch({ metric: "redis.memory.used", agg: "avg", ref, hostName: "web-1", name: "redis.memory.used on web-1" }));
    expect(d.threshold).toBe("");
    expect(d.filters.map((f) => f.field)).toEqual(["host.id", "resource.openlog.discovery.id", "resource.openlog.discovery.instance"]);
  });

  it("ignores invalid prefill values", () => {
    expect(parsePrefillFilters("not json")).toEqual([]);
    expect(parsePrefillFilters(JSON.stringify([{ field: "attr.x", op: "like", values: ["a"] }, { field: "", op: "eq", values: ["a"] }, { field: "attr.y", op: "eq", values: [1] }, { field: "attr.z", op: "in", values: ["a", "b"] }]))).toEqual([
      { field: "attr.z", op: "in", values: "a, b" },
    ]);
    const d = applyPrefill({ metric: "m", operator: "between", threshold: "abc", window: "5", severity: "fatal", forSeconds: "-1" });
    expect([d.operator, d.threshold, d.window_seconds, d.severity, d.for_seconds]).toEqual(["gt", "", 300, "warning", 0]);
  });
});
