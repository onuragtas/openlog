import { QueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { metricQuery } from "@/api/queries";
import type { MetricSeries } from "@/api/types";
import { attributeValues, iisPoolRows, iisPoolState, topSeries } from "@/lib/integrations";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { OS_HOST_IDS } from "@/mocks/fixtures";
import { server } from "@/mocks/server";
import { IIS_APP_POOL, IIS_SITE, IIS_SITES_QUERY, PANELS, panelCharts, panelResource } from "./panels";

const ref = { discoveryId: "iis", instance: "W3SVC" };
const s = (attributes: Record<string, string>, points: [number, number][]): MetricSeries => ({ attributes, points }) as MetricSeries;

describe("IIS application pool state", () => {
  it("maps every WAS state value to a name and badge tone", () => {
    expect([1, 2, 3, 4, 5, 6, 7].map((v) => iisPoolState(v))).toEqual([
      { state: "uninitialized", tone: "muted" },
      { state: "initialized", tone: "muted" },
      { state: "running", tone: "success" },
      { state: "disabling", tone: "warning" },
      { state: "disabled", tone: "destructive" },
      { state: "shutdownPending", tone: "warning" },
      { state: "deletePending", tone: "warning" },
    ]);
    expect(iisPoolState(0)).toEqual({ state: "unknown", tone: "muted" });
    expect(iisPoolState(null)).toEqual({ state: "unknown", tone: "muted" });
  });

  it("builds pool rows from the latest point, sorted by name, skipping series without a pool", () => {
    const rows = iisPoolRows(
      [s({ [IIS_APP_POOL]: "api" }, [[1000, 3], [2000, 6]]), s({ [IIS_APP_POOL]: "DefaultAppPool" }, [[2000, 3]]), s({}, [[2000, 3]]), s({ [IIS_APP_POOL]: "empty" }, [])],
      IIS_APP_POOL,
    );
    expect(rows).toEqual([
      { pool: "api", value: 6, lastSeen: 2000, state: "shutdownPending", tone: "warning" },
      { pool: "DefaultAppPool", value: 3, lastSeen: 2000, state: "running", tone: "success" },
    ]);
  });
});

describe("IIS site filter", () => {
  it("narrows IIS panel queries to the selected site and hides the per-site breakdowns", () => {
    expect(panelResource("iis", ref)).toEqual({ "openlog.discovery.id": "iis", "openlog.discovery.instance": "W3SVC" });
    expect(panelResource("iis", ref, "api")).toEqual({ "openlog.discovery.id": "iis", "openlog.discovery.instance": "W3SVC", "iis.site": "api" });
    // Other integrations ignore a stray site.
    expect(panelResource("nginx", { discoveryId: "nginx", instance: "/usr/sbin/nginx" }, "api")).not.toHaveProperty("iis.site");
    const all = panelCharts("iis").map((c) => c.id);
    expect(all).toEqual(expect.arrayContaining(["iisRequestsBySite", "iisNotFoundBySite", "iisBytesBySite", "iisRequests"]));
    const one = panelCharts("iis", "api").map((c) => c.id);
    expect(one).not.toContain("iisRequestsBySite");
    expect(one).toContain("iisRequests");
    expect(panelCharts("postgresql", "api")).toEqual(PANELS.postgresql);
  });

  it("sends resource.iis.site and group_by=resource.iis.site as query parameters", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const seen: URLSearchParams[] = [];
    server.use(
      http.get("*/api/v1/hosts/:hostId/metrics", ({ request }) => {
        seen.push(new URL(request.url).searchParams);
        return HttpResponse.json({ metric: { name: "x", type: "sum", unit: "" }, step: "10s", series: [] });
      }),
    );
    const client = new QueryClient();
    await client.fetchQuery(metricQuery({ hostId: OS_HOST_IDS.win, ...IIS_SITES_QUERY, range: { range: "1h" }, resource: panelResource("iis", ref) }));
    await client.fetchQuery(metricQuery({ hostId: OS_HOST_IDS.win, name: "iis.request.count", agg: "rate", range: { range: "1h" }, resource: panelResource("iis", ref, "Default Web Site") }));
    expect(seen[0]!.get("group_by")).toBe("resource.iis.site");
    expect(seen[0]!.get("resource.openlog.discovery.id")).toBe("iis");
    expect(seen[0]!.has("resource.iis.site")).toBe(false);
    expect(seen[1]!.get("resource.iis.site")).toBe("Default Web Site");
    expect(seen[1]!.get("resource.openlog.discovery.instance")).toBe("W3SVC");
  });

  it("mock API: sites by group_by, one site's series by the filter", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const client = new QueryClient();
    const sites = await client.fetchQuery(metricQuery({ hostId: OS_HOST_IDS.win, ...IIS_SITES_QUERY, range: { range: "1h" }, resource: panelResource("iis", ref) }));
    expect(attributeValues(sites.series, IIS_SITE)).toEqual(["api", "Default Web Site"]);
    const chart = PANELS.iis.find((c) => c.id === "iisRequestsBySite")!;
    const bySite = await client.fetchQuery(metricQuery({ hostId: OS_HOST_IDS.win, ...chart.queries.r!, range: { range: "1h" }, resource: panelResource("iis", ref) }));
    expect(chart.build({ r: bySite.series }, (k) => k).map((x) => x.label)).toEqual(["Default Web Site", "api"]);
    const one = await client.fetchQuery(metricQuery({ hostId: OS_HOST_IDS.win, ...chart.queries.r!, range: { range: "1h" }, resource: panelResource("iis", ref, "api") }));
    expect(one.series.map((x) => x.attributes[IIS_SITE])).toEqual(["api"]);
  });

  it("keeps the top sites by traffic in the breakdowns", () => {
    const series = [s({ [IIS_SITE]: "small" }, [[1, 1]]), s({ [IIS_SITE]: "big" }, [[1, 50], [2, 50]]), s({ [IIS_SITE]: "mid" }, [[1, 20]]), s({ [IIS_SITE]: "none" }, [])];
    expect(topSeries(series, 2).map((x) => x.attributes[IIS_SITE])).toEqual(["big", "mid"]);
  });
});
